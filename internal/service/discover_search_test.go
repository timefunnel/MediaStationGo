package service

import "testing"

func TestExternalMediaResultsFromMatchesPreservesCatalogIdentity(t *testing.T) {
	matches := []*Match{
		{Title: "电影一", OriginalName: "Movie One", TMDbID: 101, Year: 2026, ReleaseDate: "2026-07-18"},
		{Title: "电影二", TMDbID: 102},
	}
	items := externalMediaResultsFromMatches("tmdb", "movie", matches, 1)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	item := items[0]
	if item.Source != "tmdb" || item.MediaType != "movie" || item.TMDbID != 101 {
		t.Fatalf("identity = %#v", item)
	}
	if item.SubscribeKeyword != "电影一 2026" || item.ReleaseDate != "2026-07-18" {
		t.Fatalf("metadata = %#v", item)
	}
}

func TestSearchDiscoverCatalogWithNoProvidersIsExplicitlyEmpty(t *testing.T) {
	result := SearchDiscoverCatalog(t.Context(), "测试", nil, nil, nil, nil)
	if len(result.Items) != 0 || len(result.Errors) != 0 {
		t.Fatalf("result = %#v", result)
	}
}
