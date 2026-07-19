package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"go.uber.org/zap"
)

func TestPipelineScrapePropagatesEpisodeMetadataToSiblingRows(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{})
	repos := repository.New(db)
	svc := NewPipelineScrapeService(repos, nil)

	lib := model.Library{Name: "TV", Path: "cloud://openlist/115/剧集", Type: "tv", Enabled: true}
	if err := repos.DB.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	root := model.LibraryRoot{LibraryID: lib.ID, Name: "TV", Path: "cloud://openlist/115/剧集", Enabled: true}
	if err := repos.DB.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	rows := []model.Media{
		{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "Noisy", Path: "cloud://openlist/115/剧集/低智商犯罪/S01E01.mkv", SeasonNum: 1, EpisodeNum: 1, EpisodeTitle: "第1集", ScrapeStatus: "pending"},
		{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "低智商犯罪", Path: "cloud://openlist/115/剧集/低智商犯罪/S01E02.mkv", SeasonNum: 1, EpisodeNum: 2, EpisodeTitle: "第2集", ScrapeStatus: "matched", TMDbID: 272432, PosterURL: "/cache/poster.jpg", BackdropURL: "/cache/backdrop.jpg"},
		{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "Noisy", Path: "cloud://openlist/115/剧集/低智商犯罪/S01E03.mkv", SeasonNum: 1, EpisodeNum: 3, EpisodeTitle: "第3集", ScrapeStatus: "pending"},
		{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "Other", Path: "cloud://openlist/115/剧集/其他剧/S01E01.mkv", SeasonNum: 1, EpisodeNum: 1, ScrapeStatus: "pending"},
	}
	if err := repos.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	updated, err := svc.propagateEpisodeMatch(t.Context(), &rows[1], &rows[1], PipelineScrapeRequest{Category: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	if updated != 2 {
		t.Fatalf("updated=%d, want 2 sibling rows", updated)
	}

	var got []model.Media
	if err := repos.DB.Where("library_id = ?", lib.ID).Order("path").Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range got[:3] {
		if row.Title != "低智商犯罪" || row.ScrapeStatus != "matched" || row.TMDbID != 272432 || row.PosterURL != "/cache/poster.jpg" {
			t.Fatalf("episode metadata not propagated: %#v", row)
		}
		if row.EpisodeNum <= 0 || row.EpisodeTitle == "" {
			t.Fatalf("episode identity should be preserved: %#v", row)
		}
	}
	if got[3].Title != "Other" || got[3].ScrapeStatus != "pending" {
		t.Fatalf("unrelated row changed: %#v", got[3])
	}
}

func TestPipelineScrapeAppliesUniqueManualMatch(t *testing.T) {
	withAdultDefaultBases(t, nil)
	searchCalls := 0
	detailCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			searchCalls++
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<a class="box" href="/v/mide949"><strong>MIDE-949 manual candidate</strong></a>`))
		case "/v/mide949":
			detailCalls++
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<h2 class="title"><strong>correct title</strong></h2><img class="video-cover" src="/cover.jpg">`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	db := newServiceTestDB(t, &model.Library{}, &model.Series{}, &model.Media{}, &model.APIConfig{})
	repos := repository.New(db)
	apiConfig := NewAPIConfigService(zap.NewNop(), repos, NewCryptoService("", zap.NewNop()))
	baseURL := upstream.URL
	if _, err := apiConfig.Update(t.Context(), "adult", APIConfigPatch{BaseURL: &baseURL}); err != nil {
		t.Fatal(err)
	}
	log := zap.NewNop()
	scraper := NewScraperService(&config.Config{}, log, repos, nil, nil, nil, nil, NewHub(log), NewAdultProvider(log, apiConfig))
	svc := NewPipelineScrapeService(repos, scraper)

	lib := model.Library{Name: "Adult", Path: "cloud://openlist/115/adult", Type: "adult", Enabled: true}
	if err := repos.DB.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	media := model.Media{LibraryID: lib.ID, Title: "mide 949", OriginalName: "mide 949", Path: "cloud://openlist/115/adult/MIDE-949/MIDE-949.mp4"}
	if err := repos.DB.Create(&media).Error; err != nil {
		t.Fatal(err)
	}

	result, err := svc.Scrape(t.Context(), media.ID, PipelineScrapeRequest{
		Category:  "adult",
		Title:     "mide 949",
		Queries:   []string{"MIDE-949"},
		Provider:  "adult",
		MediaType: "adult",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != PipelineScrapeModeApply || result.Query != "MIDE-949" || result.MatchCount != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}

	var got model.Media
	if err := repos.DB.First(&got, "id = ?", media.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Title != "correct title" || got.ScrapeStatus != "matched" {
		t.Fatalf("manual match was not applied: title=%q status=%q", got.Title, got.ScrapeStatus)
	}
	if searchCalls != 2 || detailCalls != 2 {
		t.Fatalf("non-FD2 adult flow changed: search calls=%d detail calls=%d, want 2 each", searchCalls, detailCalls)
	}
}

func TestPipelineScrapeReusesResolvedFD2Match(t *testing.T) {
	withAdultDefaultBases(t, nil)
	flareCalls := 0
	flare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flareCalls++
		if r.URL.Path != "/v1" {
			t.Fatalf("FlareSolverr path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"status": 200,
				"response": `<h1 class="work-title">4516148</h1>
					<div class="work-brief">FD2 resolved title</div>
					<div class="work-original-photos">https://img.example/fd2.jpg</div>
					<div class="work-meta-label">配信日</div><div class="work-meta-value">2024-08-11</div>`,
			},
		})
	}))
	defer flare.Close()

	db := newServiceTestDB(t, &model.Library{}, &model.Series{}, &model.Media{})
	repos := repository.New(db)
	log := zap.NewNop()
	adult := NewAdultProvider(log, nil)
	adult.SetFlareSolverr(flare.URL, 5)
	scraper := NewScraperService(&config.Config{}, log, repos, nil, nil, nil, nil, NewHub(log), adult)
	svc := NewPipelineScrapeService(repos, scraper)

	lib := model.Library{Name: "Adult", Path: "cloud://openlist/115/adult", Type: "adult", Enabled: true}
	if err := repos.DB.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	media := model.Media{
		LibraryID:    lib.ID,
		Title:        "FC2-PPV-4516148",
		OriginalName: "FC2-PPV-4516148",
		Path:         "cloud://openlist/115/adult/FC2-PPV-4516148/FC2-PPV-4516148.mp4",
	}
	if err := repos.DB.Create(&media).Error; err != nil {
		t.Fatal(err)
	}

	result, err := svc.Scrape(t.Context(), media.ID, PipelineScrapeRequest{
		Category:  "adult",
		Title:     "FC2-PPV-4516148",
		Queries:   []string{"FC2-PPV-4516148"},
		Provider:  "adult",
		MediaType: "adult",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != PipelineScrapeModeApply || result.AppliedCount != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if flareCalls != 1 {
		t.Fatalf("FD2 requests = %d, want 1", flareCalls)
	}

	var got model.Media
	if err := repos.DB.First(&got, "id = ?", media.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Title != "FD2 resolved title" || got.ReleaseDate != "2024-08-11" || got.ScrapeStatus != "matched" {
		t.Fatalf("FD2 match was not applied: %+v", got)
	}
}
