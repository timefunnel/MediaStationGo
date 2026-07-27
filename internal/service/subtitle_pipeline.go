package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

type SubtitleSearchCandidate struct {
	CandidateID string `json:"candidate_id"`
	Provider    string `json:"provider"`
	Title       string `json:"title"`
	Filename    string `json:"filename"`
	Language    string `json:"language"`
	SourceScore int    `json:"source_score"`
	Rank        int    `json:"rank"`
}

type SubtitleSearchResponse struct {
	SessionID string                    `json:"session_id"`
	ExpiresAt int64                     `json:"expires_at"`
	MediaID   string                    `json:"media_id"`
	Title     string                    `json:"title"`
	Category  string                    `json:"category"`
	Query     string                    `json:"query"`
	Items     []SubtitleSearchCandidate `json:"items"`
}

type SubtitleCandidatePreview struct {
	SubtitleSearchCandidate
	MediaID       string `json:"media_id"`
	ContentSample string `json:"content_sample"`
	PreviewChars  int    `json:"preview_char_count"`
	PreviewLines  int    `json:"preview_line_count"`
}

type SubtitleApplyResult struct {
	MediaID  string `json:"media_id"`
	Status   string `json:"status"`
	Source   string `json:"source"`
	Filename string `json:"filename"`
	Count    int    `json:"count"`
	Reason   string `json:"reason"`
}

type SubtitleASRResult struct {
	Filename     string  `json:"filename"`
	Source       string  `json:"source"`
	Language     string  `json:"language"`
	SegmentCount int     `json:"segment_count"`
	Duration     float64 `json:"duration"`
}

type SubtitleASRTask struct {
	ID              string             `json:"id"`
	OwnerID         string             `json:"owner_id"`
	MediaID         string             `json:"media_id"`
	SourceLanguage  string             `json:"source_language"`
	Status          string             `json:"status"`
	Stage           string             `json:"stage"`
	ProgressCurrent int                `json:"progress_current"`
	ProgressTotal   int                `json:"progress_total"`
	Result          *SubtitleASRResult `json:"result"`
	Error           string             `json:"error"`
	CreatedAt       int64              `json:"created_at"`
	UpdatedAt       int64              `json:"updated_at"`
	StartedAt       int64              `json:"started_at"`
	CompletedAt     int64              `json:"completed_at"`
	AttemptCount    int                `json:"attempt_count"`
	MediaTitle      string             `json:"media_title,omitempty"`
	MediaFilename   string             `json:"media_filename,omitempty"`
	MediaAvailable  bool               `json:"media_available"`
}

type SubtitleASRTaskList struct {
	Items []SubtitleASRTask `json:"items"`
}

type subtitlePipelineClient interface {
	SearchSubtitles(context.Context, string, string, int) (SubtitleSearchResponse, error)
	PreviewSubtitle(context.Context, string, string, string, string) (SubtitleCandidatePreview, error)
	ApplySubtitle(context.Context, string, string, string, string) (SubtitleApplyResult, error)
	CreateSubtitleASR(context.Context, string, string, string) (SubtitleASRTask, error)
	GetSubtitleASR(context.Context, string, string) (SubtitleASRTask, error)
	ListSubtitleASR(context.Context, int) ([]SubtitleASRTask, error)
}

func (c *resourcePipelineHTTPClient) CreateSubtitleASR(ctx context.Context, ownerID, mediaID, sourceLanguage string) (SubtitleASRTask, error) {
	var out SubtitleASRTask
	err := c.doJSON(ctx, "POST", "/v1/subtitles/asr", map[string]any{
		"owner_id": ownerID, "media_id": mediaID, "source_language": sourceLanguage,
	}, "", &out)
	return out, err
}

func (c *resourcePipelineHTTPClient) GetSubtitleASR(ctx context.Context, ownerID, taskID string) (SubtitleASRTask, error) {
	var out SubtitleASRTask
	endpoint := "/v1/subtitles/asr/" + url.PathEscape(taskID) + "?owner_id=" + url.QueryEscape(ownerID)
	err := c.doJSON(ctx, "GET", endpoint, nil, "", &out)
	return out, err
}

func (c *resourcePipelineHTTPClient) ListSubtitleASR(ctx context.Context, limit int) ([]SubtitleASRTask, error) {
	var out SubtitleASRTaskList
	endpoint := "/v1/subtitles/asr?limit=" + url.QueryEscape(fmt.Sprintf("%d", limit))
	err := c.doJSON(ctx, "GET", endpoint, nil, "", &out)
	if out.Items == nil {
		out.Items = []SubtitleASRTask{}
	}
	return out.Items, err
}

type subtitlePipelineSelectionRequest struct {
	OwnerID         string `json:"owner_id"`
	MediaID         string `json:"media_id"`
	SearchSessionID string `json:"search_session_id"`
	CandidateID     string `json:"candidate_id"`
}

func (c *resourcePipelineHTTPClient) SearchSubtitles(ctx context.Context, ownerID, mediaID string, limit int) (SubtitleSearchResponse, error) {
	var out SubtitleSearchResponse
	err := c.doJSON(ctx, "POST", "/v1/subtitles/search", map[string]any{
		"owner_id": ownerID,
		"media_id": mediaID,
		"limit":    limit,
	}, "", &out)
	return out, err
}

func (c *resourcePipelineHTTPClient) PreviewSubtitle(ctx context.Context, ownerID, mediaID, sessionID, candidateID string) (SubtitleCandidatePreview, error) {
	var out SubtitleCandidatePreview
	err := c.doJSON(ctx, "POST", "/v1/subtitles/preview", subtitlePipelineSelectionRequest{
		OwnerID: ownerID, MediaID: mediaID, SearchSessionID: sessionID, CandidateID: candidateID,
	}, "", &out)
	return out, err
}

func (c *resourcePipelineHTTPClient) ApplySubtitle(ctx context.Context, ownerID, mediaID, sessionID, candidateID string) (SubtitleApplyResult, error) {
	var out SubtitleApplyResult
	err := c.doJSON(ctx, "POST", "/v1/subtitles/apply", subtitlePipelineSelectionRequest{
		OwnerID: ownerID, MediaID: mediaID, SearchSessionID: sessionID, CandidateID: candidateID,
	}, "", &out)
	return out, err
}

func (s *SubtitleService) SetPipelineClient(client subtitlePipelineClient) {
	if s != nil {
		s.pipeline = client
	}
}

func (s *SubtitleService) SearchCandidates(ctx context.Context, ownerID, mediaID string, limit int) (SubtitleSearchResponse, error) {
	if s == nil || s.pipeline == nil {
		return SubtitleSearchResponse{}, errors.New("subtitle pipeline unavailable")
	}
	ownerID = strings.TrimSpace(ownerID)
	mediaID = strings.TrimSpace(mediaID)
	if ownerID == "" || mediaID == "" {
		return SubtitleSearchResponse{}, errors.New("subtitle owner and media are required")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	return s.pipeline.SearchSubtitles(ctx, ownerID, mediaID, limit)
}

func (s *SubtitleService) PreviewCandidate(ctx context.Context, ownerID, mediaID, sessionID, candidateID string) (SubtitleCandidatePreview, error) {
	if s == nil || s.pipeline == nil {
		return SubtitleCandidatePreview{}, errors.New("subtitle pipeline unavailable")
	}
	return s.pipeline.PreviewSubtitle(ctx, strings.TrimSpace(ownerID), strings.TrimSpace(mediaID), strings.TrimSpace(sessionID), strings.TrimSpace(candidateID))
}

func (s *SubtitleService) ApplyCandidate(ctx context.Context, ownerID, mediaID, sessionID, candidateID string) (SubtitleApplyResult, error) {
	if s == nil || s.pipeline == nil {
		return SubtitleApplyResult{}, errors.New("subtitle pipeline unavailable")
	}
	result, err := s.pipeline.ApplySubtitle(ctx, strings.TrimSpace(ownerID), strings.TrimSpace(mediaID), strings.TrimSpace(sessionID), strings.TrimSpace(candidateID))
	if err != nil {
		return SubtitleApplyResult{}, err
	}
	if result.Status != "success" {
		return SubtitleApplyResult{}, errors.New("subtitle pipeline did not save the selected subtitle")
	}
	return result, nil
}

func (s *SubtitleService) CreateASRTask(ctx context.Context, ownerID, mediaID, sourceLanguage string) (SubtitleASRTask, error) {
	if s == nil || s.pipeline == nil {
		return SubtitleASRTask{}, errors.New("subtitle pipeline unavailable")
	}
	ownerID = strings.TrimSpace(ownerID)
	mediaID = strings.TrimSpace(mediaID)
	sourceLanguage = strings.ToLower(strings.TrimSpace(sourceLanguage))
	if sourceLanguage == "" {
		sourceLanguage = "auto"
	}
	if ownerID == "" || mediaID == "" {
		return SubtitleASRTask{}, errors.New("subtitle owner and media are required")
	}
	if !validSubtitleASRLanguage(sourceLanguage) {
		return SubtitleASRTask{}, errors.New("source_language must be one of: auto, ja, en, zh, ko")
	}
	return s.pipeline.CreateSubtitleASR(ctx, ownerID, mediaID, sourceLanguage)
}

func (s *SubtitleService) GetASRTask(ctx context.Context, ownerID, taskID string) (SubtitleASRTask, error) {
	if s == nil || s.pipeline == nil {
		return SubtitleASRTask{}, errors.New("subtitle pipeline unavailable")
	}
	ownerID = strings.TrimSpace(ownerID)
	taskID = strings.TrimSpace(taskID)
	if ownerID == "" || taskID == "" {
		return SubtitleASRTask{}, errors.New("subtitle owner and task are required")
	}
	return s.pipeline.GetSubtitleASR(ctx, ownerID, taskID)
}

func (s *SubtitleService) ListASRTasks(ctx context.Context, limit int) ([]SubtitleASRTask, error) {
	if s == nil || s.pipeline == nil {
		return nil, errors.New("subtitle pipeline unavailable")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	return s.pipeline.ListSubtitleASR(ctx, limit)
}

func validSubtitleASRLanguage(value string) bool {
	switch value {
	case "auto", "ja", "en", "zh", "ko":
		return true
	default:
		return false
	}
}
