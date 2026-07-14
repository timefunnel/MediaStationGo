package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"go.uber.org/zap"
)

func TestPipelineIngestFindsMediaByTargetPath(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{}, &model.PipelineIngestJobRecord{})
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

func TestPipelineIngestScansOnlyTargetOpenListPath(t *testing.T) {
	requested := []openListListTestRequest{}
	upstream := newOpenListAPIServerWithRequests(t, func(req openListListTestRequest) ([]openListTestEntry, int) {
		requested = append(requested, req)
		switch req.Path {
		case "/115/adult":
			return []openListTestEntry{{Name: "EBWH-285", IsDir: true}, {Name: "OTHER-001", IsDir: true}}, 2
		case "/115/adult/EBWH-285":
			return []openListTestEntry{{Name: "EBWH-285.mp4", Size: 5000}}, 1
		case "/115/adult/OTHER-001":
			t.Fatalf("targeted ingest should not recursively scan sibling directory %q", req.Path)
			return nil, 0
		default:
			t.Fatalf("unexpected openlist path %q", req.Path)
			return nil, 0
		}
	})
	defer upstream.Close()

	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{}, &model.Setting{}, &model.StorageConfig{}, &model.PipelineIngestJobRecord{})
	repos := repository.New(db)
	log := zap.NewNop()
	storage := NewStorageConfigService(log, repos, NewCryptoService("", log))
	if _, err := storage.Save(t.Context(), StorageInput{Type: "openlist", Config: map[string]any{"server": upstream.URL, "token": "openlist-token"}}); err != nil {
		t.Fatal(err)
	}
	libPath := BuildCloudLibraryPath("openlist", "/115/adult", "/115/adult")
	lib := model.Library{Name: "Adult", Path: libPath, Type: "adult", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	root := model.LibraryRoot{LibraryID: lib.ID, Name: "Adult", Path: libPath, Enabled: true}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	scanner := NewScannerService(&config.Config{}, log, repos, NewHub(log), nil, nil)
	scanner.SetStorageConfig(storage)
	maintenance := NewPipelineMaintenanceService(log, repos)
	svc := NewPipelineIngestService(log, repos, scanner, maintenance, nil)

	job, err := svc.Start(t.Context(), PipelineIngestRequest{
		PipelineMaintenanceTarget: PipelineMaintenanceTarget{Category: "adult", LibraryID: lib.ID, RootID: root.ID, RootOpenListPath: "/115/adult"},
		Title:                     "EBWH-285",
		TargetOpenListPaths:       []string{"/115/adult/EBWH-285"},
		RequireTargetPath:         true,
		Scan:                      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	job = waitPipelineIngestJob(t, svc, job.ID)
	if job.Status != PipelineIngestStatusCompleted {
		t.Fatalf("job status=%s error=%s", job.Status, job.Error)
	}
	if job.Result.Scan == nil || job.Result.Scan.Visited != 1 || job.Result.Scan.Added != 1 {
		t.Fatalf("scan result = %#v, want visited=1 added=1", job.Result.Scan)
	}
	if job.Result.Media == nil || job.Result.Media.MatchMode != "path" || job.Result.Media.Path != "cloud://openlist/115/adult/EBWH-285/EBWH-285.mp4" {
		t.Fatalf("unexpected media result: %#v", job.Result.Media)
	}
	if len(requested) != 2 || requested[0].Path != "/115/adult" || requested[1].Path != "/115/adult/EBWH-285" {
		t.Fatalf("openlist paths requested = %#v, want parent then target only", requested)
	}
	if !requested[0].Refresh || !requested[1].Refresh {
		t.Fatalf("openlist refresh flags = %#v, want parent and target refreshed", requested)
	}
}

func TestPipelineIngestScansOnlyTargetOpenListFile(t *testing.T) {
	const targetName = "黑衣人3.Men.in.Black.III.2012.UHD.Bluray.REMUX.2160p.mkv"
	requested := []openListListTestRequest{}
	upstream := newOpenListAPIServerWithRequests(t, func(req openListListTestRequest) ([]openListTestEntry, int) {
		requested = append(requested, req)
		if req.Path != "/115/movie" {
			t.Fatalf("targeted file ingest should only list its parent, got %q", req.Path)
		}
		return []openListTestEntry{
			{Name: targetName, Size: 5000},
			{Name: "Sibling.Movie.2024.mkv", Size: 6000},
			{Name: "Sibling.Directory", IsDir: true},
		}, 3
	})
	defer upstream.Close()

	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{}, &model.Setting{}, &model.StorageConfig{}, &model.PipelineIngestJobRecord{})
	repos := repository.New(db)
	log := zap.NewNop()
	storage := NewStorageConfigService(log, repos, NewCryptoService("", log))
	if _, err := storage.Save(t.Context(), StorageInput{Type: "openlist", Config: map[string]any{"server": upstream.URL, "token": "openlist-token"}}); err != nil {
		t.Fatal(err)
	}
	libPath := BuildCloudLibraryPath("openlist", "/115/movie", "/115/movie")
	lib := model.Library{Name: "Movies", Path: libPath, Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	root := model.LibraryRoot{LibraryID: lib.ID, Name: "Movies", Path: libPath, Enabled: true}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	scanner := NewScannerService(&config.Config{}, log, repos, NewHub(log), nil, nil)
	scanner.SetStorageConfig(storage)
	maintenance := NewPipelineMaintenanceService(log, repos)
	svc := NewPipelineIngestService(log, repos, scanner, maintenance, nil)
	targetPath := "/115/movie/" + targetName

	job, err := svc.Start(t.Context(), PipelineIngestRequest{
		PipelineMaintenanceTarget: PipelineMaintenanceTarget{Category: "movie", LibraryID: lib.ID, RootID: root.ID, RootOpenListPath: "/115/movie"},
		Title:                     "黑衣人3",
		TargetOpenListPaths:       []string{targetPath},
		RequireTargetPath:         true,
		Scan:                      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	job = waitPipelineIngestJob(t, svc, job.ID)
	if job.Status != PipelineIngestStatusCompleted {
		t.Fatalf("job status=%s error=%s", job.Status, job.Error)
	}
	if job.Result.Scan == nil || job.Result.Scan.Visited != 1 || job.Result.Scan.Added != 1 {
		t.Fatalf("scan result = %#v, want visited=1 added=1", job.Result.Scan)
	}
	wantMediaPath := "cloud://openlist/115/movie/" + targetName
	if job.Result.Media == nil || job.Result.Media.MatchMode != "path" || job.Result.Media.MatchPath != targetPath || job.Result.Media.Path != wantMediaPath {
		t.Fatalf("unexpected media result: %#v", job.Result.Media)
	}
	if len(requested) != 1 || requested[0].Path != "/115/movie" || !requested[0].Refresh {
		t.Fatalf("openlist requests = %#v, want one refreshed parent listing", requested)
	}
	var mediaRows []model.Media
	if err := db.Find(&mediaRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(mediaRows) != 1 || mediaRows[0].Path != wantMediaPath {
		t.Fatalf("media rows = %#v, want only target file", mediaRows)
	}
}

func TestPipelineIngestRejectsUnhandledTargetOpenListPaths(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.PipelineIngestJobRecord{})
	repos := repository.New(db)
	log := zap.NewNop()
	lib := model.Library{Name: "Local", Path: "/media/local", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	root := model.LibraryRoot{LibraryID: lib.ID, Name: "Local", Path: "/media/local", Enabled: true}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	scanner := NewScannerService(&config.Config{}, log, repos, NewHub(log), nil, nil)
	maintenance := NewPipelineMaintenanceService(log, repos)
	svc := NewPipelineIngestService(log, repos, scanner, maintenance, nil)

	job, err := svc.Start(t.Context(), PipelineIngestRequest{
		PipelineMaintenanceTarget: PipelineMaintenanceTarget{
			Category:         "movie",
			LibraryID:        lib.ID,
			RootID:           root.ID,
			RootOpenListPath: "/115/movie",
		},
		Title:               "Movie",
		TargetOpenListPaths: []string{"/115/movie/Movie"},
		RequireTargetPath:   true,
		Scan:                true,
	})
	if err != nil {
		t.Fatal(err)
	}
	job = waitPipelineIngestJob(t, svc, job.ID)
	if job.Status != PipelineIngestStatusFailed {
		t.Fatalf("job status=%s error=%s, want failed", job.Status, job.Error)
	}
	if !strings.Contains(job.Error, "target_openlist_paths") {
		t.Fatalf("job error = %q, want target_openlist_paths error", job.Error)
	}
	if job.Result.Scan != nil {
		t.Fatalf("scan result = %#v, want no fallback root scan", job.Result.Scan)
	}
}

func TestPipelineIngestRunsMovieExtrasRepair(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{}, &model.PipelineIngestJobRecord{})
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

func TestPipelineIngestEnsuresPersistentCloudSubtitleCache(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{}, &model.PipelineIngestJobRecord{})
	repos := repository.New(db)
	log := zap.NewNop()
	lib, root := createPipelineMaintenanceRoot(t, db, "movie", "/115/movie")
	media := model.Media{
		LibraryID:     lib.ID,
		LibraryRootID: root.ID,
		Title:         "Movie",
		Path:          "cloud://openlist/115/movie/Movie/Movie.mkv",
		SizeBytes:     5000,
	}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	mediaDir := filepath.Join(cacheDir, media.ID)
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	indexBody, err := json.Marshal(localSubtitleIndex{
		MediaID:   media.ID,
		Source:    "cloud",
		Provider:  "openlist",
		MediaPath: media.Path,
		Tracks:    []localSubtitleIndexTrack{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaDir, "tracks.json"), indexBody, 0o644); err != nil {
		t.Fatal(err)
	}
	subtitle := NewSubtitleService(log, repos)
	subtitle.SetMaterializedCacheDir(cacheDir)
	maintenance := NewPipelineMaintenanceService(log, repos)
	svc := NewPipelineIngestService(log, repos, nil, maintenance, nil)
	svc.SetSubtitleService(subtitle)

	job, err := svc.Start(t.Context(), PipelineIngestRequest{
		PipelineMaintenanceTarget: PipelineMaintenanceTarget{Category: "movie", LibraryID: lib.ID, RootID: root.ID, RootOpenListPath: "/115/movie"},
		Title:                     "Movie",
		TargetOpenListPaths:       []string{"/115/movie/Movie"},
		RequireTargetPath:         true,
		Scan:                      false,
	})
	if err != nil {
		t.Fatal(err)
	}
	job = waitPipelineIngestJob(t, svc, job.ID)
	if job.Status != PipelineIngestStatusCompleted {
		t.Fatalf("job status=%s error=%s", job.Status, job.Error)
	}
	if job.Result.CloudSubtitles == nil || job.Result.CloudSubtitles.Status != "skipped" || job.Result.CloudSubtitles.Reason != "cache_current" {
		t.Fatalf("cloud subtitle result=%#v", job.Result.CloudSubtitles)
	}
}

func TestPipelineIngestPersistsAndReusesIdempotentJob(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{}, &model.PipelineIngestJobRecord{})
	repos := repository.New(db)
	maintenance := NewPipelineMaintenanceService(zap.NewNop(), repos)
	svc := NewPipelineIngestService(zap.NewNop(), repos, nil, maintenance, nil)
	lib, root := createPipelineMaintenanceRoot(t, db, "adult", "/115/adult")
	media := model.Media{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "ABF-363", Path: "cloud://openlist/115/adult/ABF-363/ABF-363.mp4"}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	request := PipelineIngestRequest{
		PipelineMaintenanceTarget: PipelineMaintenanceTarget{Category: "adult", LibraryID: lib.ID, RootID: root.ID, RootOpenListPath: "/115/adult"},
		IdempotencyKey:            "task:ABF-363",
		Title:                     "ABF-363",
		TargetOpenListPaths:       []string{"/115/adult/ABF-363"},
		RequireTargetPath:         true,
	}

	job, err := svc.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	job = waitPipelineIngestJob(t, svc, job.ID)
	if job.Status != PipelineIngestStatusCompleted {
		t.Fatalf("job=%#v", job)
	}

	restarted := NewPipelineIngestService(zap.NewNop(), repos, nil, maintenance, nil)
	persisted, err := restarted.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != PipelineIngestStatusCompleted || persisted.Result.Media == nil || persisted.Result.Media.ID != media.ID {
		t.Fatalf("persisted=%#v", persisted)
	}
	reused, err := restarted.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != job.ID || reused.Status != PipelineIngestStatusCompleted {
		t.Fatalf("reused=%#v", reused)
	}
	conflicting := request
	conflicting.Title = "Different"
	if _, err := restarted.Start(t.Context(), conflicting); err == nil || !strings.Contains(err.Error(), "different request") {
		t.Fatalf("conflict err=%v", err)
	}
	var count int64
	if err := db.Model(&model.PipelineIngestJobRecord{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("job records=%d", count)
	}
}

func TestPipelineIngestRecoversPersistedRunningJob(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{}, &model.PipelineIngestJobRecord{})
	repos := repository.New(db)
	maintenance := NewPipelineMaintenanceService(zap.NewNop(), repos)
	lib, root := createPipelineMaintenanceRoot(t, db, "adult", "/115/adult")
	media := model.Media{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "ABF-363", Path: "cloud://openlist/115/adult/ABF-363/ABF-363.mp4"}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	seed := NewPipelineIngestService(zap.NewNop(), repos, nil, maintenance, nil)
	seedJob := PipelineIngestJob{
		ID:      "recover-job",
		Status:  PipelineIngestStatusRunning,
		Stage:   "find_media",
		Message: "finding ingested media",
		Request: PipelineIngestRequest{
			PipelineMaintenanceTarget: PipelineMaintenanceTarget{Category: "adult", LibraryID: lib.ID, RootID: root.ID, RootOpenListPath: "/115/adult"},
			IdempotencyKey:            "task:recover-ABF-363",
			Title:                     "ABF-363",
			TargetOpenListPaths:       []string{"/115/adult/ABF-363"},
			RequireTargetPath:         true,
		},
		StartedAt: now,
		UpdatedAt: now,
	}
	if err := seed.storeJob(t.Context(), seedJob); err != nil {
		t.Fatal(err)
	}

	restarted := NewPipelineIngestService(zap.NewNop(), repos, nil, maintenance, nil)
	count, err := restarted.Recover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("recovered=%d", count)
	}
	job := waitPipelineIngestJob(t, restarted, seedJob.ID)
	if job.Status != PipelineIngestStatusCompleted || job.Result.Media == nil || job.Result.Media.ID != media.ID {
		t.Fatalf("job=%#v", job)
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
