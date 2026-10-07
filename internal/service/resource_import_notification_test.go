package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestResourceImportCompletionNotificationsSeparateSources(t *testing.T) {
	for _, tt := range []struct {
		name            string
		follow          bool
		manualReplenish bool
		status          string
		noMedia         bool
		noNewEpisodes   bool
		events          string
		wantTitle       string
	}{
		{name: "manual import", status: "completed", wantTitle: "user-a-入库-Example"},
		{name: "manual replenishment", follow: true, manualReplenish: true, status: "completed", wantTitle: "user-a-入库-Example"},
		{name: "automatic follow", follow: true, status: "completed", wantTitle: "MediaStationGo 自动追更入库完成"},
		{name: "automatic follow with warning", follow: true, status: "completed_with_warning", wantTitle: "MediaStationGo 自动追更入库完成"},
		{name: "manual warning", status: "completed_with_warning", wantTitle: "user-a-入库-Example"},
		{name: "manual replenishment with no new episodes", follow: true, manualReplenish: true, status: "completed", noMedia: true, noNewEpisodes: true},
		{name: "completed without media", status: "completed", noMedia: true},
		{name: "scanning", status: "running"},
		{name: "failed", status: "failed"},
		{name: "canceled", status: "canceled"},
		{name: "unsubscribed manual channel", status: "completed", events: `["subscription_follow_completed"]`},
		{name: "unsubscribed follow channel", follow: true, status: "completed", events: `["library_ingest"]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, repos, library, root, _, user := newResourceImportTestService(t, &fakeResourcePipeline{})
			if err := repos.DB.AutoMigrate(&model.NotifyChannel{}, &model.Subscription{}); err != nil {
				t.Fatal(err)
			}
			job := model.ResourceImportJob{
				UserID: user.ID, LibraryID: library.ID, LibraryRootID: root.ID,
				SearchSessionID: "mock-search", CandidateJSON: `{}`, CandidateTitle: "Example",
				IdempotencyKey: "mock-import", PipelineJobID: "mock-pipeline-job", Attempt: 1,
				Status: ResourceImportStatusRunning, Stage: "scanning",
				SubscriptionFollow: tt.follow, ManualReplenish: tt.manualReplenish,
			}
			if tt.follow && !tt.manualReplenish {
				sub := model.Subscription{UserID: user.ID, Name: "Example", Enabled: true}
				if err := repos.DB.Create(&sub).Error; err != nil {
					t.Fatal(err)
				}
				job.SubscriptionID = sub.ID
			}
			if err := repos.DB.Create(&job).Error; err != nil {
				t.Fatal(err)
			}
			staleJob := job
			messages := make(chan string, 4)
			bark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var persisted model.ResourceImportJob
				if err := repos.DB.First(&persisted, "id = ?", job.ID).Error; err != nil {
					t.Errorf("load persisted import: %v", err)
				} else if (persisted.Status != ResourceImportStatusCompleted && persisted.Status != ResourceImportStatusCompletedWithWarning) || persisted.MediaID == "" {
					t.Errorf("notification sent before persisted completion: status=%s media=%s", persisted.Status, persisted.MediaID)
				}
				parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
				if r.Method != http.MethodGet || len(parts) != 3 || parts[0] != "mock-device-key" {
					t.Errorf("unexpected Bark request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if (!tt.follow || tt.manualReplenish) && (!strings.Contains(parts[2], "任务：Example\n") || strings.Contains(parts[2], "Scanner title")) {
					t.Errorf("notification body used scanner title instead of task name: %s", parts[2])
				}
				messages <- parts[1]
				_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
			}))
			defer bark.Close()
			events := tt.events
			if events == "" {
				events = `["library_ingest","subscription_follow_completed"]`
			}
			channel := model.NotifyChannel{Name: "Mock Bark", Type: "bark", Enabled: true, Events: events,
				Config: `{"server":"` + bark.URL + `","device_key":"mock-device-key"}`}
			if err := repos.NotifyChannel.Create(t.Context(), &channel); err != nil {
				t.Fatal(err)
			}
			notify := NewNotifyChannelService(zap.NewNop(), repos)
			svc.SetNotifyChannels(notify)
			subSvc := &SubscriptionService{repo: repos, notify: notify}
			svc.SetSubscriptionCompletionHandler(subSvc.completeResourceImportSubscription)
			child := resourcePipelineTask{Status: tt.status, Stage: tt.status, MsgMediaID: "mock-media", MsgMediaTitle: "Scanner title"}
			if tt.noMedia {
				child.MsgMediaID = ""
			}
			if tt.noNewEpisodes {
				child.Result = map[string]any{"subscription_follow": map[string]any{"outcome": "no_new_episodes"}}
			}
			if err := svc.applyPipelineTask(t.Context(), &job, child); err != nil {
				t.Fatal(err)
			}
			if tt.wantTitle != "" {
				select {
				case title := <-messages:
					if title != tt.wantTitle {
						t.Fatalf("notification title=%q, want %q", title, tt.wantTitle)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("completion notification was not sent")
				}
				// A second caller can hold a stale running snapshot. The DB claim
				// must still suppress its repeat of the same terminal response.
				if err := svc.applyPipelineTask(t.Context(), &staleJob, child); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case title := <-messages:
				t.Fatalf("unexpected or duplicate completion notification: %s", title)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func TestResourceImportCompletionNotificationTitlePreservesSelectedResource(t *testing.T) {
	job := model.ResourceImportJob{
		CandidateTitle: "  Selected-287-UC  ", MediaTitle: "selected 287 uc",
		Status: ResourceImportStatusCompletedWithWarning, PublicError: "Enhancement failed",
	}
	event := resourceImportCompletedNotification(job, "  管理员  ")
	if event.Title != "管理员-入库-Selected-287-UC" {
		t.Fatalf("notification title=%q", event.Title)
	}
	if !strings.HasPrefix(event.Message, "任务：Selected-287-UC\n") || !strings.Contains(event.Message, job.PublicError) {
		t.Fatalf("notification body=%q", event.Message)
	}
	if event.Type != EventLibraryIngest || event.Data["title"] != "Selected-287-UC" || event.Data["resource_title"] != "Selected-287-UC" {
		t.Fatalf("notification event=%+v", event)
	}
}

func TestResourceImportCompletionNotificationRejectsMissingTitleOrCreator(t *testing.T) {
	for _, name := range []string{"missing task title", "missing creator", "creator lookup failure"} {
		t.Run(name, func(t *testing.T) {
			svc, repos, library, root, _, user := newResourceImportTestService(t, &fakeResourcePipeline{})
			job := model.ResourceImportJob{
				UserID: user.ID, LibraryID: library.ID, LibraryRootID: root.ID,
				SearchSessionID: "mock-search", CandidateJSON: `{}`, CandidateTitle: "Selected-UC",
				IdempotencyKey: "mock-title", Status: ResourceImportStatusCompleted,
				Stage: "completed", MediaID: "mock-media", MediaTitle: "Scanner title",
			}
			wantError := "missing candidate_title"
			switch name {
			case "missing task title":
				job.CandidateTitle = " "
			case "missing creator":
				job.UserID = "missing-user"
				wantError = "creator is missing"
			case "creator lookup failure":
				wantError = "mock creator lookup failure"
				if err := repos.DB.Callback().Query().Before("gorm:query").Register("test:reject_creator", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" {
						tx.AddError(errors.New(wantError))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := repos.DB.Create(&job).Error; err != nil {
				t.Fatal(err)
			}
			svc.SetNotifyChannels(NewNotifyChannelService(zap.NewNop(), repos))
			if err := svc.notifyImportCompleted(t.Context(), job); err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("notification error=%v, want %q", err, wantError)
			}
			var persisted model.ResourceImportJob
			if err := repos.DB.First(&persisted, "id = ?", job.ID).Error; err != nil {
				t.Fatal(err)
			}
			if persisted.CompletionNotificationQueuedAt != nil {
				t.Fatal("invalid notification claimed the completion before resolving its title and creator")
			}
		})
	}
}

func TestResourceImportCompletionNotificationSurvivesWarningRetry(t *testing.T) {
	pipeline := &fakeResourcePipeline{}
	svc, repos, library, root, _, user := newResourceImportTestService(t, pipeline)
	job := model.ResourceImportJob{
		UserID: user.ID, SubscriptionID: "mock-subscription", SubscriptionFollow: true,
		LibraryID: library.ID, LibraryRootID: root.ID, SearchSessionID: "mock-search", CandidateJSON: `{}`,
		CandidateTitle: "Example", IdempotencyKey: "mock-retry", PipelineJobID: "mock-pipeline-job",
		Status: ResourceImportStatusRunning, Stage: "scanning", Attempt: 1,
	}
	if err := repos.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	var notificationCount int
	svc.SetSubscriptionCompletionHandler(func(_ context.Context, _ model.ResourceImportJob) error {
		notificationCount++
		return nil
	})
	child := resourcePipelineTask{Status: "completed_with_warning", Stage: "completed_with_warning", MsgMediaID: "mock-media"}
	if err := svc.applyPipelineTask(t.Context(), &job, child); err != nil {
		t.Fatal(err)
	}
	// Recreate the service to verify that deduplication does not depend on memory.
	restarted := newResourceImportServiceWithClient(svc.cfg, nil, repos, svc.ctx, pipeline)
	restarted.SetSubscriptionCompletionHandler(func(_ context.Context, _ model.ResourceImportJob) error {
		notificationCount++
		return nil
	})
	if err := repos.DB.First(&job, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	// Keep the monitor idle while exercising the real Retry path.
	restarted.executing[job.ID] = struct{}{}
	if _, err := restarted.Retry(t.Context(), user.ID, false, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := repos.DB.First(&job, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != ResourceImportStatusQueued || job.MediaID != "" || job.CompletionNotificationQueuedAt == nil {
		t.Fatalf("retry lost notification claim: %#v", job)
	}
	child.Status, child.Stage = "completed", "completed"
	if err := restarted.applyPipelineTask(t.Context(), &job, child); err != nil {
		t.Fatal(err)
	}
	if notificationCount != 1 {
		t.Fatalf("notifications=%d, want one across warning retry and restart", notificationCount)
	}
}

func TestResourceImportCompletionNotificationDoesNotMaskPersistenceFailure(t *testing.T) {
	svc, repos, library, root, _, user := newResourceImportTestService(t, &fakeResourcePipeline{})
	job := model.ResourceImportJob{
		UserID: user.ID, SubscriptionID: "mock-subscription", SubscriptionFollow: true,
		LibraryID: library.ID, LibraryRootID: root.ID, SearchSessionID: "mock-search", CandidateJSON: `{}`,
		IdempotencyKey: "mock-persist", Status: ResourceImportStatusRunning, Stage: "scanning", Attempt: 1,
	}
	if err := repos.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := repos.DB.Callback().Update().Before("gorm:update").Register("test:reject_completion", func(tx *gorm.DB) {
		patch, ok := tx.Statement.Dest.(map[string]any)
		if ok && tx.Statement.Table == "resource_import_jobs" && patch["status"] == ResourceImportStatusCompleted {
			tx.AddError(errors.New("mock completion persistence failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	var notified bool
	svc.SetSubscriptionCompletionHandler(func(_ context.Context, _ model.ResourceImportJob) error {
		notified = true
		return nil
	})
	err := svc.applyPipelineTask(t.Context(), &job, resourcePipelineTask{Status: "completed", Stage: "completed", MsgMediaID: "mock-media"})
	if err == nil || !strings.Contains(err.Error(), "mock completion persistence failure") || notified {
		t.Fatalf("err=%v notification=%v", err, notified)
	}
}

type completedCreateResourcePipeline struct{ *fakeResourcePipeline }

func (f *completedCreateResourcePipeline) CreateImport(_ context.Context, owner, _ string, _ resourcePipelineCreateRequest) (resourcePipelineTask, error) {
	return resourcePipelineTask{ID: "mock-completed-child", OwnerID: owner, Status: "completed", Stage: "completed", MsgMediaID: "mock-media", MsgMediaTitle: "Example"}, nil
}

func TestResourceImportCompletionNotificationWhenCreateReturnsCompleted(t *testing.T) {
	pipeline := &fakeResourcePipeline{}
	svc, repos, library, root, _, user := newResourceImportTestService(t, pipeline)
	svc.client = &completedCreateResourcePipeline{pipeline}
	notifications := make(chan struct{}, 4)
	bark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		notifications <- struct{}{}
		_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	defer bark.Close()
	if err := repos.DB.AutoMigrate(&model.NotifyChannel{}); err != nil {
		t.Fatal(err)
	}
	if err := repos.NotifyChannel.Create(t.Context(), &model.NotifyChannel{
		Name: "Mock Bark", Type: "bark", Enabled: true, Events: `["library_ingest"]`,
		Config: `{"server":"` + bark.URL + `","device_key":"mock-device-key"}`,
	}); err != nil {
		t.Fatal(err)
	}
	svc.SetNotifyChannels(NewNotifyChannelService(zap.NewNop(), repos))
	search, err := svc.Search(t.Context(), user.ID, library, root, ResourceSearchInput{Query: "Example", RootID: root.ID})
	if err != nil {
		t.Fatal(err)
	}
	input := ResourceImportCreateInput{SearchSessionID: search.SessionID, CandidateIndex: 0, RootID: root.ID}
	created, err := svc.Create(t.Context(), user.ID, library, root, input)
	if err != nil || created.Status != ResourceImportStatusCompleted {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	select {
	case <-notifications:
	case <-time.After(2 * time.Second):
		t.Fatal("immediately completed import did not send a notification")
	}
	reused, err := svc.Create(t.Context(), user.ID, library, root, input)
	if err != nil || reused.ID != created.ID {
		t.Fatalf("idempotent create=%+v err=%v", reused, err)
	}
	select {
	case <-notifications:
		t.Fatal("idempotent create repeated the completion notification")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestResourceImportCompletionNotificationPropagatesHandlerFailure(t *testing.T) {
	svc, repos, library, root, _, user := newResourceImportTestService(t, &fakeResourcePipeline{})
	job := model.ResourceImportJob{
		UserID: user.ID, SubscriptionID: "mock-subscription", SubscriptionFollow: true,
		LibraryID: library.ID, LibraryRootID: root.ID, SearchSessionID: "mock-search", CandidateJSON: `{}`,
		IdempotencyKey: "mock-handler-failure", Status: ResourceImportStatusRunning, Stage: "scanning", Attempt: 1,
	}
	if err := repos.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	failure := errors.New("mock notification handler failure")
	svc.SetSubscriptionCompletionHandler(func(_ context.Context, _ model.ResourceImportJob) error { return failure })
	child := resourcePipelineTask{Status: "completed", Stage: "completed", MsgMediaID: "mock-media"}
	if err := svc.applyPipelineTask(t.Context(), &job, child); !errors.Is(err, failure) {
		t.Fatalf("handler error=%v, want %v", err, failure)
	}
	var persisted model.ResourceImportJob
	if err := repos.DB.First(&persisted, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.CompletionNotificationQueuedAt != nil {
		t.Fatal("failed handler left a successful notification claim")
	}
}
