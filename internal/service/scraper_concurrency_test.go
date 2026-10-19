package service

import (
	"strings"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

func TestApplyProviderMatchAllowsConcurrentTechnicalUpdates(t *testing.T) {
	for _, scenario := range []string{"probe", "search_aliases", "episode_probe"} {
		t.Run(scenario, func(t *testing.T) {
			s, repos, closeServer := newTestScraper(t)
			defer closeServer()
			lib := model.Library{Name: "Concurrent scrape", Path: "cloud://openlist/" + t.Name(), Type: "movie"}
			match := &Match{Title: "Matched", Overview: "Provider overview", MediaType: "movie"}
			if scenario == "episode_probe" {
				lib.Type = "tv"
				match.MediaType, match.TMDbID = "tv", 12345
			}
			if err := repos.DB.Create(&lib).Error; err != nil {
				t.Fatal(err)
			}
			media := model.Media{LibraryID: lib.ID, Path: lib.Path + "/Show.S01E01.mkv", Title: "Before", ScrapeStatus: "pending"}
			media.UpdatedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := repos.DB.Create(&media).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "search_aliases" {
				if err := repos.Media.RefreshSearchAliases(t.Context(), media.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				probeMedia := media
				probe := &ProbeResult{DurationSec: 1493, Width: 1920, Height: 1080, VideoCodec: "h264", AudioCodec: "aac"}
				if err := persistMediaProbeResult(t.Context(), repos, nil, nil, nil, &probeMedia, probe); err != nil {
					t.Fatal(err)
				}
			}
			current, err := repos.Media.FindByID(t.Context(), media.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.UpdatedAt.Equal(media.UpdatedAt) {
				t.Fatal("technical update did not advance the row timestamp")
			}
			if err := s.applyProviderMatchWithOptions(t.Context(), &media, &lib, match, ScrapeOptions{deferTMDbDetails: true, deferPeople: true}); err != nil {
				t.Fatalf("technical update rejected the scrape: %v", err)
			}
			stored, err := repos.Media.FindByID(t.Context(), media.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Title != "Matched" || stored.ScrapeStatus != "matched" {
				t.Fatalf("scrape result was not applied: %+v", stored)
			}
			if scenario != "search_aliases" && (stored.DurationSec != 1493 || stored.Width != 1920 || stored.Height != 1080 || stored.VideoCodec != "h264" || stored.AudioCodec != "aac") {
				t.Fatalf("scrape overwrote probed metadata: %+v", stored)
			}
		})
	}
}

func TestApplyProviderMatchRejectsConcurrentMetadataChanges(t *testing.T) {
	for _, scenario := range []string{"overview", "poster", "external_id", "library_root", "episode_mapping", "deleted"} {
		t.Run(scenario, func(t *testing.T) {
			s, repos, closeServer := newTestScraper(t)
			defer closeServer()
			lib := model.Library{Name: "Concurrent edit", Path: "cloud://openlist/" + t.Name(), Type: "tv"}
			if err := repos.DB.Create(&lib).Error; err != nil {
				t.Fatal(err)
			}
			media := model.Media{LibraryID: lib.ID, Path: lib.Path + "/Show.S01E01.mkv", Title: "Before", ScrapeStatus: "matched"}
			if err := repos.DB.Create(&media).Error; err != nil {
				t.Fatal(err)
			}
			edits := map[string]any{}
			switch scenario {
			case "overview":
				edits["overview"] = "User overview"
			case "poster":
				edits["poster_url"] = "/user-poster.jpg"
			case "external_id":
				edits["bangumi_id"] = 9988
			case "library_root":
				edits["library_root_id"] = "changed-root"
			case "episode_mapping":
				edits["episode_end_num"] = 2
			case "deleted":
				if err := repos.DB.Delete(&media).Error; err != nil {
					t.Fatal(err)
				}
			}
			// Bypass timestamp updates to prove protection checks the actual values.
			if len(edits) > 0 {
				if err := repos.DB.Model(&model.Media{}).Where("id = ?", media.ID).UpdateColumns(edits).Error; err != nil {
					t.Fatal(err)
				}
			}
			var before model.Media
			if err := repos.DB.Unscoped().First(&before, "id = ?", media.ID).Error; err != nil {
				t.Fatal(err)
			}
			err := s.applyProviderMatchWithOptions(t.Context(), &media, &lib, &Match{Title: "Scraped", MediaType: "tv", TMDbID: 12345}, ScrapeOptions{deferTMDbDetails: true, deferPeople: true})
			if err == nil {
				t.Fatal("stale scrape accepted a concurrent metadata change")
			}
			if scenario != "deleted" && !strings.Contains(err.Error(), "已变化") {
				t.Fatalf("unexpected rejection: %v", err)
			}
			var stored model.Media
			if err := repos.DB.Unscoped().First(&stored, "id = ?", media.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Title != before.Title || stored.Overview != before.Overview || stored.PosterURL != before.PosterURL || stored.BangumiID != before.BangumiID || stored.LibraryRootID != before.LibraryRootID || stored.EpisodeEndNum != before.EpisodeEndNum || stored.TMDbID != before.TMDbID || !stored.UpdatedAt.Equal(before.UpdatedAt) || stored.DeletedAt != before.DeletedAt {
				t.Fatalf("failed scrape changed media: before=%+v after=%+v", before, stored)
			}
			var seriesCount int64
			if err := repos.DB.Model(&model.Series{}).Where("library_id = ?", lib.ID).Count(&seriesCount).Error; err != nil {
				t.Fatal(err)
			}
			if seriesCount != 0 {
				t.Fatal("failed scrape saved series metadata")
			}
		})
	}
}

func TestManualScrapePreviewAllowsProbeButRejectsMetadataEdit(t *testing.T) {
	for _, scenario := range []string{"probe", "overview"} {
		t.Run(scenario, func(t *testing.T) {
			s, repos, closeServer := newTestScraper(t)
			defer closeServer()
			lib := model.Library{Name: "Preview", Path: "cloud://openlist/" + t.Name(), Type: "tv"}
			if err := repos.DB.Create(&lib).Error; err != nil {
				t.Fatal(err)
			}
			media := model.Media{LibraryID: lib.ID, Path: lib.Path + "/Show.S01E01.mkv", Title: "Before", ScrapeStatus: "matched"}
			if err := repos.DB.Create(&media).Error; err != nil {
				t.Fatal(err)
			}
			req := ManualScrapeRequest{Source: "tmdb", MediaType: "tv", TMDbID: 12345}
			rows, err := s.PreviewManualMatch(t.Context(), []string{media.ID}, req, false)
			if err != nil || len(rows) != 1 || !rows[0].Valid {
				t.Fatalf("preview failed: %+v %v", rows, err)
			}
			req.ExpectedRevisions = map[string]string{media.ID: rows[0].Revision}
			if scenario == "probe" {
				probeMedia := media
				if err := persistMediaProbeResult(t.Context(), repos, nil, nil, nil, &probeMedia, &ProbeResult{DurationSec: 1493, Width: 1920}); err != nil {
					t.Fatal(err)
				}
			} else if err := repos.DB.Model(&model.Media{}).Where("id = ?", media.ID).UpdateColumn("overview", "User overview").Error; err != nil {
				t.Fatal(err)
			}
			stored, err := s.ApplyManualMatchWithOptions(t.Context(), media.ID, req, ScrapeOptions{deferTMDbDetails: true, deferPeople: true})
			if scenario == "probe" {
				if err != nil || stored.ScrapeStatus != "matched" || stored.TMDbID != 12345 || stored.DurationSec != 1493 || stored.Width != 1920 {
					t.Fatalf("preview could not coexist with technical update: %+v %v", stored, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "重新预览") {
					t.Fatalf("stale preview accepted a metadata edit: %+v %v", stored, err)
				}
				current, findErr := repos.Media.FindByID(t.Context(), media.ID)
				if findErr != nil || current.Overview != "User overview" || current.TMDbID != 0 {
					t.Fatalf("stale preview changed media: %+v %v", current, findErr)
				}
			}
		})
	}
}
