package service

import (
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

func TestEmbyLibraryViewExposesFolderCoverTag(t *testing.T) {
	svc := newTestEmbyService(t)
	lib := model.Library{Base: model.Base{ID: "lib-movies"}, Name: "电影", Path: "/media/movies", Type: "movie", Enabled: true}
	if err := svc.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	base := time.Now()
	rows := []model.Media{
		{
			Base:      model.Base{ID: "media-old", CreatedAt: base, UpdatedAt: base},
			LibraryID: lib.ID,
			Title:     "Old",
			Path:      "/media/movies/old.mkv",
			PosterURL: "https://img.example/old.jpg",
		},
		{
			Base:        model.Base{ID: "media-new", CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)},
			LibraryID:   lib.ID,
			Title:       "New",
			Path:        "/media/movies/new.mkv",
			PosterURL:   "https://img.example/new.jpg",
			BackdropURL: "https://img.example/new-backdrop.jpg",
		},
	}
	for i := range rows {
		if err := svc.repo.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("create media: %v", err)
		}
	}

	artworks, err := svc.FolderCoverArtwork(t.Context(), lib.ID, "Primary", 4)
	if err != nil {
		t.Fatalf("folder artworks: %v", err)
	}
	if len(artworks) != 2 || artworks[0].URL != "https://img.example/new.jpg" || artworks[1].URL != "https://img.example/old.jpg" {
		t.Fatalf("unexpected folder artworks: %#v", artworks)
	}
	tag := svc.FolderCoverTag(t.Context(), lib.ID, "Primary")
	if len(tag) != 32 {
		t.Fatalf("folder tag = %q, want 32 hex chars", tag)
	}

	view := svc.libraryAsView(t.Context(), &lib)
	tags, ok := view["ImageTags"].(map[string]string)
	if !ok || tags["Primary"] != tag {
		t.Fatalf("library view ImageTags = %#v, want Primary %q", view["ImageTags"], tag)
	}
	if view["PrimaryImageTag"] != tag {
		t.Fatalf("PrimaryImageTag = %#v, want %q", view["PrimaryImageTag"], tag)
	}
}

func TestEmbyFolderCoverArtworkPrefersBackdropForBackdropRequests(t *testing.T) {
	svc := newTestEmbyService(t)
	lib := model.Library{Base: model.Base{ID: "lib-tv"}, Name: "剧集", Path: "/media/tv", Type: "tv", Enabled: true}
	if err := svc.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := svc.repo.DB.Create(&model.Media{
		Base:        model.Base{ID: "media-1"},
		LibraryID:   lib.ID,
		Title:       "Show",
		Path:        "/media/tv/show.mkv",
		PosterURL:   "https://img.example/poster.jpg",
		BackdropURL: "https://img.example/backdrop.jpg",
	}).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}

	artworks, err := svc.FolderCoverArtwork(t.Context(), lib.ID, "Backdrop", 4)
	if err != nil {
		t.Fatalf("folder artworks: %v", err)
	}
	if len(artworks) != 1 || artworks[0].URL != "https://img.example/backdrop.jpg" {
		t.Fatalf("unexpected backdrop artwork: %#v", artworks)
	}
}
