package service

import (
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func TestEnrichExternalMediaLibraryLinksUsesVisibleProviderMatch(t *testing.T) {
	db := newServiceTestDB(t, &model.Media{})
	repos := repository.New(db)
	rows := []model.Media{
		{Base: model.Base{ID: "visible-provider"}, LibraryID: "library-a", Title: "本地标题", Path: "/a/movie.mp4", TMDbID: 42, Year: 2026},
		{Base: model.Base{ID: "hidden-provider"}, LibraryID: "library-b", Title: "本地标题", Path: "/b/movie.mp4", TMDbID: 42, Year: 2026},
		{Base: model.Base{ID: "visible-title"}, LibraryID: "library-a", Title: "标题命中", Path: "/a/title.mp4", Year: 2025},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	items := []ExternalMediaResult{
		{Title: "外部标题", TMDbID: 42, Year: 2026},
		{Title: "标题命中", Year: 2025},
	}
	EnrichExternalMediaLibraryLinks(t.Context(), repos, items, MediaVisibility{
		IncludeNSFW: true, AllowedLibraryIDs: []string{"library-a"},
	})
	if !items[0].InLibrary || items[0].LocalMediaID != "visible-provider" || items[0].LocalLibraryID != "library-a" {
		t.Fatalf("provider match = %#v", items[0])
	}
	if !items[1].InLibrary || items[1].LocalMediaID != "visible-title" {
		t.Fatalf("title fallback = %#v", items[1])
	}
}
