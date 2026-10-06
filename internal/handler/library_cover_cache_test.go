package handler

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/service"
	"go.uber.org/zap"
)

func BenchmarkLibraryCoverRendering(b *testing.B) {
	cfg := &config.Config{App: config.AppConfig{DataDir: b.TempDir()}, Cache: config.CacheConfig{CacheDir: b.TempDir()}}
	svc := &service.Container{ImageProxy: service.NewImageProxy(cfg, zap.NewNop())}
	artworks := make([]service.EmbyFolderCoverArtwork, 0, 4)
	for i := range 4 {
		img := image.NewRGBA(image.Rect(0, 0, 120, 180))
		draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{uint8(30 + 50*i), 70, 130, 255}), image.Point{}, draw.Src)
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, img); err != nil {
			b.Fatal(err)
		}
		path := filepath.Join(cfg.App.DataDir, fmt.Sprintf("poster-%d.png", i))
		if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
			b.Fatal(err)
		}
		artworks = append(artworks, service.EmbyFolderCoverArtwork{MediaID: fmt.Sprint(i), ImageType: "Primary", URL: path})
	}
	b.Run("cold_360", func(b *testing.B) {
		b.ReportAllocs()
		round := time.Now().UnixNano()
		for i := 0; i < b.N; i++ {
			artworks[0].Tag = fmt.Sprintf("cold-%d-%d", round, i)
			if _, err := renderLibraryCover(b.Context(), svc, "华语电影", artworks, 360, 203); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached_360", func(b *testing.B) {
		if _, err := renderLibraryCover(b.Context(), svc, "华语电影", artworks, 360, 203); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := renderLibraryCover(b.Context(), svc, "华语电影", artworks, 360, 203); err != nil {
				b.Fatal(err)
			}
		}
	})
}
