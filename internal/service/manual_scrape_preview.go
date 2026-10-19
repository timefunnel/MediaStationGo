package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

type ScrapePreviewRow struct {
	MediaID  string              `json:"media_id"`
	Revision string              `json:"revision"`
	Path     string              `json:"path"`
	Evidence EpisodePathEvidence `json:"evidence"`
	Season   int                 `json:"season_num"`
	Episode  int                 `json:"episode_num"`
	End      int                 `json:"episode_end_num"`
	Part     int                 `json:"episode_part_num"`
	Valid    bool                `json:"valid"`
	Error    string              `json:"error,omitempty"`
}

type ManualScrapePreview struct {
	Rows []ScrapePreviewRow `json:"items"`
	TMDb *TMDbSeriesSummary `json:"tmdb,omitempty"`
}

func scrapeRevision(media model.Media) string {
	// Guard the source identity and editable metadata used or replaced by a
	// scrape. Probe results, generated artwork and derived indexes can change
	// independently; their writes also advance UpdatedAt.
	data, _ := json.Marshal(struct {
		ID, LibraryID, LibraryRootID, SeriesID, Path, RelativePath, STRMURL string
		FileID                                                              string
		SizeBytes                                                           int64
		Title, OriginalName, Status, EpisodeTitle                           string
		Overview, PosterURL, BackdropURL, ReleaseDate                       string
		DoubanID, TheTVDBID, Languages, Countries, Genres, Actors           string
		TMDbID, BangumiID, Year, Season, Episode, End, Part                 int
		RatingBits                                                          uint32
		NSFW                                                                bool
	}{
		ID: media.ID, LibraryID: media.LibraryID, LibraryRootID: media.LibraryRootID,
		SeriesID: media.SeriesID, Path: media.Path, RelativePath: media.RelativePath, STRMURL: media.STRMURL,
		FileID: media.FileID, SizeBytes: media.SizeBytes,
		Title: media.Title, OriginalName: media.OriginalName, Status: media.ScrapeStatus, EpisodeTitle: media.EpisodeTitle,
		Overview: media.Overview, PosterURL: media.PosterURL, BackdropURL: media.BackdropURL, ReleaseDate: media.ReleaseDate,
		DoubanID: media.DoubanID, TheTVDBID: media.TheTVDBID, Languages: media.Languages,
		Countries: media.Countries, Genres: media.Genres, Actors: media.Actors,
		TMDbID: media.TMDbID, BangumiID: media.BangumiID, Year: media.Year,
		Season: media.SeasonNum, Episode: media.EpisodeNum, End: media.EpisodeEndNum, Part: media.EpisodePartNum,
		RatingBits: math.Float32bits(media.Rating), NSFW: media.NSFW,
	})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func checkScrapeRevision(req ManualScrapeRequest, media model.Media) error {
	if req.ExpectedRevisions != nil && req.ExpectedRevisions[media.ID] != scrapeRevision(media) {
		return fmt.Errorf("媒体已变化或缺少预览版本，请重新预览")
	}
	return nil
}

// PreviewManualMatch uses exactly the same validator as application, but never
// calls artwork preparation, persistence, NFO, or any cloud filesystem API.
func (s *ScraperService) PreviewManualMatch(ctx context.Context, ids []string, req ManualScrapeRequest, automatic bool) ([]ScrapePreviewRow, error) {
	preview, err := s.PreviewManualMatchDetails(ctx, ids, req, automatic)
	return preview.Rows, err
}

func (s *ScraperService) PreviewManualMatchDetails(ctx context.Context, ids []string, req ManualScrapeRequest, automatic bool) (ManualScrapePreview, error) {
	preview, _, _, err := s.previewManualMatchDetailsWithOptions(ctx, ids, req, automatic, nil)
	return preview, err
}

func (s *ScraperService) previewManualMatchDetailsWithOptions(ctx context.Context, ids []string, req ManualScrapeRequest, automatic bool, suppliedMatch *Match) (ManualScrapePreview, ScrapeOptions, *Match, error) {
	if len(ids) == 0 || len(ids) > 2000 {
		return ManualScrapePreview{}, ScrapeOptions{}, nil, fmt.Errorf("每次预览需指定 1 至 2000 条媒体")
	}
	if err := validateManualEpisodeMappings(req, ids); err != nil {
		return ManualScrapePreview{}, ScrapeOptions{}, nil, err
	}
	options := ScrapeOptions{automaticSelection: automatic, episodeValidation: make(map[[2]int]map[int]*TMDbEpisodeDetails), episodeFailures: make(map[[2]int]error)}
	if err := configureManualEpisodeMapping(req, &options); err != nil {
		return ManualScrapePreview{}, ScrapeOptions{}, nil, err
	}
	if len(req.EpisodeMappings) == 0 && (req.SeasonNum != nil || req.EpisodeNum != nil) {
		if len(ids) != 1 || req.SeasonNum == nil || req.EpisodeNum == nil || *req.SeasonNum < 0 || *req.EpisodeNum < 1 {
			return ManualScrapePreview{}, ScrapeOptions{}, nil, fmt.Errorf("显式季集映射仅支持单条，需有效季号和集号")
		}
		options.manualEpisodeIdentity = &episodeRef{Season: *req.SeasonNum, Episode: *req.EpisodeNum}
	}
	match := cloneManualScrapeMatch(suppliedMatch)
	if match == nil {
		var err error
		match, err = s.manualRequestMatch(ctx, req)
		if err != nil {
			return ManualScrapePreview{}, ScrapeOptions{}, nil, err
		}
	}
	rows, err := s.repo.Media.FindByIDs(ctx, ids)
	if err != nil {
		return ManualScrapePreview{}, ScrapeOptions{}, nil, err
	}
	byID := make(map[string]model.Media, len(rows))
	for _, m := range rows {
		byID[m.ID] = m
	}
	out := make([]ScrapePreviewRow, 0, len(ids))
	for _, id := range ids {
		m, ok := byID[id]
		r := ScrapePreviewRow{MediaID: id}
		if !ok {
			r.Error = "media not found"
			out = append(out, r)
			continue
		}
		r.Path = m.Path
		r.Revision = scrapeRevision(m)
		r.Evidence = ParseEpisodeEvidence(m.Path)
		lib, err := s.repo.Library.FindByID(ctx, m.LibraryID)
		if err != nil {
			return ManualScrapePreview{}, ScrapeOptions{}, nil, err
		}
		mediaOptions := options
		if mapping, exists := req.EpisodeMappings[id]; exists {
			configureManualEpisodeOverride(mapping, &mediaOptions)
		}
		checked, err := s.validateEpisodeMatch(ctx, &m, lib, match, mediaOptions)
		if err != nil {
			r.Error = err.Error()
		} else {
			r.Valid = true
			r.Season = checked.SeasonNum
			r.Episode = checked.EpisodeNum
			r.End = checked.EpisodeEndNum
			r.Part = checked.EpisodePartNum
		}
		out = append(out, r)
	}
	return ManualScrapePreview{Rows: out, TMDb: cloneTMDbSeriesSummary(match.TMDbSeries)}, options, match, nil
}

func cloneTMDbSeriesSummary(summary *TMDbSeriesSummary) *TMDbSeriesSummary {
	if summary == nil || summary.TMDbID <= 0 {
		return nil
	}
	cloned := *summary
	cloned.Seasons = append([]TMDbSeasonSummary(nil), summary.Seasons...)
	return &cloned
}
