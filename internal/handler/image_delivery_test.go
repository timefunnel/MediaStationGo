package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestImageDeliveryWebAndEmbyRedirectToPublicCDN(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Media{}); err != nil {
		t.Fatal(err)
	}
	const raw = "https://image.tmdb.org/t/p/w500/poster.jpg"
	if err := db.Create(&model.Media{
		Base: model.Base{ID: "media-direct"}, Title: "Direct artwork",
		Path: "/media/example.mkv", PosterURL: raw,
	}).Error; err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Cache: config.CacheConfig{CacheDir: t.TempDir(), RemoteImageDelivery: "direct"}}
	repos := repository.New(db)
	svc := &service.Container{
		Repo: repos, Emby: service.NewEmbyService(cfg, zap.NewNop(), repos),
		ImageProxy: service.NewImageProxy(cfg, zap.NewNop()),
	}
	router := gin.New()
	registerEmbyRoutes(router, "test-secret", svc)
	router.GET("/api/img", imageProxyHandler(svc))
	router.HEAD("/api/img", imageProxyHandler(svc))
	for _, path := range []string{
		"/api/img?url=" + url.QueryEscape(raw) + "&maxWidth=360&access_token=example",
		"/Items/media-direct/Images/Primary?MaxWidth=360&api_key=example",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://image.tmdb.org/t/p/w342/poster.jpg" {
				t.Fatalf("%s %s: status=%d headers=%v", method, path, rec.Code, rec.Header())
			}
			if rec.Body.Len() != 0 || rec.Header().Get("Cache-Control") != "public, max-age=300" || rec.Header().Get("Pragma") != "" || rec.Header().Get("Expires") != "" {
				t.Fatalf("%s %s: unexpected redirect response: %v", method, path, rec)
			}
		}
	}
}
