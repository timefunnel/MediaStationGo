package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

func (s *ScannerService) probeCloudMediaAsync(task cloudMediaProbeTask) {
	defer func() {
		s.cloudMediaProbeMu.Lock()
		delete(s.cloudMediaProbing, task.path)
		s.cloudMediaProbeMu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	probe, err := s.probeCloudFileMetadata(ctx, task.typ, task.ref)
	if err != nil {
		if s.log != nil {
			s.log.Debug("cloud media async probe failed", zap.String("provider", task.typ), zap.String("path", task.path), zap.Error(err))
		}
		s.cloudMediaProbeMu.Lock()
		if s.cloudMediaProbeBackoff == nil {
			s.cloudMediaProbeBackoff = make(map[string]time.Time)
		}
		s.cloudMediaProbeBackoff[task.path] = time.Now().Add(cloudMediaProbeFailureBackoff)
		s.cloudMediaProbeMu.Unlock()
		return
	}
	updates := probeResultUpdates(probe)
	if len(updates) == 0 {
		return
	}
	var previous model.Media
	findErr := s.repo.DB.WithContext(ctx).Where("path = ?", task.path).First(&previous).Error
	if err := s.repo.DB.WithContext(ctx).Model(&model.Media{}).Where("path = ?", task.path).Updates(updates).Error; err != nil {
		if s.log != nil {
			s.log.Debug("update cloud media track metadata failed", zap.String("path", task.path), zap.Error(err))
		}
		return
	}
	if findErr == nil && s.generatedArtwork != nil && previous.DurationSec != probe.DurationSec {
		if _, err := s.generatedArtwork.QueueRefreshForMedia(context.WithoutCancel(ctx), previous.ID); err != nil {
			if s.log != nil {
				s.log.Warn("queue generated artwork refresh after duration probe failed", zap.String("media_id", previous.ID), zap.Error(err))
			}
		}
	}
	s.invalidateMediaCache(context.WithoutCancel(ctx))
	s.cloudMediaProbeMu.Lock()
	delete(s.cloudMediaProbeBackoff, task.path)
	s.cloudMediaProbeMu.Unlock()
	if s.hub != nil {
		s.hub.Publish("scan", map[string]any{
			"path":          task.path,
			"cloud":         true,
			"track_probed":  true,
			"duration_sec":  probe.DurationSec,
			"video_codec":   probe.VideoCodec,
			"audio_codec":   probe.AudioCodec,
			"width":         probe.Width,
			"height":        probe.Height,
			"probe_message": "云盘媒体轨道元数据已后台补齐",
		})
	}
}

func (s *ScannerService) ffprobeWorkerCount() int {
	if s == nil || s.cfg == nil {
		return 1
	}
	return normalizeFFprobeMaxConcurrent(s.cfg.App.FFprobeMaxConcurrent)
}

func (s *ScannerService) cloudScanWorkerCount() int {
	if s == nil || s.cfg == nil {
		return 4
	}
	return normalizeCloudScanMaxConcurrent(s.cfg.App.CloudScanMaxConcurrent)
}

func normalizeCloudScanMaxConcurrent(n int) int {
	if n <= 0 {
		return 1
	}
	if n > 16 {
		return 16
	}
	return n
}

func (s *ScannerService) probeCloudFileMetadata(ctx context.Context, typ, ref string) (*ProbeResult, error) {
	if s == nil || s.probe == nil || s.storage == nil {
		return nil, errors.New("cloud probe unavailable")
	}
	return probeCloudFileMetadataWith(ctx, s.storage, s.probe, typ, ref)
}

func probeCloudFileMetadataWith(ctx context.Context, resolver cloudPlaybackResolver, prober cloudPlaybackProber, typ, ref string) (*ProbeResult, error) {
	link, err := resolver.CloudResolve(ctx, typ, ref, cloudMediaInternalUserAgent)
	if err != nil {
		return nil, err
	}
	if link == nil || strings.TrimSpace(link.URL) == "" {
		return nil, errors.New("cloud media resolved to an empty URL")
	}
	return prober.ProbeHTTP(ctx, link.URL, cloudMediaInternalHeaders(link.Headers))
}

func probeResultUpdates(probe *ProbeResult) map[string]any {
	updates := map[string]any{}
	if probe == nil {
		return updates
	}
	if probe.DurationSec > 0 {
		updates["duration_sec"] = probe.DurationSec
	}
	if probe.Width > 0 {
		updates["width"] = probe.Width
	}
	if probe.Height > 0 {
		updates["height"] = probe.Height
	}
	if strings.TrimSpace(probe.VideoCodec) != "" {
		updates["video_codec"] = probe.VideoCodec
	}
	if strings.TrimSpace(probe.AudioCodec) != "" {
		updates["audio_codec"] = probe.AudioCodec
	}
	if probe.Container != "" {
		updates["container"] = probe.Container
	}
	if probe.BitRate > 0 {
		updates["bit_rate"] = probe.BitRate
	}
	if probe.VideoBitRate > 0 {
		updates["video_bit_rate"] = probe.VideoBitRate
	}
	if probe.FrameRate > 0 {
		updates["frame_rate"] = probe.FrameRate
	}
	if strings.TrimSpace(probe.VideoProfile) != "" {
		updates["video_profile"] = probe.VideoProfile
	}
	if strings.TrimSpace(probe.VideoRange) != "" {
		updates["video_range"] = probe.VideoRange
	}
	if probe.VideoBitDepth > 0 {
		updates["video_bit_depth"] = probe.VideoBitDepth
	}
	if probe.AudioBitRate > 0 {
		updates["audio_bit_rate"] = probe.AudioBitRate
	}
	if probe.AudioChannels > 0 {
		updates["audio_channels"] = probe.AudioChannels
	}
	if strings.TrimSpace(probe.AudioChannelLayout) != "" {
		updates["audio_channel_layout"] = probe.AudioChannelLayout
	}
	if probe.AudioSampleRate > 0 {
		updates["audio_sample_rate"] = probe.AudioSampleRate
	}
	if len(updates) > 0 {
		updates["media_probe_version"] = mediaProbeMetadataVersion
	}
	return updates
}
