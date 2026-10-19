package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
)

func TestImageDeliveryDirectURL(t *testing.T) {
	const original = "https://image.tmdb.org/t/p/original/poster.jpg"
	proxy := NewImageProxy(&config.Config{Cache: config.CacheConfig{
		CacheDir: t.TempDir(), RemoteImageDelivery: "direct",
	}}, zap.NewNop())
	tests := []struct {
		name          string
		raw           string
		width, height int
		want          string
	}{
		{"card width", original, 360, 0, "https://image.tmdb.org/t/p/w342/poster.jpg"},
		{"small card", original, 96, 0, "https://image.tmdb.org/t/p/w92/poster.jpg"},
		{"source width ceiling", "https://image.tmdb.org/t/p/w185/poster.jpg", 640, 0, "https://image.tmdb.org/t/p/w185/poster.jpg"},
		{"no size request", original, 0, 0, original},
		{"original SVG", "https://image.tmdb.org/t/p/original/logo.svg", 360, 0, "https://image.tmdb.org/t/p/original/logo.svg"},
		{"invalid SVG size", "https://image.tmdb.org/t/p/w500/logo.svg", 360, 0, ""},
		{"exact height", original, 360, 500, ""},
		{"below CDN minimum", original, 24, 0, ""},
		{"other provider", "https://img9.doubanio.com/poster.jpg", 360, 0, ""},
		{"lookalike host", "https://image.tmdb.org.example.com/t/p/original/poster.jpg", 360, 0, ""},
		{"userinfo", "https://user@image.tmdb.org/t/p/original/poster.jpg", 360, 0, ""},
		{"HTTP", "http://image.tmdb.org/t/p/original/poster.jpg", 360, 0, ""},
		{"query", original + "?token=example", 360, 0, ""},
		{"empty query", original + "?", 360, 0, ""},
		{"fragment", original + "#example", 360, 0, ""},
		{"encoded path", "https://image.tmdb.org/t/p/original/poster%2Ftest.jpg", 360, 0, ""},
		{"unsupported path", "https://image.tmdb.org/private/poster.jpg", 360, 0, ""},
		{"invalid size", "https://image.tmdb.org/t/p/wzero/poster.jpg", 360, 0, ""},
		{"local file", "/media/poster.jpg", 360, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := proxy.directRemoteImageURL(tt.raw, imageVariantOptions{maxWidth: tt.width, maxHeight: tt.height})
			if got != tt.want || ok != (tt.want != "") {
				t.Fatalf("direct URL = %q, %v; want %q", got, ok, tt.want)
			}
		})
	}
	proxy.cfg.Cache.RemoteImageDelivery = "proxy"
	if _, ok := proxy.directRemoteImageURL(original, imageVariantOptions{}); ok {
		t.Fatal("proxy mode must not redirect")
	}
}

func TestImageDeliveryDirectServeAndServerFetch(t *testing.T) {
	proxy := NewImageProxy(&config.Config{Cache: config.CacheConfig{
		CacheDir: t.TempDir(), RemoteImageDelivery: "direct",
	}}, zap.NewNop())
	var calls atomic.Int32
	proxy.client = &http.Client{Transport: imageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"image/jpeg"}},
			Body:       io.NopCloser(bytes.NewReader(testJPEG)), Request: req,
		}, nil
	})}
	const raw = "https://image.tmdb.org/t/p/w500/poster.jpg"
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/img?maxWidth=360&quality=80", nil)
		if err := proxy.Serve(t.Context(), rec, req, raw); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://image.tmdb.org/t/p/w342/poster.jpg" {
			t.Fatalf("%s: status=%d headers=%v", method, rec.Code, rec.Header())
		}
		if rec.Body.Len() != 0 || rec.Header().Get("Cache-Control") != "public, max-age=300" {
			t.Fatalf("redirect must have an empty body and short cache lifetime: %v", rec)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("direct serving must not fetch the image on the server")
	}
	_, cachePath, _, err := proxy.remoteImageCachePaths(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("direct serving must not write an image cache: %v", err)
	}
	// Server-side composition/notifications still require real image bytes.
	body, contentType, err := proxy.Fetch(t.Context(), raw)
	if err != nil || !bytes.Equal(body, testJPEG) || contentType != "image/jpeg" || calls.Load() != 1 {
		t.Fatalf("server fetch: bytes=%d type=%q calls=%d err=%v", len(body), contentType, calls.Load(), err)
	}
	// A provider requiring request headers continues through the proxy.
	rec := httptest.NewRecorder()
	if err := proxy.Serve(t.Context(), rec, httptest.NewRequest(http.MethodGet, "/api/img", nil), "https://img9.doubanio.com/poster.jpg"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), testJPEG) || calls.Load() != 2 {
		t.Fatalf("provider proxy: status=%d calls=%d", rec.Code, calls.Load())
	}
}

func TestImageDeliveryDirectSkipsBackgroundPrefetch(t *testing.T) {
	proxy := NewImageProxy(&config.Config{Cache: config.CacheConfig{
		CacheDir: filepath.Join(t.TempDir(), "cache"), RemoteImageDelivery: "direct",
	}}, zap.NewNop())
	var calls atomic.Int32
	proxy.client = &http.Client{Transport: imageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"image/jpeg"}},
			Body: io.NopCloser(bytes.NewReader(testJPEG)), Request: req,
		}, nil
	})}
	const raw = "https://image.tmdb.org/t/p/w500/poster.jpg"
	discover := NewDiscoverService(zap.NewNop(), nil).SetImageProxy(proxy)
	if queued := discover.WarmMatchArtwork([]Match{{PosterURL: raw}}); queued != 0 {
		t.Fatalf("direct discovery queued %d downloads", queued)
	}
	scanner := &ScannerService{imageProxy: proxy, log: zap.NewNop()}
	scanner.prefetchRemoteArtworkFromScan(t.Context(), raw)
	scraper := &ScraperService{images: proxy, log: zap.NewNop()}
	const old = "https://image.tmdb.org/t/p/w500/old.jpg"
	if next, remove := scraper.prepareScrapedArtworkURL(t.Context(), "media-id", "poster", old, raw); next != raw || remove != old {
		t.Fatalf("direct scrape returned next=%q remove=%q", next, remove)
	}
	if calls.Load() != 0 {
		t.Fatalf("direct artwork caused %d background downloads", calls.Load())
	}
}
