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
		{Base: model.Base{ID: "tv-id-collision"}, LibraryID: "library-a", SeriesID: "series-240", Title: "成龙历险记", Path: "/a/tv.mp4", TMDbID: 240, Year: 2000, SeasonNum: 1, EpisodeNum: 1},
		{Base: model.Base{ID: "movie-year-mismatch"}, LibraryID: "library-a", Title: "同类型错误年份", Path: "/a/wrong-year.mp4", TMDbID: 241, Year: 2001},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	items := []ExternalMediaResult{
		{Title: "外部标题", MediaType: "movie", TMDbID: 42, Year: 2026},
		{Title: "标题命中", Year: 2025},
		{Title: "教父2", MediaType: "movie", TMDbID: 240, Year: 1974},
		{Title: "成龙历险记", MediaType: "tv", TMDbID: 240, Year: 2000},
		{Title: "年份冲突", MediaType: "movie", TMDbID: 241, Year: 1974},
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
	if items[2].InLibrary || items[2].LocalMediaID != "" {
		t.Fatalf("movie must not match a TV item with the same TMDb number: %#v", items[2])
	}
	if !items[3].InLibrary || items[3].LocalMediaID != "tv-id-collision" {
		t.Fatalf("TV provider match = %#v", items[3])
	}
	if items[4].InLibrary || items[4].LocalMediaID != "" {
		t.Fatalf("known year mismatch must not fall through: %#v", items[4])
	}
}
