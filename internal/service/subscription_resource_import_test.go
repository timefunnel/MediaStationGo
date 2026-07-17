package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func TestSubscriptionTargetLocalAvailabilityScopesRootAndSeason(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{})
	repos := repository.New(db)
	library := model.Library{Name: "Anime", Path: "cloud://openlist/115%2Fanime", Type: "anime", Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	root := model.LibraryRoot{LibraryID: library.ID, Name: "Main", Path: library.Path, Enabled: true}
	otherRoot := model.LibraryRoot{LibraryID: library.ID, Name: "Other", Path: "cloud://openlist/115%2Fother", Enabled: true}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&otherRoot).Error; err != nil {
		t.Fatal(err)
	}
	rows := []model.Media{
		{LibraryID: library.ID, LibraryRootID: root.ID, Title: "Test Show", Path: root.Path + "/Test Show/S01E01.mkv", SeasonNum: 1, EpisodeNum: 1},
		{LibraryID: library.ID, LibraryRootID: root.ID, Title: "Test Show", Path: root.Path + "/Test Show/S02E01.mkv", SeasonNum: 2, EpisodeNum: 1},
		{LibraryID: library.ID, LibraryRootID: otherRoot.ID, Title: "Test Show", Path: otherRoot.Path + "/Test Show/S02E02.mkv", SeasonNum: 2, EpisodeNum: 2},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	sub := &model.Subscription{
		Name: "Test Show", Filter: "Test Show", MediaType: "anime", TotalEpisodes: 2,
		LibraryID: library.ID, LibraryRootID: root.ID, SeasonNumber: 2,
	}

	got, err := SubscriptionTargetLocalAvailability(t.Context(), repos, sub)
	if err != nil {
		t.Fatal(err)
	}
	if got.LocalMediaCount != 2 || got.DownloadedEpisodes != 1 || len(got.MissingEpisodes) != 1 || got.MissingEpisodes[0] != 2 {
		t.Fatalf("availability = %+v", got)
	}
	if _, ok := got.ExistingEpisodeKeys[episodeKey(2, 1)]; !ok {
		t.Fatalf("season 2 episode 1 not found: %+v", got.ExistingEpisodeKeys)
	}
	if _, ok := got.ExistingEpisodeKeys[episodeKey(1, 1)]; ok {
		t.Fatalf("season 1 leaked into target season: %+v", got.ExistingEpisodeKeys)
	}
}

func TestSelectResourceImportSubscriptionCandidatesKeepsOnlyMissingSingles(t *testing.T) {
	sub := &model.Subscription{
		Name: "Test Show", Filter: "Test Show", MediaType: "tv", DeliveryMode: subscriptionDeliveryResourceImport,
		SeasonNumber: 1, TotalEpisodes: 3,
	}
	local := LocalAvailability{
		LocalMediaCount: 1, DownloadedEpisodes: 1, TotalEpisodes: 3,
		ExistingEpisodeKeys: map[string]struct{}{episodeKey(1, 1): {}},
		MissingEpisodes:     []int{2, 3},
	}
	items := []ResourceSearchCandidate{
		{Index: 0, Title: "Test Show S01E01 1080p"},
		{Index: 1, Title: "Test Show S01E02 1080p"},
		{Index: 2, Title: "Test Show Complete 1080p"},
	}

	got := selectResourceImportSubscriptionCandidates(items, sub, local)
	if len(got) != 1 || got[0].Episode != 2 || got[0].Item.SiteID != "1" {
		t.Fatalf("selected candidates = %+v", got)
	}
	if !resourceCandidateIsExplicitlyMissing(got[0], local) {
		t.Fatal("missing episode should be allowed to bypass the title-level duplicate check")
	}
}

type subscriptionResourcePipeline struct {
	mu       sync.Mutex
	items    []map[string]any
	creates  []resourcePipelineCreateRequest
	nextTask int
}

func (f *subscriptionResourcePipeline) Search(_ context.Context, in resourcePipelineSearchRequest) (resourcePipelineSearchResponse, error) {
	return resourcePipelineSearchResponse{
		SessionID:    "pipeline-" + in.OwnerID,
		ExpiresAt:    time.Now().Add(15 * time.Minute).Unix(),
		Items:        f.items,
		Capabilities: ResourceSearchCapabilities{Pansou: true},
	}, nil
}

func (f *subscriptionResourcePipeline) CreateImport(_ context.Context, owner, _ string, in resourcePipelineCreateRequest) (resourcePipelineTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTask++
	f.creates = append(f.creates, in)
	return resourcePipelineTask{ID: fmt.Sprintf("job-%d", f.nextTask), OwnerID: owner, Status: "queued", Stage: "queued"}, nil
}

func (f *subscriptionResourcePipeline) GetImport(_ context.Context, owner, id string) (resourcePipelineTask, error) {
	return resourcePipelineTask{ID: id, OwnerID: owner, Status: "completed", Stage: "completed", MsgMediaID: "media-" + id}, nil
}

func (f *subscriptionResourcePipeline) CancelImport(_ context.Context, owner, id string) (resourcePipelineTask, error) {
	return resourcePipelineTask{ID: id, OwnerID: owner, Status: "canceled", Stage: "canceled"}, nil
}

func (f *subscriptionResourcePipeline) RetryImport(_ context.Context, owner, id string) (resourcePipelineTask, error) {
	return resourcePipelineTask{ID: id, OwnerID: owner, Status: "queued", Stage: "queued"}, nil
}

func TestRunResourceImportSubscriptionQueuesMissingEpisodesThroughExistingService(t *testing.T) {
	db := newServiceTestDB(t,
		&model.User{}, &model.Library{}, &model.LibraryRoot{}, &model.Media{}, &model.Subscription{},
		&model.ResourceSearchSession{}, &model.ResourceImportJob{},
	)
	repos := repository.New(db)
	user := model.User{Username: "admin", PasswordHash: "x", Role: "admin", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	library := model.Library{Name: "TV", Path: "cloud://openlist/115%2Ftv", Type: "tv", Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	root := model.LibraryRoot{LibraryID: library.ID, Name: "TV", Path: library.Path, Enabled: true}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Media{
		LibraryID: library.ID, LibraryRootID: root.ID, Title: "Test Show",
		Path: root.Path + "/Test Show/S01E01.mkv", SeasonNum: 1, EpisodeNum: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	sub := &model.Subscription{
		UserID: user.ID, Name: "Test Show", FeedURL: "resource-import://pansou", Filter: "Test Show",
		DeliveryMode: subscriptionDeliveryResourceImport, ResourceSource: "pansou",
		LibraryID: library.ID, LibraryRootID: root.ID, MediaType: "tv", SeasonNumber: 1,
		TotalEpisodes: 3, MaxImportsPerRun: 2, Enabled: true,
	}
	if err := db.Create(sub).Error; err != nil {
		t.Fatal(err)
	}
	pipeline := &subscriptionResourcePipeline{items: []map[string]any{
		{"candidate_id": "c1", "title": "Test Show S01E01 1080p"},
		{"candidate_id": "c2", "title": "Test Show S01E02 1080p"},
		{"candidate_id": "c3", "title": "Test Show S01E03 1080p"},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resourceImport := newResourceImportServiceWithClient(config.ResourceImportConfig{
		Enabled: true, MaxConcurrent: 1, MaxConcurrentPerUser: 1, PollSeconds: 1,
	}, nil, repos, ctx, pipeline)
	svc := NewSubscriptionService(nil, nil, repos, nil, nil, nil)
	svc.SetResourceImport(resourceImport)

	queued, err := svc.runOne(t.Context(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if queued != 2 || len(pipeline.creates) != 2 {
		t.Fatalf("queued=%d creates=%d", queued, len(pipeline.creates))
	}
	for _, request := range pipeline.creates {
		if !request.ForceDuplicate {
			t.Fatalf("missing episode was not force-enabled: %+v", request)
		}
	}
	var jobs []model.ResourceImportJob
	if err := db.Where("subscription_id = ?", sub.ID).Find(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("subscription jobs = %d", len(jobs))
	}
}
