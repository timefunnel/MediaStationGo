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
	assertPixelNear(t, img, 210, 50, color.RGBA{220, 40, 40, 255})
	assertPixelNear(t, img, 294, 68, color.RGBA{40, 80, 220, 255})
	assertPixelNear(t, img, 180, 145, color.RGBA{40, 180, 80, 255})
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

func TestEmbyFolderCoverGalleryUsesEverySelectedPosterOnce(t *testing.T) {
	colors := []color.RGBA{
		{255, 0, 0, 255}, {0, 255, 0, 255},
		{0, 0, 255, 255}, {255, 255, 0, 255},
	}
	for count := 1; count <= embyFolderCoverGridLimit; count++ {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			images := make([]image.Image, count)
			for i := range images {
				poster := image.NewRGBA(image.Rect(0, 0, 80, 120))
				draw.Draw(poster, poster.Bounds(), &image.Uniform{C: colors[i]}, image.Point{}, draw.Src)
				images[i] = poster
			}
			placements := embyFolderCoverPosterPlacements(320, 180, images)
			if len(placements) != count {
				t.Fatalf("selected %d works, got %d placements", count, len(placements))
			}
			body, err := buildEmbyFolderCoverGallery(images, "影视库", 320, 180)
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
			for _, posterColor := range colors[:count] {
				found := false
				for y := 0; y < 180 && !found; y++ {
					for x := 0; x < 320; x++ {
						if color.RGBAModel.Convert(img.At(x, y)).(color.RGBA) == posterColor {
							found = true
							break
						}
					}
				}
				if !found {
					t.Fatalf("selected poster %v was not visible", posterColor)
				}
			}
			second, err := buildEmbyFolderCoverGallery(images, "影视库", 320, 180)
			if err != nil || !bytes.Equal(body, second) {
				t.Fatalf("identical artwork must produce identical cover bytes: %v", err)
			}
		})
	}
}

func TestEmbyFolderCoverGalleryRejectsMissingArtwork(t *testing.T) {
	for _, images := range [][]image.Image{nil, {nil}, {image.NewRGBA(image.Rectangle{})}} {
		if _, err := buildEmbyFolderCoverGallery(images, "影视库", 320, 180); err == nil {
			t.Fatal("missing artwork must not produce a successful cover")
		}
	}
}

func TestEmbyFolderCoverBackgroundUsesWholeFirstPosterAndStableLighting(t *testing.T) {
	// Shifted bounds catch accidental origin-based sampling and cover cropping.
	first := image.NewRGBA(image.Rect(5, 8, 45, 68))
	draw.Draw(first, first.Bounds(), &image.Uniform{C: color.RGBA{255, 0, 0, 255}}, image.Point{}, draw.Src)
	draw.Draw(first, image.Rect(25, 8, 45, 68), &image.Uniform{C: color.RGBA{0, 0, 255, 255}}, image.Point{}, draw.Src)
	second := image.NewRGBA(image.Rect(0, 0, 40, 60))
	draw.Draw(second, second.Bounds(), &image.Uniform{C: color.RGBA{0, 255, 0, 255}}, image.Point{}, draw.Src)
	hue, saturation := embyFolderCoverPalette(first)
	if hue > 0.05 && hue < 0.95 || saturation > 0.43 {
		t.Fatalf("equal-area colors should select the stable red hue bin, not average into gray: h=%v s=%v", hue, saturation)
	}
	for _, width := range []int{320, 640} {
		background := image.NewRGBA(image.Rect(0, 0, width, width*9/16))
		drawEmbyFolderCoverBackground(background, first)
		again := image.NewRGBA(background.Bounds())
		drawEmbyFolderCoverBackground(again, first)
		if !bytes.Equal(background.Pix, again.Pix) {
			t.Fatal("grain must be stable between previews")
		}
		left := background.RGBAAt(width/10, width*9/32)
		right := background.RGBAAt(width*9/10, width*9/32)
		if int(right.R)+int(right.G)+int(right.B) <= int(left.R)+int(left.G)+int(left.B) {
			t.Fatal("soft lighting should brighten the right side")
		}
		for y := 0; y < background.Bounds().Dy(); y++ {
			for x := 0; x < width; x++ {
				if background.RGBAAt(x, y).A != 255 {
					t.Fatal("cover background must be opaque")
				}
			}
		}
		other := image.NewRGBA(background.Bounds())
		drawEmbyFolderCoverBackground(other, second)
		if bytes.Equal(background.Pix, other.Pix) {
			t.Fatal("changing the first poster should change the palette")
		}
	}
}

func TestEmbyFolderCoverPaletteKeepsDominantColorWithComplementaryAccent(t *testing.T) {
	poster := image.NewRGBA(image.Rect(0, 0, 100, 150))
	draw.Draw(poster, poster.Bounds(), &image.Uniform{C: color.RGBA{93, 48, 135, 255}}, image.Point{}, draw.Src)
	draw.Draw(poster, image.Rect(20, 40, 80, 75), &image.Uniform{C: color.RGBA{130, 195, 140, 255}}, image.Point{}, draw.Src)
	draw.Draw(poster, image.Rect(0, 100, 100, 150), &image.Uniform{C: color.RGBA{18, 22, 28, 255}}, image.Point{}, draw.Src)
	hue, saturation := embyFolderCoverPalette(poster)
	if hue < 0.72 || hue > 0.82 || saturation < 0.25 {
		t.Fatalf("purple artwork with a green accent lost its dominant color: h=%v s=%v", hue, saturation)
	}
	gray := image.NewRGBA(image.Rect(0, 0, 20, 30))
	draw.Draw(gray, gray.Bounds(), &image.Uniform{C: color.RGBA{145, 145, 145, 255}}, image.Point{}, draw.Src)
	if _, saturation := embyFolderCoverPalette(gray); saturation != 0 {
		t.Fatalf("gray artwork should stay neutral, got saturation=%v", saturation)
	}
}

func TestEmbyFolderCoverGalleryPreservesLandscapeAspect(t *testing.T) {
	images := []image.Image{image.NewRGBA(image.Rect(0, 0, 160, 90))}
	placements := embyFolderCoverPosterPlacements(320, 180, images)
	if len(placements) != 1 {
		t.Fatalf("unexpected landscape placements: %v", placements)
	}
	if delta := absInt(placements[0].width*9 - placements[0].height*16); delta > 16 {
		t.Fatalf("landscape aspect was distorted: %v", placements[0])
	}
}

func TestEmbyFolderCoverTitleFitsLongNamesAndRejectsUnsupportedGlyphs(t *testing.T) {
	poster := image.NewRGBA(image.Rect(0, 0, 40, 60))
	for _, title := range []string{"动画电影", "港台劇", "Movies & TV", "这是一个很长很长的测试影视媒体库名称"} {
		body, err := buildEmbyFolderCoverGallery([]image.Image{poster}, title, 320, 180)
		if err != nil {
			t.Fatalf("render %q: %v", title, err)
		}
		img, err := png.Decode(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		glyphPixels := 0
		for y := 0; y < 180; y++ {
			for x := 0; x < 320; x++ {
				c := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
				if c.R > 225 && c.G > 225 && c.B > 225 {
					glyphPixels++
					if x >= 128 || y < 36 || y >= 144 {
						t.Fatalf("title %q escaped its reserved area at (%d,%d)", title, x, y)
					}
				}
			}
		}
		if glyphPixels == 0 {
			t.Fatalf("title %q has no visible glyphs", title)
		}
	}
	for _, title := range []string{"", "库\U0010ffff"} {
		if _, err := buildEmbyFolderCoverGallery([]image.Image{poster}, title, 320, 180); err == nil {
			t.Fatalf("invalid title %q must not silently lose its glyphs", title)
		}
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
