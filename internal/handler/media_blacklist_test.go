package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/middleware"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"github.com/ShukeBta/MediaStationGo/internal/service"
)

func blacklistRouteRequest(t *testing.T, router *gin.Engine, user, method, path string, status int) map[string]any {
	t.Helper()
	claims := middleware.Claims{UserID: user, Role: "user", RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("blacklist-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, nil)
	if user != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Emby-Token", token)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != status {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.Code, status, resp.Body.String())
	}
	payload := map[string]any{}
	if resp.Body.Len() > 0 {
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
	}
	return payload
}

func TestMediaBlacklistRoutesAndEmbyAliases(t *testing.T) {
	_, db := embyFavoriteRouteFixture(t)
	repos := repository.New(db)
	cfg := &config.Config{}
	emby := service.NewEmbyService(cfg, zap.NewNop(), repos).SetRuntimeCache(service.NewRuntimeCacheService(cfg, zap.NewNop()))
	svc := &service.Container{Repo: repos, Emby: emby, Permissions: service.NewPermissionService(zap.NewNop(), repos)}
	router := gin.New()
	registerEmbyRoutes(router, "blacklist-test-secret", svc)
	api := router.Group("/api")
	api.Use(middleware.AuthRequired("blacklist-test-secret"), activeUserRequired(svc))
	registerAuthedFavoriteAndMediaActionRoutes(api, svc)
	request := func(user, method, path string, status int) map[string]any {
		return blacklistRouteRequest(t, router, user, method, path, status)
	}
	request("", http.MethodGet, "/api/blacklist", http.StatusUnauthorized)
	// Warm each supported Emby mount before changing the MSG blacklist.
	for _, path := range []string{"/Items", "/emby/Items", "/items", "/emby/items", "/Users/user-1/Items"} {
		request("user-1", http.MethodGet, path+"?ParentId=movies&IncludeItemTypes=Movie", http.StatusOK)
	}
	for _, id := range []string{"movie", "episode-2"} {
		request("user-1", http.MethodPost, "/api/media/"+id+"/blacklist", http.StatusOK)
		request("user-1", http.MethodPost, "/api/media/"+id+"/blacklist", http.StatusOK)
	}
	if state := request("user-1", http.MethodGet, "/api/media/episode-1/blacklist/status", http.StatusOK); state["blocked"] != true {
		t.Fatalf("whole-series state=%v", state)
	}
	for _, path := range []string{"/Items", "/emby/Items", "/items", "/emby/items", "/Users/user-1/Items"} {
		page := request("user-1", http.MethodGet, path+"?ParentId=movies&IncludeItemTypes=Movie", http.StatusOK)
		if page["TotalRecordCount"] != float64(0) || len(page["Items"].([]any)) != 0 {
			t.Fatalf("alias leaked blocked movie: %v", page)
		}
	}
	for _, path := range []string{"/Items/movie", "/emby/Items/episode-1", "/Items/series", "/Items/movie/PlaybackInfo", "/Videos/movie/stream"} {
		request("user-1", http.MethodGet, path, http.StatusNotFound)
	}
	if item := request("user-2", http.MethodGet, "/Items/movie", http.StatusOK); item["Id"] != "movie" {
		t.Fatal("another account lost its movie")
	}
	items := request("user-1", http.MethodGet, "/api/blacklist", http.StatusOK)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("duplicate blacklist entries: %v", items)
	}
	for _, raw := range items {
		item := raw.(map[string]any)
		encoded, err := json.Marshal(item)
		if err != nil || strings.Contains(string(encoded), "/test/") {
			t.Fatalf("management card exposed storage paths: %s %v", encoded, err)
		}
		id := item["id"].(string)
		request("user-2", http.MethodDelete, "/api/blacklist/"+id, http.StatusOK)
	}
	if state := request("user-1", http.MethodGet, "/api/media/movie/blacklist/status", http.StatusOK); state["blocked"] != true {
		t.Fatal("another account removed a blacklist entry")
	}
	request("user-1", http.MethodDelete, "/api/media/episode-3/blacklist", http.StatusOK)
	request("user-1", http.MethodGet, "/Items/episode-1", http.StatusOK)
	request("user-1", http.MethodPost, "/api/media/missing/blacklist", http.StatusNotFound)
	if err := db.Model(&model.UserPermission{}).Where("user_id = ?", "user-1").Update("can_favorite", false).Error; err != nil {
		t.Fatal(err)
	}
	request("user-1", http.MethodGet, "/api/blacklist", http.StatusForbidden)
	request("user-1", http.MethodPost, "/api/media/movie/blacklist", http.StatusForbidden)
}
