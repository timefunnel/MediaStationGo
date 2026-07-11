package service

import (
	"slices"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestPipelineMaintenanceRepairMovieExtras(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{})
	repos := repository.New(db)
	svc := NewPipelineMaintenanceService(zap.NewNop(), repos)

	lib, root := createPipelineMaintenanceRoot(t, db, "movie", "/115/movie")
	rows := []model.Media{
		{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "Movie", Path: "cloud://openlist/115/movie/Movie/Movie.mkv"},
		{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "PV", Path: "cloud://openlist/115/movie/Movie/Extras/PV.mp4"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	main := rows[0]
	extra := rows[1]

	result, err := svc.RepairMovieExtras(t.Context(), main.ID, PipelineMaintenanceTarget{
		Category:         "movie",
		LibraryID:        lib.ID,
		RootID:           root.ID,
		RootOpenListPath: "/115/movie",
	})
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != "success" || result.Updated != 1 || result.MediaCount != 2 || result.OpenListHidePath != "/115/movie/Movie" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !slices.Equal(result.OpenListHidePatterns, []string{"^Extras$"}) {
		t.Fatalf("hide patterns = %#v", result.OpenListHidePatterns)
	}

	var stored model.Media
	if err := db.Unscoped().Where("id = ?", extra.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.DeletedAt.Valid {
		t.Fatal("extra media should be soft-deleted")
	}
}

func TestPipelineMaintenanceRepairEpisodeVisibility(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{})
	repos := repository.New(db)
	svc := NewPipelineMaintenanceService(zap.NewNop(), repos)

	lib, root := createPipelineMaintenanceRoot(t, db, "anime", "/115/anime")
	created := []model.Media{
		{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "Show", Path: "cloud://openlist/115/anime/Show/S01E01.mkv"},
		{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "Show", Path: "cloud://openlist/115/anime/Show/S01E02.mkv"},
	}
	if err := db.Create(&created).Error; err != nil {
		t.Fatal(err)
	}
	first := created[0]

	result, err := svc.RepairEpisodeVisibility(t.Context(), first.ID, PipelineMaintenanceTarget{
		Category:         "anime",
		LibraryID:        lib.ID,
		RootID:           root.ID,
		RootOpenListPath: "/115/anime",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.Updated != 2 || result.MediaCount != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}

	var rows []model.Media
	if err := db.Where("library_id = ?", lib.ID).Order("path").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].SeasonNum != 1 || rows[0].EpisodeNum != 1 || rows[0].RelativePath != "Show/S01E01.mkv" {
		t.Fatalf("first row not repaired: %#v", rows[0])
	}
	if rows[1].SeasonNum != 1 || rows[1].EpisodeNum != 2 || rows[1].RelativePath != "Show/S01E02.mkv" {
		t.Fatalf("second row not repaired: %#v", rows[1])
	}
}

func TestPipelineMaintenancePruneDeletedMedia(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{})
	repos := repository.New(db)
	svc := NewPipelineMaintenanceService(zap.NewNop(), repos)

	lib, root := createPipelineMaintenanceRoot(t, db, "adult", "/115/adult")
	created := []model.Media{
		{
			LibraryID:     lib.ID,
			LibraryRootID: root.ID,
			Title:         "Deleted",
			Path:          "cloud://openlist/115/adult/ABF-363/ABF-363.mp4",
		},
		{
			LibraryID:     lib.ID,
			LibraryRootID: root.ID,
			Title:         "Active",
			Path:          "cloud://openlist/115/adult/ABF-363/Active.mp4",
		},
	}
	if err := db.Create(&created).Error; err != nil {
		t.Fatal(err)
	}
	deleted := created[0]
	active := created[1]
	if err := db.Model(&model.Media{}).Where("id = ?", deleted.ID).Update("deleted_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}

	result, err := svc.PruneDeletedMedia(t.Context(), PipelineMaintenanceTarget{
		Category:         "adult",
		LibraryID:        lib.ID,
		RootID:           root.ID,
		RootOpenListPath: "/115/adult",
	}, []string{"/115/adult/ABF-363"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.Deleted != 1 || !slices.Equal(result.MediaIDs, []string{deleted.ID}) {
		t.Fatalf("unexpected result: %#v", result)
	}

	var deletedCount int64
	if err := db.Unscoped().Model(&model.Media{}).Where("id = ?", deleted.ID).Count(&deletedCount).Error; err != nil {
		t.Fatal(err)
	}
	if deletedCount != 0 {
		t.Fatal("deleted media should be hard-deleted")
	}
	var activeCount int64
	if err := db.Model(&model.Media{}).Where("id = ?", active.ID).Count(&activeCount).Error; err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 {
		t.Fatal("active media should be kept")
	}
}

func createPipelineMaintenanceRoot(t *testing.T, db *gorm.DB, libraryType, rootPath string) (model.Library, model.LibraryRoot) {
	t.Helper()
	lib := model.Library{Name: libraryType, Path: pipelineOpenListPathToCloudPath(rootPath), Type: libraryType, Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	root := model.LibraryRoot{LibraryID: lib.ID, Name: libraryType, Path: pipelineOpenListPathToCloudPath(rootPath), Enabled: true}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	return lib, root
}
