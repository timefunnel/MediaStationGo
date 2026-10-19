package service

import (
	"errors"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"go.uber.org/zap"
)

func TestCloudArtworkAliasUsesCurrentMediaField(t *testing.T) {
	db := newServiceTestDB(t, &model.Media{}, &model.CloudArtworkAlias{})
	repos := repository.New(db)
	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos)
	media := model.Media{Base: model.Base{ID: "artwork-owner"}, Path: "/media/example.mkv", PosterURL: "https://image.tmdb.org/t/p/w500/first.jpg", BackdropURL: "https://image.tmdb.org/t/p/w1280/backdrop.jpg"}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"poster_url", "backdrop_url"} {
		alias := model.CloudArtworkAlias{SourceHash: CloudArtworkSourceHash("openlist", "/example/"+field+".jpg"), MediaID: media.ID, ImageField: field}
		if err := db.Create(&alias).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"poster_url", "backdrop_url"} {
		want := media.PosterURL
		if field == "backdrop_url" {
			want = media.BackdropURL
		}
		raw, found, err := svc.ResolveCloudArtworkAlias(t.Context(), "openlist", "/example/"+field+".jpg")
		if err != nil || !found || raw != want {
			t.Fatalf("%s: raw=%q found=%v err=%v", field, raw, found, err)
		}
	}
	const updated = "https://image.tmdb.org/t/p/w500/current.jpg"
	if err := db.Model(&media).Update("poster_url", updated).Error; err != nil {
		t.Fatal(err)
	}
	raw, found, err := svc.ResolveCloudArtworkAlias(t.Context(), "openlist", "/example/poster_url.jpg")
	if err != nil || !found || raw != updated {
		t.Fatalf("alias retained an old target: raw=%q found=%v err=%v", raw, found, err)
	}
	if raw, found, err := svc.ResolveCloudArtworkAlias(t.Context(), "openlist", "/unknown.jpg"); err != nil || found || raw != "" {
		t.Fatalf("unknown alias: raw=%q found=%v err=%v", raw, found, err)
	}
	if err := db.Delete(&media).Error; err != nil {
		t.Fatal(err)
	}
	if _, found, err := svc.ResolveCloudArtworkAlias(t.Context(), "openlist", "/example/poster_url.jpg"); !found || !errors.Is(err, ErrCloudArtworkTargetGone) {
		t.Fatalf("deleted target: found=%v err=%v", found, err)
	}
}

func TestCloudArtworkAliasRejectsUnavailableTargets(t *testing.T) {
	for _, target := range []string{"", "/api/img/cloud/openlist?ref=%2Fexample%2Fposter.jpg", "https://example.invalid/api/cloud/play/openlist?ref=%2Fexample%2Fposter.jpg"} {
		t.Run(target, func(t *testing.T) {
			db := newServiceTestDB(t, &model.Media{}, &model.CloudArtworkAlias{})
			svc := NewMediaService(&config.Config{}, zap.NewNop(), repository.New(db))
			media := model.Media{Base: model.Base{ID: "owner"}, Path: "/media/example.mkv", PosterURL: target}
			if err := db.Create(&media).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.CloudArtworkAlias{SourceHash: CloudArtworkSourceHash("openlist", "/old.jpg"), MediaID: media.ID, ImageField: "poster_url"}).Error; err != nil {
				t.Fatal(err)
			}
			if raw, found, err := svc.ResolveCloudArtworkAlias(t.Context(), "openlist", "/old.jpg"); !found || err == nil || raw != "" {
				t.Fatalf("invalid target accepted: raw=%q found=%v err=%v", raw, found, err)
			}
		})
	}
}
