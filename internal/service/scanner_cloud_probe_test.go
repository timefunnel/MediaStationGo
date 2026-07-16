package service

import (
	"context"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/service/cloud"
)

type probeTestResolver struct {
	ua string
}

func (r *probeTestResolver) CloudResolve(_ context.Context, _, _, clientUA string) (*cloud.DirectLink, error) {
	r.ua = clientUA
	return &cloud.DirectLink{URL: "https://cdn.example.test/video.mkv"}, nil
}

type probeTestProber struct {
	headers map[string]string
}

func (p *probeTestProber) ProbeHTTP(_ context.Context, _ string, headers map[string]string) (*ProbeResult, error) {
	p.headers = headers
	return &ProbeResult{DurationSec: 3600}, nil
}

func TestProbeCloudFileMetadataUsesSameSigningAndReadingUserAgent(t *testing.T) {
	resolver := &probeTestResolver{}
	prober := &probeTestProber{}
	result, err := probeCloudFileMetadataWith(t.Context(), resolver, prober, "openlist", "/other/video.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if result.DurationSec != 3600 {
		t.Fatalf("result = %+v", result)
	}
	if resolver.ua != cloudMediaInternalUserAgent || prober.headers["User-Agent"] != cloudMediaInternalUserAgent {
		t.Fatalf("resolve ua = %q, probe headers = %#v", resolver.ua, prober.headers)
	}
}

func TestPreserveCloudTrackMetadataKeepsCompletedProbeFields(t *testing.T) {
	media := &model.Media{Container: "mkv"}
	preserveCloudTrackMetadata(media, existingCloudMedia{
		DurationSec:        3600,
		Width:              3840,
		Height:             2160,
		VideoCodec:         "hevc",
		AudioCodec:         "truehd",
		Container:          "matroska,webm",
		BitRate:            50000000,
		VideoBitRate:       45000000,
		FrameRate:          23.976,
		VideoProfile:       "Main 10",
		VideoRange:         "Dolby Vision",
		VideoBitDepth:      10,
		AudioBitRate:       4000000,
		AudioChannels:      8,
		AudioChannelLayout: "7.1",
		AudioSampleRate:    48000,
		MediaProbeVersion:  mediaProbeMetadataVersion,
	})
	if media.DurationSec != 3600 || media.VideoCodec != "hevc" || media.BitRate != 50000000 || media.VideoRange != "Dolby Vision" || media.AudioChannels != 8 || media.MediaProbeVersion != mediaProbeMetadataVersion {
		t.Fatalf("preserved metadata = %+v", media)
	}
}
