package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/middleware"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"github.com/ShukeBta/MediaStationGo/internal/service"
)

func embyFavoriteRouteFixture(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	for _, id := range []string{"user-1", "user-2"} {
		user := model.User{Base: model.Base{ID: id}, Username: id, Role: "user", IsActive: true, AllowedLibraryIDs: []string{"movies", "tv"}}
		if err := repos.User.Create(t.Context(), &user); err != nil {
			t.Fatal(err)
		}
	}
	for _, lib := range []model.Library{
		{Base: model.Base{ID: "movies"}, Name: "Movies", Type: "movie", Path: "/test/movies", Enabled: true},
		{Base: model.Base{ID: "tv"}, Name: "TV", Type: "tv", Path: "/test/tv", Enabled: true},
		{Base: model.Base{ID: "hidden"}, Name: "Hidden", Type: "tv", Path: "/test/hidden", Enabled: true},
	} {
		if err := repos.Library.Create(t.Context(), &lib); err != nil {
			t.Fatal(err)
		}
	}
	for _, series := range []model.Series{
		{Base: model.Base{ID: "series"}, LibraryID: "tv", Title: "B Show"},
		{Base: model.Base{ID: "hidden-series"}, LibraryID: "hidden", Title: "Hidden"},
	} {
		if err := db.Create(&series).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows := []model.Media{{Base: model.Base{ID: "movie"}, LibraryID: "movies", Title: "A Movie", Path: "/test/movies/a.mkv"}}
	for i := 1; i <= 3; i++ {
		rows = append(rows, model.Media{Base: model.Base{ID: fmt.Sprintf("episode-%d", i)}, LibraryID: "tv", SeriesID: "series", Title: "B Show",
			Path: fmt.Sprintf("/test/tv/B Show/Season 01/%d.mkv", i), SeasonNum: 1, EpisodeNum: i})
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	emby := service.NewEmbyService(cfg, zap.NewNop(), repos).SetRuntimeCache(service.NewRuntimeCacheService(cfg, zap.NewNop()))
	if _, err := emby.InitializeBrowseKeys(t.Context()); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	registerEmbyRoutes(router, "favorite-test-secret", &service.Container{Repo: repos, Emby: emby})
	return router, db
}

func favoriteRouteRequest(t *testing.T, router *gin.Engine, user, method, path string, status int) map[string]any {
	t.Helper()
	claims := middleware.Claims{UserID: user, Role: "user", RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("favorite-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Emby-Token", token)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != status {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, resp.Code, status, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestEmbyFavoriteRoutesSeriesReadbackAndWarmList(t *testing.T) {
	router, db := embyFavoriteRouteFixture(t)
	const list = "/Items?UserId=user-1&Filters=IsFavorite&Recursive=true&IncludeItemTypes=Movie,Series,Video,MusicVideo&SortBy=SortName&SortOrder=Ascending&Fields=Overview&Limit=1"
	for _, favorite := range []bool{true, false, true} {
		// Prime both the collection and the long-lived ordinary series page.
		favoriteRouteRequest(t, router, "user-1", http.MethodGet, list, http.StatusOK)
		favoriteRouteRequest(t, router, "user-1", http.MethodGet, "/Users/user-1/Items?ParentId=tv&IncludeItemTypes=Series", http.StatusOK)
		method := http.MethodDelete
		if favorite {
			method = http.MethodPost
		}
		for _, id := range []string{"series", "movie"} {
			action := favoriteRouteRequest(t, router, "user-1", method, "/Users/user-1/FavoriteItems/"+id, http.StatusOK)
			if action["IsFavorite"] != favorite {
				t.Fatalf("favorite action returned wrong persisted state: %v", action)
			}
			item := favoriteRouteRequest(t, router, "user-1", http.MethodGet, "/Users/user-1/Items/"+id, http.StatusOK)
			if item["UserData"].(map[string]any)["IsFavorite"] != favorite {
				t.Fatalf("Windows readback mismatch: %v", item)
			}
		}
		seriesPage := favoriteRouteRequest(t, router, "user-1", http.MethodGet, "/Users/user-1/Items?ParentId=tv&IncludeItemTypes=Series", http.StatusOK)
		seriesItems := seriesPage["Items"].([]any)
		if len(seriesItems) != 1 || seriesItems[0].(map[string]any)["UserData"].(map[string]any)["IsFavorite"] != favorite {
			t.Fatalf("warm ordinary series page retained old favorite state: %v", seriesPage)
		}
		for start, id := range []string{"movie", "series", ""} {
			page := favoriteRouteRequest(t, router, "user-1", http.MethodGet, fmt.Sprintf("%s&StartIndex=%d", list, start), http.StatusOK)
			items := page["Items"].([]any)
			wantTotal := float64(0)
			if favorite {
				wantTotal = 2
			}
			if page["TotalRecordCount"] != wantTotal || page["StartIndex"] != float64(start) {
				t.Fatalf("favorite pagination: %v", page)
			}
			if !favorite || id == "" {
				if len(items) != 0 {
					t.Fatalf("unexpected favorite rows: %v", page)
				}
				continue
			}
			if len(items) != 1 {
				t.Fatalf("favorite page size: %v", page)
			}
			item := items[0].(map[string]any)
			if item["Id"] != id || item["UserData"].(map[string]any)["IsFavorite"] != true {
				t.Fatalf("favorite page item: %v", item)
			}
			if id == "series" && (item["Type"] != "Series" || item["RecursiveItemCount"] != float64(3)) {
				t.Fatalf("series collapsed incorrectly: %v", item)
			}
		}
		favoriteRouteRequest(t, router, "user-2", http.MethodGet, list, http.StatusForbidden)
		other := favoriteRouteRequest(t, router, "user-2", http.MethodGet, "/Items?Filters=IsFavorite&Recursive=true&IncludeItemTypes=Movie,Series", http.StatusOK)
		if other["TotalRecordCount"] != float64(0) {
			t.Fatalf("query UserId bypassed authenticated user: %v", other)
		}
	}
	for _, id := range []string{"missing", "hidden-series"} {
		favoriteRouteRequest(t, router, "user-1", http.MethodPost, "/Users/user-1/FavoriteItems/"+id, http.StatusNotFound)
		favoriteRouteRequest(t, router, "user-1", http.MethodGet, "/Users/user-1/Items/"+id, http.StatusNotFound)
	}
	var count int64
	if err := db.Model(&model.Favorite{}).Where("media_id NOT IN ?", []string{"movie", "series"}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("unexpected favorite rows after actions: %d %v", count, err)
	}
}

func TestEmbyFavoriteRouteReadbackFailureReturnsError(t *testing.T) {
	router, db := embyFavoriteRouteFixture(t)
	reads := 0
	if err := db.Callback().Query().Before("gorm:query").Register("fail-favorite-action-readback", func(tx *gorm.DB) {
		if tx.Statement.Table == "favorites" {
			reads++
			if reads == 2 {
				tx.AddError(errors.New("favorite readback failed"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	payload := favoriteRouteRequest(t, router, "user-1", http.MethodPost, "/Users/user-1/FavoriteItems/series", http.StatusInternalServerError)
	if payload["error"] != "favorite readback failed" || payload["IsFavorite"] != nil {
		t.Fatalf("readback error was hidden by synthetic success: %v", payload)
	}
	var count int64
	if err := db.Model(&model.Favorite{}).Where("user_id = ? AND media_id = ?", "user-1", "series").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("test did not reach persisted-write/readback-failure boundary: %d %v", count, err)
	}
}
