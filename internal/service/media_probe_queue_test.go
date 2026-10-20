package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"github.com/ShukeBta/MediaStationGo/internal/service/cloud"
)

type explicitQueueProber struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	result  *ProbeResult
	fail    bool
}

func (p *explicitQueueProber) Probe(ctx context.Context, _ string) (*ProbeResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.fail {
		return nil, errors.New("media read failed https://private.example/file?token=secret")
	}
	if p.result != nil {
		return p.result, nil
	}
	return &ProbeResult{DurationSec: 120, Width: 1920, Height: 1080, VideoCodec: "hevc", AudioCodec: "aac"}, nil
}
func (p *explicitQueueProber) ProbeHTTP(ctx context.Context, raw string, _ map[string]string) (*ProbeResult, error) {
	return p.Probe(ctx, raw)
}
func (p *explicitQueueProber) count() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

func newExplicitProbeQueue(t *testing.T) (*MediaProbeQueueService, *explicitQueueProber) {
	t.Helper()
	db := newServiceTestDB(t, &model.Media{}, &model.Library{}, &model.Setting{}, &model.MediaProbeJob{})
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	repos := repository.New(db)
	stream := NewStreamService(&config.Config{}, zap.NewNop(), repos, nil)
	probe := &explicitQueueProber{}
	queue := NewMediaProbeQueueService(repos, stream, probe, zap.NewNop())
	t.Cleanup(func() { queue.Pause(); waitExplicitQueueStopped(t, queue) })
	return queue, probe
}

func seedExplicitProbeMedia(t *testing.T, queue *MediaProbeQueueService, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := queue.repo.DB.Create(&model.Media{Base: model.Base{ID: id}, Title: "Synthetic " + id, Path: "/synthetic/" + id + ".mkv", LibraryID: "library-a", SizeBytes: 1024}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func waitExplicitQueueStopped(t *testing.T, queue *MediaProbeQueueService) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		queue.mu.Lock()
		running := queue.running
		queue.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("queue did not stop")
}

func startExplicitQueue(t *testing.T, queue *MediaProbeQueueService) *MediaProbeQueuePlan {
	t.Helper()
	plan, err := queue.CreatePlan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(t.Context(), plan.ID, true); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestMediaProbeQueueCollectIsAutomaticDatabaseOnlyAndDoesNotRetry(t *testing.T) {
	queue, probe := newExplicitProbeQueue(t)
	seedExplicitProbeMedia(t, queue, "a", "b", "complete")
	complete := map[string]any{"media_probe_version": 1, "duration_sec": 120, "width": 1920, "height": 1080, "video_codec": "hevc", "audio_codec": "aac"}
	if err := queue.repo.DB.Model(&model.Media{}).Where("id = ?", "complete").Updates(complete).Error; err != nil {
		t.Fatal(err)
	}
	queue.collectAutomatically(t.Context())
	snap, err := queue.Snapshot(t.Context(), "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Counts["pending"] != 2 || probe.count() != 0 {
		t.Fatalf("snapshot=%+v calls=%d", snap.Counts, probe.count())
	}
	if err := queue.repo.DB.Model(&model.MediaProbeJob{}).Where("media_id = ?", "a").Updates(map[string]any{"status": "failed", "attempts": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := queue.Collect(t.Context()); err != nil || n != 0 {
		t.Fatalf("repeat collect: added=%d err=%v", n, err)
	}
	var job model.MediaProbeJob
	if err := queue.repo.DB.First(&job, "media_id = ?", "a").Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != "failed" || job.Attempts != 1 {
		t.Fatalf("failed job retried: %+v", job)
	}
	if err := queue.repo.DB.Model(&model.Media{}).Where("id = ?", "a").Update("size_bytes", 2048).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := queue.Collect(t.Context()); err != nil || n != 1 {
		t.Fatalf("changed file: added=%d err=%v", n, err)
	}
	if err := queue.repo.DB.First(&job, "media_id = ?", "a").Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != "pending" || job.Attempts != 0 {
		t.Fatalf("changed source not reset: %+v", job)
	}
}

func TestMediaProbeQueueRequiresConfirmedFrozenPlan(t *testing.T) {
	queue, probe := newExplicitProbeQueue(t)
	seedExplicitProbeMedia(t, queue, "a", "b")
	cfg := defaultMediaProbeQueueConfig()
	cfg.BatchSize = 1
	if err := queue.SaveConfig(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	plan, err := queue.CreatePlan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Count != 1 || plan.Concurrency != 1 || plan.LocalCount != 1 {
		t.Fatalf("plan=%+v", plan)
	}
	if err := queue.Start(t.Context(), plan.ID, false); err == nil {
		t.Fatal("unconfirmed plan accepted")
	}
	if err := queue.Start(t.Context(), "invented", true); err == nil {
		t.Fatal("unknown plan accepted")
	}
	seedExplicitProbeMedia(t, queue, "c")
	if _, err := queue.Collect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if probe.count() != 0 {
		t.Fatal("collect or preview accessed media")
	}
	if err := queue.Start(t.Context(), plan.ID, true); err != nil {
		t.Fatal(err)
	}
	waitExplicitQueueStopped(t, queue)
	if probe.count() != 1 {
		t.Fatalf("confirmed batch expanded: calls=%d", probe.count())
	}
	snap, err := queue.Snapshot(t.Context(), "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Counts["completed"] != 1 || snap.Counts["pending"] != 2 {
		t.Fatalf("counts=%v", snap.Counts)
	}
	if err := queue.Start(t.Context(), plan.ID, true); err == nil {
		t.Fatal("consumed plan reused")
	}
}

func TestMediaProbeQueueFailureStopsBatchAndRetryOnlyRequeues(t *testing.T) {
	queue, probe := newExplicitProbeQueue(t)
	probe.fail = true
	seedExplicitProbeMedia(t, queue, "a", "b")
	startExplicitQueue(t, queue)
	waitExplicitQueueStopped(t, queue)
	if probe.count() != 1 {
		t.Fatalf("continued after failure: %d", probe.count())
	}
	snap, err := queue.Snapshot(t.Context(), "failed", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Items) != 1 || snap.Counts["pending"] != 1 || strings.Contains(snap.Items[0].Error, "secret") {
		t.Fatalf("snapshot=%+v", snap)
	}
	if _, err := queue.Collect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := queue.UpdateJob(t.Context(), snap.Items[0].MediaID, "retry"); err != nil {
		t.Fatal(err)
	}
	if probe.count() != 1 {
		t.Fatal("requeue triggered a read")
	}
}

func TestMediaProbeQueuePauseCancelsReadAndRecoveryDoesNotResume(t *testing.T) {
	queue, probe := newExplicitProbeQueue(t)
	probe.started = make(chan struct{}, 1)
	probe.release = make(chan struct{})
	seedExplicitProbeMedia(t, queue, "a", "b")
	startExplicitQueue(t, queue)
	select {
	case <-probe.started:
	case <-time.After(time.Second):
		t.Fatal("probe not started")
	}
	queue.Pause()
	waitExplicitQueueStopped(t, queue)
	snap, err := queue.Snapshot(t.Context(), "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Counts["interrupted"] != 1 || snap.Counts["pending"] != 1 {
		t.Fatalf("counts=%v", snap.Counts)
	}
	if err := queue.repo.DB.Model(&model.MediaProbeJob{}).Where("media_id = ?", "a").Update("status", "running").Error; err != nil {
		t.Fatal(err)
	}
	restarted := NewMediaProbeQueueService(queue.repo, queue.stream, probe, zap.NewNop())
	if err := restarted.recoverInterrupted(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Collect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if probe.count() != 1 {
		t.Fatal("restart reissued media read")
	}
}

func TestMediaProbeQueuePartialResultIsDurableAndNotAutomaticallyRepeated(t *testing.T) {
	queue, probe := newExplicitProbeQueue(t)
	probe.result = &ProbeResult{DurationSec: 300, Width: 1920, Height: 1080, VideoCodec: "hevc"}
	seedExplicitProbeMedia(t, queue, "a")
	startExplicitQueue(t, queue)
	waitExplicitQueueStopped(t, queue)
	snap, err := queue.Snapshot(t.Context(), "partial", 1)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Counts["partial"] != 1 {
		t.Fatalf("counts=%v", snap.Counts)
	}
	var media model.Media
	if err := queue.repo.DB.First(&media, "id = ?", "a").Error; err != nil {
		t.Fatal(err)
	}
	if media.DurationSec != 300 || media.MediaProbeVersion != 1 {
		t.Fatalf("metadata not persisted: %+v", media)
	}
	if _, err := queue.Collect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.CreatePlan(t.Context()); err == nil {
		t.Fatal("partial result automatically scheduled again")
	}
}

func TestMediaProbeQueueRejectsSourceChangedDuringRead(t *testing.T) {
	queue, probe := newExplicitProbeQueue(t)
	probe.started = make(chan struct{}, 1)
	probe.release = make(chan struct{})
	seedExplicitProbeMedia(t, queue, "a")
	startExplicitQueue(t, queue)
	select {
	case <-probe.started:
	case <-time.After(time.Second):
		t.Fatal("probe not started")
	}
	if err := queue.repo.DB.Model(&model.Media{}).Where("id = ?", "a").Update("size_bytes", 9999).Error; err != nil {
		t.Fatal(err)
	}
	close(probe.release)
	waitExplicitQueueStopped(t, queue)
	var media model.Media
	if err := queue.repo.DB.First(&media, "id = ?", "a").Error; err != nil {
		t.Fatal(err)
	}
	if media.MediaProbeVersion != 0 || media.DurationSec != 0 {
		t.Fatal("old file metadata written onto replaced file")
	}
}

func TestMediaProbeQueueSharesPlaybackReservation(t *testing.T) {
	queue, probe := newExplicitProbeQueue(t)
	seedExplicitProbeMedia(t, queue, "a")
	if !queue.stream.reserveExplicitMediaProbe("a") {
		t.Fatal("reservation failed")
	}
	startExplicitQueue(t, queue)
	if err := queue.repo.DB.Model(&model.Media{}).Where("id = ?", "a").Updates(map[string]any{"media_probe_version": 1, "duration_sec": 120, "width": 1920, "height": 1080, "video_codec": "hevc", "audio_codec": "aac"}).Error; err != nil {
		t.Fatal(err)
	}
	queue.stream.finishPlaybackCloudProbe("a", 0)
	waitExplicitQueueStopped(t, queue)
	if probe.count() != 0 {
		t.Fatal("already-completed playback metadata probed again")
	}
}

func TestMediaProbeQueueLibraryScopeAndPlanInvalidation(t *testing.T) {
	queue, probe := newExplicitProbeQueue(t)
	if err := queue.repo.DB.Create(&model.Library{Base: model.Base{ID: "library-a"}, Name: "Synthetic"}).Error; err != nil {
		t.Fatal(err)
	}
	seedExplicitProbeMedia(t, queue, "a", "b")
	if err := queue.repo.DB.Model(&model.Media{}).Where("id = ?", "b").Update("library_id", "library-b").Error; err != nil {
		t.Fatal(err)
	}
	cfg := defaultMediaProbeQueueConfig()
	cfg.LibraryIDs = []string{"library-a"}
	if err := queue.SaveConfig(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	plan, err := queue.CreatePlan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Count != 1 {
		t.Fatalf("scope ignored: %d", plan.Count)
	}
	if err := queue.UpdateJob(t.Context(), "a", "cancel"); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(t.Context(), plan.ID, true); err == nil {
		t.Fatal("modified plan accepted")
	}
	cfg.IntervalSeconds = 0
	if err := queue.SaveConfig(t.Context(), cfg); err == nil {
		t.Fatal("unbounded configuration accepted")
	}
	if probe.count() != 0 {
		t.Fatal("validation caused media read")
	}
}

func TestMediaProbeQueueCloudSourceResolvesOnceAndPreservesUserAgent(t *testing.T) {
	queue, _ := newExplicitProbeQueue(t)
	seedExplicitProbeMedia(t, queue, "a")
	if err := queue.repo.DB.Model(&model.Media{}).Where("id = ?", "a").Update("strm_url", "/api/cloud/play/openlist?ref=%2Fsynthetic%2Fa.mkv").Error; err != nil {
		t.Fatal(err)
	}
	resolver := &streamCloudResolver{link: &cloud.DirectLink{URL: "https://synthetic.invalid/a.mkv"}}
	probe := &streamProbeTestProber{result: &ProbeResult{DurationSec: 120, Width: 1920, Height: 1080, VideoCodec: "hevc", AudioCodec: "aac"}}
	queue.stream.SetCloudProbe(resolver)
	queue.probe = probe
	plan := startExplicitQueue(t, queue)
	waitExplicitQueueStopped(t, queue)
	if plan.OpenListCount != 1 || resolver.calls != 1 || probe.httpCalls != 1 || probe.localCalls != 0 {
		t.Fatalf("openlist=%d resolves=%d http=%d local=%d", plan.OpenListCount, resolver.calls, probe.httpCalls, probe.localCalls)
	}
	if resolver.ua != cloudMediaInternalUserAgent || probe.headers["User-Agent"] != cloudMediaInternalUserAgent {
		t.Fatalf("resolver UA=%q probe headers=%v", resolver.ua, probe.headers)
	}
}
