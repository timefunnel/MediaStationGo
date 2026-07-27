package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type SubtitleTranslationSegment struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type SubtitleTranslationResult struct {
	Translations []SubtitleTranslationSegment `json:"translations"`
}

func (s *SubtitleService) TranslateSegments(ctx context.Context, provider, selectedModel string, segments []SubtitleTranslationSegment) (SubtitleTranslationResult, error) {
	if s == nil || s.apiConfig == nil {
		return SubtitleTranslationResult{}, errors.New("AI translation configuration unavailable")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider != "openai" && provider != "deepseek" && provider != "siliconflow" {
		return SubtitleTranslationResult{}, errors.New("unsupported cloud translation provider")
	}
	selectedModel = strings.TrimSpace(selectedModel)
	resolved, err := s.apiConfig.Resolve(ctx, provider)
	if err != nil {
		return SubtitleTranslationResult{}, err
	}
	if !resolved.Enabled || strings.TrimSpace(resolved.APIKey) == "" || strings.TrimSpace(resolved.BaseURL) == "" {
		return SubtitleTranslationResult{}, errors.New("cloud translation provider is not configured or enabled")
	}
	if selectedModel == "" || selectedModel != strings.TrimSpace(resolved.Model) {
		return SubtitleTranslationResult{}, errors.New("selected cloud translation model does not match the configured model")
	}
	validated, err := validateSubtitleTranslationSegments(segments)
	if err != nil {
		return SubtitleTranslationResult{}, err
	}
	segmentsJSON, err := json.Marshal(map[string]any{"segments": validated})
	if err != nil {
		return SubtitleTranslationResult{}, err
	}
	payload := map[string]any{
		"model":           selectedModel,
		"max_tokens":      4096,
		"stream":          false,
		"response_format": map[string]string{"type": "json_object"},
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "Translate spoken subtitle dialogue into natural Simplified Chinese (zh-CN). Return strict JSON only with schema {\"translations\":[{\"id\":0,\"text\":\"translated text\"}]}. Keep every input id exactly once and in the same order. Do not add, omit, merge, or split segments. Preserve names, tone, and meaning. Translation text must not be empty.",
			},
			{
				"role":    "user",
				"content": string(segmentsJSON),
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return SubtitleTranslationResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, aiChatCompletionsEndpoint(resolved.BaseURL), bytes.NewReader(body))
	if err != nil {
		return SubtitleTranslationResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+resolved.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := NewExternalHTTPClient(120 * time.Second).Do(req)
	if err != nil {
		return SubtitleTranslationResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return SubtitleTranslationResult{}, fmt.Errorf("cloud translation failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var upstream struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 32*1024*1024))
	if err := decoder.Decode(&upstream); err != nil {
		return SubtitleTranslationResult{}, fmt.Errorf("cloud translation response is invalid: %w", err)
	}
	if len(upstream.Choices) == 0 || strings.TrimSpace(upstream.Choices[0].Message.Content) == "" {
		return SubtitleTranslationResult{}, errors.New("cloud translation returned empty content")
	}
	var result SubtitleTranslationResult
	if err := json.Unmarshal([]byte(upstream.Choices[0].Message.Content), &result); err != nil {
		return SubtitleTranslationResult{}, fmt.Errorf("cloud translation returned invalid JSON: %w", err)
	}
	if err := validateSubtitleTranslationResult(validated, result.Translations); err != nil {
		return SubtitleTranslationResult{}, err
	}
	return result, nil
}

func validateSubtitleTranslationSegments(segments []SubtitleTranslationSegment) ([]SubtitleTranslationSegment, error) {
	if len(segments) == 0 || len(segments) > 25 {
		return nil, errors.New("translation segments must contain between 1 and 25 items")
	}
	totalChars := 0
	validated := make([]SubtitleTranslationSegment, len(segments))
	for i, segment := range segments {
		segment.Text = strings.TrimSpace(segment.Text)
		if segment.ID < 0 || segment.Text == "" {
			return nil, errors.New("translation segment contains an invalid id or empty text")
		}
		totalChars += len([]rune(segment.Text))
		validated[i] = segment
	}
	if totalChars > 5000 {
		return nil, errors.New("translation segment text exceeds the request limit")
	}
	return validated, nil
}

func validateSubtitleTranslationResult(input, translated []SubtitleTranslationSegment) error {
	if len(input) != len(translated) {
		return errors.New("cloud translation response is missing translations")
	}
	seen := make(map[int]struct{}, len(translated))
	for i := range translated {
		translated[i].Text = strings.TrimSpace(translated[i].Text)
		if translated[i].ID != input[i].ID || translated[i].Text == "" {
			return errors.New("cloud translation segment IDs do not match the requested batch")
		}
		if _, exists := seen[translated[i].ID]; exists {
			return errors.New("cloud translation returned duplicate segments")
		}
		seen[translated[i].ID] = struct{}{}
	}
	return nil
}

func aiChatCompletionsEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
	return strings.TrimRight(baseURL, "/") + "/chat/completions"
}
