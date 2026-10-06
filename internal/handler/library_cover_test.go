package handler

import (
	"bytes"
	"encoding/json"
	"image/color"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/middleware"
	"github.com/ShukeBta/MediaStationGo/internal/service"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

func TestLibraryCoverHTTPPreviewSaveAndPermissions(t *testing.T) {
	router, svc, secret := newPlaybackScopeTestRouter(t)
	dir := t.TempDir()
	cfg := &config.Config{App: config.AppConfig{DataDir: dir}, Cache: config.CacheConfig{CacheDir: t.TempDir()}}
	svc.Emby = service.NewEmbyService(cfg, zap.NewNop(), svc.Repo)
	svc.ImageProxy = service.NewImageProxy(cfg, zap.NewNop())
	for i, id := range []string{"media-1", "media-2"} {
		path := writeTestPNG(t, filepath.Join(dir, id+".png"), color.RGBA{uint8(40 + i*180), 60, 100, 255})
		if err := svc.Repo.DB.Table("media").Where("id = ?", id).Update("poster_url", path).Error; err != nil {
			t.Fatal(err)
		}
	}
	authed := router.Group("/api", middleware.AuthRequired(secret))
	registerAuthedLibraryRoutes(authed, svc)
	registerEmbyRoutes(router, secret, svc)
	admin := signedTestToken(t, secret)
	viewer, err := jwt.NewWithClaims(jwt.SigningMethodHS256, middleware.Claims{UserID: "user-1", Role: "user", RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	const payload = `{"media_ids":["media-2","media-1"]}`
	for _, route := range []struct{ method, path string }{{"GET", "/api/libraries/lib-1/cover"}, {"GET", "/api/libraries/lib-1/cover/candidates"}, {"PUT", "/api/libraries/lib-1/cover"}, {"POST", "/api/libraries/lib-1/cover/preview"}} {
		if w := request(route.method, route.path, payload, ""); w.Code != 401 {
			t.Fatalf("anonymous status=%d", w.Code)
		}
		if w := request(route.method, route.path, payload, viewer); w.Code != 403 {
			t.Fatalf("viewer status=%d body=%s", w.Code, w.Body)
		}
	}
	preview := request("POST", "/api/libraries/lib-1/cover/preview", payload, admin)
	if preview.Code != 200 || preview.Header().Get("Content-Type") != "image/png" || preview.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body)
	}
	lib, err := svc.Repo.Library.FindByID(t.Context(), "lib-1")
	if err != nil || len(lib.CoverMediaIDs) != 0 {
		t.Fatal("preview persisted draft")
	}
	if w := request("PUT", "/api/libraries/lib-1/cover", payload, admin); w.Code != 200 {
		t.Fatalf("save status=%d body=%s", w.Code, w.Body)
	}
	for _, route := range []string{"/api/libraries/lib-1/cover", "/api/libraries/lib-1/cover/candidates"} {
		w := request("GET", route, "", admin)
		var response struct {
			Items []service.LibraryCoverItem `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || len(response.Items) != 2 {
			t.Fatalf("poster card response %s: %s err=%v", route, w.Body, err)
		}
		for _, item := range response.Items {
			if item.UpdatedAt.IsZero() {
				t.Fatalf("card poster metadata missing: %#v", item)
			}
			if item.ID == "media-1" || item.ID == "media-2" {
				if item.PosterURL != filepath.Join(dir, item.ID+".png") || item.SelectionError != "" {
					t.Fatalf("card artwork differs from renderer: %#v", item)
				}
			} else if item.PosterURL != "" || item.SelectionError == "" {
				t.Fatalf("missing candidate artwork not explicitly marked: %#v", item)
			}
		}
	}
	for _, query := range []string{"?page=0", "?page=1.5", "?page=10000001"} {
		if w := request("GET", "/api/libraries/lib-1/cover/candidates"+query, "", admin); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid candidate page accepted: %s status=%d", query, w.Code)
		}
	}
	search := request("GET", "/api/libraries/lib-1/cover/candidates?q=no-matching-cover-title", "", admin)
	var empty struct {
		Items []service.LibraryCoverItem `json:"items"`
		Total int64                      `json:"total"`
	}
	if err := json.Unmarshal(search.Body.Bytes(), &empty); err != nil || search.Code != http.StatusOK || empty.Items == nil || len(empty.Items) != 0 || empty.Total != 0 {
		t.Fatalf("candidate search did not preserve browse contract: %s err=%v", search.Body, err)
	}
	image := request("GET", "/Items/lib-1/Images/Primary?maxWidth=960", "", admin)
	if image.Code != 200 || !bytes.Equal(image.Body.Bytes(), preview.Body.Bytes()) {
		t.Fatalf("saved Emby image differs from preview: status=%d", image.Code)
	}
	native := request("GET", "/api/libraries/lib-1/cover/image?maxWidth=960", "", admin)
	if native.Code != 200 || !bytes.Equal(native.Body.Bytes(), preview.Body.Bytes()) || native.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("Web image differs from preview: status=%d", native.Code)
	}
	if err := svc.Repo.DB.Table("libraries").Where("id = ?", "lib-1").Update("name", "华语电影").Error; err != nil {
		t.Fatal(err)
	}
	renamedPreview := request("POST", "/api/libraries/lib-1/cover/preview", payload, admin)
	renamedImage := request("GET", "/Items/lib-1/Images/Primary?maxWidth=960", "", admin)
	renamedNative := request("GET", "/api/libraries/lib-1/cover/image?maxWidth=960", "", admin)
	if renamedPreview.Code != 200 || renamedImage.Code != 200 || renamedNative.Code != 200 ||
		!bytes.Equal(renamedPreview.Body.Bytes(), renamedImage.Body.Bytes()) || !bytes.Equal(renamedPreview.Body.Bytes(), renamedNative.Body.Bytes()) {
		t.Fatal("renamed library preview and saved images disagree")
	}
	if bytes.Equal(image.Body.Bytes(), renamedImage.Body.Bytes()) || image.Header().Get("ETag") == renamedImage.Header().Get("ETag") {
		t.Fatal("renaming must update both the rendered library name and its cache tag")
	}
	wantTag := service.EmbyFolderCoverTag("lib-1", "华语电影", []service.EmbyFolderCoverArtwork{
		{MediaID: "media-2", ImageType: "Primary", Tag: "media-2", URL: filepath.Join(dir, "media-2.png")},
		{MediaID: "media-1", ImageType: "Primary", Tag: "media-1", URL: filepath.Join(dir, "media-1.png")},
	})
	if renamedImage.Header().Get("ETag") != `"`+wantTag+`"` {
		t.Fatal("Emby response cache tag must include the authoritative library name")
	}
	if w := request("GET", "/api/libraries/lib-1/cover/image", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous Web image status=%d", w.Code)
	}
	if err := svc.Repo.DB.Table("libraries").Where("id = ?", "lib-1").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/api/libraries/lib-1/cover/image", "", admin); w.Code != http.StatusNotFound {
		t.Fatalf("disabled library exposed Web image: status=%d", w.Code)
	}
	if err := svc.Repo.DB.Table("libraries").Where("id = ?", "lib-1").Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{}`, `{"media_ids":null}`, `{"media_ids":["media-1","media-1"]}`, `{"media_ids":["missing"]}`} {
		if w := request("PUT", "/api/libraries/lib-1/cover", bad, admin); w.Code != 400 {
			t.Fatalf("invalid save %s returned %d", bad, w.Code)
		}
	}
	if err := svc.Repo.DB.Table("media").Where("id = ?", "media-1").Update("poster_url", filepath.Join(dir, "missing.png")).Error; err != nil {
		t.Fatal(err)
	}
	if w := request("POST", "/api/libraries/lib-1/cover/preview", payload, admin); w.Code != http.StatusBadGateway {
		t.Fatalf("missing poster must not produce partial success: %d", w.Code)
	}
	if w := request("PUT", "/api/libraries/lib-1/cover", `{"media_ids":[]}`, admin); w.Code != 200 {
		t.Fatalf("reset failed: %d body=%s", w.Code, w.Body)
	}
	w := request("GET", "/api/libraries/lib-1/cover", "", admin)
	var result struct {
		MediaIDs []string `json:"media_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.MediaIDs == nil || len(result.MediaIDs) != 0 {
		t.Fatalf("reset configuration: %s err=%v", w.Body, err)
	}
}
