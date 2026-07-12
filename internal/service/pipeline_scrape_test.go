package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"go.uber.org/zap"
)

func TestPipelineScrapeAppliesUniqueManualMatch(t *testing.T) {
	withAdultDefaultBases(t, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<a class="box" href="/v/mide949"><strong>MIDE-949 manual candidate</strong></a>`))
		case "/v/mide949":
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
}
