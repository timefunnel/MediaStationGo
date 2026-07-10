package handler

import (
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"github.com/ShukeBta/MediaStationGo/internal/service"
)

func TestEmbyLibraryImageServesFolderCoverGrid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	posterA := writeTestPNG(t, filepath.Join(dir, "poster-a.png"), color.RGBA{220, 40, 40, 255})
	posterB := writeTestPNG(t, filepath.Join(dir, "poster-b.png"), color.RGBA{40, 80, 220, 255})

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
	for _, row := range []model.Media{
		{Base: model.Base{ID: "media-a"}, LibraryID: lib.ID, Title: "A", Path: filepath.Join(dir, "a.mkv"), PosterURL: posterA},
		{Base: model.Base{ID: "media-b"}, LibraryID: lib.ID, Title: "B", Path: filepath.Join(dir, "b.mkv"), PosterURL: posterB},
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
	if w.Header().Get("ETag") == "" {
		t.Fatal("folder cover should expose a stable ETag")
	}
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "max-age=2592000") {
		t.Fatalf("folder cover Cache-Control = %q, want long cache", got)
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
