package service

import (
	"errors"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestClientDanmakuScopesReferencesToAccountAndPhysicalVersion(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Media{}, &model.UserMediaPlaybackPreference{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, id := range []string{"media-1", "media-2"} {
		if err := db.Create(&model.Media{Base: model.Base{ID: id}, Title: "测试", Path: "/synthetic/" + id, TMDbID: 1, SeasonNum: 1, EpisodeNum: 2}).Error; err != nil {
			t.Fatal(err)
		}
	}
	repos := repository.New(db)
	svc := NewDanmakuService(nil, repos)
	t.Cleanup(svc.Close)
	// No backend is attached: both operations must be database-only.
	in := ClientDanmakuUpdate{ClientDanmakuAssociation: ClientDanmakuAssociation{
		Source: "iqiyi", EpisodeURL: "https://www.iqiyi.com/v_test.html", OffsetSeconds: 12}, ExpectedRevision: "0"}
	saved, err := svc.SaveClientAssociation(t.Context(), "media-1", "user-1", in)
	if err != nil || saved.Association == nil || saved.Association.EpisodeURL != in.EpisodeURL {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	for _, scope := range [][2]string{{"media-1", "user-2"}, {"media-2", "user-1"}} {
		state, err := svc.ClientContext(t.Context(), scope[0], scope[1])
		if err != nil || state.Association != nil {
			t.Fatalf("scope=%v state=%+v err=%v", scope, state, err)
		}
	}
	// Stale device writes cannot replace a correction made on another device.
	if _, err := svc.SaveClientAssociation(t.Context(), "media-1", "user-1", in); !errors.Is(err, ErrDanmakuConflict) {
		t.Fatalf("stale update: %v", err)
	}
	if err := repos.MediaPlaybackPreference.Upsert(t.Context(), &model.UserMediaPlaybackPreference{UserID: "user-1", MediaID: "media-1", SubtitleEnabled: false, AudioTrackKey: "stream:1"}); err != nil {
		t.Fatal(err)
	}
	state, err := svc.ClientContext(t.Context(), "media-1", "user-1")
	if err != nil || state.Revision != saved.Revision || state.Association.OffsetSeconds != 12 {
		t.Fatalf("audio update damaged danmaku: %+v %v", state, err)
	}
	in.ExpectedRevision, in.OffsetSeconds = state.Revision, 0
	if _, err := svc.SaveClientAssociation(t.Context(), "media-1", "user-1", in); err != nil {
		t.Fatal(err)
	}
	pref, err := repos.MediaPlaybackPreference.FindByUserAndMedia(t.Context(), "user-1", "media-1")
	if err != nil || pref.SubtitleEnabled || pref.AudioTrackKey != "stream:1" || pref.DanmakuOffsetSeconds != 0 {
		t.Fatalf("preference=%+v err=%v", pref, err)
	}
}

func TestClientDanmakuRejectsUntrustedReferences(t *testing.T) {
	in := ClientDanmakuUpdate{ClientDanmakuAssociation: ClientDanmakuAssociation{Source: "iqiyi", EpisodeURL: "https://www.iqiyi.com/v_test.html"}}
	for _, invalid := range []string{"https://127.0.0.1/v_test.html", "https://www.iqiyi.com:443/v_test.html", "https://www.iqiyi.com/v_test.html?token=x", "https://www.iqiyi.com/%76_test.html", "https://www.iqiyi.com/v_test.html#"} {
		in.EpisodeURL = invalid
		if validateClientDanmaku(in) == nil {
			t.Errorf("accepted %s", invalid)
		}
	}
}
