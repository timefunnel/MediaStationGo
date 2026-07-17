package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func TestPreviewAndApplyMediaTitleCleanup(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.Media{})
	repos := repository.New(db)
	lib := model.Library{
		Name: "其他媒体", Path: "cloud://openlist/115%2F其他", Type: "movie",
		TitleMode: LibraryTitleModeFilename, Enabled: true,
	}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	rows := []model.Media{
		{LibraryID: lib.ID, Title: "mp4_ (2)", Path: "cloud://openlist/115/其他/作品 A/视图/V/mp4_ (2).mp4", ScrapeStatus: "pending"},
		{LibraryID: lib.ID, Title: "mp4_ (3)", Path: "cloud://openlist/115/其他/作品 A/视图/V/mp4_ (3).mp4", ScrapeStatus: "no_match"},
		{LibraryID: lib.ID, Title: "正式元数据", Path: "cloud://openlist/115/其他/作品 B/movie.mp4", ScrapeStatus: "matched", TMDbID: 123},
	}
	if err := repos.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		system, _ := decodeAIRequestMessages(t, r)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(system, "标题标准化器") {
			writeAIContent(t, w, `{"items":[{"media_id":"`+rows[0].ID+`","title":"作品 A 第一段","year":0,"confidence":0.92,"reason":"第一段"},{"media_id":"`+rows[1].ID+`","title":"作品 A 第二段","year":0,"confidence":0.88,"reason":"第二段"}]}`)
			return
		}
		writeAIContent(t, w, `{"items":[{"media_id":"`+rows[0].ID+`","relation":"part","group_key":"work-a","group_title":"作品 A","part_index":1,"confidence":0.92,"reason":"连续分段"},{"media_id":"`+rows[1].ID+`","relation":"part","group_key":"work-a","group_title":"作品 A","part_index":2,"confidence":0.88,"reason":"连续分段"}]}`)
	}))
	defer server.Close()
	ai := NewAIService(&config.Config{AI: config.AIConfig{
		Enabled: true, APIBase: server.URL, APIKey: "test", Model: "test",
	}}, zap.NewNop(), nil)
	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos).SetAI(ai)

	preview, err := svc.PreviewTitleCleanup(t.Context(), lib.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CandidateCount != 2 || preview.BatchCount != 2 || len(preview.Groups) != 1 {
		t.Fatalf("preview = %#v", preview)
	}
	if preview.Groups[0].SourceDirectory != "作品 A" || preview.Groups[0].Items[0].DirectoryChain != "作品 A / 视图 / V" {
		t.Fatalf("group context = %#v", preview.Groups[0])
	}

	result, err := svc.ApplyTitleCleanup(t.Context(), lib.ID, MediaTitleCleanupApplyRequest{Items: preview.Suggestions})
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 2 {
		t.Fatalf("apply result = %#v", result)
	}
	for i, want := range []string{"作品 A 第一段", "作品 A 第二段"} {
		var stored model.Media
		if err := repos.DB.First(&stored, "id = ?", rows[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Title != want || stored.ScrapeStatus != MediaScrapeStatusTitleCleaned ||
			stored.PartGroupKey == "" || stored.PartGroupTitle != "作品 A" || stored.PartIndex != i+1 ||
			stored.TitleCleanupVersion != currentMediaTitleCleanupVersion {
			t.Fatalf("stored media = %#v", stored)
		}
	}
	grouped := groupMediaVersions(rowsFromDB(t, repos, []string{rows[0].ID, rows[1].ID}))
	if len(grouped) != 1 || len(grouped[0].Parts) != 2 || grouped[0].Title != "作品 A" {
		t.Fatalf("part grouping = %#v", grouped)
	}
	parts, err := svc.ListMediaParts(t.Context(), rows[1].ID)
	if err != nil || len(parts.Items) != 2 || parts.Items[0].PartIndex != 1 || parts.Items[1].PartIndex != 2 {
		t.Fatalf("part list = %#v, err=%v", parts, err)
	}
	detail, err := svc.GetMedia(t.Context(), rows[1].ID)
	if err != nil || detail == nil || detail.DisplayTitle != "作品 A" {
		t.Fatalf("part detail = %#v, err=%v", detail, err)
	}
}

func TestMediaTitleCleanupJobReturnsProgressAndPreview(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.Media{})
	repos := repository.New(db)
	lib := model.Library{Name: "其他媒体", Path: "/media/other", Type: "movie", TitleMode: LibraryTitleModeFilename, Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	row := model.Media{LibraryID: lib.ID, Title: "raw", Path: "/media/other/作品 C/raw.mp4", ScrapeStatus: "pending"}
	if err := repos.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		system, _ := decodeAIRequestMessages(t, r)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(system, "标题标准化器") {
			writeAIContent(t, w, `{"items":[{"media_id":"`+row.ID+`","title":"作品 C","year":0,"confidence":0.91,"reason":"目录名"}]}`)
			return
		}
		writeAIContent(t, w, `{"items":[{"media_id":"`+row.ID+`","relation":"standalone","confidence":0.91,"reason":"单文件"}]}`)
	}))
	defer server.Close()
	ai := NewAIService(&config.Config{AI: config.AIConfig{
		Enabled: true, APIBase: server.URL, APIKey: "test", Model: "test",
	}}, zap.NewNop(), nil)
	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos).SetAI(ai).SetTaskTracker(NewTaskTrackerService(zap.NewNop(), nil))

	job, err := svc.StartTitleCleanupJob(context.Background(), lib.ID, 5)
	if err != nil || job.Status != MediaTitleCleanupJobQueued {
		t.Fatalf("start job = %#v, err=%v", job, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err = svc.GetTitleCleanupJob(lib.ID, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == MediaTitleCleanupJobCompleted {
			if job.Progress != 100 || job.Preview == nil || len(job.Preview.Suggestions) != 1 {
				t.Fatalf("completed job = %#v", job)
			}
			return
		}
		if job.Status == MediaTitleCleanupJobFailed {
			t.Fatalf("job failed: %s", job.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job did not finish: %#v", job)
}

func TestMediaTitleCleanupReusesExistingPartGroupForNewFile(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.Media{})
	repos := repository.New(db)
	lib := model.Library{Name: "其他媒体", Path: "/media/other", Type: "movie", TitleMode: LibraryTitleModeFilename, Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	const existingKey = "00112233445566778899aabbccddeeff"
	rows := []model.Media{
		{LibraryID: lib.ID, Title: "作品 A 第 1 段", Path: "/media/other/作品 A/001.mp4", ScrapeStatus: MediaScrapeStatusTitleCleaned, TitleCleanupVersion: currentMediaTitleCleanupVersion, PartGroupKey: existingKey, PartGroupTitle: "作品 A", PartIndex: 1},
		{LibraryID: lib.ID, Title: "作品 A 第 2 段", Path: "/media/other/作品 A/002.mp4", ScrapeStatus: MediaScrapeStatusTitleCleaned, TitleCleanupVersion: currentMediaTitleCleanupVersion, PartGroupKey: existingKey, PartGroupTitle: "作品 A", PartIndex: 2},
		{LibraryID: lib.ID, Title: "003", Path: "/media/other/作品 A/003.mp4", ScrapeStatus: "pending"},
	}
	if err := repos.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		system, user := decodeAIRequestMessages(t, r)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(system, "标题标准化器") {
			writeAIContent(t, w, `{"items":[{"media_id":"`+rows[2].ID+`","title":"作品 A 第 3 段","year":0,"confidence":0.94,"reason":"目录和序号"}]}`)
			return
		}
		if !strings.Contains(user, existingKey) {
			t.Fatalf("existing part group missing from relationship request: %s", user)
		}
		writeAIContent(t, w, `{"items":[{"media_id":"`+rows[2].ID+`","relation":"part","group_key":"`+existingKey+`","group_title":"作品 A","part_index":3,"confidence":0.93,"reason":"续入已有分段"}]}`)
	}))
	defer server.Close()
	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos).SetAI(newTestTitleCleanupAI(server.URL))

	preview, err := svc.PreviewTitleCleanup(t.Context(), lib.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Suggestions) != 1 || preview.Suggestions[0].ExistingGroupKey != existingKey {
		t.Fatalf("preview did not reuse group: %#v", preview.Suggestions)
	}
	if _, err := svc.ApplyTitleCleanup(t.Context(), lib.ID, MediaTitleCleanupApplyRequest{Items: preview.Suggestions}); err != nil {
		t.Fatal(err)
	}
	var stored model.Media
	if err := repos.DB.First(&stored, "id = ?", rows[2].ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.PartGroupKey != existingKey || stored.PartIndex != 3 {
		t.Fatalf("new part was not appended to existing group: %#v", stored)
	}
	grouped := groupMediaVersions(rowsFromDB(t, repos, []string{rows[0].ID, rows[1].ID, rows[2].ID}))
	if len(grouped) != 1 || len(grouped[0].Parts) != 3 {
		t.Fatalf("expected one three-part work, got %#v", grouped)
	}
}

func rowsFromDB(t *testing.T, repos *repository.Container, ids []string) []model.Media {
	t.Helper()
	var rows []model.Media
	if err := repos.DB.Where("id IN ? AND deleted_at IS NULL", ids).Order("path ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}
