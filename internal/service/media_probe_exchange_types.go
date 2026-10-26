package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

const ProbeExchangeVersion = 1
const ProbeExchangeMaxBytes = 64 << 20

type ProbeExchangeItem struct {
	MediaID      string       `json:"media_id"`
	LibraryID    string       `json:"library_id"`
	RootID       string       `json:"root_id"`
	RelativePath string       `json:"relative_path"`
	SizeBytes    int64        `json:"size_bytes"`
	Fingerprint  string       `json:"fingerprint"`
	Metadata     *ProbeResult `json:"metadata"`
}

type ProbeExchangeManifest struct {
	Version   int                 `json:"version"`
	ID        string              `json:"id"`
	CreatedAt time.Time           `json:"created_at"`
	Digest    string              `json:"digest"`
	Items     []ProbeExchangeItem `json:"items"`
}

type ProbeExchangeResult struct {
	UnknownFields    []string     `json:"unknown_fields,omitempty"`
	MediaID          string       `json:"media_id"`
	Fingerprint      string       `json:"fingerprint"`
	FileID           string       `json:"b_file_id,omitempty"`
	SHA1             string       `json:"sha1,omitempty"`
	SizeBytes        int64        `json:"size_bytes"`
	Source           string       `json:"source,omitempty"`
	Status           string       `json:"status"`
	Error            string       `json:"error,omitempty"`
	Metadata         *ProbeResult `json:"metadata,omitempty"`
	EstimatedBitRate bool         `json:"estimated_bit_rate,omitempty"`
}

type ProbeExchangeBundle struct {
	AccountID      string                `json:"b_account_id"`
	Version        int                   `json:"version"`
	ManifestID     string                `json:"manifest_id"`
	ManifestDigest string                `json:"manifest_digest"`
	PlanDigest     string                `json:"plan_digest"`
	BatchID        string                `json:"batch_id"`
	Provenance     string                `json:"provenance"`
	ToolVersion    string                `json:"tool_version"`
	CreatedAt      time.Time             `json:"created_at"`
	StopReason     string                `json:"stop_reason,omitempty"`
	Stats          ProbeWorkerStats      `json:"stats"`
	Results        []ProbeExchangeResult `json:"results"`
}

type ProbeExchangeChange struct {
	RelativePath    string         `json:"relative_path"`
	UnknownFields   []string       `json:"unknown_fields,omitempty"`
	EstimatedFields []string       `json:"estimated_fields,omitempty"`
	MediaID         string         `json:"media_id"`
	Status          string         `json:"status"`
	Error           string         `json:"error,omitempty"`
	Before          map[string]any `json:"before,omitempty"`
	After           map[string]any `json:"after,omitempty"`
}

type ProbeExchangePreview struct {
	Digest         string                `json:"digest"`
	AlreadyApplied bool                  `json:"already_applied"`
	Counts         map[string]int        `json:"counts"`
	Changes        []ProbeExchangeChange `json:"changes"`
}

func probeExchangeDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("无法序列化任务信息: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (m ProbeExchangeManifest) CheckDigest() error {
	expected := m.Digest
	m.Digest = ""
	actual, err := probeExchangeDigest(m)
	if err != nil || m.Version != ProbeExchangeVersion || m.ID == "" || expected == "" || expected != actual {
		return errors.New("任务清单版本或校验和无效")
	}
	seen := map[string]bool{}
	for _, item := range m.Items {
		if seen[item.MediaID] || item.MediaID == "" || item.RootID == "" || item.SizeBytes <= 0 || !probeSHA256RE.MatchString(item.Fingerprint) {
			return errors.New("任务清单存在重复项或无效文件身份")
		}
		if err := validateProbeRelativePath(item.RelativePath); err != nil {
			return err
		}
		if err := validateExchangeProbe(item.Metadata); err != nil {
			return err
		}
		seen[item.MediaID] = true
	}
	return nil
}

var probeSHA1RE = regexp.MustCompile(`^[A-Fa-f0-9]{40}$`)
var probeSHA256RE = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validateProbeRelativePath(value string) error {
	if value == "" || value != path.Clean(value) || strings.HasPrefix(value, "/") || value == "." || value == ".." || strings.HasPrefix(value, "../") || strings.ContainsAny(value, "\\\x00\r\n") {
		return errors.New("批次相对路径无效")
	}
	return nil
}

func probeMetadataFromMedia(m *model.Media) *ProbeResult {
	return &ProbeResult{DurationSec: m.DurationSec, Width: m.Width, Height: m.Height, VideoCodec: m.VideoCodec,
		AudioCodec: m.AudioCodec, Container: m.Container, BitRate: m.BitRate, VideoBitRate: m.VideoBitRate,
		FrameRate: m.FrameRate, VideoProfile: m.VideoProfile, VideoRange: m.VideoRange, VideoBitDepth: m.VideoBitDepth,
		AudioBitRate: m.AudioBitRate, AudioChannels: m.AudioChannels, AudioChannelLayout: m.AudioChannelLayout,
		AudioSampleRate: m.AudioSampleRate}
}

func probeExchangeBasicComplete(p *ProbeResult) bool {
	return p != nil && p.DurationSec > 0 && p.Width > 0 && p.Height > 0 && p.VideoCodec != "" && p.AudioCodec != ""
}

func validateExchangeProbe(p *ProbeResult) error {
	if p == nil {
		return errors.New("缺少媒体技术信息")
	}
	if p.DurationSec < 0 || p.DurationSec > 31*86400 || p.Width < 0 || p.Width > 65536 || p.Height < 0 || p.Height > 65536 ||
		p.BitRate < 0 || p.VideoBitRate < 0 || p.AudioBitRate < 0 || p.AudioChannels < 0 || p.AudioChannels > 128 ||
		p.AudioSampleRate < 0 || p.AudioSampleRate > 1536000 || p.VideoBitDepth < 0 || p.VideoBitDepth > 64 ||
		math.IsNaN(p.FrameRate) || math.IsInf(p.FrameRate, 0) || p.FrameRate < 0 || p.FrameRate > 1000 {
		return errors.New("媒体技术字段超出合理范围")
	}
	for _, s := range []string{p.VideoCodec, p.AudioCodec, p.Container, p.VideoProfile, p.VideoRange, p.AudioChannelLayout} {
		if len(s) > 128 || s != strings.TrimSpace(s) || strings.ContainsAny(s, "\x00\r\n") || strings.Contains(s, "://") {
			return errors.New("媒体技术字段格式无效")
		}
	}
	if len(p.VideoCodec) > 32 || len(p.AudioCodec) > 32 || len(p.VideoProfile) > 64 || len(p.VideoRange) > 32 || len(p.AudioChannelLayout) > 64 {
		return errors.New("媒体技术字段长度超限")
	}
	return nil
}

func probeExchangeUnknownFields(p *ProbeResult) []string {
	values := probeResultUpdates(p)
	fields := []string{"duration_sec", "width", "height", "video_codec", "audio_codec", "container", "bit_rate", "video_bit_rate", "frame_rate", "video_profile", "video_range", "video_bit_depth", "audio_bit_rate", "audio_channels", "audio_channel_layout", "audio_sample_rate"}
	unknown := []string{}
	for _, field := range fields {
		if _, found := values[field]; !found {
			unknown = append(unknown, field)
		}
	}
	return unknown
}

// Missing-only merges also reject incompatible nonempty fields. Subtitle
// catalogs are intentionally outside this exchange format's persistence scope.
func mergeExchangeProbe(current, incoming *ProbeResult) (*ProbeResult, error) {
	if err := validateExchangeProbe(incoming); err != nil {
		return nil, err
	}
	result := *current
	result.SubtitleStreams = nil
	dst, src := reflect.ValueOf(&result).Elem(), reflect.ValueOf(incoming).Elem()
	for i := 0; i < dst.NumField(); i++ {
		field := dst.Type().Field(i)
		if field.Name == "SubtitleStreams" || src.Field(i).IsZero() {
			continue
		}
		if dst.Field(i).IsZero() {
			dst.Field(i).Set(src.Field(i))
		} else if !reflect.DeepEqual(dst.Field(i).Interface(), src.Field(i).Interface()) {
			conflict := false
			switch field.Name {
			case "DurationSec":
				conflict = math.Abs(float64(dst.Field(i).Int()-src.Field(i).Int())) > 1
			case "Width", "Height", "VideoBitDepth", "AudioChannels", "AudioSampleRate":
				conflict = true
			case "VideoCodec", "AudioCodec":
				conflict = normalizeProbeWorkerCodec(dst.Field(i).String()) != normalizeProbeWorkerCodec(src.Field(i).String())
			case "FrameRate":
				conflict = math.Abs(dst.Field(i).Float()-src.Field(i).Float()) > 0.01
			}
			if conflict {
				return nil, fmt.Errorf("已有技术字段冲突：%s", field.Name)
			}
			// Preserve existing optional values. Container aliases, profiles and
			// independently estimated bitrates need not use identical notation.
		}
	}
	return &result, nil
}
