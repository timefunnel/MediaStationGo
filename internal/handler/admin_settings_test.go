package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/middleware"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"github.com/ShukeBta/MediaStationGo/internal/service"
)

func TestPlaybackProbeSettingValidatesAndPersistsForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := db.AutoMigrate(&model.Setting{}); err != nil {
		t.Fatal(err)
	}
	svc := &service.Container{Repo: repository.New(db), Cfg: &config.Config{}}
	svc.Stream = service.NewStreamService(svc.Cfg, zap.NewNop(), svc.Repo, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(middleware.CtxUserRole, c.GetHeader("Test-Role")) })
	router.PUT("/settings", middleware.AdminRequired(), updateSettingHandler(svc))
	for _, test := range []struct {
		role, value string
		status      int
	}{
		{"user", "true", http.StatusForbidden},
		{"admin", "invalid", http.StatusBadRequest},
		{"admin", "", http.StatusBadRequest},
		{"admin", "true", http.StatusNoContent},
		{"admin", "false", http.StatusNoContent},
	} {
		body := fmt.Sprintf(`{"key":%q,"value":%q}`, service.PlaybackCloudProbeEnabledSettingKey, test.value)
		request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Test-Role", test.role)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("role=%s value=%q status=%d body=%s", test.role, test.value, response.Code, response.Body.String())
		}
		persisted, err := svc.Repo.Setting.Get(t.Context(), service.PlaybackCloudProbeEnabledSettingKey)
		if err != nil {
			t.Fatal(err)
		}
		if test.status == http.StatusNoContent && persisted != test.value {
			t.Fatalf("saved setting=%q, want %q", persisted, test.value)
		}
		if test.status != http.StatusNoContent && persisted != "" {
			t.Fatal("rejected setting was persisted")
		}
	}
}
