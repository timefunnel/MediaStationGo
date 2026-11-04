package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"go.uber.org/zap"
)

func TestPipelineIngestReceiptExcludesHistoricalEpisodes(t *testing.T) {
	for _, episode := range []int{197, 247} {
		t.Run(fmt.Sprint(episode), func(t *testing.T) {
			db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Media{})
			repos := repository.New(db)
			lib, root := createPipelineMaintenanceRoot(t, db, "anime", "/115/anime")
			const folder = "/115/anime/Show"
			rows := make([]model.Media, 0, episode+1)
			for i := 1; i < episode; i++ {
				rows = append(rows, model.Media{LibraryID: lib.ID, LibraryRootID: root.ID,
					Path:  cloudMediaPath("openlist", fmt.Sprintf("%s/Show.S01E%03d.mkv", folder, i)),
					Title: "Show", SeasonNum: 1, EpisodeNum: i, TMDbID: 12345, ScrapeStatus: "matched", SizeBytes: 999999})
			}
			identities := []PipelineIngestMediaIdentity{}
			for _, ext := range []string{"mkv", "mp4"} {
				path := fmt.Sprintf("%s/Show.S01E%03d.%s", folder, episode, ext)
				rows = append(rows, model.Media{LibraryID: lib.ID, LibraryRootID: root.ID,
					Path: cloudMediaPath("openlist", path), Title: "Show", SeasonNum: 1, EpisodeNum: episode, SizeBytes: 100})
				identities = append(identities, PipelineIngestMediaIdentity{OpenListPath: path, SeasonNum: 1, EpisodeNum: episode})
			}
			if err := db.Create(&rows).Error; err != nil {
				t.Fatal(err)
			}
			svc := NewPipelineIngestService(zap.NewNop(), repos, nil, nil, nil)
			target := pipelineResolvedTarget{LibraryID: lib.ID, RootID: root.ID, RootOpenListPath: "/115/anime", Category: "anime"}
			req := PipelineIngestRequest{TargetOpenListPaths: []string{folder}, TargetMediaIdentities: identities, RequireTargetPath: true}
			anchor, selected, _, _, err := svc.findMedia(t.Context(), target, req)
			if err != nil {
				t.Fatal(err)
			}
			if len(selected) != 2 || anchor.EpisodeNum != episode {
				t.Fatalf("receipt has %d media, anchor episode=%d; want only the two planned files for %d", len(selected), anchor.EpisodeNum, episode)
			}
			// A path belonging to another root must not satisfy the identity contract.
			if err := db.Model(&model.Media{}).Where("id = ?", selected[0].ID).Update("library_root_id", "other-root").Error; err != nil {
				t.Fatal(err)
			}
			if _, _, _, _, err := svc.findMedia(t.Context(), target, req); err == nil {
				t.Fatal("accepted an identity outside the requested root")
			}
		})
	}
}

func TestPipelineScrapeUsesConfirmedEpisodeIdentity(t *testing.T) {
	for _, mode := range []string{"success", "identity_conflict", "upstream_failure"} {
		t.Run(mode, func(t *testing.T) {
			conflict := mode == "identity_conflict"
			requests := []string{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/tv/272432":
					if mode == "upstream_failure" {
						http.Error(w, "metadata upstream unavailable", http.StatusBadGateway)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 272432, "name": "示例剧", "original_name": "示例剧", "first_air_date": "2020-01-01"})
				case "/tv/272432/season/1":
					_ = json.NewEncoder(w).Encode(map[string]any{"episodes": []map[string]any{{"episode_number": 197, "name": "新的一集"}}})
				default:
					t.Errorf("unexpected metadata request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			db := newServiceTestDB(t, &model.Library{}, &model.LibraryRoot{}, &model.Series{}, &model.Media{})
			repos := repository.New(db)
			cfg := &config.Config{}
			cfg.Secrets.TMDbAPIKey, cfg.Secrets.TMDbAPIProxy = "test-key", upstream.URL
			log := zap.NewNop()
			scraper := NewScraperService(cfg, log, repos, NewTMDbProvider(cfg, log, nil), nil, nil, nil, NewHub(log))
			svc := NewPipelineScrapeService(repos, scraper)
			lib, root := createPipelineMaintenanceRoot(t, db, "anime", "/115/anime")
			base := cloudMediaPath("openlist", "/115/anime/示例剧")
			old := model.Media{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "示例剧", Path: base + "/示例剧.S01E196.mkv", SeasonNum: 1, EpisodeNum: 196, TMDbID: 272432, ScrapeStatus: "matched"}
			rows := []model.Media{
				{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "示例剧", Path: base + "/示例剧.S01E197.mkv", SeasonNum: 1, EpisodeNum: 197, ScrapeStatus: "pending"},
				{LibraryID: lib.ID, LibraryRootID: root.ID, Title: "示例剧", Path: base + "/示例剧.S01E197.mp4", SeasonNum: 1, EpisodeNum: 197, ScrapeStatus: "pending"},
			}
			if err := db.Create(&old).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if conflict {
				other := old
				other.Base = model.Base{}
				other.Path = base + "/示例剧.S01E195.mkv"
				other.TMDbID = 99999
				if err := db.Create(&other).Error; err != nil {
					t.Fatal(err)
				}
			}
			req := PipelineScrapeRequest{Category: "anime", Provider: "tmdb", MediaType: "anime", Queries: []string{"资源标题不会参与搜索"},
				UseExistingEpisodeIdentity: true, MediaIDs: []string{rows[0].ID, rows[1].ID},
				EpisodeMappings: map[string]ManualEpisodeMapping{rows[0].ID: {SeasonNum: 1, EpisodeNum: 197}, rows[1].ID: {SeasonNum: 1, EpisodeNum: 197}}}
			result, err := svc.Scrape(t.Context(), rows[0].ID, req)
			if conflict {
				if err == nil || !strings.Contains(err.Error(), "身份冲突") || len(requests) != 0 {
					t.Fatalf("conflict error=%v requests=%v", err, requests)
				}
			} else if mode == "upstream_failure" {
				if err == nil || !strings.Contains(err.Error(), "读取已确认") {
					t.Fatalf("upstream error was hidden: %v", err)
				}
				for _, row := range rows {
					after, loadErr := repos.Media.FindByID(t.Context(), row.ID)
					if loadErr != nil {
						t.Fatal(loadErr)
					}
					if !after.UpdatedAt.Equal(row.UpdatedAt) || after.ScrapeStatus != "pending" {
						t.Fatal("metadata request failure modified a target episode")
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if result.AppliedCount != 2 {
					t.Fatalf("result=%+v", result)
				}
				requestsBefore := len(requests)
				req.VerifyOnly = true
				verified, err := svc.Scrape(t.Context(), rows[0].ID, req)
				if err != nil || verified.Mode != "verified" || verified.VerifiedCount != 2 || verified.AppliedCount != 0 || len(requests) != requestsBefore {
					t.Fatalf("read-only metadata verification: result=%+v err=%v requests=%v", verified, err, requests)
				}
				for _, row := range rows {
					after, err := repos.Media.FindByID(t.Context(), row.ID)
					if err != nil {
						t.Fatal(err)
					}
					if after.TMDbID != 272432 || after.EpisodeNum != 197 || after.ScrapeStatus != "matched" {
						t.Fatalf("incorrect applied row: %+v", after)
					}
				}
				if err := db.Model(&model.Media{}).Where("id = ?", rows[0].ID).Update("tm_db_id", 99999).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := svc.Scrape(t.Context(), rows[0].ID, req); err == nil || !strings.Contains(err.Error(), "身份冲突") {
					t.Fatalf("read-only verification accepted conflicting manual metadata: %v", err)
				}
				if len(requests) != requestsBefore {
					t.Fatal("read-only verification requested external metadata")
				}
			}
			after, err := repos.Media.FindByID(t.Context(), old.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !after.UpdatedAt.Equal(old.UpdatedAt) || after.EpisodeNum != 196 {
				t.Fatal("historical episode was modified")
			}
		})
	}
}
