package service

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

func TestLibraryCoverMigrationPreservesExistingLibrary(t *testing.T) {
	e := newTestEmbyService(t)
	if err := e.repo.DB.Exec("ALTER TABLE libraries DROP COLUMN cover_media_ids").Error; err != nil {
		t.Fatal(err)
	}
	if err := e.repo.DB.Exec("INSERT INTO libraries (id, name, path, type, enabled) VALUES (?, ?, ?, ?, ?)", "existing", "Existing", "/test/existing", "movie", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.repo.DB.AutoMigrate(&model.Library{}); err != nil {
		t.Fatal(err)
	}
	lib, err := e.repo.Library.FindByID(t.Context(), "existing")
	if err != nil || lib == nil || lib.Name != "Existing" || lib.Path != "/test/existing" || lib.CoverMediaIDs == nil || len(lib.CoverMediaIDs) != 0 {
		t.Fatalf("existing library migration: %#v err=%v", lib, err)
	}
}

func TestLibraryCoverSelectionPersistsOrderAndCanReset(t *testing.T) {
	e := newTestEmbyService(t)
	lib := model.Library{Base: model.Base{ID: "cover-library"}, Name: "电影", Path: "/test/movies", Type: "movie", Enabled: true}
	if err := e.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		m := model.Media{Base: model.Base{ID: id}, LibraryID: lib.ID, Title: id, Path: "/test/" + id + ".mkv", PosterURL: "https://img.example/" + id + ".jpg"}
		if err := e.repo.DB.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{"two", "one"}
	if err := e.SaveLibraryCoverSelection(t.Context(), lib.ID, ids); err != nil {
		t.Fatal(err)
	}
	stored, err := e.repo.Library.FindByID(t.Context(), lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.CoverMediaIDs, ids) || stored.Name != lib.Name || stored.Path != lib.Path || stored.Type != lib.Type {
		t.Fatalf("unexpected saved library: %#v", stored)
	}
	art, err := e.FolderCoverArtwork(t.Context(), lib.ID, "Primary", 4)
	if err != nil || len(art) != 2 || art[0].MediaID != "two" || art[1].MediaID != "one" {
		t.Fatalf("manual order: %#v err=%v", art, err)
	}
	for _, invalid := range [][]string{{"one", "one"}, {""}, {"missing"}, {"one", "two", "a", "b", "c"}} {
		if err := e.SaveLibraryCoverSelection(t.Context(), lib.ID, invalid); !errors.Is(err, ErrLibraryCoverSelection) {
			t.Fatalf("invalid selection %v accepted: %v", invalid, err)
		}
	}
	stored, err = e.repo.Library.FindByID(t.Context(), lib.ID)
	if err != nil || !reflect.DeepEqual(stored.CoverMediaIDs, ids) {
		t.Fatal("invalid save changed persisted selection")
	}
	if err := e.repo.DB.Delete(&model.Media{}, "id = ?", "two").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := e.FolderCoverArtwork(t.Context(), lib.ID, "Primary", 4); !errors.Is(err, ErrLibraryCoverSelection) {
		t.Fatalf("deleted selection silently switched to automatic: %v", err)
	}
	if err := e.SaveLibraryCoverSelection(t.Context(), lib.ID, []string{}); err != nil {
		t.Fatal(err)
	}
	art, err = e.FolderCoverArtwork(t.Context(), lib.ID, "Primary", 4)
	if err != nil || len(art) != 1 || art[0].MediaID != "one" {
		t.Fatalf("automatic reset: %#v err=%v", art, err)
	}
}

func TestLibraryCoverSelectionRejectsForeignAndMissingPosters(t *testing.T) {
	e := newTestEmbyService(t)
	for _, id := range []string{"target", "other"} {
		lib := model.Library{Base: model.Base{ID: id}, Name: id, Path: "/test/" + id, Type: "movie", Enabled: true}
		if err := e.repo.Library.Create(t.Context(), &lib); err != nil {
			t.Fatal(err)
		}
	}
	rows := []model.Media{
		{Base: model.Base{ID: "foreign"}, LibraryID: "other", Title: "Foreign", Path: "/test/other/movie.mkv", PosterURL: "https://img.example/foreign.jpg"},
		{Base: model.Base{ID: "no-poster"}, LibraryID: "target", Title: "No poster", Path: "/test/target/movie.mkv", BackdropURL: "https://img.example/backdrop.jpg"},
	}
	if err := e.repo.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"foreign", "no-poster"} {
		if err := e.SaveLibraryCoverSelection(t.Context(), "target", []string{id}); !errors.Is(err, ErrLibraryCoverSelection) {
			t.Fatalf("selection %s accepted: %v", id, err)
		}
	}
}

func TestLibraryCoverSelectionUsesCanonicalSeriesPoster(t *testing.T) {
	e := newTestEmbyService(t)
	lib := model.Library{Base: model.Base{ID: "series-library"}, Name: "剧集", Path: "/test/tv", Type: "tv", Enabled: true}
	if err := e.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	s := model.Series{Base: model.Base{ID: "series-identity"}, LibraryID: lib.ID, Title: "Canonical", TMDbID: 777, PosterURL: "https://img.example/series.jpg"}
	if err := e.repo.DB.Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"episode-one", "episode-two"} {
		m := model.Media{Base: model.Base{ID: id}, LibraryID: lib.ID, SeriesID: s.ID, Title: "Canonical", Path: "/test/tv/Canonical/" + id + ".mkv", TMDbID: 777, SeasonNum: 1, EpisodeNum: 1, PosterURL: "https://img.example/episode.jpg"}
		if err := e.repo.DB.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}
	art, _, err := e.LibraryCoverSelection(t.Context(), lib.ID, []string{"episode-one"})
	if err != nil || len(art) != 1 || art[0].URL != s.PosterURL {
		t.Fatalf("canonical artwork: %#v err=%v", art, err)
	}
	if _, _, err := e.LibraryCoverSelection(t.Context(), lib.ID, []string{"episode-one", "episode-two"}); !errors.Is(err, ErrLibraryCoverSelection) {
		t.Fatalf("duplicate series accepted: %v", err)
	}
}
