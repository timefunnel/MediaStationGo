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

func TestDeriveAdultPosterIfPosterMatchesBackdrop(t *testing.T) {
	cacheDir := t.TempDir()
	backdropPath := filepath.Join(cacheDir, "same-cover.jpg")
	writeAdultArtworkTestImage(t, backdropPath, 800, 538)

	images := NewImageProxy(&config.Config{Cache: config.CacheConfig{CacheDir: cacheDir}}, zap.NewNop())
	scraper := &ScraperService{images: images, log: zap.NewNop()}
	match := &Match{MediaType: "adult", PosterURL: backdropPath, BackdropURL: backdropPath}

	scraper.deriveAdultPosterIfNeeded(t.Context(), &model.Media{Base: model.Base{ID: "media-2"}}, &model.Library{Type: "adult"}, match)

	if match.PosterURL == backdropPath {
		t.Fatal("poster should be regenerated when poster and backdrop are the same landscape image")
	}
	if match.BackdropURL != backdropPath {
		t.Fatalf("backdrop should stay unchanged: %s", match.BackdropURL)
	}
	assertAdultPosterSize(t, images, match.PosterURL)
}

func TestDeriveAdultPosterKeepsPortraitPoster(t *testing.T) {
	cacheDir := t.TempDir()
	posterPath := filepath.Join(cacheDir, "poster.jpg")
	backdropPath := filepath.Join(cacheDir, "backdrop.jpg")
	writeAdultArtworkTestImage(t, posterPath, 600, 900)
	writeAdultArtworkTestImage(t, backdropPath, 800, 538)

	images := NewImageProxy(&config.Config{Cache: config.CacheConfig{CacheDir: cacheDir}}, zap.NewNop())
	scraper := &ScraperService{images: images, log: zap.NewNop()}
	match := &Match{MediaType: "adult", PosterURL: posterPath, BackdropURL: backdropPath}

	scraper.deriveAdultPosterIfNeeded(t.Context(), &model.Media{Base: model.Base{ID: "media-3"}}, &model.Library{Type: "adult"}, match)

	if match.PosterURL != posterPath {
		t.Fatalf("portrait poster should stay unchanged: %s", match.PosterURL)
	}
}

func writeAdultArtworkTestImage(t *testing.T, path string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 80, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(file, img, &jpeg.Options{Quality: 90}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertAdultPosterSize(t *testing.T, images *ImageProxy, path string) {
	t.Helper()
	data, _, err := images.Fetch(t.Context(), path)
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
