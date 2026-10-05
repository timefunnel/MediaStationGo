package service

import (
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

func TestEmbySearchExcludesOverviewOnlyMatches(t *testing.T) {
	svc := newTestEmbyService(t)
	lib := model.Library{Name: "Movies", Path: "/media/movies", Type: "movie", Enabled: true}
	if err := svc.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	rows := []model.Media{
		{Base: model.Base{ID: "title-match"}, LibraryID: lib.ID, Title: "江南", Path: "/media/movies/target.mkv"},
		{Base: model.Base{ID: "overview-only"}, LibraryID: lib.ID, Title: "唐伯虎点秋香", Path: "/media/movies/other.mkv", Overview: "江南四大才子"},
		{Base: model.Base{ID: "episode-overview-only"}, LibraryID: lib.ID, Title: "吞噬星空", Path: "/media/movies/show/Season 1/E01.mkv", SeasonNum: 1, EpisodeNum: 1, Overview: "江南基地市"},
	}
	for i := range rows {
		if err := svc.repo.Media.Upsert(t.Context(), &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	for _, parentID := range []string{"", lib.ID} {
		out, err := svc.Items(t.Context(), ItemsParams{ParentID: parentID, SearchTerm: "江南", Recursive: true, Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		items := out["Items"].([]map[string]any)
		if len(items) != 1 || items[0]["Id"] != "title-match" {
			t.Fatalf("parent=%q items=%#v; want only title-match", parentID, items)
		}
	}
	if embyMediaMatchesSearch(rows[1], ItemsParams{SearchTerm: "江南"}) || embyMediaMatchesSearch(rows[2], ItemsParams{SearchTerm: "江南"}) {
		t.Fatal("in-memory episode search matched an overview-only term")
	}
}
