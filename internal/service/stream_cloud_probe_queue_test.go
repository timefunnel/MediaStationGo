package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/service/cloud"
)

type playbackQueueProber struct {
	mu      sync.Mutex
	calls   int
	rawURL  string
	headers map[string]string
	result  *ProbeResult
	err     error
	started chan struct{}
	release chan struct{}
}

func (p *playbackQueueProber) ProbeHTTP(ctx context.Context, rawURL string, headers map[string]string) (*ProbeResult, error) {
	p.mu.Lock()
	p.calls++
	p.rawURL = rawURL
	p.headers = cloneStringMap(headers)
	started := p.started
	release := p.release
	result := p.result
	err := p.err
	p.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return result, err
}

func (p *playbackQueueProber) snapshot() (int, string, map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.rawURL, cloneStringMap(p.headers)
}

func cloneStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func newPlaybackProbeTestService(t *testing.T, mediaID string, prober cloudPlaybackProber) (*StreamService, *streamCloudResolver) {
	t.Helper()
	repos := newStreamTestRepo(t)
	if sqlDB, err := repos.DB.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := repos.Setting.Set(t.Context(), CloudPlaybackModeSettingKey, CloudPlaybackModeSTRM); err != nil {
		t.Fatal(err)
	}
	if err := repos.Setting.Set(t.Context(), PlaybackCloudProbeEnabledSettingKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := repos.DB.Create(&model.Media{
		Base:    model.Base{ID: mediaID},
		Title:   "Cloud",
		Path:    "cloud://openlist/Movie.mkv",
		STRMURL: "/api/cloud/play/openlist?ref=movie",
	}).Error; err != nil {
		t.Fatal(err)
	}
	resolver := &streamCloudResolver{link: &cloud.DirectLink{
		URL: "https://video.115cdn.net/movie.mkv?sign=cdn",
		Headers: map[string]string{
			"Referer":    "https://openlist.example.test/",
			"User-Agent": "provider-placeholder",
		},
	}}
	svc := NewStreamService(&config.Config{}, zap.NewNop(), repos, nil)
	svc.SetCloudProbe(resolver)
	svc.SetCloudTrackProbe(prober)
	return svc, resolver
}

func waitPlaybackProbeStarted(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("playback metadata probe did not start")
	}
}

func waitPlaybackProbePersisted(t *testing.T, svc *StreamService, mediaID string) model.Media {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var media model.Media
		if err := svc.repo.DB.First(&media, "id = ?", mediaID).Error; err != nil {
			t.Fatal(err)
		}
		if media.MediaProbeVersion == mediaProbeMetadataVersion {
			return media
		}
		if time.Now().After(deadline) {
			t.Fatalf("playback metadata probe was not persisted: %#v", media)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitPlaybackProbeBackoff(t *testing.T, svc *StreamService, mediaID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		svc.cloudTrackProbeMu.Lock()
		until, ok := svc.cloudTrackProbeBackoff[mediaID]
		svc.cloudTrackProbeMu.Unlock()
		if ok && until.After(time.Now()) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("failed playback metadata probe did not enter backoff")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSTRMGETReusesResolvedLinkForAsyncTrackProbe(t *testing.T) {
	started := make(chan struct{}, 1)
	prober := &playbackQueueProber{
		started: started,
		result: &ProbeResult{
			DurationSec: 3661,
			Width:       3840,
			Height:      2160,
			VideoCodec:  "hevc",
			AudioCodec:  "eac3",
			Container:   "matroska,webm",
		},
	}
	svc, resolver := newPlaybackProbeTestService(t, "cloud-probe-get", prober)
	req := httptest.NewRequest(http.MethodGet, "http://nas.local/api/stream/cloud-probe-get", nil)
	req.Header.Set("User-Agent", "Filmly/1.0")
	w := httptest.NewRecorder()

	if err := svc.ServeFile(w, req, "cloud-probe-get"); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusFound || w.Header().Get("Location") != resolver.link.URL {
		t.Fatalf("playback redirect = status %d location %q", w.Code, w.Header().Get("Location"))
	}
	waitPlaybackProbeStarted(t, started)
	persisted := waitPlaybackProbePersisted(t, svc, "cloud-probe-get")
	if persisted.DurationSec != 3661 || persisted.VideoCodec != "hevc" || persisted.AudioCodec != "eac3" {
		t.Fatalf("persisted metadata = %#v", persisted)
	}
	calls, rawURL, headers := prober.snapshot()
	if resolver.calls != 1 {
		t.Fatalf("one playback issued %d CloudResolve calls, want 1", resolver.calls)
	}
	if calls != 1 || rawURL != resolver.link.URL {
		t.Fatalf("probe calls=%d url=%q, want the already resolved URL", calls, rawURL)
	}
	if headers["User-Agent"] != "Filmly/1.0" || headers["Referer"] != "https://openlist.example.test/" {
		t.Fatalf("probe headers did not reuse playback binding: %#v", headers)
	}
	if resolver.link.Headers["User-Agent"] != "provider-placeholder" {
		t.Fatalf("resolved link headers were mutated: %#v", resolver.link.Headers)
	}

	second := httptest.NewRecorder()
	if err := svc.ServeFile(second, req.Clone(t.Context()), "cloud-probe-get"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	calls, _, _ = prober.snapshot()
	if calls != 1 {
		t.Fatalf("completed metadata was probed again, calls=%d", calls)
	}
}

func TestSTRMHEADDoesNotQueueTrackProbe(t *testing.T) {
	started := make(chan struct{}, 1)
	prober := &playbackQueueProber{started: started, result: &ProbeResult{DurationSec: 120}}
	svc, resolver := newPlaybackProbeTestService(t, "cloud-probe-head", prober)
	req := httptest.NewRequest(http.MethodHead, "http://nas.local/api/stream/cloud-probe-head", nil)
	req.Header.Set("User-Agent", "Filmly/1.0")
	w := httptest.NewRecorder()

	if err := svc.ServeFile(w, req, "cloud-probe-head"); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusFound || resolver.calls != 1 {
		t.Fatalf("HEAD playback status=%d resolve_calls=%d", w.Code, resolver.calls)
	}
	select {
	case <-started:
		t.Fatal("HEAD request must not queue a metadata probe")
	case <-time.After(100 * time.Millisecond):
	}
	if calls, _, _ := prober.snapshot(); calls != 0 {
		t.Fatalf("HEAD probe calls=%d, want 0", calls)
	}
}

func TestSTRMPlaybackProbeDeduplicatesWhileInFlight(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	prober := &playbackQueueProber{
		started: started,
		release: release,
		result:  &ProbeResult{DurationSec: 120, Width: 1920, Height: 1080, VideoCodec: "h264", AudioCodec: "aac"},
	}
	svc, resolver := newPlaybackProbeTestService(t, "cloud-probe-dedupe", prober)

	firstReq := httptest.NewRequest(http.MethodGet, "http://nas.local/api/stream/cloud-probe-dedupe", nil)
	firstReq.Header.Set("User-Agent", "Filmly/1.0")
	if err := svc.ServeFile(httptest.NewRecorder(), firstReq, "cloud-probe-dedupe"); err != nil {
		t.Fatal(err)
	}
	waitPlaybackProbeStarted(t, started)
	secondReq := firstReq.Clone(t.Context())
	if err := svc.ServeFile(httptest.NewRecorder(), secondReq, "cloud-probe-dedupe"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if calls, _, _ := prober.snapshot(); calls != 1 {
		t.Fatalf("in-flight duplicate started %d probes, want 1", calls)
	}
	if resolver.calls != 2 {
		t.Fatalf("two real playback requests should each resolve normally, calls=%d", resolver.calls)
	}
	close(release)
	waitPlaybackProbePersisted(t, svc, "cloud-probe-dedupe")
}

func TestSTRMPlaybackProbeFailureDoesNotRetry(t *testing.T) {
	started := make(chan struct{}, 2)
	prober := &playbackQueueProber{started: started, err: errors.New("probe failed")}
	svc, resolver := newPlaybackProbeTestService(t, "cloud-probe-fail", prober)

	firstReq := httptest.NewRequest(http.MethodGet, "http://nas.local/api/stream/cloud-probe-fail", nil)
	firstReq.Header.Set("User-Agent", "Filmly/1.0")
	if err := svc.ServeFile(httptest.NewRecorder(), firstReq, "cloud-probe-fail"); err != nil {
		t.Fatal(err)
	}
	waitPlaybackProbeStarted(t, started)
	waitPlaybackProbeBackoff(t, svc, "cloud-probe-fail")
	if err := svc.ServeFile(httptest.NewRecorder(), firstReq.Clone(t.Context()), "cloud-probe-fail"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if calls, _, _ := prober.snapshot(); calls != 1 {
		t.Fatalf("failed probe retried automatically, calls=%d", calls)
	}
	if resolver.calls != 2 {
		t.Fatalf("backoff must not block real playback resolution, calls=%d", resolver.calls)
	}
}

func TestSTRMPlaybackIncompleteProbeBacksOff(t *testing.T) {
	started := make(chan struct{}, 2)
	prober := &playbackQueueProber{started: started, result: &ProbeResult{DurationSec: 120}}
	svc, _ := newPlaybackProbeTestService(t, "cloud-probe-incomplete", prober)

	request := httptest.NewRequest(http.MethodGet, "http://nas.local/api/stream/cloud-probe-incomplete", nil)
	request.Header.Set("User-Agent", "Filmly/1.0")
	if err := svc.ServeFile(httptest.NewRecorder(), request, "cloud-probe-incomplete"); err != nil {
		t.Fatal(err)
	}
	waitPlaybackProbeStarted(t, started)
	waitPlaybackProbeBackoff(t, svc, "cloud-probe-incomplete")
	if err := svc.ServeFile(httptest.NewRecorder(), request.Clone(t.Context()), "cloud-probe-incomplete"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if calls, _, _ := prober.snapshot(); calls != 1 {
		t.Fatalf("incomplete probe retried without backoff, calls=%d", calls)
	}
}

func TestPlaybackProbeQueueFullDropsTaskAndBacksOff(t *testing.T) {
	repos := newStreamTestRepo(t)
	if err := repos.Setting.Set(t.Context(), PlaybackCloudProbeEnabledSettingKey, "true"); err != nil {
		t.Fatal(err)
	}
	svc := NewStreamService(&config.Config{}, zap.NewNop(), repos, nil)
	svc.cloudTrackProbeQueue = make(chan playbackCloudProbeTask)
	svc.cloudTrackProbeMu.Lock()
	svc.probe = &playbackQueueProber{}
	svc.cloudTrackProbeMu.Unlock()
	media := &model.Media{Base: model.Base{ID: "cloud-probe-full"}}
	link := &cloud.DirectLink{URL: "https://video.115cdn.net/movie.mkv?sign=cdn"}

	if svc.enqueuePlaybackCloudProbe(t.Context(), media, link, "Filmly/1.0") {
		t.Fatal("full queue should drop the probe task")
	}
	waitPlaybackProbeBackoff(t, svc, media.ID)
	if calls, _, _ := svc.probe.(*playbackQueueProber).snapshot(); calls != 0 {
		t.Fatalf("dropped queue task unexpectedly ran, calls=%d", calls)
	}
}

func TestSTRMPlaybackProbeDisabledStillRedirects(t *testing.T) {
	for _, value := range []string{"", "false", "invalid"} {
		t.Run("setting="+value, func(t *testing.T) {
			prober := &playbackQueueProber{}
			svc, resolver := newPlaybackProbeTestService(t, "probe-disabled", prober)
			var err error
			if value == "" {
				err = svc.repo.Setting.Delete(t.Context(), PlaybackCloudProbeEnabledSettingKey)
			} else {
				err = svc.repo.Setting.Set(t.Context(), PlaybackCloudProbeEnabledSettingKey, value)
			}
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/stream/probe-disabled", nil)
			response := httptest.NewRecorder()
			if err := svc.ServeFile(response, request, "probe-disabled"); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusFound || response.Header().Get("Location") != resolver.link.URL || resolver.calls != 1 {
				t.Fatalf("playback changed when probe disabled: status=%d resolves=%d", response.Code, resolver.calls)
			}
			svc.cloudTrackProbeMu.Lock()
			pending := len(svc.cloudTrackProbePending)
			svc.cloudTrackProbeMu.Unlock()
			if calls, _, _ := prober.snapshot(); calls != 0 || pending != 0 {
				t.Fatalf("disabled playback probe calls=%d pending=%d", calls, pending)
			}
		})
	}
}

func waitPlaybackProbesIdle(t *testing.T, svc *StreamService) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		svc.cloudTrackProbeMu.Lock()
		pending := len(svc.cloudTrackProbePending)
		cancels := len(svc.cloudTrackProbeCancels)
		svc.cloudTrackProbeMu.Unlock()
		if pending == 0 && cancels == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe tasks did not stop: pending=%d cancels=%d", pending, cancels)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPlaybackProbeDisableCancelsActiveAndQueuedAndCanReenable(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	prober := &playbackQueueProber{started: started, release: release,
		result: &ProbeResult{DurationSec: 120, Width: 1920, Height: 1080, VideoCodec: "h264", AudioCodec: "aac"}}
	svc, resolver := newPlaybackProbeTestService(t, "probe-active", prober)
	if err := svc.repo.DB.Create(&model.Media{Base: model.Base{ID: "probe-queued"}, Path: "cloud://openlist/queued.mkv"}).Error; err != nil {
		t.Fatal(err)
	}
	var active, queued model.Media
	if err := svc.repo.DB.First(&active, "id = ?", "probe-active").Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.repo.DB.First(&queued, "id = ?", "probe-queued").Error; err != nil {
		t.Fatal(err)
	}
	if !svc.enqueuePlaybackCloudProbe(t.Context(), &active, resolver.link, "test") {
		t.Fatal("enabled active task rejected")
	}
	waitPlaybackProbeStarted(t, started)
	if !svc.enqueuePlaybackCloudProbe(t.Context(), &queued, resolver.link, "test") {
		t.Fatal("enabled queued task rejected")
	}
	if err := svc.repo.Setting.Set(t.Context(), PlaybackCloudProbeEnabledSettingKey, "false"); err != nil {
		t.Fatal(err)
	}
	svc.CancelPlaybackCloudProbes()
	waitPlaybackProbesIdle(t, svc)
	if calls, _, _ := prober.snapshot(); calls != 1 {
		t.Fatalf("canceled queued probe ran: calls=%d", calls)
	}
	var updated model.Media
	if err := svc.repo.DB.First(&updated, "id = ?", active.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.MediaProbeVersion != 0 || len(svc.cloudTrackProbeBackoff) != 0 {
		t.Fatal("disabling probe persisted a result or created failure backoff")
	}
	close(release)
	if err := svc.repo.Setting.Set(t.Context(), PlaybackCloudProbeEnabledSettingKey, "true"); err != nil {
		t.Fatal(err)
	}
	if !svc.enqueuePlaybackCloudProbe(t.Context(), &active, resolver.link, "test") {
		t.Fatal("re-enabled probe rejected")
	}
	waitPlaybackProbePersisted(t, svc, active.ID)
}

func TestPlaybackProbeWorkerRechecksPersistedSwitch(t *testing.T) {
	prober := &playbackQueueProber{}
	svc, resolver := newPlaybackProbeTestService(t, "probe-recheck", prober)
	// Stop automatic workers for this test so the setting changes after enqueue.
	queued := NewStreamService(&config.Config{}, zap.NewNop(), svc.repo, nil)
	queued.probe = prober
	var media model.Media
	if err := svc.repo.DB.First(&media, "id = ?", "probe-recheck").Error; err != nil {
		t.Fatal(err)
	}
	if !queued.enqueuePlaybackCloudProbe(t.Context(), &media, resolver.link, "test") {
		t.Fatal("enabled task rejected")
	}
	if err := svc.repo.Setting.Set(t.Context(), PlaybackCloudProbeEnabledSettingKey, "false"); err != nil {
		t.Fatal(err)
	}
	queued.runPlaybackCloudProbe(<-queued.cloudTrackProbeQueue)
	waitPlaybackProbesIdle(t, queued)
	if calls, _, _ := prober.snapshot(); calls != 0 {
		t.Fatal("queued probe ignored persisted disabled switch")
	}
}
