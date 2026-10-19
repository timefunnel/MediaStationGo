package handler

import (
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"github.com/ShukeBta/MediaStationGo/internal/service"
)

const testMigratedArtworkRef = "/example/old poster.jpg"

func migratedArtworkRouter(t *testing.T, target string) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Media{}, &model.CloudArtworkAlias{}); err != nil {
		t.Fatal(err)
	}
	media := model.Media{Base: model.Base{ID: "owner"}, Path: "/media/example.mkv", PosterURL: target}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CloudArtworkAlias{SourceHash: service.CloudArtworkSourceHash("openlist", testMigratedArtworkRef), MediaID: media.ID, ImageField: "poster_url"}).Error; err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{App: config.AppConfig{DataDir: filepath.Dir(target)}, Cache: config.CacheConfig{CacheDir: t.TempDir(), RemoteImageDelivery: "direct"}}
	repos := repository.New(db)
	svc := &service.Container{Repo: repos, Media: service.NewMediaService(cfg, zap.NewNop(), repos), ImageProxy: service.NewImageProxy(cfg, zap.NewNop())}
	router := gin.New()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		router.Handle(method, "/api/img/cloud/:type", cloudArtworkProxyHandler(svc))
		router.Handle(method, "/api/cloud/play/:type", cloudPlayHandler(svc))
	}
	return router, db, cfg.Cache.CacheDir
}

func TestCloudArtworkAliasesRedirectDifferentWidthsWithoutCloudResolver(t *testing.T) {
	router, _, _ := migratedArtworkRouter(t, "https://image.tmdb.org/t/p/w500/current.jpg")
	for _, endpoint := range []string{"/api/img/cloud/openlist", "/api/cloud/play/openlist"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, size := range []struct{ query, target string }{{"maxWidth=360&quality=70", "w342"}, {"MaxWidth=185", "w185"}, {"width=640", "w500"}} {
				request := httptest.NewRequest(method, endpoint+"?ref="+url.QueryEscape(testMigratedArtworkRef)+"&"+size.query, nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, request)
				want := "https://image.tmdb.org/t/p/" + size.target + "/current.jpg"
				if rec.Code != http.StatusFound || rec.Header().Get("Location") != want || rec.Body.Len() != 0 {
					t.Fatalf("%s %s: code=%d location=%q bytes=%d", method, request.URL, rec.Code, rec.Header().Get("Location"), rec.Body.Len())
				}
			}
		}
	}
}

func TestCloudArtworkAliasPreservesHeightAndQualityForLocalOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "original.png")
	img := image.NewRGBA(image.Rect(0, 0, 400, 600))
	for y := range 600 {
		for x := range 400 {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x + y), 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	router, _, _ := migratedArtworkRouter(t, path)
	for _, endpoint := range []string{"/api/img/cloud/openlist", "/api/cloud/play/openlist"} {
		outputs := make(map[int]string)
		for _, quality := range []int{30, 90} {
			rec := httptest.NewRecorder()
			query := fmt.Sprintf("?ref=%s&MaxWidth=100&MaxHeight=120&Quality=%d", url.QueryEscape(testMigratedArtworkRef), quality)
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint+query, nil))
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
				t.Fatalf("variant failed: code=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
			}
			decoded, _, err := image.Decode(rec.Body)
			if err != nil || decoded.Bounds().Dx() != 80 || decoded.Bounds().Dy() != 120 {
				t.Fatalf("variant dimensions: image=%v err=%v", decoded, err)
			}
			outputs[quality] = rec.Header().Get("ETag")
		}
		if outputs[30] == "" || outputs[30] == outputs[90] {
			t.Fatalf("quality parameter lost: %v", outputs)
		}
	}
}

func TestCloudArtworkAliasFailureDoesNotUseOldCacheOrCloudResolver(t *testing.T) {
	router, db, cacheDir := migratedArtworkRouter(t, "")
	// A usable legacy original must not bypass a known, removed migration target.
	key := fmt.Sprintf("cloud-%x", sha256.Sum256([]byte("cloud-image:openlist:"+testMigratedArtworkRef)))
	cache := filepath.Join(cacheDir, "images", key)
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/api/img/cloud/openlist", "/api/cloud/play/openlist"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint+"?ref="+url.QueryEscape(testMigratedArtworkRef)+"&maxWidth=360", nil))
		if rec.Code != http.StatusGone {
			t.Fatalf("missing target fell through: code=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	if err := db.Migrator().DropTable(&model.CloudArtworkAlias{}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/img/cloud/openlist?ref="+url.QueryEscape(testMigratedArtworkRef), nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("lookup failure fell through: code=%d", rec.Code)
	}
}

func TestUnmigratedCloudArtworkKeepsExistingCache(t *testing.T) {
	router, _, cacheDir := migratedArtworkRouter(t, "https://image.tmdb.org/t/p/w500/current.jpg")
	const ref = "/example/unmigrated.jpg"
	key := fmt.Sprintf("cloud-%x", sha256.Sum256([]byte("cloud-image:openlist:"+ref)))
	path := filepath.Join(cacheDir, "images", key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/api/img/cloud/openlist", "/api/cloud/play/openlist"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint+"?ref="+url.QueryEscape(ref), nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("unmigrated cache failed: code=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}
