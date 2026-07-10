package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestEmbyLowercasePlaybackInfoRouteReturnsJSON(t *testing.T) {
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
	lib := model.Library{Name: "电影", Path: t.TempDir(), Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := db.Create(&model.Media{
		Base:      model.Base{ID: "media-1"},
		LibraryID: lib.ID,
		Title:     "Lowercase Playback",
		Path:      filepath.Join(lib.Path, "lowercase-playback.mp4"),
		Container: "mp4",
	}).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}

	const secret = "test-secret"
	router := gin.New()
	registerEmbyRoutes(router, secret, &service.Container{
		Repo: repos,
		Emby: service.NewEmbyService(&config.Config{}, zap.NewNop(), repos),
	})

	req := httptest.NewRequest(http.MethodGet, "/users/user-1/items/media-1/playbackinfo", nil)
	req.Header.Set("X-Emby-Token", signedTestToken(t, secret))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode playback info: %v", err)
	}
	if _, ok := body["MediaSources"]; !ok {
		t.Fatalf("missing MediaSources: %#v", body)
	}
	sources, ok := body["MediaSources"].([]any)
	if !ok || len(sources) == 0 {
		t.Fatalf("unexpected MediaSources: %#v", body["MediaSources"])
	}
	source, ok := sources[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected MediaSource: %#v", sources[0])
	}
	directURL, _ := source["DirectStreamUrl"].(string)
	if !strings.Contains(directURL, "api_key=") {
		t.Fatalf("DirectStreamUrl should carry api_key for clients that do not repeat auth headers: %#v", source)
	}
	transcodeURL, _ := source["TranscodingUrl"].(string)
	if transcodeURL != "" && !strings.Contains(transcodeURL, "api_key=") {
		t.Fatalf("TranscodingUrl should carry api_key: %#v", source)
	}
}

func TestEmbyPlaybackInfoSubtitleDeliveryURLServesVTT(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Movie.zh.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\n你好\n"), 0o644); err != nil {
		t.Fatalf("write subtitle: %v", err)
	}
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
	lib := model.Library{Name: "电影", Path: dir, Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := db.Create(&model.Media{
		Base:       model.Base{ID: "media-sub"},
		LibraryID:  lib.ID,
		Title:      "Movie",
		Path:       filepath.Join(dir, "Movie.mkv"),
		Container:  "mkv",
		VideoCodec: "h264",
		AudioCodec: "aac",
	}).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}

	const secret = "test-secret"
	emby := service.NewEmbyService(&config.Config{}, zap.NewNop(), repos)
	subtitle := service.NewSubtitleService(zap.NewNop(), repos)
	emby.SetSubtitleService(subtitle)
	router := gin.New()
	registerEmbyRoutes(router, secret, &service.Container{
		Repo:     repos,
		Emby:     emby,
		Subtitle: subtitle,
	})

	token := signedTestToken(t, secret)
	req := httptest.NewRequest(http.MethodGet, "/emby/Items/media-sub/PlaybackInfo", nil)
	req.Header.Set("X-Emby-Token", token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected playback status: %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode playback info: %v", err)
	}
	source := body["MediaSources"].([]any)[0].(map[string]any)
	streams := source["MediaStreams"].([]any)
	var deliveryURL string
	for _, raw := range streams {
		stream := raw.(map[string]any)
		if stream["Type"] == "Subtitle" {
			deliveryURL, _ = stream["DeliveryUrl"].(string)
			break
		}
	}
	if deliveryURL == "" || !strings.Contains(deliveryURL, "api_key=") {
		t.Fatalf("subtitle DeliveryUrl should carry api_key: %#v", streams)
	}

	req = httptest.NewRequest(http.MethodGet, deliveryURL, nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected subtitle status: %d body=%s", w.Code, w.Body.String())
	}
	if contentType := w.Header().Get("Content-Type"); !strings.Contains(contentType, "text/vtt") {
		t.Fatalf("subtitle content type = %q", contentType)
	}
	if body := w.Body.String(); !strings.Contains(body, "WEBVTT") || !strings.Contains(body, "你好") {
		t.Fatalf("unexpected subtitle body: %q", body)
	}
}

func TestEmbyLegacyVideoSubtitleRouteServesVTT(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Movie.zh.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n"), 0o644); err != nil {
		t.Fatalf("write subtitle: %v", err)
	}
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
	lib := model.Library{Name: "Movies", Path: dir, Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := db.Create(&model.Media{
		Base:       model.Base{ID: "media-sub"},
		LibraryID:  lib.ID,
		Title:      "Movie",
		Path:       filepath.Join(dir, "Movie.mkv"),
		Container:  "mkv",
		VideoCodec: "h264",
	}).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}

	const secret = "test-secret"
	emby := service.NewEmbyService(&config.Config{}, zap.NewNop(), repos)
	subtitle := service.NewSubtitleService(zap.NewNop(), repos)
	emby.SetSubtitleService(subtitle)
	router := gin.New()
	registerEmbyRoutes(router, secret, &service.Container{
		Repo:     repos,
		Emby:     emby,
		Subtitle: subtitle,
	})

	req := httptest.NewRequest(http.MethodGet, "/emby/Videos/media-sub/media-sub/Subtitles/1/Stream.vtt?mp_track=0", nil)
	req.Header.Set("X-Emby-Token", signedTestToken(t, secret))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected legacy subtitle status: %d body=%s", w.Code, w.Body.String())
	}
	if contentType := w.Header().Get("Content-Type"); !strings.Contains(contentType, "text/vtt") {
		t.Fatalf("legacy subtitle content type = %q", contentType)
	}
	if body := w.Body.String(); !strings.Contains(body, "WEBVTT") || !strings.Contains(body, "hello") {
		t.Fatalf("unexpected legacy subtitle body: %q", body)
	}
}

func TestEmbyPlaybackInfoDoesNotExposeTokenInCloudPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	if err := repos.Setting.Set(t.Context(), service.CloudPlaybackModeSettingKey, service.CloudPlaybackModeSTRM); err != nil {
		t.Fatalf("set cloud playback mode: %v", err)
	}
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
	lib := model.Library{Name: "OpenList", Path: "cloud://openlist/Movies", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := db.Create(&model.Media{
		Base:      model.Base{ID: "cloud-1"},
		LibraryID: lib.ID,
		Title:     "Cloud Movie",
		Path:      "cloud://openlist/Movies/Movie.mkv",
		STRMURL:   "/api/cloud/play/openlist?ref=%2FMovies%2FMovie.mkv",
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

	req := httptest.NewRequest(http.MethodGet, "/users/user-1/items/cloud-1/playbackinfo", nil)
	req.Header.Set("X-Emby-Token", signedTestToken(t, secret))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode playback info: %v", err)
	}
	source := body["MediaSources"].([]any)[0].(map[string]any)
	pathURL, _ := source["Path"].(string)
	if pathURL != "/api/stream/cloud-1" {
		t.Fatalf("cloud Path should stay as non-tokenized display stream URL, got %#v", source)
	}
	if strings.Contains(pathURL, "api_key=") || strings.Contains(pathURL, "token=") {
		t.Fatalf("cloud Path must not expose auth key/token: %#v", source)
	}
	if strings.Contains(pathURL, "/api/cloud/play/") {
		t.Fatalf("cloud Path should not expose naked cloud play URL: %#v", source)
	}
	directURL, _ := source["DirectStreamUrl"].(string)
	if !strings.HasPrefix(directURL, "/api/stream/cloud-1") || !strings.Contains(directURL, "api_key=") {
		t.Fatalf("DirectStreamUrl should stay tokenized: %#v", source)
	}
	if source["SupportsDirectPlay"] != true {
		t.Fatalf("cloud media should advertise DirectPlay when tokenized Path is playable: %#v", source)
	}
	if source["SupportsTranscoding"] != false {
		t.Fatalf("cloud media should not advertise host transcoding: %#v", source)
	}
}

func TestEmbyItemsDoNotExposeTokenInEmbeddedCloudPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	if err := repos.Setting.Set(t.Context(), service.CloudPlaybackModeSettingKey, service.CloudPlaybackModeSTRM); err != nil {
		t.Fatalf("set cloud playback mode: %v", err)
	}
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
	lib := model.Library{Name: "OpenList", Path: "cloud://openlist/Movies", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := db.Create(&model.Media{
		Base:      model.Base{ID: "cloud-1"},
		LibraryID: lib.ID,
		Title:     "Cloud Movie",
		Path:      "cloud://openlist/Movies/Movie.mkv",
		STRMURL:   "/api/cloud/play/openlist?ref=%2FMovies%2FMovie.mkv",
		Container: "mkv",
	}).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}

	const secret = "test-secret"
	token := signedTestToken(t, secret)
	router := gin.New()
	registerEmbyRoutes(router, secret, &service.Container{
		Repo: repos,
		Emby: service.NewEmbyService(&config.Config{}, zap.NewNop(), repos),
	})

	req := httptest.NewRequest(http.MethodGet, "/emby/Users/user-1/Items?IncludeItemTypes=Movie&Recursive=true&Limit=5&X-Emby-Token="+token, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode items: %v", err)
	}
	items := body["Items"].([]any)
	if len(items) != 1 {
		t.Fatalf("unexpected items: %#v", body["Items"])
	}
	source := items[0].(map[string]any)["MediaSources"].([]any)[0].(map[string]any)
	pathURL, _ := source["Path"].(string)
	if pathURL != "/api/stream/cloud-1" {
		t.Fatalf("embedded cloud Path should stay as non-tokenized display stream URL, got %#v", source)
	}
	if strings.Contains(pathURL, "api_key=") || strings.Contains(pathURL, "token=") {
		t.Fatalf("embedded cloud Path must not expose auth key/token: %#v", source)
	}
}
