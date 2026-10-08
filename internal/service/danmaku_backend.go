package service

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

// 独立后端只收到数据库里已识别的字段，不会反向登录 MSG 或读取媒体文件。
type danmakuHTTPClient struct {
	*resourcePipelineHTTPClient
	repos *repository.Container
}

func newDanmakuHTTPClient(cfg config.DanmakuConfig, repos *repository.Container) (*danmakuHTTPClient, error) {
	if strings.TrimSpace(cfg.URL) == "" || strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("danmaku.url and danmaku.token are required")
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 120
	}
	// 复用已验证的受信 HTTP 传输；地址、凭据、超时均来自独立配置。
	transport, err := newResourcePipelineHTTPClient(config.ResourceImportConfig{
		PipelineURL: cfg.URL, PipelineToken: cfg.Token, SearchTimeoutSeconds: cfg.TimeoutSeconds,
	})
	if err != nil {
		return nil, fmt.Errorf("danmaku server configuration: %w", err)
	}
	return &danmakuHTTPClient{resourcePipelineHTTPClient: transport, repos: repos}, nil
}

type DanmakuTarget struct {
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title,omitempty"`
	TMDBID        string `json:"tmdb_id"`
	Season        int    `json:"season"`
	Episode       int    `json:"episode"`
	Year          int    `json:"year"`
}

func danmakuTargetFromMedia(media model.Media) DanmakuTarget {
	return DanmakuTarget{Title: media.Title, OriginalTitle: media.OriginalName,
		TMDBID: strconv.Itoa(media.TMDbID), Season: media.SeasonNum, Episode: media.EpisodeNum, Year: media.Year}
}

type danmakuMatchEnvelope struct {
	MediaID string              `json:"media_id"`
	Target  map[string]any      `json:"target"`
	Match   *DanmakuMatchResult `json:"match"`
}

func (c *danmakuHTTPClient) MatchDanmaku(ctx context.Context, mediaID string) (DanmakuMatchResult, error) {
	if c.repos == nil {
		return DanmakuMatchResult{}, fmt.Errorf("danmaku media repository is unavailable")
	}
	var media model.Media
	if err := c.repos.DB.WithContext(ctx).Where("id = ?", mediaID).First(&media).Error; err != nil {
		return DanmakuMatchResult{}, err
	}
	var out danmakuMatchEnvelope
	if err := c.doJSON(ctx, "POST", "/v1/danmaku/match", map[string]any{
		"media_id": mediaID,
		"target":   danmakuTargetFromMedia(media),
	}, "", &out); err != nil {
		return DanmakuMatchResult{}, err
	}
	if out.Match == nil || (!out.Match.Matched && len(out.Match.Attempts) == 0) {
		return DanmakuMatchResult{}, fmt.Errorf("danmaku server returned an invalid match response")
	}
	if out.Match.Matched && (out.Match.Provider == "" || out.Match.EpisodeID == "") {
		return DanmakuMatchResult{}, fmt.Errorf("danmaku server returned a match without a source or episode reference")
	}
	if out.Match.Status == "" {
		if out.Match.Matched {
			out.Match.Status = DanmakuStatusMatched
		} else {
			out.Match.Status = DanmakuStatusUnmatched
		}
	}
	return *out.Match, nil
}

func (c *danmakuHTTPClient) FetchDanmaku(ctx context.Context, request DanmakuFetchRequest) (DanmakuPayload, error) {
	var out DanmakuPayload
	err := c.doJSON(ctx, "POST", "/v1/danmaku/comment", map[string]any{
		"media_id":               request.MediaID,
		"episode_id":             request.EpisodeID,
		"source":                 request.Source,
		"ch_convert":             request.ChConvert,
		"offset_seconds":         request.OffsetSeconds,
		"provider_shift_seconds": request.ProviderShiftSeconds,
		"anime_title":            request.AnimeTitle,
		"episode_title":          request.EpisodeTitle,
		"match_mode":             request.MatchMode,
		"with_related":           request.WithRelated,
	}, "", &out)
	return out, err
}

// ParseDanmaku 由独立弹幕服务解析、归一化本地弹幕文件。
func (c *danmakuHTTPClient) ParseDanmaku(ctx context.Context, request DanmakuParseRequest) (DanmakuPayload, error) {
	var out DanmakuPayload
	err := c.doJSON(ctx, "POST", "/v1/danmaku/parse", map[string]any{
		"content":        request.Content,
		"format":         request.Format,
		"offset_seconds": request.OffsetSeconds,
		"ch_convert":     request.ChConvert,
		"title":          request.Title,
	}, "", &out)
	return out, err
}

// StartDanmakuPrewarm 触发独立服务的一次整季串行预热。
func (c *danmakuHTTPClient) StartDanmakuPrewarm(ctx context.Context, request DanmakuPrewarmRequest) (DanmakuPrewarmTask, error) {
	var out DanmakuPrewarmTask
	err := c.doJSON(ctx, "POST", "/v1/danmaku/season/prewarm", map[string]any{
		"owner_id": request.OwnerID,
		"media_id": request.MediaID,
		"season":   request.Season,
		"episodes": request.Episodes,
	}, "", &out)
	return out, err
}

func (c *danmakuHTTPClient) GetDanmakuPrewarm(ctx context.Context, taskID string) (DanmakuPrewarmTask, error) {
	var out DanmakuPrewarmTask
	endpoint := "/v1/danmaku/season/prewarm/" + url.PathEscape(taskID)
	if err := c.doJSON(ctx, "GET", endpoint, nil, "", &out); err != nil {
		return DanmakuPrewarmTask{}, err
	}
	return out, nil
}
