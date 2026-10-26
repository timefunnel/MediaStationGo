package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"github.com/ShukeBta/MediaStationGo/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestProbeExchangeAdminRoutesAndImportConfirmation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "msg.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := db.AutoMigrate(&model.Media{}, &model.Library{}, &model.LibraryRoot{}, &model.Setting{}, &model.MediaProbeJob{}, &model.MediaProbeExchange{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Library{Base: model.Base{ID: "lib"}, Name: "Synthetic", Path: "cloud://openlist/Synthetic", Type: "movie"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Media{Base: model.Base{ID: "media"}, LibraryID: "lib", Title: "Synthetic", Path: "cloud://openlist/Synthetic/A.mkv", SizeBytes: 12000000}).Error; err != nil {
		t.Fatal(err)
	}
	queue := service.NewMediaProbeQueueService(repository.New(db), nil, nil, zap.NewNop())
	router := gin.New()
	admin := router.Group("/api/admin")
	admin.Use(func(c *gin.Context) {
		if c.GetHeader("X-Synthetic-Role") != "admin" {
			c.AbortWithStatus(403)
			return
		}
		c.Next()
	})
	registerMediaProbeQueueRoutes(admin, &service.Container{MediaProbeQueue: queue})
	request := func(endpoint string, value any, authorized bool) *httptest.ResponseRecorder {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/admin/media-probe-queue/exchange/"+endpoint, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if authorized {
			req.Header.Set("X-Synthetic-Role", "admin")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	if response := request("manifest", map[string]any{"library_ids": []string{}}, false); response.Code != 403 {
		t.Fatalf("nonadmin export: %d", response.Code)
	}
	response := request("manifest", map[string]any{"library_ids": []string{}}, true)
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("export: %d %s", response.Code, response.Body.String())
	}
	var manifest service.ProbeExchangeManifest
	if err := json.Unmarshal(response.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if err := manifest.CheckDigest(); err != nil {
		t.Fatal(err)
	}
	bundle := service.ProbeExchangeBundle{Version: 1, ManifestID: manifest.ID, ManifestDigest: manifest.Digest, PlanDigest: strings.Repeat("a", 64), BatchID: "batch", Provenance: "synthetic original transfer", Results: []service.ProbeExchangeResult{{MediaID: "media", Fingerprint: manifest.Items[0].Fingerprint, FileID: "201", SHA1: strings.Repeat("A", 40), SizeBytes: 12000000, Source: "ffprobe", Status: "completed", Metadata: &service.ProbeResult{DurationSec: 120, Width: 1920, Height: 1080, VideoCodec: "hevc", AudioCodec: "aac"}}}}
	bundle.AccountID = "42"
	response = request("preview", bundle, true)
	if response.Code != 200 {
		t.Fatalf("preview: %d %s", response.Code, response.Body.String())
	}
	var preview service.ProbeExchangePreview
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	response = request("apply", map[string]any{"bundle": bundle, "preview_digest": preview.Digest, "confirmed": false}, true)
	if response.Code != 409 {
		t.Fatal("import without confirmation accepted")
	}
	response = request("apply", map[string]any{"bundle": bundle, "preview_digest": preview.Digest, "confirmed": true}, true)
	if response.Code != 200 {
		t.Fatalf("apply: %d %s", response.Code, response.Body.String())
	}
	var current model.Media
	if err := db.First(&current, "id = ?", "media").Error; err != nil {
		t.Fatal(err)
	}
	if current.Width != 1920 || current.Path != "cloud://openlist/Synthetic/A.mkv" {
		t.Fatalf("wrong result: %+v", current)
	}
	if response := request("preview", map[string]any{"not_a_bundle": true}, true); response.Code != 400 {
		t.Fatal("unknown wire field accepted")
	}
	legacy := httptest.NewRequest(http.MethodGet, "/api/admin/media-probe-queue", nil)
	legacy.Header.Set("X-Synthetic-Role", "admin")
	legacyResponse := httptest.NewRecorder()
	router.ServeHTTP(legacyResponse, legacy)
	if legacyResponse.Code != 200 {
		t.Fatalf("legacy queue route changed: %d", legacyResponse.Code)
	}
}
