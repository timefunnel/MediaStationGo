package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/middleware"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"github.com/ShukeBta/MediaStationGo/internal/service"
)

func TestMediaProbeQueueAdminOnlyAndNoImplicitExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Setting{}, &model.Media{}, &model.Library{}, &model.MediaProbeJob{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	svc := &service.Container{Repo: repos, Log: zap.NewNop(), MediaProbeQueue: service.NewMediaProbeQueueService(repos, nil, nil, zap.NewNop())}
	router := gin.New()
	Register(router, &config.Config{Secrets: config.SecretsConfig{JWTSecret: "synthetic-secret"}}, zap.NewNop(), svc)
	token := func(role string) string {
		value, err := jwt.NewWithClaims(jwt.SigningMethodHS256, &middleware.Claims{UserID: "synthetic", Role: role}).SignedString([]byte("synthetic-secret"))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, test := range []struct {
		role, method, path, body string
		status                   int
	}{
		{"", "GET", "/api/admin/media-probe-queue", "", 401},
		{"user", "GET", "/api/admin/media-probe-queue", "", 403},
		{"user", "POST", "/api/admin/media-probe-queue/start", `{}`, 403},
		{"admin", "GET", "/api/admin/media-probe-queue", "", 200},
		{"admin", "POST", "/api/admin/media-probe-queue/collect", "", 200},
		{"admin", "POST", "/api/admin/media-probe-queue/plan", "", 400},
		{"admin", "POST", "/api/admin/media-probe-queue/start", `{"confirmed":false}`, 409},
		{"admin", "GET", "/api/admin/media-probe-queue?page=invalid", "", 400},
		{"admin", "PUT", "/api/admin/media-probe-queue/config", `{}`, 400},
	} {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		if test.role != "" {
			request.Header.Set("Authorization", "Bearer "+token(test.role))
		}
		if test.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("%s %s as %q: status=%d body=%s", test.method, test.path, test.role, response.Code, response.Body.String())
		}
		if test.path == "/api/admin/media-probe-queue" && test.role == "admin" && !strings.Contains(response.Body.String(), `"running":false`) {
			t.Fatal("queue not paused by default")
		}
	}
}
