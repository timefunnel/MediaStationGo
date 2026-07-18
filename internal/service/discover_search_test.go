package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
)

type discoverSearchRoundTripFunc func(*http.Request) (*http.Response, error)

func (f discoverSearchRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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

func TestSearchDiscoverCatalogOnlySearchesAdultMoviesForAdultCode(t *testing.T) {
	var catalogRequests atomic.Int32
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

	client := &http.Client{Transport: discoverSearchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		catalogRequests.Add(1)
		body := `{"results":[],"list":[]}`
		if strings.Contains(req.URL.Host, "douban") {
			body = `[]`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	cfg := &config.Config{Secrets: config.SecretsConfig{
		TMDbAPIKey:   "test-key",
		TMDbAPIProxy: "https://tmdb.invalid",
	}}
	tmdb := NewTMDbProvider(cfg, zap.NewNop(), nil)
	tmdb.client = client
	douban := NewDoubanProvider(cfg, zap.NewNop())
	douban.client = client
	bangumi := NewBangumiProvider(cfg, zap.NewNop())
	bangumi.base = "https://bangumi.invalid"
	bangumi.client = client
	adult := NewAdultProvider(zap.NewNop(), nil)
	result := SearchDiscoverCatalog(t.Context(), "MIZD-534", tmdb, douban, bangumi, adult)
	if len(result.Errors) != 0 || len(result.Items) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if got := catalogRequests.Load(); got != 0 {
		t.Fatalf("non-adult catalog requests = %d, want 0", got)
	}
	if got := performerRequests.Load(); got != 0 {
		t.Fatalf("performer requests = %d, want 0", got)
	}
	if got := movieRequests.Load(); got != 1 {
		t.Fatalf("movie requests = %d, want 1", got)
	}
}
