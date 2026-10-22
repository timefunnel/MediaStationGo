package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"github.com/ShukeBta/MediaStationGo/internal/service"
	"github.com/ShukeBta/MediaStationGo/internal/service/cloud"
)

type embyPlaybackDanmakuBackend struct {
	stubDanmakuPipeline
	matches atomic.Int32
	fetches atomic.Int32
	entered chan service.DanmakuFetchRequest
	release chan struct{}
}

func (b *embyPlaybackDanmakuBackend) MatchDanmaku(context.Context, string) (service.DanmakuMatchResult, error) {
	b.matches.Add(1)
	return service.DanmakuMatchResult{Matched: true, Status: service.DanmakuStatusMatched,
		Provider: "youku", EpisodeID: "123", MatchMode: "native", AnimeTitle: "Synthetic"}, nil
}

func (b *embyPlaybackDanmakuBackend) FetchDanmaku(ctx context.Context, request service.DanmakuFetchRequest) (service.DanmakuPayload, error) {
	b.fetches.Add(1)
	b.entered <- request
	select {
	case <-ctx.Done():
		return service.DanmakuPayload{}, ctx.Err()
	case <-b.release:
		return service.DanmakuPayload{Source: "youku", EpisodeID: "123", Count: 1,
			Comments: []service.DanmakuComment{{CID: "1", P: "1,1,16777215,test", M: "synthetic requested danmaku", Time: 1}}}, nil
	}
}

func newEmbyPlaybackDanmakuRouter(t *testing.T) (*gin.Engine, *service.Container, *embyPlaybackDanmakuBackend) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	repos := repository.New(db)
	if err := repos.User.Create(t.Context(), &model.User{Base: model.Base{ID: "user-1"},
		Username: "tester", PasswordHash: "x", Role: "admin", Tier: "plus", IsActive: true}); err != nil {
		t.Fatal(err)
	}
	lib := model.Library{Name: "Synthetic", Path: "cloud://openlist/Synthetic", Type: "tv", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"media-1", "media-2"} {
		if err := db.Create(&model.Media{Base: model.Base{ID: id}, LibraryID: lib.ID, Title: "Synthetic",
			Path: "cloud://openlist/Synthetic/" + id + ".mkv", Container: "mkv",
			STRMURL:  "/api/cloud/play/openlist?ref=%2FSynthetic%2F" + id + ".mkv",
			SeriesID: "synthetic-series", SeasonNum: 1, EpisodeNum: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := repos.Setting.Set(t.Context(), service.CloudPlaybackModeSettingKey, service.CloudPlaybackModeSTRM); err != nil {
		t.Fatal(err)
	}
	if err := repos.Setting.Set(t.Context(), service.CloudPlaybackSTRMEnabledSettingKey, "true"); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	b := &embyPlaybackDanmakuBackend{entered: make(chan service.DanmakuFetchRequest, 10), release: make(chan struct{})}
	danmaku := service.NewDanmakuService(zap.NewNop(), repos)
	danmaku.SetBackendClient(b)
	t.Cleanup(danmaku.Close)
	stream := service.NewStreamService(cfg, zap.NewNop(), repos, nil)
	stream.SetCloudProbe(&embyRouteCloudResolver{link: &cloud.DirectLink{URL: "https://video.example.test/synthetic.mkv"}})
	svc := &service.Container{Repo: repos, Log: zap.NewNop(), Danmaku: danmaku,
		Emby: service.NewEmbyService(cfg, zap.NewNop(), repos), Stream: stream}
	router := gin.New()
	registerEmbyRoutes(router, "test-secret", svc)
	return router, svc, b
}

func playbackDanmakuRequest(t *testing.T, router http.Handler, method, path, client, userAgent string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Emby-Token", signedTestToken(t, "test-secret"))
	if client != "" {
		req.Header.Set("X-Emby-Client", client)
	}
	req.Header.Set("User-Agent", userAgent)
	w := httptest.NewRecorder()
	completed := make(chan struct{})
	go func() {
		router.ServeHTTP(w, req)
		close(completed)
	}()
	select {
	case <-completed:
		return w
	case <-time.After(3 * time.Second):
		t.Fatal("video/PlaybackInfo response waited for blocked danmaku backend")
		return w
	}
}

func TestEmbyDanmakuPlaybackDoesNotPrefetch(t *testing.T) {
	cases := []struct {
		name, method, path, client, userAgent string
		status                                int
	}{
		{"senplayer-start", "POST", "/Items/media-1/PlaybackInfo?IsPlayback=true", "SenPlayer", "SenPlayer/6.2.2", 200},
		{"senplayer-ua", "GET", "/Items/media-1/PlaybackInfo?IsPlayback=true", "", "SenPlayer/6.2.2", 200},
		{"windows-start", "GET", "/Items/media-1/PlaybackInfo?IsPlayback=true", "MediaStation Windows", "MediaStationGoWindows/0.1.0-dev", 200},
		{"windows-ua", "GET", "/Items/media-1/PlaybackInfo?IsPlayback=true", "", "MediaStationGoWindows/0.1.0-dev", 200},
		{"selected-source", "GET", "/Items/media-1/PlaybackInfo?IsPlayback=true&MediaSourceId=media-2", "MediaStation Windows", "MediaStationGoWindows/0.1.0-dev", 200},
		{"detail-query", "GET", "/Items/media-1/PlaybackInfo", "SenPlayer", "SenPlayer/6.2.2", 200},
		{"other-client", "POST", "/Items/media-1/PlaybackInfo?IsPlayback=true", "Infuse", "Infuse/1", 200},
		{"invalid-selection", "GET", "/Items/media-1/PlaybackInfo?IsPlayback=true&MediaSourceId=missing", "SenPlayer", "SenPlayer/6.2.2", 400},
		{"senplayer-stream", "GET", "/Videos/media-1/stream", "SenPlayer", "SenPlayer/6.2.2", 302},
		{"windows-stream", "GET", "/Videos/media-1/stream", "MediaStation Windows", "MediaStationGoWindows/0.1.0-dev", 302},
		{"windows-native-stream", "GET", "/emby/api/stream/media-1", "MediaStation Windows", "MediaStationGoWindows/0.1.0-dev", 302},
		{"head-probe", "HEAD", "/Videos/media-1/stream", "SenPlayer", "SenPlayer/6.2.2", 302},
		{"windows-native-head", "HEAD", "/emby/api/stream/media-1", "MediaStation Windows", "MediaStationGoWindows/0.1.0-dev", 302},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			router, _, b := newEmbyPlaybackDanmakuRouter(t)
			w := playbackDanmakuRequest(t, router, item.method, item.path, item.client, item.userAgent)
			if w.Code != item.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			select {
			case <-b.entered:
				t.Fatal("playback fetched danmaku without an explicit danmaku request")
			case <-time.After(20 * time.Millisecond):
			}
			if b.matches.Load() != 0 || b.fetches.Load() != 0 {
				t.Fatalf("playback started danmaku work: matches=%d fetches=%d", b.matches.Load(), b.fetches.Load())
			}
		})
	}
}

func TestEmbyDanmakuClientContextDoesNotFetchComments(t *testing.T) {
	router, _, backend := newEmbyPlaybackDanmakuRouter(t)
	response := playbackDanmakuRequest(t, router, "GET", "/Items/media-1/Danmaku?MediaSourceId=media-1", "MediaStation Windows", "MediaStationGoWindows/test")
	var context service.ClientDanmakuContext
	if err := json.Unmarshal(response.Body.Bytes(), &context); err != nil || response.Code != 200 || context.MediaID != "media-1" {
		t.Fatalf("context=%d %s err=%v", response.Code, response.Body.String(), err)
	}
	for _, path := range []string{"/api/danmu/media-1/raw", "/api/danmaku/media-1"} {
		if w := playbackDanmakuRequest(t, router, "GET", path, "MediaStation Windows", "MediaStationGoWindows/test"); w.Code != 404 {
			t.Fatalf("retired route: %s => %d", path, w.Code)
		}
	}
	if backend.matches.Load() != 0 || backend.fetches.Load() != 0 {
		t.Fatal("client context triggered upstream work")
	}
}

func TestEmbyDanmakuAssociationUsesAuthenticatedAccountAndSelectedVersion(t *testing.T) {
	router, svc, backend := newEmbyPlaybackDanmakuRouter(t)
	put := func(path, body string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if authenticated {
			req.Header.Set("X-Emby-Token", signedTestToken(t, "test-secret"))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	body := `{"source":"iqiyi","episode_url":"https://www.iqiyi.com/v_test.html","offset_seconds":-2.5,"expected_revision":"0","user_id":"other-user"}`
	path := "/Items/media-1/Danmaku?MediaSourceId=media-2"
	if w := put(path, body, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated write: %d %s", w.Code, w.Body.String())
	}
	if w := put(path+"&UserId=other-user", body, true); w.Code != http.StatusForbidden {
		t.Fatalf("forged user scope: %d %s", w.Code, w.Body.String())
	}
	if w := put(path, body, true); w.Code != http.StatusOK {
		t.Fatalf("save selected version: %d %s", w.Code, w.Body.String())
	}
	for _, scope := range [][2]string{{"media-1", "user-1"}, {"media-2", "other-user"}} {
		out, err := svc.Danmaku.ClientContext(t.Context(), scope[0], scope[1])
		if err != nil || out.Association != nil {
			t.Fatalf("unexpected association in %v: %+v %v", scope, out, err)
		}
	}
	out, err := svc.Danmaku.ClientContext(t.Context(), "media-2", "user-1")
	if err != nil || out.Association == nil || out.Association.OffsetSeconds != -2.5 {
		t.Fatalf("missing authenticated association: %+v %v", out, err)
	}
	if w := put(path, body, true); w.Code != http.StatusConflict {
		t.Fatalf("stale update: %d %s", w.Code, w.Body.String())
	}
	if w := put("/Items/media-1/Danmaku?MediaSourceId=missing", body, true); w.Code != http.StatusBadRequest {
		t.Fatalf("unrelated version: %d %s", w.Code, w.Body.String())
	}
	if backend.matches.Load() != 0 || backend.fetches.Load() != 0 {
		t.Fatal("saving a reference triggered source traffic")
	}
}
