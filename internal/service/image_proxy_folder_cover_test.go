package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"go.uber.org/zap"
)

func folderCoverCacheFixture(t *testing.T) (*ImageProxy, *config.Config, []EmbyFolderCoverArtwork, []byte) {
	t.Helper()
	cfg := &config.Config{App: config.AppConfig{DataDir: t.TempDir()}, Cache: config.CacheConfig{CacheDir: t.TempDir()}}
	img := image.NewRGBA(image.Rect(0, 0, 16, 9))
	img.Set(0, 0, color.RGBA{120, 60, 30, 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.App.DataDir, "poster.png")
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	art := []EmbyFolderCoverArtwork{{MediaID: "movie", ImageType: "Primary", Tag: "movie", URL: path}}
	return NewImageProxy(cfg, zap.NewNop()), cfg, art, buffer.Bytes()
}

func TestFolderCoverCachePersistsAndInvalidates(t *testing.T) {
	proxy, cfg, art, body := folderCoverCacheFixture(t)
	var renders int
	render := func() ([]byte, error) { renders++; return body, nil }
	tag := EmbyFolderCoverTag("", "电影", art)
	for range 3 {
		got, err := proxy.FetchFolderCover(t.Context(), tag, art, 16, 9, render)
		if err != nil || !bytes.Equal(got, body) || renders != 1 {
			t.Fatalf("warm cover renders=%d err=%v", renders, err)
		}
	}
	proxy = NewImageProxy(cfg, zap.NewNop())
	if _, err := proxy.FetchFolderCover(t.Context(), tag, art, 16, 9, render); err != nil || renders != 1 {
		t.Fatalf("restart lost disk cover: renders=%d err=%v", renders, err)
	}
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(art[0].URL, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.FetchFolderCover(t.Context(), tag, art, 16, 9, render); err != nil || renders != 2 {
		t.Fatalf("same-URL artwork update did not invalidate: renders=%d err=%v", renders, err)
	}
	newTag := EmbyFolderCoverTag("", "华语电影", art)
	if _, err := proxy.FetchFolderCover(t.Context(), newTag, art, 16, 9, render); err != nil || renders != 3 {
		t.Fatalf("library rename did not invalidate: renders=%d err=%v", renders, err)
	}
	files, err := filepath.Glob(filepath.Join(cfg.Cache.CacheDir, "images", "variants", "folder-covers", "*.png"))
	if err != nil || len(files) != 3 {
		t.Fatalf("cached cover files=%v err=%v", files, err)
	}
	for _, path := range files {
		expired := time.Now().Add(-imageVariantCacheTTL - time.Hour)
		if err := os.Chtimes(path, expired, expired); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := proxy.FetchFolderCover(t.Context(), newTag, art, 16, 9, render); err != nil || renders != 4 {
		t.Fatalf("expired cover reused: renders=%d err=%v", renders, err)
	}
}

func TestFolderCoverCacheColdRemoteSourceIsReused(t *testing.T) {
	proxy, cfg, _, body := folderCoverCacheFixture(t)
	var fetches atomic.Int32
	transport := imageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		fetches.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"image/png"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
	})
	proxy.client = &http.Client{Transport: transport}
	art := []EmbyFolderCoverArtwork{{MediaID: "remote", URL: "https://image.tmdb.org/t/p/w500/folder-cover-test.png"}}
	tag := EmbyFolderCoverTag("", "电影", art)
	renders := 0
	render := func() ([]byte, error) {
		renders++
		data, _, err := proxy.Fetch(t.Context(), art[0].URL)
		return data, err
	}
	for range 2 {
		if _, err := proxy.FetchFolderCover(t.Context(), tag, art, 16, 9, render); err != nil {
			t.Fatal(err)
		}
	}
	proxy = NewImageProxy(cfg, zap.NewNop())
	proxy.client = &http.Client{Transport: transport}
	if _, err := proxy.FetchFolderCover(t.Context(), tag, art, 16, 9, render); err != nil {
		t.Fatal(err)
	}
	if renders != 1 || fetches.Load() != 1 {
		t.Fatalf("cold source caused redundant generation: renders=%d fetches=%d", renders, fetches.Load())
	}
}

func TestFolderCoverCacheSeparatesCompositionOrderArtworkAndSize(t *testing.T) {
	proxy, _, art, body := folderCoverCacheFixture(t)
	second := art[0]
	second.MediaID, second.Tag = "second", "second"
	second.URL = filepath.Join(filepath.Dir(art[0].URL), "second.png")
	if err := os.WriteFile(second.URL, body, 0o600); err != nil {
		t.Fatal(err)
	}
	pair := []EmbyFolderCoverArtwork{art[0], second}
	replacement := pair[0]
	replacement.URL = filepath.Join(filepath.Dir(art[0].URL), "replacement.png")
	if err := os.WriteFile(replacement.URL, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		art           []EmbyFolderCoverArtwork
		width, height int
	}{
		{pair, 16, 9},
		{[]EmbyFolderCoverArtwork{second, art[0]}, 16, 9},
		{[]EmbyFolderCoverArtwork{replacement, second}, 16, 9},
		{pair, 32, 18},
	}
	renders := 0
	for i, item := range cases {
		tag := EmbyFolderCoverTag("", "电影", item.art)
		render := func() ([]byte, error) {
			renders++
			var buffer bytes.Buffer
			err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, item.width, item.height)))
			return buffer.Bytes(), err
		}
		for range 2 {
			if _, err := proxy.FetchFolderCover(t.Context(), tag, item.art, item.width, item.height, render); err != nil {
				t.Fatal(err)
			}
		}
		if renders != i+1 {
			t.Fatalf("order/artwork/size reused another cover: case=%d renders=%d", i, renders)
		}
	}
}

func TestFolderCoverCacheSharesConcurrentGenerationAndCancelsWaiter(t *testing.T) {
	proxy, _, art, body := folderCoverCacheFixture(t)
	var renders atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	render := func() ([]byte, error) {
		renders.Add(1)
		close(started)
		<-release
		return body, nil
	}
	tag := EmbyFolderCoverTag("", "电影", art)
	errorsOut := make(chan error, 17)
	go func() {
		_, err := proxy.FetchFolderCover(t.Context(), tag, art, 16, 9, render)
		errorsOut <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	waiter := make(chan error, 1)
	go func() { _, err := proxy.FetchFolderCover(ctx, tag, art, 16, 9, render); waiter <- err }()
	cancel()
	if err := <-waiter; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter=%v", err)
	}
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			_, err := proxy.FetchFolderCover(t.Context(), tag, art, 16, 9, render)
			errorsOut <- err
		})
	}
	// A different composition is not blocked by this in-flight render.
	if _, err := proxy.FetchFolderCover(t.Context(), "another-composition", art, 16, 9, func() ([]byte, error) { return body, nil }); err != nil {
		t.Fatal(err)
	}
	close(release)
	workers.Wait()
	for range 17 {
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
	if renders.Load() != 1 {
		t.Fatalf("concurrent cover rendered %d times", renders.Load())
	}
}

func TestFolderCoverCacheFailuresAreExplicitAndRecoverable(t *testing.T) {
	proxy, cfg, art, body := folderCoverCacheFixture(t)
	tag := EmbyFolderCoverTag("", "电影", art)
	failure := errors.New("poster unavailable")
	if got, err := proxy.FetchFolderCover(t.Context(), tag, art, 16, 9, func() ([]byte, error) { return nil, failure }); got != nil || !errors.Is(err, failure) {
		t.Fatalf("renderer failure hidden: body=%d err=%v", len(got), err)
	}
	dir := filepath.Join(cfg.Cache.CacheDir, "images", "variants", "folder-covers")
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("blocked directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	render := func() ([]byte, error) { return body, nil }
	if got, err := proxy.FetchFolderCover(t.Context(), tag, art, 16, 9, render); got != nil || err == nil {
		t.Fatalf("cache write failure hidden: body=%d err=%v", len(got), err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.FetchFolderCover(t.Context(), tag, art, 16, 9, render); err != nil {
		t.Fatalf("failed render was cached: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.png"))
	if err != nil || len(files) != 1 {
		t.Fatalf("successful retry files=%v err=%v", files, err)
	}
	if err := os.WriteFile(files[0], []byte("corrupt PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := proxy.FetchFolderCover(t.Context(), tag, art, 16, 9, func() ([]byte, error) {
		t.Error("corrupt cache must report its failure")
		return body, nil
	}); got != nil || err == nil {
		t.Fatalf("corrupt cache returned success: body=%d err=%v", len(got), err)
	}
}
