package service

import (
	"context"
	"errors"
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

type subtitlePipelineClient interface {
	SearchSubtitles(context.Context, string, string, int) (SubtitleSearchResponse, error)
	PreviewSubtitle(context.Context, string, string, string, string) (SubtitleCandidatePreview, error)
	ApplySubtitle(context.Context, string, string, string, string) (SubtitleApplyResult, error)
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
