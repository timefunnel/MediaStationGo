package service

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

type postprocessResourcePipeline struct {
	*fakeResourcePipeline
	read func(string, string) (resourcePipelineTask, error)
}

func (p *postprocessResourcePipeline) GetImport(_ context.Context, owner, id string) (resourcePipelineTask, error) {
	return p.read(owner, id)
}

func waitResourceImportPostprocess(t *testing.T, svc *ResourceImportService, id string, ready func(model.ResourceImportJob) bool) model.ResourceImportJob {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		job, err := svc.loadOwnedJob(t.Context(), "", true, id)
		if err != nil {
			t.Fatal(err)
		}
		if ready(job) {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("postprocess state did not converge: status=%s error=%q result=%s", job.Status, job.Error, job.ResultJSON)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestResourceImportMonitorTracksPostprocessWithoutBlockingImports(t *testing.T) {
	for _, outcome := range []string{"success", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			pipeline := &fakeResourcePipeline{}
			svc, repos, library, root, _, user := newResourceImportTestService(t, pipeline)
			svc.globalSem = make(chan struct{}, 1)
			svc.cfg.MaxConcurrentPerUser = 1
			var finish atomic.Bool
			svc.client = &postprocessResourcePipeline{fakeResourcePipeline: pipeline, read: func(owner, id string) (resourcePipelineTask, error) {
				child := resourcePipelineTask{ID: id, OwnerID: owner, Status: "completed", Stage: "completed", MsgMediaID: "media-" + id}
				if id == "enhancing" {
					postStatus := "running"
					if finish.Load() {
						postStatus = outcome
						if outcome == "failed" {
							child.Status, child.Stage, child.Error = "completed_with_warning", "completed_with_warning", "metadata scrape no_match"
						}
					}
					child.Result = map[string]any{"task": map[string]any{"post_enhancement_status": postStatus}}
				}
				return child, nil
			}}
			jobs := make([]model.ResourceImportJob, 2)
			for i, id := range []string{"enhancing", "next-import"} {
				jobs[i] = model.ResourceImportJob{
					UserID: user.ID, LibraryID: library.ID, LibraryRootID: root.ID,
					SearchSessionID: "search", CandidateJSON: "{}", CandidateTitle: "Example",
					IdempotencyKey: id, PipelineJobID: id, Status: "running", Stage: "scanning", Attempt: 1,
				}
				if err := repos.DB.Create(&jobs[i]).Error; err != nil {
					t.Fatal(err)
				}
			}
			svc.schedule(jobs[0].ID)
			waitResourceImportPostprocess(t, svc, jobs[0].ID, func(job model.ResourceImportJob) bool {
				return job.Status == "completed" && strings.Contains(job.ResultJSON, `"post_enhancement_status":"running"`)
			})
			svc.schedule(jobs[1].ID)
			waitResourceImportPostprocess(t, svc, jobs[1].ID, func(job model.ResourceImportJob) bool { return job.Status == "completed" })
			finish.Store(true)
			final := waitResourceImportPostprocess(t, svc, jobs[0].ID, func(job model.ResourceImportJob) bool {
				return strings.Contains(job.ResultJSON, `"post_enhancement_status":"`+outcome+`"`)
			})
			if outcome == "failed" {
				if final.Status != "completed_with_warning" || final.Error != "metadata scrape no_match" {
					t.Fatalf("postprocess failure was not reflected in parent: %+v", final)
				}
			} else if final.Status != "completed" || final.Error != "" {
				t.Fatalf("successful postprocess = %+v", final)
			}
			if pipeline.createCalls != 0 || len(pipeline.retriedIDs) != 0 || len(pipeline.canceledOwners) != 0 {
				t.Fatal("postprocess monitoring must only read the existing pipeline job")
			}
		})
	}
}

func TestResourceImportRecoverTracksPendingPostprocess(t *testing.T) {
	for _, phase := range []string{"pending", "running"} {
		t.Run(phase, func(t *testing.T) {
			pipeline := &fakeResourcePipeline{getResponses: []resourcePipelineTask{{
				Status: "completed_with_warning", Stage: "completed_with_warning", MsgMediaID: "existing-media", Error: "metadata scrape no_match",
				Result: map[string]any{"task": map[string]any{"post_enhancement_status": "failed"}},
			}}}
			svc, repos, library, root, _, user := newResourceImportTestService(t, pipeline)
			var pending model.ResourceImportJob
			for _, state := range []string{phase, "success", "failed"} {
				job := model.ResourceImportJob{
					UserID: user.ID, LibraryID: library.ID, LibraryRootID: root.ID,
					SearchSessionID: "search", CandidateJSON: "{}", CandidateTitle: "Example",
					IdempotencyKey: state, PipelineJobID: "pipeline-" + state, Status: "completed", Stage: "completed", Attempt: 1,
					MediaID: "existing-media", ResultJSON: `{"task":{"post_enhancement_status":"` + state + `"}}`,
				}
				if err := repos.DB.Create(&job).Error; err != nil {
					t.Fatal(err)
				}
				if state == phase {
					pending = job
				}
			}
			if count, err := svc.Recover(t.Context()); err != nil || count != 1 {
				t.Fatalf("Recover() = %d, %v; want one pending postprocess", count, err)
			}
			waitResourceImportPostprocess(t, svc, pending.ID, func(job model.ResourceImportJob) bool { return job.Status == "completed_with_warning" })
			pipeline.mu.Lock()
			defer pipeline.mu.Unlock()
			if pipeline.getCalls != 1 || pipeline.createCalls != 0 || len(pipeline.retriedIDs) != 0 {
				t.Fatalf("recovery should only read the pending job: reads=%d creates=%d retries=%v", pipeline.getCalls, pipeline.createCalls, pipeline.retriedIDs)
			}
		})
	}
}

func TestResourceImportRecoverReportsInvalidPostprocessResult(t *testing.T) {
	svc, repos, library, root, _, user := newResourceImportTestService(t, &fakeResourcePipeline{})
	job := model.ResourceImportJob{
		UserID: user.ID, LibraryID: library.ID, LibraryRootID: root.ID,
		SearchSessionID: "search", CandidateJSON: "{}", CandidateTitle: "Example", IdempotencyKey: "invalid-result",
		PipelineJobID: "pipeline-invalid", Status: "completed", Stage: "completed", ResultJSON: `{"task":{"post_enhancement_status":`,
	}
	if err := repos.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Recover(t.Context()); err == nil {
		t.Fatal("invalid postprocess result must be reported")
	}
	if err := repos.DB.Model(&job).Updates(map[string]any{
		"result_json": `{"task":{"post_enhancement_status":"pending"}}`, "pipeline_job_id": "",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Recover(t.Context()); err == nil || !strings.Contains(err.Error(), "missing pipeline_job_id") {
		t.Fatalf("missing pipeline id must be reported instead of recreating the import: %v", err)
	}
}

func TestResourceImportPostprocessPollCannotOverwriteManualRetry(t *testing.T) {
	pipeline := &fakeResourcePipeline{}
	svc, repos, library, root, _, user := newResourceImportTestService(t, pipeline)
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	svc.client = &postprocessResourcePipeline{fakeResourcePipeline: pipeline, read: func(owner, id string) (resourcePipelineTask, error) {
		if reads.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-svc.ctx.Done():
				return resourcePipelineTask{}, svc.ctx.Err()
			}
			return resourcePipelineTask{ID: id, OwnerID: owner, Status: "completed_with_warning", Stage: "completed_with_warning", MsgMediaID: "old-media", Error: "stale enhancement failure"}, nil
		}
		return resourcePipelineTask{ID: id, OwnerID: owner, Status: "completed", Stage: "completed", MsgMediaID: "retried-media"}, nil
	}}
	job := model.ResourceImportJob{
		UserID: user.ID, LibraryID: library.ID, LibraryRootID: root.ID,
		SearchSessionID: "search", CandidateJSON: "{}", CandidateTitle: "Example", IdempotencyKey: "retry-while-polling",
		PipelineJobID: "pipeline-retry", Status: "completed_with_warning", Stage: "completed", Attempt: 1,
		ResultJSON: `{"task":{"post_enhancement_status":"running"}}`,
	}
	if err := repos.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	svc.schedule(job.ID)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("postprocess poll did not start")
	}
	if _, err := svc.Retry(t.Context(), user.ID, false, job.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	final := waitResourceImportPostprocess(t, svc, job.ID, func(job model.ResourceImportJob) bool { return job.Status == "completed" })
	if final.Attempt != 2 || final.MediaID != "retried-media" || final.Error != "" {
		t.Fatalf("old poll replaced the manual retry: %+v", final)
	}
}
