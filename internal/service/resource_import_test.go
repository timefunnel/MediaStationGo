package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

type fakeResourcePipeline struct {
	mu             sync.Mutex
	duplicate      *ResourceImportDuplicate
	createCalls    int
	getDelay       time.Duration
	active         int
	maxActive      int
	activeByOwner  map[string]int
	maxByOwner     map[string]int
	createdOwners  []string
	createRequests []resourcePipelineCreateRequest
	canceledOwners []string
	retriedOwners  []string
}

func (f *fakeResourcePipeline) Search(_ context.Context, in resourcePipelineSearchRequest) (resourcePipelineSearchResponse, error) {
	return resourcePipelineSearchResponse{
		SessionID: "pipeline-session-" + in.OwnerID,
		ExpiresAt: time.Now().Add(15 * time.Minute).Unix(),
		Items: []map[string]any{{
			"candidate_id": "candidate-1", "title": in.Query, "size": float64(1024), "indexer": "test",
		}},
		Capabilities: ResourceSearchCapabilities{Pansou: true},
	}, nil
}

func (f *fakeResourcePipeline) CreateImport(_ context.Context, owner, _ string, in resourcePipelineCreateRequest) (resourcePipelineTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.createdOwners = append(f.createdOwners, owner)
	f.createRequests = append(f.createRequests, in)
	if f.duplicate != nil {
		return resourcePipelineTask{}, &resourcePipelineError{
			StatusCode: 409, Code: "duplicate_media", Message: "duplicate",
			Duplicate: f.duplicate,
		}
	}
	return resourcePipelineTask{ID: "pipeline-job-1", OwnerID: owner, Status: "queued", Stage: "queued", Message: "queued"}, nil
}

func (f *fakeResourcePipeline) GetImport(_ context.Context, owner, id string) (resourcePipelineTask, error) {
	f.mu.Lock()
	if f.activeByOwner == nil {
		f.activeByOwner = map[string]int{}
		f.maxByOwner = map[string]int{}
	}
	f.active++
	f.activeByOwner[owner]++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	if f.activeByOwner[owner] > f.maxByOwner[owner] {
		f.maxByOwner[owner] = f.activeByOwner[owner]
	}
	f.mu.Unlock()
	if f.getDelay > 0 {
		time.Sleep(f.getDelay)
	}
	f.mu.Lock()
	f.active--
	f.activeByOwner[owner]--
	f.mu.Unlock()
	return resourcePipelineTask{
		ID: id, OwnerID: owner, Status: "completed", Stage: "completed",
		Message: "done", MsgMediaID: "media-" + id, MsgMediaTitle: "Done",
	}, nil
}

func (f *fakeResourcePipeline) CancelImport(_ context.Context, owner, id string) (resourcePipelineTask, error) {
	f.mu.Lock()
	f.canceledOwners = append(f.canceledOwners, owner)
	f.mu.Unlock()
	return resourcePipelineTask{ID: id, OwnerID: owner, Status: "canceled", Stage: "canceled", Message: "canceled"}, nil
}

func (f *fakeResourcePipeline) RetryImport(_ context.Context, owner, id string) (resourcePipelineTask, error) {
	f.mu.Lock()
	f.retriedOwners = append(f.retriedOwners, owner)
	f.mu.Unlock()
	return resourcePipelineTask{ID: id, OwnerID: owner, Status: "queued", Stage: "queued", Message: "queued"}, nil
}

func newResourceImportTestService(t *testing.T, pipeline *fakeResourcePipeline) (*ResourceImportService, *repository.Container, model.Library, model.LibraryRoot, model.User, model.User) {
	t.Helper()
	db := newServiceTestDB(t,
		&model.User{}, &model.Library{}, &model.LibraryRoot{},
		&model.ResourceSearchSession{}, &model.ResourceImportJob{},
	)
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	repos := repository.New(db)
	admin := model.User{Username: "admin", PasswordHash: "x", Role: "admin", IsActive: true}
	user := model.User{Username: "user-a", PasswordHash: "x", Role: "user", IsActive: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	library := model.Library{Name: "Movies", Path: "cloud://openlist/115%2F电影", Type: "movie", Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	root := model.LibraryRoot{LibraryID: library.ID, Name: "115 Movies", Path: "cloud://openlist/115%2F电影?dir=电影&auto_category=1", Enabled: true}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	library.Roots = []model.LibraryRoot{root}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc := newResourceImportServiceWithClient(config.ResourceImportConfig{
		Enabled: true, MaxConcurrent: 3, MaxConcurrentPerUser: 2, PollSeconds: 1,
	}, nil, repos, ctx, pipeline)
	return svc, repos, library, root, admin, user
}

func TestResourceImportSearchAndSessionAreOwnerScoped(t *testing.T) {
	pipeline := &fakeResourcePipeline{}
	svc, _, library, root, _, user := newResourceImportTestService(t, pipeline)
	search, err := svc.Search(t.Context(), user.ID, library, root, ResourceSearchInput{Query: "Sintel", RootID: root.ID})
	if err != nil {
		t.Fatal(err)
	}
	if search.Total != 1 || search.Results[0].Title != "Sintel" {
		t.Fatalf("unexpected search response: %+v", search)
	}
	publicJSON, err := json.Marshal(search.Results[0])
	if err != nil || strings.Contains(string(publicJSON), "candidate-1") {
		t.Fatalf("public candidate leaked pipeline id: %s, err=%v", publicJSON, err)
	}
	_, err = svc.Create(t.Context(), "another-user", library, root, ResourceImportCreateInput{
		SearchSessionID: search.SessionID, CandidateIndex: 0, RootID: root.ID,
	})
	if err == nil || err.Error() != "resource search session not found" {
		t.Fatalf("cross-owner session create err = %v", err)
	}
	if pipeline.createCalls != 0 {
		t.Fatalf("pipeline create calls = %d", pipeline.createCalls)
	}
}

func TestResourceImportDuplicateDoesNotCreateParentJob(t *testing.T) {
	pipeline := &fakeResourcePipeline{duplicate: &ResourceImportDuplicate{CanForce: false, MediaID: "media-existing", Title: "Existing"}}
	svc, repos, library, root, _, user := newResourceImportTestService(t, pipeline)
	search, err := svc.Search(t.Context(), user.ID, library, root, ResourceSearchInput{Query: "Sintel", RootID: root.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(t.Context(), user.ID, library, root, ResourceImportCreateInput{
		SearchSessionID: search.SessionID, CandidateIndex: 0, RootID: root.ID,
	})
	var duplicate *ResourceImportDuplicateError
	if !errors.As(err, &duplicate) || duplicate.Duplicate.MediaID != "media-existing" || duplicate.Duplicate.CanForce {
		t.Fatalf("duplicate error = %#v", err)
	}
	var count int64
	if err := repos.DB.Model(&model.ResourceImportJob{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("parent job count = %d", count)
	}
}

func TestResourceImportListAndActionsAreOwnerScoped(t *testing.T) {
	pipeline := &fakeResourcePipeline{}
	svc, repos, library, root, admin, user := newResourceImportTestService(t, pipeline)
	other := model.User{Username: "user-b", PasswordHash: "x", Role: "user", IsActive: true}
	if err := repos.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	jobs := []model.ResourceImportJob{
		{UserID: user.ID, LibraryID: library.ID, LibraryRootID: root.ID, SearchSessionID: "s1", CandidateJSON: "{}", CandidateTitle: "A", IdempotencyKey: "key-a", Status: "running", Stage: "transferring", PipelineJobID: "p-a", Attempt: 1},
		{UserID: other.ID, LibraryID: library.ID, LibraryRootID: root.ID, SearchSessionID: "s2", CandidateJSON: "{}", CandidateTitle: "B", IdempotencyKey: "key-b", Status: "failed", Stage: "failed", PipelineJobID: "p-b", Attempt: 1},
	}
	if err := repos.DB.Create(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	own, err := svc.List(t.Context(), user.ID, false, ResourceImportListFilter{UserID: other.ID})
	if err != nil || own.Total != 1 || own.Items[0].CandidateTitle != "A" || own.Items[0].UserID != "" {
		t.Fatalf("owner list = %+v, err=%v", own, err)
	}
	all, err := svc.List(t.Context(), admin.ID, true, ResourceImportListFilter{})
	if err != nil || all.Total != 2 || all.Items[0].UserID == "" || all.Items[0].CreatorUsername == "" {
		t.Fatalf("admin list = %+v, err=%v", all, err)
	}
	if _, err := svc.Get(t.Context(), user.ID, false, jobs[1].ID); err == nil {
		t.Fatal("user A read user B task")
	}
	if _, err := svc.Cancel(t.Context(), user.ID, false, jobs[1].ID); err == nil {
		t.Fatal("user A canceled user B task")
	}
	if _, err := svc.Retry(t.Context(), user.ID, false, jobs[1].ID); err == nil {
		t.Fatal("user A retried user B task")
	}
	if _, err := svc.Retry(t.Context(), admin.ID, true, jobs[1].ID); err != nil {
		t.Fatalf("admin retry: %v", err)
	}
	if len(pipeline.retriedOwners) != 1 || pipeline.retriedOwners[0] != other.ID {
		t.Fatalf("pipeline retry owners = %v", pipeline.retriedOwners)
	}
}

func TestResourceImportRecoveryHonorsGlobalAndPerUserLimits(t *testing.T) {
	pipeline := &fakeResourcePipeline{getDelay: 80 * time.Millisecond}
	svc, repos, library, root, _, user := newResourceImportTestService(t, pipeline)
	other := model.User{Username: "user-b", PasswordHash: "x", Role: "user", IsActive: true}
	third := model.User{Username: "user-c", PasswordHash: "x", Role: "user", IsActive: true}
	if err := repos.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := repos.DB.Create(&third).Error; err != nil {
		t.Fatal(err)
	}
	owners := []string{user.ID, user.ID, user.ID, other.ID, third.ID}
	for i, owner := range owners {
		job := model.ResourceImportJob{
			UserID: owner, LibraryID: library.ID, LibraryRootID: root.ID,
			SearchSessionID: "session", CandidateJSON: "{}", CandidateTitle: "Task",
			IdempotencyKey: "recover-" + string(rune('a'+i)), Status: "queued", Stage: "duplicate_check",
			PipelineJobID: "pipeline-" + string(rune('a'+i)), Attempt: 1,
		}
		if err := repos.DB.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
	}
	if count, err := svc.Recover(t.Context()); err != nil || count != len(owners) {
		t.Fatalf("recover count=%d err=%v", count, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	completed := int64(0)
	for time.Now().Before(deadline) {
		if err := repos.DB.Model(&model.ResourceImportJob{}).Where("status = ?", "completed").Count(&completed).Error; err != nil {
			t.Fatal(err)
		}
		if completed == int64(len(owners)) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if completed != int64(len(owners)) {
		t.Fatalf("completed jobs = %d, want %d", completed, len(owners))
	}
	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()
	if pipeline.maxActive > 3 {
		t.Fatalf("global max active = %d", pipeline.maxActive)
	}
	if pipeline.maxByOwner[user.ID] > 2 {
		t.Fatalf("owner max active = %d", pipeline.maxByOwner[user.ID])
	}
}

func TestResourceRootOpenListPathIgnoresQueryAndDecodesEscapedSlash(t *testing.T) {
	got, err := resourceRootOpenListPath("cloud://openlist/115%2F成人?dir=成人&auto_category=1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/115/成人" {
		t.Fatalf("root path = %q", got)
	}
}

func TestStoredResourceSearchDoesNotExposePipelineCandidateID(t *testing.T) {
	candidate := ResourceSearchCandidate{Index: 0, Title: "Movie", CandidateID: "secret-candidate"}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || string(encoded) == "null" {
		t.Fatal("candidate marshal failed")
	}
	if string(encoded) != `{"index":0,"title":"Movie"}` {
		t.Fatalf("public candidate JSON = %s", encoded)
	}
}

func TestResourceImportPersistsPipelineCandidateIDForCreate(t *testing.T) {
	pipeline := &fakeResourcePipeline{}
	svc, repos, library, root, _, user := newResourceImportTestService(t, pipeline)
	search, err := svc.Search(t.Context(), user.ID, library, root, ResourceSearchInput{Query: "Sintel", RootID: root.ID})
	if err != nil {
		t.Fatal(err)
	}
	var session model.ResourceSearchSession
	if err := repos.DB.First(&session, "id = ?", search.SessionID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(session.ResultsJSON, `"candidate_id":"candidate-1"`) {
		t.Fatalf("stored search session has no pipeline candidate id: %s", session.ResultsJSON)
	}
	if _, err := svc.Create(t.Context(), user.ID, library, root, ResourceImportCreateInput{
		SearchSessionID: search.SessionID, CandidateIndex: 0, RootID: root.ID,
	}); err != nil {
		t.Fatal(err)
	}
	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()
	if len(pipeline.createRequests) != 1 || pipeline.createRequests[0].CandidateID != "candidate-1" {
		t.Fatalf("pipeline create requests = %#v", pipeline.createRequests)
	}
}
