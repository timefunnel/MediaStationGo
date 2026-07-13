package service

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
)

func TestImageProxyServeRespectsEmbyImageVariantQuery(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "poster.jpg")
	writeTestJPEG(t, path, 200, 300)

	proxy := NewImageProxy(&config.Config{
		App:   config.AppConfig{DataDir: root},
		Cache: config.CacheConfig{CacheDir: t.TempDir()},
	}, zap.NewNop())

	req := httptest.NewRequest(http.MethodGet, "/Items/media-1/Images/Primary?maxWidth=50&quality=70", nil)
	rec := httptest.NewRecorder()

	if err := proxy.Serve(context.Background(), rec, req, path); err != nil {
		t.Fatalf("serve image: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "image/jpeg") {
		t.Fatalf("Content-Type = %q, want jpeg", contentType)
	}
	img, err := jpeg.Decode(rec.Body)
	if err != nil {
		t.Fatalf("decode response jpeg: %v", err)
	}
	if got := img.Bounds().Dx(); got != 50 {
		t.Fatalf("width = %d, want 50", got)
	}
	if got := img.Bounds().Dy(); got != 75 {
		t.Fatalf("height = %d, want 75", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != imageBrowserCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, imageBrowserCacheControl)
	}
}

func writeTestJPEG(t *testing.T, path string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create jpeg: %v", err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
}
