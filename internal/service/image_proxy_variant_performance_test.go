package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
)

type imageVariantWaitContext struct {
	context.Context
	waiting chan<- struct{}
}

func (c imageVariantWaitContext) Done() <-chan struct{} {
	c.waiting <- struct{}{}
	return c.Context.Done()
}

func TestImageVariantCacheHitDoesNotLoadSource(t *testing.T) {
	p := NewImageProxy(&config.Config{Cache: config.CacheConfig{CacheDir: t.TempDir()}}, zap.NewNop())
	data := testJPEGBytes(t, 300, 450)
	modified := time.Now().Add(-time.Minute)
	variant := imageVariantOptions{maxWidth: 100, quality: 82, hasQuality: true}
	req := httptest.NewRequest(http.MethodGet, "/image?maxWidth=100&quality=82", nil)
	first := httptest.NewRecorder()
	if err := p.serveImageVariantWithLoader(first, req, "source", modified, int64(len(data)), imageBrowserCacheControl, variant, func() ([]byte, string, error) {
		return data, "image/jpeg", nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, "conditional"} {
		t.Run(method, func(t *testing.T) {
			request := req.Clone(t.Context())
			if method == "conditional" {
				request.Header.Set("If-None-Match", first.Header().Get("ETag"))
			} else {
				request.Method = method
			}
			rec := httptest.NewRecorder()
			err := p.serveImageVariantWithLoader(rec, request, "source", modified, int64(len(data)), imageBrowserCacheControl, variant, func() ([]byte, string, error) {
				t.Error("cache hit read the original image")
				return nil, "", errors.New("original must not be loaded")
			})
			want := http.StatusOK
			if method == "conditional" {
				want = http.StatusNotModified
			}
			if err != nil || rec.Code != want {
				t.Fatalf("status=%d err=%v", rec.Code, err)
			}
			if method == http.MethodGet && !bytes.Equal(rec.Body.Bytes(), first.Body.Bytes()) {
				t.Fatal("cache hit changed image bytes")
			}
			if method != http.MethodGet && rec.Body.Len() != 0 {
				t.Fatal("HEAD/304 returned a body")
			}
			if !strings.Contains(strings.Join(rec.Header().Values("Server-Timing"), ","), "img_cache;") {
				t.Fatal("cache timing is missing")
			}
		})
	}
	changed := errors.New("source version changed")
	if err := p.serveImageVariantWithLoader(httptest.NewRecorder(), req, "source", modified.Add(time.Second), int64(len(data)), imageBrowserCacheControl, variant, func() ([]byte, string, error) {
		return nil, "", changed
	}); !errors.Is(err, changed) {
		t.Fatalf("new source version reused stale variant: %v", err)
	}
}

func TestImageVariantConcurrentMissesShareWorkAndAllowCanceledWaiters(t *testing.T) {
	p := NewImageProxy(&config.Config{Cache: config.CacheConfig{CacheDir: t.TempDir()}}, zap.NewNop())
	data := testJPEGBytes(t, 300, 450)
	variant := imageVariantOptions{maxWidth: 100, quality: 82, hasQuality: true}
	key, path := p.imageVariantCachePaths("source", time.Now(), int64(len(data)), variant)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	load := func() ([]byte, string, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return data, "image/jpeg", nil
	}
	type outcome struct {
		result imageVariantResult
		err    error
	}
	results := make(chan outcome, 17)
	go func() {
		result, _, err := p.loadImageVariant(t.Context(), key, path, variant, load)
		results <- outcome{result, err}
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	_, shared, err := p.loadImageVariant(ctx, key, path, variant, load)
	cancel()
	if !shared || !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("waiter did not cancel independently: shared=%v err=%v", shared, err)
	}
	// Different dimensions must not wait behind this source's blocked variant.
	other := imageVariantOptions{maxWidth: 80}
	otherKey, otherPath := p.imageVariantCachePaths("source", time.Now(), int64(len(data)), other)
	if _, shared, err := p.loadImageVariant(t.Context(), otherKey, otherPath, other, func() ([]byte, string, error) {
		return data, "image/jpeg", nil
	}); err != nil || shared {
		close(release)
		t.Fatalf("different variant was blocked/shared: %v", err)
	}
	waiting := make(chan struct{}, 16)
	for i := 0; i < 16; i++ {
		go func() {
			ctx := imageVariantWaitContext{Context: t.Context(), waiting: waiting}
			result, _, err := p.loadImageVariant(ctx, key, path, variant, load)
			results <- outcome{result, err}
		}()
	}
	for i := 0; i < 16; i++ {
		<-waiting
	}
	close(release)
	var expected []byte
	for i := 0; i < 17; i++ {
		got := <-results
		if got.err != nil || len(got.result.body) == 0 {
			t.Fatalf("empty/failed shared result: %v", got.err)
		}
		if expected == nil {
			expected = got.result.body
		} else if !bytes.Equal(expected, got.result.body) {
			t.Fatal("shared requests returned different images")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("source reads=%d, want 1", calls.Load())
	}
	failed := errors.New("source unavailable")
	_, _, err = p.loadImageVariant(t.Context(), "failed", filepath.Join(t.TempDir(), "missing"), variant, func() ([]byte, string, error) {
		return nil, "", failed
	})
	if !errors.Is(err, failed) {
		t.Fatalf("source failure was hidden: %v", err)
	}
}

func TestImageVariantSharedFailureReleasesWaiters(t *testing.T) {
	for _, panics := range []bool{false, true} {
		name := "error"
		if panics {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			p := NewImageProxy(&config.Config{Cache: config.CacheConfig{CacheDir: t.TempDir()}}, zap.NewNop())
			variant := imageVariantOptions{maxWidth: 40}
			path := filepath.Join(p.imageVariantCacheDir(), "failure")
			entered, release := make(chan struct{}), make(chan struct{})
			leaderDone := make(chan any, 1)
			failed := errors.New("unavailable source")
			go func() {
				defer func() { leaderDone <- recover() }()
				_, _, _ = p.loadImageVariant(t.Context(), "failure", path, variant, func() ([]byte, string, error) {
					close(entered)
					<-release
					if panics {
						panic("decoder panic")
					}
					return nil, "", failed
				})
			}()
			<-entered
			waiting := make(chan struct{}, 1)
			waiterDone := make(chan error, 1)
			go func() {
				_, shared, err := p.loadImageVariant(imageVariantWaitContext{Context: t.Context(), waiting: waiting}, "failure", path, variant, func() ([]byte, string, error) {
					return nil, "", errors.New("waiter unexpectedly generated a variant")
				})
				if !shared {
					err = errors.New("waiter did not join the flight")
				}
				waiterDone <- err
			}()
			<-waiting
			close(release)
			if recovered := <-leaderDone; (recovered != nil) != panics {
				t.Fatalf("panic=%v, want %v", recovered, panics)
			}
			if err := <-waiterDone; err == nil || (!panics && !errors.Is(err, failed)) {
				t.Fatalf("shared failure was lost: %v", err)
			}
			data := testJPEGBytes(t, 80, 120)
			if _, shared, err := p.loadImageVariant(t.Context(), "failure", path, variant, func() ([]byte, string, error) {
				return data, "image/jpeg", nil
			}); err != nil || shared {
				t.Fatalf("failed flight was not removed: shared=%v err=%v", shared, err)
			}
		})
	}
}

func BenchmarkImageVariantCacheReadOrder(b *testing.B) {
	// Use a committed, non-private illustration; only the cache read order
	// differs. Source size is reported so these are not production predictions.
	source := filepath.Join("..", "..", "docs", "assets", "library-cover-textured-wall-preview.png")
	data, err := os.ReadFile(source)
	if err != nil {
		b.Fatal(err)
	}
	p := NewImageProxy(&config.Config{Cache: config.CacheConfig{CacheDir: b.TempDir()}}, zap.NewNop())
	req := httptest.NewRequest(http.MethodGet, "/image?maxWidth=360&quality=82", nil)
	if !p.serveImageFile(httptest.NewRecorder(), req, "source", source, imageBrowserCacheControl) {
		b.Fatal("warm variant cache failed")
	}
	for _, eager := range []bool{true, false} {
		name := "metadata-first"
		if eager {
			name = "original-first"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.ReportMetric(float64(len(data)), "source-bytes")
			for i := 0; i < b.N; i++ {
				rec := httptest.NewRecorder()
				if !eager {
					if !p.serveImageFile(rec, req, "source", source, imageBrowserCacheControl) {
						b.Fatal("cache miss")
					}
					continue
				}
				file, err := os.Open(source)
				if err != nil {
					b.Fatal(err)
				}
				stat, err := file.Stat()
				if err != nil {
					b.Fatal(err)
				}
				var sample [512]byte
				n, _ := file.Read(sample[:])
				_, _ = file.Seek(0, io.SeekStart)
				body, err := io.ReadAll(io.LimitReader(file, maxImageVariantInputBytes))
				_ = file.Close()
				if err != nil {
					b.Fatal(err)
				}
				if err := p.serveImageVariant(rec, req, "source", stat.ModTime(), stat.Size(), body, detectContentType(sample[:n]), imageBrowserCacheControl, imageVariantFromRequest(req)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
