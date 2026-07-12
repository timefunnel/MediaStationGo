package service

import (
	"context"
	"errors"
	"strings"

	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"go.uber.org/zap"
)

const (
	PipelineScrapeModeApply = "apply"
	PipelineScrapeModeSmart = "smart"
)

type PipelineScrapeService struct {
	log       *zap.Logger
	repos     *repository.Container
	scraper   *ScraperService
	organizer *OrganizerService
}

func NewPipelineScrapeService(log *zap.Logger, repos *repository.Container, scraper *ScraperService, organizer *OrganizerService) *PipelineScrapeService {
	return &PipelineScrapeService{log: log, repos: repos, scraper: scraper, organizer: organizer}
}

type PipelineScrapeRequest struct {
	Category  string   `json:"category,omitempty"`
	Title     string   `json:"title,omitempty"`
	Queries   []string `json:"queries,omitempty"`
	Provider  string   `json:"provider,omitempty"`
	MediaType string   `json:"media_type,omitempty"`
}

type PipelineScrapeResult struct {
	Mode         string               `json:"mode"`
	Query        string               `json:"query,omitempty"`
	MatchCount   int                  `json:"match_count,omitempty"`
	Match        *ExternalMediaResult `json:"match,omitempty"`
	MediaID      string               `json:"media_id"`
	MediaTitle   string               `json:"media_title,omitempty"`
	Reclassified int                  `json:"reclassified,omitempty"`
}

func (s *PipelineScrapeService) Scrape(ctx context.Context, mediaID string, req PipelineScrapeRequest) (PipelineScrapeResult, error) {
	if s == nil || s.repos == nil || s.repos.Media == nil || s.scraper == nil {
		return PipelineScrapeResult{}, errors.New("pipeline scrape service unavailable")
	}
	mediaID = strings.TrimSpace(mediaID)
	if mediaID == "" {
		return PipelineScrapeResult{}, errors.New("media id is required")
	}
	media, err := s.repos.Media.FindByID(ctx, mediaID)
	if err != nil || media == nil {
		return PipelineScrapeResult{}, errors.New("media not found")
	}
	queries := pipelineCompactStrings(req.Queries)
	if len(queries) == 0 && strings.TrimSpace(req.Title) != "" {
		queries = []string{strings.TrimSpace(req.Title)}
	}
	provider := strings.TrimSpace(req.Provider)
	mediaType := strings.TrimSpace(req.MediaType)
	options := pipelineScrapeOptions()

	if provider != "" && mediaType != "" {
		for _, query := range queries {
			matches, err := s.scraper.ManualSearch(ctx, media, query, provider, mediaType)
			if err != nil {
				return PipelineScrapeResult{}, err
			}
			if len(matches) != 1 {
				continue
			}
			match := matches[0]
			manualReq := pipelineManualScrapeRequestFromMatch(match)
			refreshed, err := s.scraper.ApplyManualMatchWithOptions(ctx, mediaID, manualReq, options)
			if err != nil {
				return PipelineScrapeResult{}, err
			}
			result := PipelineScrapeResult{
				Mode:       PipelineScrapeModeApply,
				Query:      query,
				MatchCount: len(matches),
				Match:      &match,
				MediaID:    mediaID,
			}
			if refreshed != nil {
				result.MediaTitle = pipelineMediaDisplayTitle(*refreshed)
			}
			result.Reclassified = s.reclassify(ctx, mediaID, manualReq.MediaType)
			return result, nil
		}
	}

	if err := s.scraper.EnrichOneWithOptions(ctx, media, options); err != nil {
		return PipelineScrapeResult{}, err
	}
	refreshed, _ := s.repos.Media.FindByID(ctx, mediaID)
	result := PipelineScrapeResult{Mode: PipelineScrapeModeSmart, MediaID: mediaID}
	if refreshed != nil {
		result.MediaTitle = pipelineMediaDisplayTitle(*refreshed)
	}
	result.Reclassified = s.reclassify(ctx, mediaID, "")
	return result, nil
}

func pipelineScrapeOptions() ScrapeOptions {
	episodeArtwork := false
	return ScrapeOptions{RetryNoMatch: true, IncludeMatched: true, EpisodeArtwork: &episodeArtwork}
}

func pipelineManualScrapeRequestFromMatch(match ExternalMediaResult) ManualScrapeRequest {
	episodeArtwork := false
	return ManualScrapeRequest{
		Source:        match.Source,
		MediaType:     match.MediaType,
		Title:         match.Title,
		OriginalName:  match.OriginalName,
		Overview:      match.Overview,
		PosterURL:     match.PosterURL,
		BackdropURL:   match.BackdropURL,
		Year:          match.Year,
		ReleaseDate:   match.ReleaseDate,
		Rating:        match.Rating,
		TMDbID:        match.TMDbID,
		BangumiID:     match.BangumiID,
		DoubanID:      match.DoubanID,
		TheTVDBID:     match.TheTVDBID,
		Languages:     append([]string(nil), match.Languages...),
		Countries:     append([]string(nil), match.Countries...),
		Genres:        append([]string(nil), match.Genres...),
		NSFW:          match.NSFW,
		EpisodeImages: &episodeArtwork,
	}
}

func (s *PipelineScrapeService) reclassify(ctx context.Context, mediaID, mediaType string) int {
	if s == nil || s.organizer == nil {
		return 0
	}
	options := MediaCategoryReclassifyOptions{MediaIDs: []string{mediaID}}
	if strings.TrimSpace(mediaType) != "" {
		options.MediaTypeHints = map[string]string{mediaID: strings.TrimSpace(mediaType)}
	}
	result, err := s.organizer.ReclassifyMisclassifiedMedia(ctx, options)
	if err != nil {
		if s.log != nil {
			s.log.Warn("pipeline scrape reclassify media failed", zap.String("media_id", mediaID), zap.Error(err))
		}
		return 0
	}
	return result.Reclassified
}
