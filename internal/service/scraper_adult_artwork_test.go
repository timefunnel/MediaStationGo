package service

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
)

func TestDeriveAdultPosterIfNeededFromBackdrop(t *testing.T) {
	backdrop := image.NewRGBA(image.Rect(0, 0, 1200, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 1200; x++ {
			backdrop.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 80, A: 255})
		}
	}
	cacheDir := t.TempDir()
	backdropPath := filepath.Join(cacheDir, "backdrop.jpg")
	file, err := os.Create(backdropPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(file, backdrop, &jpeg.Options{Quality: 90}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	images := NewImageProxy(&config.Config{Cache: config.CacheConfig{CacheDir: cacheDir}}, zap.NewNop())
	scraper := &ScraperService{images: images, log: zap.NewNop()}
	match := &Match{MediaType: "adult", BackdropURL: backdropPath}

	scraper.deriveAdultPosterIfNeeded(t.Context(), &model.Media{Base: model.Base{ID: "media-1"}}, &model.Library{Type: "adult"}, match)

	if match.PosterURL == "" {
		t.Fatal("poster was not generated")
	}
	if !strings.Contains(match.PosterURL, "adult-posters") {
		t.Fatalf("poster path should use adult-posters cache: %s", match.PosterURL)
	}
	data, _, err := images.Fetch(t.Context(), match.PosterURL)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 600 || img.Bounds().Dy() != 900 {
		t.Fatalf("poster size = %dx%d, want 600x900", img.Bounds().Dx(), img.Bounds().Dy())
	}
}
