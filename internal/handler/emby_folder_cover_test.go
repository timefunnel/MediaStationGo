package handler

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"github.com/ShukeBta/MediaStationGo/internal/service"
)

func TestEmbyLibraryImageServesFolderCoverGallery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	posterA := writeTestPNG(t, filepath.Join(dir, "poster-a.png"), color.RGBA{220, 40, 40, 255})
	posterB := writeTestPNG(t, filepath.Join(dir, "poster-b.png"), color.RGBA{40, 80, 220, 255})
	posterC := writeTestPNG(t, filepath.Join(dir, "poster-c.png"), color.RGBA{40, 180, 80, 255})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	lib := model.Library{Base: model.Base{ID: "lib-folder"}, Name: "电影", Path: dir, Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	base := time.Now()
	for _, row := range []model.Media{
		{Base: model.Base{ID: "media-a", CreatedAt: base.Add(2 * time.Minute), UpdatedAt: base.Add(2 * time.Minute)}, LibraryID: lib.ID, Title: "A", Path: filepath.Join(dir, "a.mkv"), PosterURL: posterA},
		{Base: model.Base{ID: "media-b", CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)}, LibraryID: lib.ID, Title: "B", Path: filepath.Join(dir, "b.mkv"), PosterURL: posterB},
		{Base: model.Base{ID: "media-c", CreatedAt: base, UpdatedAt: base}, LibraryID: lib.ID, Title: "C", Path: filepath.Join(dir, "c.mkv"), PosterURL: posterC},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("create media: %v", err)
		}
	}

	cfg := &config.Config{App: config.AppConfig{DataDir: dir}, Cache: config.CacheConfig{CacheDir: t.TempDir()}}
	imageProxy := service.NewImageProxy(cfg, zap.NewNop())
	router := gin.New()
	registerEmbyRoutes(router, "test-secret", &service.Container{
		Repo:       repos,
		Emby:       service.NewEmbyService(cfg, zap.NewNop(), repos),
		ImageProxy: imageProxy,
	})

	req := httptest.NewRequest(http.MethodGet, "/Items/lib-folder/Images/Primary?maxWidth=320", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	if contentType := w.Header().Get("Content-Type"); !strings.Contains(contentType, "image/png") {
		t.Fatalf("expected png content type, got %q", contentType)
	}
	img, err := png.Decode(w.Body)
	if err != nil {
		t.Fatalf("decode folder cover: %v", err)
	}
	if got := img.Bounds(); got.Dx() != 320 || got.Dy() != 180 {
		t.Fatalf("folder cover dimensions = %v, want 320x180", got)
	}
	assertPixelNear(t, img, 54, 90, color.RGBA{220, 40, 40, 255})
	assertPixelNear(t, img, 160, 90, color.RGBA{40, 80, 220, 255})
	assertPixelNear(t, img, 267, 90, color.RGBA{40, 180, 80, 255})
	if w.Header().Get("ETag") == "" {
		t.Fatal("folder cover should expose a stable ETag")
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000" {
		t.Fatalf("folder cover Cache-Control = %q, want long cache", got)
	}
	for _, key := range []string{"Last-Modified", "Expires"} {
		if w.Header().Get(key) == "" {
			t.Fatalf("folder cover should expose %s", key)
		}
	}
	if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
	if got := w.Header().Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Fatalf("Cross-Origin-Resource-Policy = %q, want cross-origin", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
}

func TestEmbyFolderCoverGalleryPreservesPosterEdges(t *testing.T) {
	// Edge markers catch the old strip renderer's horizontal/vertical cropping.
	edges := []color.RGBA{
		{255, 0, 0, 255}, {0, 255, 0, 255},
		{0, 0, 255, 255}, {255, 255, 0, 255},
	}
	poster := image.NewRGBA(image.Rect(0, 0, 80, 120))
	draw.Draw(poster, poster.Bounds(), &image.Uniform{C: color.RGBA{100, 100, 100, 255}}, image.Point{}, draw.Src)
	for i, rect := range []image.Rectangle{
		image.Rect(10, 0, 70, 10), image.Rect(10, 110, 70, 120),
		image.Rect(0, 10, 10, 110), image.Rect(70, 10, 80, 110),
	} {
		draw.Draw(poster, rect, &image.Uniform{C: edges[i]}, image.Point{}, draw.Src)
	}
	for count := 1; count <= embyFolderCoverGridLimit; count++ {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			images := make([]image.Image, count)
			for i := range images {
				images[i] = poster
			}
			body, err := buildEmbyFolderCoverGallery(images, 320, 180)
			if err != nil {
				t.Fatal(err)
			}
			img, err := png.Decode(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if img.Bounds() != image.Rect(0, 0, 320, 180) {
				t.Fatalf("unexpected cover dimensions: %v", img.Bounds())
			}
			for _, edge := range edges {
				found := false
				for y := 0; y < 180 && !found; y++ {
					for x := 0; x < 320; x++ {
						if color.RGBAModel.Convert(img.At(x, y)).(color.RGBA) == edge {
							found = true
							break
						}
					}
				}
				if !found {
					t.Fatalf("poster edge %v was cropped out", edge)
				}
			}
			second, err := buildEmbyFolderCoverGallery(images, 320, 180)
			if err != nil || !bytes.Equal(body, second) {
				t.Fatalf("identical artwork must produce identical cover bytes: %v", err)
			}
		})
	}
}

func TestEmbyFolderCoverGalleryRejectsMissingArtwork(t *testing.T) {
	for _, images := range [][]image.Image{nil, {nil}, {image.NewRGBA(image.Rectangle{})}} {
		if _, err := buildEmbyFolderCoverGallery(images, 320, 180); err == nil {
			t.Fatal("missing artwork must not produce a successful cover")
		}
	}
}

func TestEmbyFolderCoverGalleryPreservesLandscapeAspect(t *testing.T) {
	images := []image.Image{image.NewRGBA(image.Rect(0, 0, 160, 90))}
	rects := embyFolderCoverPosterRects(320, 180, images)
	if len(rects) != 1 || !rects[0].In(image.Rect(0, 0, 320, 180)) {
		t.Fatalf("landscape artwork outside cover: %v", rects)
	}
	if delta := absInt(rects[0].Dx()*9 - rects[0].Dy()*16); delta > 9 {
		t.Fatalf("landscape aspect was distorted: %v", rects[0])
	}
}

func TestEmbyLibraryImageWithoutArtworkReturnsNoStore404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	lib := model.Library{Base: model.Base{ID: "lib-empty"}, Name: "空库", Path: dir, Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}

	cfg := &config.Config{App: config.AppConfig{DataDir: dir}, Cache: config.CacheConfig{CacheDir: t.TempDir()}}
	router := gin.New()
	registerEmbyRoutes(router, "test-secret", &service.Container{
		Repo:       repos,
		Emby:       service.NewEmbyService(cfg, zap.NewNop(), repos),
		ImageProxy: service.NewImageProxy(cfg, zap.NewNop()),
	})

	req := httptest.NewRequest(http.MethodGet, "/Items/lib-empty/Images/Primary?maxWidth=320", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func writeTestPNG(t *testing.T, path string, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 24))
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			img.SetRGBA(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return path
}

func assertPixelNear(t *testing.T, img image.Image, x, y int, want color.RGBA) {
	t.Helper()
	got := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
	const tolerance = 2
	if absInt(int(got.R)-int(want.R)) > tolerance ||
		absInt(int(got.G)-int(want.G)) > tolerance ||
		absInt(int(got.B)-int(want.B)) > tolerance {
		t.Fatalf("pixel (%d,%d) = %#v, want near %#v", x, y, got, want)
	}
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
