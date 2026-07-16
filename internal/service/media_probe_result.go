package service

import (
	"context"
	"errors"
	"strings"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

var ErrMediaProbeEmpty = errors.New("ffprobe returned no supported media metadata")

func persistMediaProbeResult(
	ctx context.Context,
	repo *repository.Container,
	cache *RuntimeCacheService,
	generated *GeneratedArtworkService,
	log *zap.Logger,
	media *model.Media,
	probe *ProbeResult,
) error {
	if repo == nil || repo.DB == nil || media == nil {
		return ErrMediaNotFound
	}
	updates := probeResultUpdates(probe)
	if len(updates) == 0 {
		return ErrMediaProbeEmpty
	}
	previousDuration := media.DurationSec
	if err := repo.DB.WithContext(ctx).Model(&model.Media{}).Where("id = ?", media.ID).Updates(updates).Error; err != nil {
		return err
	}
	applyProbeResultToMediaValue(media, probe)
	if generated != nil && previousDuration != media.DurationSec {
		if _, err := generated.QueueRefreshForMedia(context.WithoutCancel(ctx), media.ID); err != nil && log != nil {
			log.Warn("queue generated artwork refresh after media probe failed", zap.String("media_id", media.ID), zap.Error(err))
		}
	}
	if cache != nil {
		cache.DeletePrefix(context.WithoutCancel(ctx), "media:")
	}
	return nil
}

func applyProbeResultToMediaValue(media *model.Media, probe *ProbeResult) {
	if media == nil || probe == nil {
		return
	}
	if probe.DurationSec > 0 {
		media.DurationSec = probe.DurationSec
	}
	if probe.Width > 0 {
		media.Width = probe.Width
	}
	if probe.Height > 0 {
		media.Height = probe.Height
	}
	if strings.TrimSpace(probe.VideoCodec) != "" {
		media.VideoCodec = probe.VideoCodec
	}
	if strings.TrimSpace(probe.AudioCodec) != "" {
		media.AudioCodec = probe.AudioCodec
	}
	if strings.TrimSpace(probe.Container) != "" {
		media.Container = probe.Container
	}
	if probe.BitRate > 0 {
		media.BitRate = probe.BitRate
	}
	if probe.VideoBitRate > 0 {
		media.VideoBitRate = probe.VideoBitRate
	}
	if probe.FrameRate > 0 {
		media.FrameRate = probe.FrameRate
	}
	if strings.TrimSpace(probe.VideoProfile) != "" {
		media.VideoProfile = probe.VideoProfile
	}
	if strings.TrimSpace(probe.VideoRange) != "" {
		media.VideoRange = probe.VideoRange
	}
	if probe.VideoBitDepth > 0 {
		media.VideoBitDepth = probe.VideoBitDepth
	}
	if probe.AudioBitRate > 0 {
		media.AudioBitRate = probe.AudioBitRate
	}
	if probe.AudioChannels > 0 {
		media.AudioChannels = probe.AudioChannels
	}
	if strings.TrimSpace(probe.AudioChannelLayout) != "" {
		media.AudioChannelLayout = probe.AudioChannelLayout
	}
	if probe.AudioSampleRate > 0 {
		media.AudioSampleRate = probe.AudioSampleRate
	}
	media.MediaProbeVersion = mediaProbeMetadataVersion
}
