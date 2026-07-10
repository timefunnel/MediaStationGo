package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func TestSubtitleDiscoverNoTracksReturnsEmptySlice(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	media := model.Media{
		Title: "No Subtitles",
		Path:  filepath.Join(dir, "No Subtitles.mkv"),
	}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewSubtitleService(zap.NewNop(), repository.New(db))
	tracks, err := svc.Discover(t.Context(), media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tracks == nil {
		t.Fatal("tracks is nil, want empty slice")
	}
	if len(tracks) != 0 {
		t.Fatalf("len(tracks) = %d, want 0", len(tracks))
	}
}

func TestSubtitleDiscoverMergesLocalCacheTracks(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	media := model.Media{
		Base:  model.Base{ID: "media-cached"},
		Title: "Cached Subtitle",
		Path:  filepath.Join(dir, "Cached Subtitle.mkv"),
	}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}

	cacheDir := t.TempDir()
	mediaCacheDir := filepath.Join(cacheDir, media.ID)
	if err := os.MkdirAll(mediaCacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n")
	if err := os.WriteFile(filepath.Join(mediaCacheDir, "subtitlecat-test.srt"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaCacheDir, "tracks.json"), []byte(`{
  "media_id": "media-cached",
  "tracks": [
    {
      "media_id": "media-cached",
      "filename": "subtitlecat-test.srt",
      "lang": "zh-Hans",
      "label": "简体中文"
    }
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := NewSubtitleService(zap.NewNop(), repository.New(db))
	svc.SetLocalCacheDir(cacheDir)
	tracks, err := svc.Discover(t.Context(), media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 {
		t.Fatalf("len(tracks) = %d, want 1: %#v", len(tracks), tracks)
	}
	if tracks[0].Path != "local-subtitle://media-cached/subtitlecat-test.srt" || tracks[0].Lang != "zh-Hans" || tracks[0].Label != "简体中文" || tracks[0].Codec != "srt" {
		t.Fatalf("unexpected local cache track: %#v", tracks[0])
	}

	var native bytes.Buffer
	if err := svc.ServeAs(t.Context(), media.ID, tracks[0].Path, ".srt", &native); err != nil {
		t.Fatalf("serve srt: %v", err)
	}
	if native.String() != string(body) {
		t.Fatalf("native subtitle body = %q, want %q", native.String(), string(body))
	}

	var vtt bytes.Buffer
	if err := svc.ServeAs(t.Context(), media.ID, tracks[0].Path, ".vtt", &vtt); err != nil {
		t.Fatalf("serve vtt: %v", err)
	}
	if !strings.HasPrefix(vtt.String(), "WEBVTT") || !strings.Contains(vtt.String(), "hello") {
		t.Fatalf("unexpected vtt body: %q", vtt.String())
	}
}
