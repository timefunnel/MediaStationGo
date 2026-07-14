package service

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"go.uber.org/zap"
)

func TestCloudSubtitleMaterializePersistsTracksAndAvoidsPlaybackDiscovery(t *testing.T) {
	var listCalls atomic.Int32
	var readCalls atomic.Int32

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/list":
			w.Header().Set("Content-Type", "application/json")
			listCalls.Add(1)
			var req struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			var entries []map[string]any
			switch req.Path {
			case "/115/movie/Movie":
				entries = []map[string]any{
					{"name": "Movie.mkv", "size": 5000, "is_dir": false},
					{"name": "Movie.zh.srt", "size": 128, "is_dir": false},
					{"name": "Subtitles", "size": 0, "is_dir": true},
				}
			case "/115/movie/Movie/Subtitles":
				entries = []map[string]any{{"name": "commentary.ass", "size": 64, "is_dir": false}}
			default:
				t.Fatalf("unexpected list path %q", req.Path)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": entries, "total": len(entries)}})
		case "/dav/115/movie/Movie/Movie.zh.srt":
			readCalls.Add(1)
			_, _ = w.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n"))
		case "/dav/115/movie/Movie/Subtitles/commentary.ass":
			readCalls.Add(1)
			_, _ = w.Write([]byte("[Script Info]\nTitle: commentary\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	db := newServiceTestDB(t, &model.Media{}, &model.StorageConfig{})
	repos := repository.New(db)
	media := model.Media{Base: model.Base{ID: "movie-materialized"}, Title: "Movie", Path: "cloud://openlist/115/movie/Movie/Movie.mkv"}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	log := zap.NewNop()
	storage := NewStorageConfigService(log, repos, NewCryptoService("", log))
	if _, err := storage.Save(t.Context(), StorageInput{Type: "openlist", Config: map[string]any{"server": api.URL, "token": "token"}}); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	svc := NewSubtitleService(log, repos, storage)
	svc.SetMaterializedCacheDir(cacheDir)

	result, err := svc.EnsureCloudSubtitles(t.Context(), media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.Discovered != 2 || result.Cached != 2 {
		t.Fatalf("materialize result=%#v", result)
	}
	if listCalls.Load() != 2 || readCalls.Load() != 2 {
		t.Fatalf("list=%d read=%d, want 2/2", listCalls.Load(), readCalls.Load())
	}

	again, err := svc.EnsureCloudSubtitles(t.Context(), media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != "skipped" || again.Reason != "cache_current" || listCalls.Load() != 2 || readCalls.Load() != 2 {
		t.Fatalf("second materialize=%#v list=%d read=%d", again, listCalls.Load(), readCalls.Load())
	}
	indexRaw, err := os.ReadFile(filepath.Join(cacheDir, media.ID, "tracks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index localSubtitleIndex
	if err := json.Unmarshal(indexRaw, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Tracks) == 0 {
		t.Fatal("materialized subtitle index is empty")
	}
	if err := os.Remove(filepath.Join(cacheDir, media.ID, index.Tracks[0].Filename)); err != nil {
		t.Fatal(err)
	}
	repaired, err := svc.EnsureCloudSubtitles(t.Context(), media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Status != "success" || listCalls.Load() != 4 || readCalls.Load() != 4 {
		t.Fatalf("repaired materialize=%#v list=%d read=%d", repaired, listCalls.Load(), readCalls.Load())
	}

	restarted := NewSubtitleService(log, repos)
	restarted.SetMaterializedCacheDir(cacheDir)
	tracks, err := restarted.DiscoverForPlayback(t.Context(), media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks=%#v, want 2", tracks)
	}
	var srtTrack *SubtitleTrack
	for i := range tracks {
		if tracks[i].Codec == "srt" {
			srtTrack = &tracks[i]
		}
	}
	if srtTrack == nil || len(srtTrack.Path) < len(materializedSubtitleScheme) || srtTrack.Path[:len(materializedSubtitleScheme)] != materializedSubtitleScheme {
		t.Fatalf("materialized srt track=%#v", srtTrack)
	}
	var body bytes.Buffer
	if err := restarted.ServeAs(t.Context(), media.ID, srtTrack.Path, ".srt", &body); err != nil {
		t.Fatal(err)
	}
	if body.String() != "1\n00:00:01,000 --> 00:00:02,000\nhello" {
		t.Fatalf("cached subtitle body=%q", body.String())
	}
}

func TestCloudSubtitleMaterializePersistsEmptyDiscoveryMarker(t *testing.T) {
	var listCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		listCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{
				"content": []map[string]any{{"name": "Movie.mkv", "size": 5000, "is_dir": false}},
				"total":   1,
			},
		})
	}))
	defer api.Close()

	db := newServiceTestDB(t, &model.Media{}, &model.StorageConfig{})
	repos := repository.New(db)
	media := model.Media{Base: model.Base{ID: "movie-no-subtitles"}, Title: "Movie", Path: "cloud://openlist/115/movie/Movie/Movie.mkv"}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	log := zap.NewNop()
	storage := NewStorageConfigService(log, repos, NewCryptoService("", log))
	if _, err := storage.Save(t.Context(), StorageInput{Type: "openlist", Config: map[string]any{"server": api.URL, "token": "token"}}); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	svc := NewSubtitleService(log, repos, storage)
	svc.SetMaterializedCacheDir(cacheDir)
	result, err := svc.EnsureCloudSubtitles(t.Context(), media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.Discovered != 0 || result.Cached != 0 || listCalls.Load() != 1 {
		t.Fatalf("result=%#v list=%d", result, listCalls.Load())
	}
	if _, err := os.Stat(filepath.Join(cacheDir, media.ID, "tracks.json")); err != nil {
		t.Fatal(err)
	}

	restarted := NewSubtitleService(log, repos)
	restarted.SetMaterializedCacheDir(cacheDir)
	tracks, err := restarted.DiscoverForPlayback(t.Context(), media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 0 || listCalls.Load() != 1 {
		t.Fatalf("tracks=%#v list=%d", tracks, listCalls.Load())
	}
}
