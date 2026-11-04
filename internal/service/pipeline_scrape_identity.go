package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

// Resolve identities only within the already established physical work/season.
// The returned provider ID is still applied through the normal strict preview.
func (s *PipelineScrapeService) confirmedEpisodeIdentityQuery(ctx context.Context, anchor *model.Media, req PipelineScrapeRequest) (string, error) {
	category := normalizePipelineCategory(req.Category)
	if category != "tv" && category != "anime" {
		return "", errors.New("复用剧集身份仅支持电视剧或动漫")
	}
	if req.MediaType != "tv" && req.MediaType != "anime" {
		return "", errors.New("复用剧集身份需要明确的剧集媒体类型")
	}
	ids := pipelineScrapeBatchMediaIDs(anchor.ID, req.MediaIDs, req.EpisodeMappings)
	if len(ids) == 0 || len(req.EpisodeMappings) != len(ids) {
		return "", errors.New("复用剧集身份需要本次媒体及完整季集映射")
	}
	season := req.EpisodeMappings[anchor.ID].SeasonNum
	folder := pipelineScrapeParentPath(anchor.Path)
	if folder == "" || folder == "/" || anchor.LibraryRootID == "" {
		return "", errors.New("复用剧集身份缺少明确正式目录或根目录")
	}
	var targets []model.Media
	if err := s.repos.DB.WithContext(ctx).Where("id IN ? AND library_id = ? AND library_root_id = ?", ids, anchor.LibraryID, anchor.LibraryRootID).Find(&targets).Error; err != nil {
		return "", err
	}
	if len(targets) != len(ids) {
		return "", errors.New("本次刮削媒体不完整或不属于目标根目录")
	}
	for _, row := range targets {
		mapping, ok := req.EpisodeMappings[row.ID]
		if !ok || mapping.SeasonNum != season || row.SeasonNum != season || pipelineScrapeParentPath(row.Path) != folder {
			return "", errors.New("本次刮削媒体不属于同一正式目录和季")
		}
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider != "tmdb" && provider != "bangumi" && provider != "douban" && provider != "thetvdb" {
		return "", errors.New("复用剧集身份需要明确的刮削来源")
	}
	var rows []model.Media
	if err := s.repos.DB.WithContext(ctx).Where("library_id = ? AND library_root_id = ? AND season_num = ? AND scrape_status = ?", anchor.LibraryID, anchor.LibraryRootID, season, "matched").Find(&rows).Error; err != nil {
		return "", err
	}
	confirmed := ""
	for _, row := range rows {
		if pipelineScrapeParentPath(row.Path) != folder {
			continue
		}
		id := pipelineEpisodeProviderID(row, provider)
		if id == "" {
			continue
		}
		if confirmed != "" && confirmed != id {
			return "", fmt.Errorf("正式目录的 %s 剧集身份冲突，请确认刮削条目", provider)
		}
		confirmed = id
	}
	if confirmed == "" {
		return "", nil
	}
	for _, row := range targets {
		if hint, ok := parseProviderIDString(row.Path, provider); ok && hint != confirmed {
			return "", errors.New("路径与已确认剧集身份冲突，请确认刮削条目")
		}
	}
	return confirmed, nil
}

func pipelineEpisodeProviderID(row model.Media, provider string) string {
	switch provider {
	case "tmdb":
		if row.TMDbID > 0 {
			return strconv.Itoa(row.TMDbID)
		}
	case "bangumi":
		if row.BangumiID > 0 {
			return strconv.Itoa(row.BangumiID)
		}
	case "douban":
		return strings.TrimSpace(row.DoubanID)
	case "thetvdb":
		return strings.TrimSpace(row.TheTVDBID)
	}
	return ""
}

func (s *PipelineScrapeService) verifyConfirmedEpisodeMetadata(ctx context.Context, anchor *model.Media, req PipelineScrapeRequest, query string) (PipelineScrapeResult, error) {
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	id, ok := parseProviderIDString(query, provider)
	if !ok {
		return PipelineScrapeResult{}, errors.New("没有已确认的剧集身份可供核验")
	}
	ids := pipelineScrapeBatchMediaIDs(anchor.ID, req.MediaIDs, req.EpisodeMappings)
	var rows []model.Media
	if err := s.repos.DB.WithContext(ctx).Where("id IN ? AND library_id = ? AND library_root_id = ?", ids, anchor.LibraryID, anchor.LibraryRootID).Find(&rows).Error; err != nil {
		return PipelineScrapeResult{}, err
	}
	if len(rows) != len(ids) {
		return PipelineScrapeResult{}, errors.New("刮削核验媒体不完整")
	}
	for _, row := range rows {
		mapping := req.EpisodeMappings[row.ID]
		if pipelineScrapeParentPath(row.Path) != pipelineScrapeParentPath(anchor.Path) || row.ScrapeStatus != "matched" || pipelineEpisodeProviderID(row, provider) != id || row.SeasonNum != mapping.SeasonNum || row.EpisodeNum != mapping.EpisodeNum || row.EpisodeEndNum != mapping.EpisodeEndNum || row.EpisodePartNum != mapping.EpisodePartNum || (provider == "tmdb" && strings.TrimSpace(row.EpisodeTitle) == "") {
			return PipelineScrapeResult{}, errors.New("本次媒体的作品身份或季集元数据尚未完整匹配")
		}
	}
	return PipelineScrapeResult{Mode: "verified", Query: query, MediaID: anchor.ID,
		MediaTitle: pipelineMediaDisplayTitle(*anchor), VerifiedCount: len(rows), ScrapeStatus: "matched"}, nil
}

func (s *PipelineScrapeService) confirmedEpisodeMatch(ctx context.Context, query, provider string) (ExternalMediaResult, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	id, ok := parseProviderIDString(query, provider)
	if !ok {
		return ExternalMediaResult{}, errors.New("已确认剧集身份无效")
	}
	var match *Match
	var err error
	mediaType := "tv"
	switch provider {
	case "tmdb":
		if s.scraper.tmdb == nil || !s.scraper.tmdb.Enabled() {
			return ExternalMediaResult{}, errors.New("TMDB 刮削来源未启用")
		}
		match, err = s.scraper.tmdb.GetTVMatch(ctx, mustAtoi(id))
	case "bangumi":
		if s.scraper.bangumi == nil || !s.scraper.bangumi.Enabled() {
			return ExternalMediaResult{}, errors.New("Bangumi 刮削来源未启用")
		}
		match, err = s.scraper.bangumi.GetSubject(ctx, mustAtoi(id))
		mediaType = "anime"
	case "douban":
		if s.scraper.douban == nil || !s.scraper.douban.Enabled() {
			return ExternalMediaResult{}, errors.New("豆瓣刮削来源未启用")
		}
		match, err = s.scraper.douban.GetMatchByID(ctx, id)
	case "thetvdb":
		if s.scraper.thetvdb == nil || !s.scraper.thetvdb.Enabled() {
			return ExternalMediaResult{}, errors.New("TVDB 刮削来源未启用")
		}
		match, err = s.scraper.thetvdb.GetSeriesMatchByID(ctx, id)
	default:
		return ExternalMediaResult{}, errors.New("不支持的剧集身份来源")
	}
	if err != nil {
		return ExternalMediaResult{}, fmt.Errorf("读取已确认的 %s 剧集身份失败: %w", provider, err)
	}
	if match == nil || strings.TrimSpace(match.Title) == "" {
		return ExternalMediaResult{}, errors.New("已确认剧集身份未返回有效元数据")
	}
	return externalMediaResultFromMatch(provider, mediaType, match), nil
}
