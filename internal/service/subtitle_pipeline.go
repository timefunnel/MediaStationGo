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
	Filename            string  `json:"filename"`
	Source              string  `json:"source"`
	Language            string  `json:"language"`
	SegmentCount        int     `json:"segment_count"`
	Duration            float64 `json:"duration"`
	TranslationProvider string  `json:"translation_provider,omitempty"`
	TranslationModel    string  `json:"translation_model,omitempty"`
}

type SubtitleASRProfile struct {
	Provider      string `json:"provider"`
	ProviderLabel string `json:"provider_label"`
	Model         string `json:"model"`
	Local         bool   `json:"local"`
}

type SubtitleASRProfileList struct {
	Items []SubtitleASRProfile `json:"items"`
}

type SubtitleASRTask struct {
	ID                  string             `json:"id"`
	OwnerID             string             `json:"owner_id"`
	MediaID             string             `json:"media_id"`
	SourceLanguage      string             `json:"source_language"`
	TranslationProvider string             `json:"translation_provider"`
	TranslationModel    string             `json:"translation_model"`
	Status              string             `json:"status"`
	Stage               string             `json:"stage"`
	ProgressCurrent     int                `json:"progress_current"`
	ProgressTotal       int                `json:"progress_total"`
	Result              *SubtitleASRResult `json:"result"`
	Error               string             `json:"error"`
	CreatedAt           int64              `json:"created_at"`
	UpdatedAt           int64              `json:"updated_at"`
	StartedAt           int64              `json:"started_at"`
	CompletedAt         int64              `json:"completed_at"`
	AttemptCount        int                `json:"attempt_count"`
	CachedAudio         bool               `json:"cached_audio"`
	CachedTranscript    bool               `json:"cached_transcript"`
	MediaTitle          string             `json:"media_title,omitempty"`
	MediaFilename       string             `json:"media_filename,omitempty"`
	MediaAvailable      bool               `json:"media_available"`
}

type SubtitleASRTaskList struct {
	Items []SubtitleASRTask `json:"items"`
}

type subtitlePipelineClient interface {
	SearchSubtitles(context.Context, string, string, int) (SubtitleSearchResponse, error)
	PreviewSubtitle(context.Context, string, string, string, string) (SubtitleCandidatePreview, error)
	ApplySubtitle(context.Context, string, string, string, string) (SubtitleApplyResult, error)
	CreateSubtitleASR(context.Context, string, string, string, string, string) (SubtitleASRTask, error)
	GetSubtitleASR(context.Context, string, string) (SubtitleASRTask, error)
	ListSubtitleASR(context.Context, int) ([]SubtitleASRTask, error)
	ListSubtitleASRModels(context.Context) ([]string, error)
	RetrySubtitleASR(context.Context, string, string, string, string) (SubtitleASRTask, error)
	DeleteSubtitleASR(context.Context, string, string) error
}

func (c *resourcePipelineHTTPClient) CreateSubtitleASR(ctx context.Context, ownerID, mediaID, sourceLanguage, provider, model string) (SubtitleASRTask, error) {
	var out SubtitleASRTask
	err := c.doJSON(ctx, "POST", "/v1/subtitles/asr", map[string]any{
		"owner_id": ownerID, "media_id": mediaID, "source_language": sourceLanguage,
		"translation_provider": provider, "translation_model": model,
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

func (c *resourcePipelineHTTPClient) ListSubtitleASRModels(ctx context.Context) ([]string, error) {
	var out struct {
		Models []string `json:"models"`
	}
	err := c.doJSON(ctx, "GET", "/v1/subtitles/asr/models", nil, "", &out)
	if out.Models == nil {
		out.Models = []string{}
	}
	return out.Models, err
}

func (c *resourcePipelineHTTPClient) RetrySubtitleASR(ctx context.Context, ownerID, taskID, provider, model string) (SubtitleASRTask, error) {
	var out SubtitleASRTask
	endpoint := "/v1/subtitles/asr/" + url.PathEscape(taskID) + "/retry"
	err := c.doJSON(ctx, "POST", endpoint, map[string]any{
		"owner_id": ownerID, "translation_provider": provider, "translation_model": model,
	}, "", &out)
	return out, err
}

func (c *resourcePipelineHTTPClient) DeleteSubtitleASR(ctx context.Context, ownerID, taskID string) error {
	endpoint := "/v1/subtitles/asr/" + url.PathEscape(taskID) + "?owner_id=" + url.QueryEscape(ownerID)
	var out map[string]any
	return c.doJSON(ctx, "DELETE", endpoint, nil, "", &out)
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

func (s *SubtitleService) CreateASRTask(ctx context.Context, ownerID, mediaID, sourceLanguage, provider, translationModel string) (SubtitleASRTask, error) {
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
	profile, err := s.resolveASRProfile(ctx, provider, translationModel)
	if err != nil {
		return SubtitleASRTask{}, err
	}
	return s.pipeline.CreateSubtitleASR(ctx, ownerID, mediaID, sourceLanguage, profile.Provider, profile.Model)
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

func (s *SubtitleService) ListASRProfiles(ctx context.Context) ([]SubtitleASRProfile, error) {
	if s == nil || s.pipeline == nil {
		return nil, errors.New("subtitle pipeline unavailable")
	}
	models, err := s.pipeline.ListSubtitleASRModels(ctx)
	if err != nil {
		return nil, err
	}
	profiles := make([]SubtitleASRProfile, 0, len(models)+3)
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model != "" {
			profiles = append(profiles, SubtitleASRProfile{
				Provider: "local", ProviderLabel: "本机 Ollama", Model: model, Local: true,
			})
		}
	}
	if s.apiConfig == nil {
		return profiles, nil
	}
	for _, provider := range []string{"openai", "deepseek", "siliconflow"} {
		resolved, resolveErr := s.apiConfig.Resolve(ctx, provider)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if !resolved.Enabled || strings.TrimSpace(resolved.APIKey) == "" || strings.TrimSpace(resolved.BaseURL) == "" || strings.TrimSpace(resolved.Model) == "" {
			continue
		}
		profiles = append(profiles, SubtitleASRProfile{
			Provider: provider, ProviderLabel: subtitleASRProviderLabel(provider), Model: strings.TrimSpace(resolved.Model),
		})
	}
	return profiles, nil
}

func (s *SubtitleService) RetryASRTask(ctx context.Context, ownerID, taskID, provider, translationModel string) (SubtitleASRTask, error) {
	if s == nil || s.pipeline == nil {
		return SubtitleASRTask{}, errors.New("subtitle pipeline unavailable")
	}
	profile, err := s.resolveASRProfile(ctx, provider, translationModel)
	if err != nil {
		return SubtitleASRTask{}, err
	}
	return s.pipeline.RetrySubtitleASR(
		ctx, strings.TrimSpace(ownerID), strings.TrimSpace(taskID), profile.Provider, profile.Model,
	)
}

func (s *SubtitleService) DeleteASRTask(ctx context.Context, ownerID, taskID string) error {
	if s == nil || s.pipeline == nil {
		return errors.New("subtitle pipeline unavailable")
	}
	return s.pipeline.DeleteSubtitleASR(ctx, strings.TrimSpace(ownerID), strings.TrimSpace(taskID))
}

func (s *SubtitleService) resolveASRProfile(ctx context.Context, provider, translationModel string) (SubtitleASRProfile, error) {
	profiles, err := s.ListASRProfiles(ctx)
	if err != nil {
		return SubtitleASRProfile{}, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	translationModel = strings.TrimSpace(translationModel)
	if provider == "" && translationModel == "" && len(profiles) > 0 {
		return profiles[0], nil
	}
	for _, profile := range profiles {
		if profile.Provider == provider && profile.Model == translationModel {
			return profile, nil
		}
	}
	return SubtitleASRProfile{}, errors.New("selected AI subtitle translation model is not available")
}

func subtitleASRProviderLabel(provider string) string {
	switch provider {
	case "openai":
		return "OpenAI"
	case "deepseek":
		return "DeepSeek"
	case "siliconflow":
		return "硅基流动"
	default:
		return provider
	}
}

func validSubtitleASRLanguage(value string) bool {
	switch value {
	case "auto", "ja", "en", "zh", "ko":
		return true
	default:
		return false
	}
}
