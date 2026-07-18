package service

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
)

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

func TestSearchDiscoverCatalogSkipsPerformerSearchForAdultCode(t *testing.T) {
	var performerRequests atomic.Int32
	var movieRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("f") {
		case "actor":
			performerRequests.Add(1)
		case "all":
			movieRequests.Add(1)
			_, _ = w.Write([]byte(`<a class="box" href="/v/mizd534" title="Sample title"><strong>MIZD-534</strong></a>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	withAdultDefaultBases(t, []string{server.URL})

	adult := NewAdultProvider(zap.NewNop(), nil)
	result := SearchDiscoverCatalog(t.Context(), "MIZD-534", nil, nil, nil, adult)
	if len(result.Errors) != 0 || len(result.Items) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if got := performerRequests.Load(); got != 0 {
		t.Fatalf("performer requests = %d, want 0", got)
	}
	if got := movieRequests.Load(); got != 1 {
		t.Fatalf("movie requests = %d, want 1", got)
	}
}
