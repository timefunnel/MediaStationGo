package service

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"go.uber.org/zap"
)

// Optional actual-codec comparisons use only locally generated media and a
// loopback HTTP fixture. No cloud path, credential or production media is used.
func TestMKVRealFFprobeMetadata(t *testing.T) {
	ffmpeg, ffprobe := os.Getenv("MSG_MKV_TEST_FFMPEG"), os.Getenv("MSG_MKV_TEST_FFPROBE")
	if ffmpeg == "" {
		ffmpeg, _ = exec.LookPath("ffmpeg")
	}
	if ffprobe == "" {
		ffprobe, _ = exec.LookPath("ffprobe")
	}
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("local ffmpeg/ffprobe unavailable; set MSG_MKV_TEST_FFMPEG and MSG_MKV_TEST_FFPROBE")
	}
	encoders, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").CombinedOutput()
	if err != nil {
		t.Fatalf("read local encoders: %v", err)
	}
	for _, fixture := range []struct {
		name, codec, audio             string
		hdr, bit10, vfr, missing, dovi bool
	}{
		{name: "ffv1-aac-subtitles", codec: "ffv1", audio: "aac"},
		{name: "ffv1-10bit-eac3-multi-audio", codec: "ffv1", audio: "eac3", bit10: true},
		{name: "synthetic-DOVI-configuration-preserved", codec: "ffv1", audio: "aac", dovi: true},
		{name: "h264-aac", codec: "libx264", audio: "aac"},
		{name: "hevc-10bit-HDR10-eac3", codec: "libx265", audio: "eac3", hdr: true, bit10: true},
		{name: "vfr-without-default-duration", codec: "ffv1", audio: "aac", vfr: true},
		{name: "missing-source-duration", codec: "ffv1", audio: "aac", missing: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if !strings.Contains(string(encoders), " "+fixture.codec+" ") {
				t.Skip("encoder unavailable: " + fixture.codec)
			}
			work := t.TempDir()
			font, err := os.Create(filepath.Join(work, "dummy.ttf"))
			if err != nil {
				t.Fatal(err)
			}
			if err := font.Truncate(24 << 20); err != nil {
				t.Fatal(err)
			}
			if err := font.Close(); err != nil {
				t.Fatal(err)
			}
			subtitle := filepath.Join(work, "subtitle.srt")
			if err := os.WriteFile(subtitle, []byte("1\n00:00:00,000 --> 00:00:01,800\nSynthetic subtitle\n"), 0600); err != nil {
				t.Fatal(err)
			}
			mediaPath := filepath.Join(work, "fixture.mkv")
			args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000", "-i", subtitle, "-map", "0:v", "-map", "1:a", "-map", "1:a", "-map", "2:s", "-t", "2", "-c:v", fixture.codec, "-threads", "1", "-c:a", fixture.audio, "-ac", "2", "-c:s", "srt", "-metadata:s:a:0", "language=jpn", "-metadata:s:a:1", "language=zho", "-metadata:s:s:0", "language=zho", "-metadata:s:s:0", "title=Synthetic Chinese", "-disposition:s:0", "default+forced", "-attach", font.Name(), "-metadata:s:t", "mimetype=application/x-truetype-font"}
			if fixture.codec == "libx264" {
				args = append(args, "-preset", "ultrafast")
			}
			if fixture.codec == "libx265" {
				args = append(args, "-preset", "ultrafast", "-x265-params", "pools=1:frame-threads=1:log-level=error:colorprim=9:transfer=16:colormatrix=9:hdr10=1")
			}
			if fixture.bit10 {
				args = append(args, "-pix_fmt", "yuv420p10le")
			}
			if fixture.hdr {
				args = append(args, "-color_trc", "smpte2084", "-color_primaries", "bt2020", "-colorspace", "bt2020nc")
			}
			if fixture.vfr {
				args = append(args, "-vf", "select=not(mod(n\\,3))+not(mod(n\\,5))", "-fps_mode", "vfr")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			if output, err := exec.CommandContext(ctx, ffmpeg, append(args, "-y", mediaPath)...).CombinedOutput(); err != nil {
				t.Fatalf("generate local fixture: %v: %s", err, output)
			}
			data, err := os.ReadFile(mediaPath)
			if err != nil {
				t.Fatal(err)
			}
			if fixture.dovi {
				data = mkvTestAddDOVIConfiguration(t, data)
				if err := os.WriteFile(mediaPath, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if fixture.missing || fixture.vfr {
				// Replace only the authoritative duration/default-duration field
				// with same-sized Void elements, keeping all source offsets valid.
				mkvTestVoidMetadata(t, data, fixture.missing, fixture.vfr)
				if err := os.WriteFile(mediaPath, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &config.Config{}
			cfg.App.FFprobePath = ffprobe
			svc := NewFFprobeService(cfg, zap.NewNop())
			original, err := svc.probeLocal(t.Context(), mediaPath, false)
			if err != nil {
				t.Fatal(err)
			}
			server, state := serveMKVFixture(t, data)
			actual, err := svc.probeRemoteMKV(t.Context(), server.URL+"/opaque-url", nil, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if actual.BitRate != 0 || actual.VideoBitRate != 0 || actual.AudioBitRate != 0 {
				t.Fatal("sampled bitrates were accepted")
			}
			original.BitRate, original.VideoBitRate, original.AudioBitRate = 0, 0, 0
			if fixture.missing {
				original.DurationSec = 0 // Do not accept ffprobe's sample/tag estimate.
			}
			if fixture.vfr {
				original.FrameRate = 0
			} else if math.Abs(original.FrameRate-actual.FrameRate) < 0.001 {
				original.FrameRate = actual.FrameRate
			}
			if !reflect.DeepEqual(original, actual) {
				want, _ := json.Marshal(original)
				got, _ := json.Marshal(actual)
				t.Fatalf("full source vs compact sample:\nwant %s\ngot  %s", want, got)
			}
			if fixture.hdr && (actual.VideoRange != "HDR10" || actual.VideoBitDepth != 10) {
				t.Fatalf("HDR metadata lost: %+v", actual)
			}
			if fixture.dovi && actual.VideoRange != "Dolby Vision" {
				t.Fatalf("DOVI configuration was lost: %+v", actual)
			}
			if state.requests > 6 || state.bytes > 4<<20 || actual.DurationSec > 3 || len(actual.SubtitleStreams) != 1 || !actual.SubtitleStreams[0].Default || !actual.SubtitleStreams[0].Forced {
				t.Fatalf("incorrect source parameters/budget: %+v requests=%d bytes=%d", actual, state.requests, state.bytes)
			}
			t.Logf("source_bytes=%d range_requests=%d range_bytes=%d video=%s %dx%d %s %dbit audio=%s subtitles=%d duration=%d fps=%f", len(data), state.requests, state.bytes, actual.VideoCodec, actual.Width, actual.Height, actual.VideoRange, actual.VideoBitDepth, actual.AudioCodec, len(actual.SubtitleStreams), actual.DurationSec, actual.FrameRate)
		})
	}
}

// Inject the same dvcC configuration record parsed by FFmpeg's Matroska
// BlockAdditionMapping path. This verifies metadata preservation, not Dolby
// Vision RPU decoding or playback. The actual coded frames remain synthetic.
func mkvTestAddDOVIConfiguration(t *testing.T, data []byte) []byte {
	t.Helper()
	header, err := mkvElementAt(data, 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	segment, err := mkvElementAt(data[header.end:], header.end, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var before, tags, after []byte
	err = mkvChildren(data[segment.body:segment.end], func(e mkvElement, value []byte) error {
		original := data[segment.body+e.start : segment.body+e.end]
		switch e.id {
		case mkvSeekHead, 0x1c53bb6b, 0xbf:
			return nil
		case mkvTracks:
			var tracks []byte
			if err := mkvChildren(value, func(entry mkvElement, trackBody []byte) error {
				if entry.id == 0xbf {
					return nil
				}
				if entry.id != 0xae {
					tracks = append(tracks, value[entry.start:entry.end]...)
					return nil
				}
				var track []byte
				var kind uint64
				if err := mkvChildren(trackBody, func(field mkvElement, payload []byte) error {
					if field.id == 0x83 {
						var err error
						kind, err = mkvUInt(payload)
						return err
					}
					return nil
				}); err != nil {
					return err
				}
				if err := mkvChildren(trackBody, func(field mkvElement, payload []byte) error {
					if field.id != 0xbf {
						track = append(track, trackBody[field.start:field.end]...)
					}
					return nil
				}); err != nil {
					return err
				}
				if kind == 1 {
					record := append([]byte{1, 0, 16, 45, 16}, make([]byte, 19)...)
					mapping := mkvWrap(0x41e4, mkvTestJoin(mkvTestUInt(0x41e7, 0x64766343), mkvWrap(0x41ed, record)))
					track = append(track, mapping...)
				}
				tracks = append(tracks, mkvWrap(0xae, track)...)
				return nil
			}); err != nil {
				return err
			}
			before = append(before, mkvWrap(mkvTracks, tracks)...)
		case mkvTags:
			tags = append(tags, original...)
		case mkvCluster:
			after = append(after, original...)
		default:
			before = append(before, original...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return mkvTestJoin(data[:header.end], mkvWrap(mkvSegment, mkvTestJoin(before, tags, after)))
}

func mkvTestVoidMetadata(t *testing.T, data []byte, duration, defaultDuration bool) {
	t.Helper()
	var walk func([]byte)
	walk = func(body []byte) {
		if err := mkvChildren(body, func(e mkvElement, value []byte) error {
			if e.id == mkvCluster || e.id == mkvAttachments {
				return nil
			}
			if e.id == 0xbf || (duration && e.id == 0x4489) || (defaultDuration && e.id == 0x23e383) {
				size := int(e.end - e.start)
				copy(body[e.start:e.end], mkvWrap(0xec, make([]byte, size-2)))
				return nil
			}
			if e.id == mkvSegment || e.id == mkvInfo || e.id == mkvTracks || e.id == 0xae {
				walk(value)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	walk(data)
}
