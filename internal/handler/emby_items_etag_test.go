package handler

import (
	"net/http"
	"net/http/httptest"
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

func TestEmbyIfNoneMatchMatchesWeakAndListValidators(t *testing.T) {
	const etag = `"current"`
	for _, header := range []string{
		`"current"`,
		`W/"current"`,
		`"stale", W/"current"`,
		`*`,
	} {
		if !embyIfNoneMatchMatches(header, etag) {
			t.Fatalf("If-None-Match %q should match %q", header, etag)
		}
	}
	if embyIfNoneMatchMatches(`"stale"`, etag) {
		t.Fatal("stale validator must not match")
	}
}

func TestEmbyItemsSupportsConditionalGET(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	if err := repos.User.Create(t.Context(), &model.User{
		Base:         model.Base{ID: "user-1"},
		Username:     "tester",
		PasswordHash: "x",
		Role:         "admin",
		Tier:         "plus",
		IsActive:     true,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	library := model.Library{Name: "电影", Path: "D:\\media\\movies", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &library); err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := db.Create(&model.Media{
		Base:      model.Base{ID: "movie-1"},
		LibraryID: library.ID,
		Title:     "条件请求测试",
		Path:      "D:\\media\\movies\\conditional.mkv",
		Container: "mkv",
	}).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}

	const secret = "test-secret"
	router := gin.New()
	registerEmbyRoutes(router, secret, &service.Container{
		Repo: repos,
		Emby: service.NewEmbyService(&config.Config{}, zap.NewNop(), repos),
	})
	token := signedTestToken(t, secret)
	path := "/emby/Items?UserId=user-1&ParentId=" + library.ID + "&StartIndex=0&Limit=48"

	first := performAuthenticatedItemsRequest(router, path, token, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("first response must include ETag")
	}
	if got := first.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}

	notModified := performAuthenticatedItemsRequest(router, path, token, etag)
	if notModified.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d body=%s", notModified.Code, notModified.Body.String())
	}
	if notModified.Body.Len() != 0 {
		t.Fatalf("304 body must be empty, got %q", notModified.Body.String())
	}
	if got := notModified.Header().Get("ETag"); got != etag {
		t.Fatalf("304 ETag = %q, want %q", got, etag)
	}

	stale := performAuthenticatedItemsRequest(router, path, token, `"stale"`)
	if stale.Code != http.StatusOK {
		t.Fatalf("stale validator status = %d body=%s", stale.Code, stale.Body.String())
	}
	if stale.Body.Len() == 0 {
		t.Fatal("stale validator must receive the current JSON payload")
	}
}

func performAuthenticatedItemsRequest(
	router http.Handler,
	path string,
	token string,
	ifNoneMatch string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Emby-Token", token)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}
