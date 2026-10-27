package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

func TestMediaProbeQueueSnapshotShowsEpisodeAndPersistedProbeParameters(t *testing.T) {
	queue, probe := newExplicitProbeQueue(t)
	seedExplicitProbeMedia(t, queue, "episode")
	if err := queue.repo.DB.Model(&model.Media{}).Where("id = ?", "episode").Updates(map[string]any{
		"title": "Synthetic Show", "season_num": 2, "episode_num": 7, "episode_end_num": 8, "episode_title": "Synthetic episode",
		"path": "https://synthetic.invalid/Show.S02E07.mkv?token=PRIVATE",
	}).Error; err != nil {
		t.Fatal(err)
	}
	probe.result = &ProbeResult{DurationSec: 1440, Width: 3840, Height: 1600, VideoCodec: "hevc", AudioCodec: "eac3", Container: "matroska", BitRate: 24000000, FrameRate: 23.976, VideoRange: "HDR10", VideoBitDepth: 10, AudioChannels: 6, AudioSampleRate: 48000}
	startExplicitQueue(t, queue)
	waitExplicitQueueStopped(t, queue)
	snapshot, err := queue.Snapshot(t.Context(), "completed", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("items=%+v", snapshot.Items)
	}
	item := snapshot.Items[0]
	if item.SeasonNum != 2 || item.EpisodeNum != 7 || item.EpisodeEndNum != 8 || item.EpisodeTitle != "Synthetic episode" || item.FileName != "Show.S02E07.mkv" {
		t.Fatalf("identity=%+v", item)
	}
	if item.Result == nil || item.Result.Width != 3840 || item.Result.Height != 1600 || item.Result.FrameRate != 23.976 || item.Result.AudioChannels != 6 || item.Result.BitRate != 24000000 {
		t.Fatalf("result=%+v", item.Result)
	}
	payload, _ := json.Marshal(item)
	if strings.Contains(string(payload), "PRIVATE") || strings.Contains(string(payload), "synthetic.invalid") || strings.Contains(string(payload), "source_path") || strings.Contains(string(payload), "fingerprint") {
		t.Fatalf("source exposed: %s", payload)
	}
}

func TestMediaProbeQueueSnapshotDoesNotInventResultFromFilename(t *testing.T) {
	queue, _ := newExplicitProbeQueue(t)
	seedExplicitProbeMedia(t, queue, "pending")
	if err := queue.repo.DB.Model(&model.Media{}).Where("id = ?", "pending").Updates(map[string]any{"path": "/synthetic/Show.4K.2160p.mkv", "width": 1920, "height": 1080}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Collect(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := queue.Snapshot(t.Context(), "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Items[0].Result != nil {
		t.Fatalf("unprobed result invented: %+v", snapshot.Items[0].Result)
	}
}

func TestMediaProbeQueueSingleRetryRequiresConfirmationAndDoesNotProcessOtherPendingJobs(t *testing.T) {
	queue, probe := newExplicitProbeQueue(t)
	seedExplicitProbeMedia(t, queue, "a-pending", "b-failed")
	if _, err := queue.Collect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := queue.repo.DB.Model(&model.MediaProbeJob{}).Where("media_id = ?", "b-failed").Updates(map[string]any{"status": "failed", "attempts": 1, "error": "previous timeout", "failure_kind": "timeout"}).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := queue.CreateRetryPlan(t.Context(), "b-failed")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Count != 1 || plan.RetryMediaID != "b-failed" || probe.count() != 0 {
		t.Fatalf("plan=%+v calls=%d", plan, probe.count())
	}
	var job model.MediaProbeJob
	if err := queue.repo.DB.First(&job, "media_id = ?", "b-failed").Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != "failed" || job.Attempts != 1 || job.Error != "previous timeout" {
		t.Fatalf("preview mutated job: %+v", job)
	}
	if err := queue.Start(t.Context(), plan.ID, false); err == nil {
		t.Fatal("unconfirmed retry accepted")
	}
	if err := queue.Start(t.Context(), plan.ID, true); err != nil {
		t.Fatal(err)
	}
	waitExplicitQueueStopped(t, queue)
	snapshot, err := queue.Snapshot(t.Context(), "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if probe.count() != 1 || snapshot.Counts["completed"] != 1 || snapshot.Counts["pending"] != 1 {
		t.Fatalf("expanded retry: calls=%d counts=%v", probe.count(), snapshot.Counts)
	}
	if err := queue.repo.DB.First(&job, "media_id = ?", "b-failed").Error; err != nil {
		t.Fatal(err)
	}
	if job.Attempts != 2 || job.Status != "completed" || job.FailureKind != "" || job.Error != "" {
		t.Fatalf("retry outcome=%+v", job)
	}
	if err := queue.Start(t.Context(), plan.ID, true); err == nil {
		t.Fatal("consumed retry accepted")
	}
}

func TestMediaProbeQueueSingleRetryRejectsChangedFileAndNonFailedRecord(t *testing.T) {
	queue, probe := newExplicitProbeQueue(t)
	seedExplicitProbeMedia(t, queue, "a")
	if _, err := queue.Collect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.CreateRetryPlan(t.Context(), "a"); err == nil {
		t.Fatal("pending job accepted as failed")
	}
	if err := queue.repo.DB.Model(&model.MediaProbeJob{}).Where("media_id = ?", "a").Update("status", "failed").Error; err != nil {
		t.Fatal(err)
	}
	plan, err := queue.CreateRetryPlan(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.repo.DB.Model(&model.Media{}).Where("id = ?", "a").Update("size_bytes", 2048).Error; err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(t.Context(), plan.ID, true); err == nil {
		t.Fatal("changed source accepted")
	}
	if _, err := queue.CreateRetryPlan(t.Context(), "a"); err == nil {
		t.Fatal("stale fingerprint accepted")
	}
	if probe.count() != 0 {
		t.Fatal("validation read media")
	}
}
