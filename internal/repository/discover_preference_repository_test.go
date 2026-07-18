package repository

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

func TestDiscoverPreferenceRepositoryIsUserScopedAndPersistsEmptySelection(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.UserDiscoverPreference{}); err != nil {
		t.Fatal(err)
	}
	repo := &DiscoverPreferenceRepository{db: db}
	preference := &model.UserDiscoverPreference{
		UserID: "user-1", SelectedSections: []string{"tmdb_trending_day", "adult_followed"},
	}
	if err := repo.Upsert(t.Context(), preference); err != nil {
		t.Fatal(err)
	}
	row, err := repo.FindByUserID(t.Context(), "user-1")
	if err != nil || row == nil || len(row.SelectedSections) != 2 {
		t.Fatalf("row = %#v err=%v", row, err)
	}
	if other, err := repo.FindByUserID(t.Context(), "user-2"); err != nil || other != nil {
		t.Fatalf("other = %#v err=%v", other, err)
	}
	preference.SelectedSections = []string{}
	if err := repo.Upsert(t.Context(), preference); err != nil {
		t.Fatal(err)
	}
	row, err = repo.FindByUserID(t.Context(), "user-1")
	if err != nil || row == nil || len(row.SelectedSections) != 0 {
		t.Fatalf("empty selection row = %#v err=%v", row, err)
	}
}
