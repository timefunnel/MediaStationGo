package service

import (
	"bytes"
	"context"
	"errors"
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

func TestImageProxyServeUsesFallbackForMalformedJPEG(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "broken.jpg")
	writeTestJPEG(t, path, 200, 300)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read jpeg: %v", err)
	}
	broken := append(append([]byte(nil), original[:len(original)/2]...), make([]byte, 4096)...)
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatalf("write broken jpeg: %v", err)
	}

	proxy := NewImageProxy(&config.Config{
		App:   config.AppConfig{DataDir: root},
		Cache: config.CacheConfig{CacheDir: t.TempDir()},
	}, zap.NewNop())
	fallbackCalled := false
	proxy.variantFallback = func(_ context.Context, _ []byte, variant imageVariantOptions) ([]byte, string, error) {
		fallbackCalled = true
		if variant.maxWidth != 50 || variant.quality != 70 {
			t.Fatalf("variant = %+v", variant)
		}
		return testJPEGBytes(t, 50, 75), "image/jpeg", nil
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/media-1/Images/Primary?maxWidth=50&quality=70", nil)
	rec := httptest.NewRecorder()
	if err := proxy.Serve(context.Background(), rec, req, path); err != nil {
		t.Fatalf("serve image: %v", err)
	}
	if !fallbackCalled {
		t.Fatal("malformed jpeg did not use variant fallback")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q", rec.Code, rec.Body.String())
	}
	if bytes.Equal(rec.Body.Bytes(), broken) {
		t.Fatal("variant response silently returned malformed original")
	}
	img, err := jpeg.Decode(rec.Body)
	if err != nil {
		t.Fatalf("decode fallback jpeg: %v", err)
	}
	if got := img.Bounds().Dx(); got != 50 {
		t.Fatalf("width = %d, want 50", got)
	}
}

func TestImageProxyServeRejectsMalformedJPEGWhenFallbackFails(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "broken.jpg")
	broken := []byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x04, 0x00, 0x00}
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatalf("write broken jpeg: %v", err)
	}

	proxy := NewImageProxy(&config.Config{
		App:   config.AppConfig{DataDir: root},
		Cache: config.CacheConfig{CacheDir: t.TempDir()},
	}, zap.NewNop())
	proxy.variantFallback = func(context.Context, []byte, imageVariantOptions) ([]byte, string, error) {
		return nil, "", errors.New("fallback failed")
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/media-1/Images/Primary?maxWidth=50&quality=70", nil)
	rec := httptest.NewRecorder()
	if err := proxy.Serve(context.Background(), rec, req, path); err != nil {
		t.Fatalf("serve image: %v", err)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
	if bytes.Equal(rec.Body.Bytes(), broken) {
		t.Fatal("failed variant response returned malformed original")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestImageVariantFFmpegScaleFilter(t *testing.T) {
	tests := []struct {
		variant imageVariantOptions
		want    string
	}{
		{variant: imageVariantOptions{maxWidth: 342}, want: `scale=min(iw\,342):-2`},
		{variant: imageVariantOptions{maxHeight: 428}, want: `scale=-2:min(ih\,428)`},
		{variant: imageVariantOptions{maxWidth: 342, maxHeight: 428}, want: `scale=min(iw\,342):min(ih\,428):force_original_aspect_ratio=decrease`},
	}
	for _, tt := range tests {
		if got := imageVariantFFmpegScaleFilter(tt.variant); got != tt.want {
			t.Fatalf("filter = %q, want %q", got, tt.want)
		}
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

func testJPEGBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}
