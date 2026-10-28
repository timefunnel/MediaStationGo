package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/service/cloud"
	"go.uber.org/zap"
)

func mkvTestUInt(id, value uint64) []byte {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	first := 0
	for first < 7 && data[first] == 0 {
		first++
	}
	return mkvWrap(id, data[first:])
}

func mkvTestJoin(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func mkvTestFixture(fontSizes []int, missingDuration, unknownCluster, largeCluster, encrypted bool) []byte {
	header := mkvWrap(mkvEBML, mkvWrap(0x4282, []byte("matroska")))
	var duration [8]byte
	binary.BigEndian.PutUint64(duration[:], math.Float64bits(123456))
	info := mkvTestUInt(0x2ad7b1, 1000000)
	if !missingDuration {
		info = append(info, mkvWrap(0x4489, duration[:])...)
	}
	info = mkvWrap(mkvInfo, info)
	video := mkvTestJoin(mkvTestUInt(0xd7, 1), mkvTestUInt(0x73c5, 11), mkvTestUInt(0x83, 1), mkvTestUInt(0x23e383, 40000000), mkvWrap(0x86, []byte("V_MPEG4/ISO/AVC")))
	if encrypted {
		video = append(video, mkvWrap(0x6d80, mkvWrap(0x6240, mkvTestUInt(0x5033, 1)))...)
	}
	audio := mkvTestJoin(mkvTestUInt(0xd7, 2), mkvTestUInt(0x73c5, 22), mkvTestUInt(0x83, 2), mkvWrap(0x86, []byte("A_AAC")))
	tracks := mkvWrap(mkvTracks, mkvTestJoin(mkvWrap(0xae, video), mkvWrap(0xae, audio)))
	var attachments []byte
	for _, size := range fontSizes {
		attachments = append(attachments, mkvWrap(0x61a7, mkvWrap(0x465c, bytes.Repeat([]byte{0x52}, size)))...)
	}
	attachments = mkvWrap(mkvAttachments, attachments)
	clusterBody := mkvTestJoin(mkvTestUInt(0xe7, 0), mkvWrap(0xbf, []byte{0, 0, 0, 0}), mkvWrap(0xa3, []byte{0x81, 0, 0, 0x80, 1, 2}), mkvWrap(0xa3, []byte{0x82, 0, 0, 0x80, 3, 4}))
	if largeCluster {
		clusterBody = append(clusterBody, mkvWrap(0xa3, mkvTestJoin([]byte{0x81, 0, 1, 0x80}, bytes.Repeat([]byte{0x37}, 3<<20)))...)
	}
	cluster := mkvWrap(mkvCluster, clusterBody)
	if unknownCluster {
		cluster = mkvTestJoin([]byte{0x1f, 0x43, 0xb6, 0x75, 0xff}, clusterBody)
	}
	tags := mkvWrap(mkvTags, mkvWrap(0x7373, mkvWrap(0x67c8, mkvTestJoin(mkvWrap(0x45a3, []byte("TITLE")), mkvWrap(0x4487, []byte("source title"))))))
	seek := func(offset int) []byte {
		var data [4]byte
		binary.BigEndian.PutUint32(data[:], uint32(offset))
		return mkvWrap(mkvSeekHead, mkvWrap(0x4dbb, mkvTestJoin(mkvWrap(0x53ab, []byte{0x12, 0x54, 0xc3, 0x67}), mkvWrap(0x53ac, data[:]))))
	}
	seekHead := seek(0)
	seekHead = seek(len(seekHead) + len(info) + len(tracks) + len(attachments) + len(cluster))
	segment := mkvWrap(mkvSegment, mkvTestJoin(seekHead, info, tracks, attachments, cluster, tags))
	return mkvTestJoin(header, segment)
}

type mkvTestHTTP struct {
	mu       sync.Mutex
	requests int
	bytes    int
	ranges   []string
	starts   []time.Time
	mode     string
}

func serveMKVFixture(t *testing.T, data []byte) (*httptest.Server, *mkvTestHTTP) {
	t.Helper()
	state := &mkvTestHTTP{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.requests++
		state.ranges = append(state.ranges, req.Header.Get("Range"))
		state.starts = append(state.starts, time.Now())
		if state.mode == "redirect" {
			w.Header().Set("Location", "/redirect-loop")
			w.WriteHeader(302)
			return
		}
		if state.mode == "ignored" {
			w.WriteHeader(200)
			return
		}
		if state.mode == "risk" || state.mode == "risk-late" || state.mode == "limited" {
			if state.mode != "limited" {
				w.WriteHeader(403)
				if state.mode == "risk-late" {
					fmt.Fprint(w, strings.Repeat("padding ", 220))
				}
				fmt.Fprint(w, `{"code":40140117,"message":"risk control","token":"SYNTHETIC_SECRET"}`)
			} else {
				w.WriteHeader(429)
			}
			return
		}
		var start, end int
		if _, err := fmt.Sscanf(req.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || start < 0 || start >= len(data) {
			w.WriteHeader(416)
			return
		}
		end = min(end, len(data)-1)
		claimedStart := start
		if state.mode == "wrong" {
			claimedStart++
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", claimedStart, end, len(data)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		etag := `"source-v1"`
		if state.mode == "changed" && state.requests > 1 {
			etag = `"source-v2"`
		}
		w.Header().Set("ETag", etag)
		w.WriteHeader(206)
		n, _ := w.Write(data[start : end+1])
		state.bytes += n
	}))
	t.Cleanup(server.Close)
	return server, state
}

func TestMKVSampleSkipsAttachmentsAndPreservesSourceMetadata(t *testing.T) {
	for _, fonts := range [][]int{nil, {24 << 20}, {10 << 20, 10 << 20, 10 << 20}} {
		t.Run(fmt.Sprint(fonts), func(t *testing.T) {
			data := mkvTestFixture(fonts, false, false, false, false)
			server, state := serveMKVFixture(t, data)
			reader := newMKVProbeReader(t.Context(), server.URL+"/signed?token=SECRET", map[string]string{"User-Agent": "test-agent"}, int64(len(data)))
			reader.interval = 0
			sample, err := buildMKVProbeSample(reader)
			if err != nil {
				t.Fatal(err)
			}
			if sample.duration != 123 || len(sample.tracks) != 2 || sample.tracks[0].defaultDuration != 40000000 {
				t.Fatalf("wrong source metadata: %+v", sample)
			}
			if len(sample.data) > 2048 || reader.readBytes > 256<<10 || state.requests != reader.requests || state.requests > mkvProbeMaxRequests {
				t.Fatalf("unexpected budget: sample=%d read=%d requests=%d", len(sample.data), reader.readBytes, state.requests)
			}
			if len(fonts) > 0 && sample.skippedBytes < int64(fonts[0]) {
				t.Fatalf("attachments not skipped: %d", sample.skippedBytes)
			}
			if !bytes.Contains(sample.data, []byte("source title")) || bytes.Contains(sample.data, []byte{0x19, 0x41, 0xa4, 0x69}) || bytes.Contains(sample.data, []byte{0xbf, 0x84, 0, 0, 0, 0}) {
				t.Fatal("tail Tags were lost, or attachments/invalid Cluster CRC were retained")
			}
		})
	}
}

func TestMKVLargeAndUnknownClustersOnlyKeepCompleteBlocks(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		data := mkvTestFixture([]int{1 << 20}, false, unknown, true, false)
		server, _ := serveMKVFixture(t, data)
		reader := newMKVProbeReader(t.Context(), server.URL, nil, int64(len(data)))
		reader.interval = 0
		sample, err := buildMKVProbeSample(reader)
		if err != nil {
			t.Fatalf("unknown=%v: %v", unknown, err)
		}
		if len(sample.data) > 2048 || reader.readBytes > mkvProbeMaxBytes || bytes.Contains(sample.data, bytes.Repeat([]byte{0x37}, 128)) {
			t.Fatal("incomplete trailing block leaked into sample")
		}
	}
}

func TestMKVMissingDurationStaysUnknownAndEncryptionIsRejected(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		data := mkvTestFixture(nil, true, false, false, encrypted)
		server, _ := serveMKVFixture(t, data)
		reader := newMKVProbeReader(t.Context(), server.URL, nil, 0)
		reader.interval = 0
		sample, err := buildMKVProbeSample(reader)
		if encrypted {
			if err == nil || !strings.Contains(err.Error(), "ContentEncodings") {
				t.Fatalf("encrypted source not rejected: %v", err)
			}
		} else if err != nil || sample.duration != 0 {
			t.Fatalf("missing source duration: %+v, %v", sample, err)
		}
	}
}

func TestMKVRangeRejectsProtocolAndRiskWithoutRetry(t *testing.T) {
	data := mkvTestFixture([]int{1 << 20}, false, false, false, false)
	for _, mode := range []string{"ignored", "wrong", "redirect", "changed", "risk", "risk-late", "limited"} {
		t.Run(mode, func(t *testing.T) {
			server, state := serveMKVFixture(t, data)
			state.mode = mode
			reader := newMKVProbeReader(t.Context(), server.URL+"?token=SYNTHETIC_SECRET", nil, 0)
			reader.interval = 0
			_, err := buildMKVProbeSample(reader)
			if err == nil || strings.Contains(err.Error(), "SYNTHETIC_SECRET") {
				t.Fatalf("unsafe or missing error: %v", err)
			}
			want := 1
			if mode == "changed" {
				want = 2
			}
			if state.requests != want {
				t.Fatalf("requests=%d want=%d", state.requests, want)
			}
			failure := classifyMediaProbeFailure(err)
			if strings.HasPrefix(mode, "risk") && (failure.Kind != "risk_control" || failure.ProviderCode != "40140117" || failure.HTTPStatus != 403) {
				t.Fatalf("risk alert lost: %+v, %v", failure, err)
			}
			if mode == "limited" && failure.Kind != "rate_limited" {
				t.Fatalf("rate limit alert lost: %+v", failure)
			}
		})
	}
}

type mkvQueueTestResolver struct {
	url   string
	calls int
	ua    string
}

func (r *mkvQueueTestResolver) CloudResolve(_ context.Context, _, _, ua string) (*cloud.DirectLink, error) {
	r.calls++
	r.ua = ua
	return &cloud.DirectLink{URL: r.url}, nil
}

func TestMKVQueueBindsSourceAndResolvesOnlyOnceOnFailure(t *testing.T) {
	server, state := serveMKVFixture(t, nil)
	state.mode = "limited"
	resolver := &mkvQueueTestResolver{url: server.URL + "/opaque?token=SECRET"}
	stream := &StreamService{storage: resolver}
	prober := mediaProbeQueueFFprobe{service: NewFFprobeService(&config.Config{}, zap.NewNop())}
	_, err := stream.probeMediaSource(t.Context(), &model.Media{Path: "cloud://openlist/115/episode.mkv", STRMURL: "/api/cloud/play/openlist?ref=episode.mkv"}, prober)
	if err == nil || classifyMediaProbeFailure(err).Kind != "rate_limited" || resolver.calls != 1 || state.requests != 1 || resolver.ua != cloudMediaInternalUserAgent || prober.mkv {
		t.Fatalf("source binding/resolution policy changed: %v resolve=%d range=%d", err, resolver.calls, state.requests)
	}
}

func TestMKVRangeHardBudgetsAndCancellation(t *testing.T) {
	server, state := serveMKVFixture(t, bytes.Repeat([]byte{1}, 8<<20))
	reader := newMKVProbeReader(t.Context(), server.URL, nil, 0)
	reader.interval = 0
	for i := 0; i < 6; i++ {
		if _, err := reader.readAt(int64(i)*100000, 12); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reader.readAt(1000000, 12); err == nil || state.requests != 6 {
		t.Fatal("request limit did not stop before sending")
	}
	reader = newMKVProbeReader(t.Context(), server.URL, nil, 0)
	reader.interval = 0
	if _, err := reader.readAt(0, mkvProbeMaxBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.readAt(mkvProbeMaxBytes, 12); err == nil || reader.readBytes != mkvProbeMaxBytes || reader.requests != 1 {
		t.Fatal("byte cap did not stop before sending")
	}
	ctx, cancel := context.WithCancel(t.Context())
	reader = newMKVProbeReader(ctx, server.URL, nil, 0)
	cancel()
	if _, err := reader.readAt(0, 12); err != context.Canceled || reader.requests != 0 {
		t.Fatalf("cancellation ignored: %v", err)
	}
	reader = newMKVProbeReader(t.Context(), server.URL, nil, 123)
	if _, err := reader.readAt(0, 12); err == nil || reader.requests != 1 || reader.readBytes != 0 {
		t.Fatal("database/source size mismatch not rejected before reading")
	}
}

func TestMKVRangeEnforcesSerialSpacing(t *testing.T) {
	server, state := serveMKVFixture(t, bytes.Repeat([]byte{1}, 1<<20))
	reader := newMKVProbeReader(t.Context(), server.URL, nil, 0)
	if _, err := reader.readAt(0, 12); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.readAt(100000, 12); err != nil {
		t.Fatal(err)
	}
	if state.starts[1].Sub(state.starts[0]) < time.Second {
		t.Fatal("requests were not spaced by at least one second")
	}
}

func TestMKVSourceUsesOriginalPathInsteadOfSignedURLExtension(t *testing.T) {
	for _, media := range []model.Media{
		{Path: "cloud://openlist/115/anime/episode.MKV"},
		{Path: "episode.strm", STRMURL: "/api/cloud/play/openlist?ref=%2F115%2Fepisode.mkv"},
		{Path: `C:\media\episode.mkv`},
	} {
		if !mediaProbeSourceIsMKV(&media) {
			t.Fatalf("format not bound: %q", media.Path)
		}
	}
	if mediaProbeSourceIsMKV(&model.Media{Path: "movie.mp4"}) {
		t.Fatal("non-MKV behavior changed")
	}
}

func TestMKVSampleRejectsMalformedBoundsAndIncompleteAV(t *testing.T) {
	if _, err := mkvElementAt([]byte{0xec, 0x88, 1}, 0, 3); err == nil {
		t.Fatal("out-of-bounds element accepted")
	}
	if _, err := mkvSampleCluster(mkvTestJoin(mkvTestUInt(0xe7, 0), []byte{0xa3, 0x88, 0x81}), true, map[uint64]bool{}); err == nil {
		t.Fatal("truncated complete cluster accepted")
	}
	if err := mkvRequireAVPackets([]mkvProbeTrack{{number: 1, kind: 1}, {number: 2, kind: 2}}, map[uint64]bool{1: true}); err == nil {
		t.Fatal("missing audio packet accepted")
	}
}
