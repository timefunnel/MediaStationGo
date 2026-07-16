package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func TestPreviewAndApplyMediaTitleCleanup(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.Media{})
	repos := repository.New(db)
	lib := model.Library{
		Name: "其他媒体", Path: "cloud://openlist/115%2F其他", Type: "movie",
		TitleMode: LibraryTitleModeFilename, Enabled: true,
	}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	rows := []model.Media{
		{LibraryID: lib.ID, Title: "mp4_ (2)", Path: "cloud://openlist/115/其他/作品 A/视图/V/mp4_ (2).mp4", ScrapeStatus: "pending"},
		{LibraryID: lib.ID, Title: "mp4_ (3)", Path: "cloud://openlist/115/其他/作品 A/视图/V/mp4_ (3).mp4", ScrapeStatus: "no_match"},
		{LibraryID: lib.ID, Title: "正式元数据", Path: "cloud://openlist/115/其他/作品 B/movie.mp4", ScrapeStatus: "matched", TMDbID: 123},
	}
	if err := repos.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"items\":[{\"media_id\":\"` + rows[0].ID + `\",\"title\":\"作品 A 第一段\",\"relation\":\"part\",\"confidence\":0.92},{\"media_id\":\"` + rows[1].ID + `\",\"title\":\"作品 A 第二段\",\"relation\":\"part\",\"confidence\":0.88}]}"}}]}`))
	}))
	defer server.Close()
	ai := NewAIService(&config.Config{AI: config.AIConfig{
		Enabled: true, APIBase: server.URL, APIKey: "test", Model: "test",
	}}, zap.NewNop(), nil)
	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos).SetAI(ai)

	preview, err := svc.PreviewTitleCleanup(t.Context(), lib.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CandidateCount != 2 || preview.BatchCount != 2 || len(preview.Groups) != 1 {
		t.Fatalf("preview = %#v", preview)
	}
	if preview.Groups[0].SourceDirectory != "作品 A" || preview.Groups[0].Items[0].DirectoryChain != "作品 A / 视图 / V" {
		t.Fatalf("group context = %#v", preview.Groups[0])
	}

	result, err := svc.ApplyTitleCleanup(t.Context(), lib.ID, MediaTitleCleanupApplyRequest{Items: preview.Suggestions})
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 2 {
		t.Fatalf("apply result = %#v", result)
	}
	for i, want := range []string{"作品 A 第一段", "作品 A 第二段"} {
		var stored model.Media
		if err := repos.DB.First(&stored, "id = ?", rows[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Title != want || stored.ScrapeStatus != MediaScrapeStatusTitleCleaned {
			t.Fatalf("stored media = %#v", stored)
		}
	}
}
