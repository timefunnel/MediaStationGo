package service

import (
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"go.uber.org/zap"
)

func TestPipelineIngestFindsMediaByTargetPath(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{})
	repos := repository.New(db)
	maintenance := NewPipelineMaintenanceService(zap.NewNop(), repos)
	svc := NewPipelineIngestService(zap.NewNop(), repos, nil, maintenance, nil)

	lib, root := createPipelineMaintenanceRoot(t, db, "adult", "/115/adult")
	rows := []model.Media{
		{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "PV", Path: "cloud://openlist/115/adult/ABF-363/特典/PV.mp4", SizeBytes: 10},
		{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "ABF-363", Path: "cloud://openlist/115/adult/ABF-363/ABF-363.mp4", SizeBytes: 3000},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	main := rows[1]

	job, err := svc.Start(t.Context(), PipelineIngestRequest{
		PipelineMaintenanceTarget: PipelineMaintenanceTarget{
			Category:         "adult",
			LibraryID:        lib.ID,
			RootID:           root.ID,
			RootOpenListPath: "/115/adult",
		},
		Title:               "ABF-363",
		TargetOpenListPaths: []string{"/115/adult/ABF-363"},
		RequireTargetPath:   true,
		Scan:                false,
	})
	if err != nil {
		t.Fatal(err)
	}
	job = waitPipelineIngestJob(t, svc, job.ID)
	if job.Status != PipelineIngestStatusCompleted {
		t.Fatalf("job status=%s error=%s", job.Status, job.Error)
	}
	if job.Result.Media == nil || job.Result.Media.ID != main.ID || job.Result.Media.MatchMode != "path" {
		t.Fatalf("unexpected media result: %#v", job.Result.Media)
	}
}

func TestPipelineIngestRunsMovieExtrasRepair(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{})
	repos := repository.New(db)
	maintenance := NewPipelineMaintenanceService(zap.NewNop(), repos)
	svc := NewPipelineIngestService(zap.NewNop(), repos, nil, maintenance, nil)

	lib, root := createPipelineMaintenanceRoot(t, db, "movie", "/115/movie")
	rows := []model.Media{
		{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "Movie", Path: "cloud://openlist/115/movie/Movie/Movie.mkv", SizeBytes: 5000},
		{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "PV", Path: "cloud://openlist/115/movie/Movie/Extras/PV.mp4", SizeBytes: 10},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	job, err := svc.Start(t.Context(), PipelineIngestRequest{
		PipelineMaintenanceTarget: PipelineMaintenanceTarget{
			Category:         "movie",
			LibraryID:        lib.ID,
			RootID:           root.ID,
			RootOpenListPath: "/115/movie",
		},
		TargetOpenListPaths: []string{"/115/movie/Movie"},
		RequireTargetPath:   true,
		RepairMovieExtras:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	job = waitPipelineIngestJob(t, svc, job.ID)
	if job.Status != PipelineIngestStatusCompleted {
		t.Fatalf("job status=%s error=%s", job.Status, job.Error)
	}
	if job.Result.MovieExtras == nil || job.Result.MovieExtras.Updated != 1 || job.Result.MovieExtras.OpenListHidePath != "/115/movie/Movie" {
		t.Fatalf("unexpected extras result: %#v", job.Result.MovieExtras)
	}
}

func waitPipelineIngestJob(t *testing.T, svc *PipelineIngestService, id string) PipelineIngestJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		job, err := svc.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != PipelineIngestStatusRunning {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("pipeline ingest job timed out: %#v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
