package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/config"
)

func TestSubtitlePipelineHTTPClientUsesAuthenticatedCandidateSessions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["owner_id"] != "admin-id" || body["media_id"] != "media-1" {
			t.Fatalf("unexpected request body: %#v", body)
		}
		switch r.URL.Path {
		case "/v1/subtitles/search":
			_ = json.NewEncoder(w).Encode(SubtitleSearchResponse{
				SessionID: "session-1", MediaID: "media-1",
				Items: []SubtitleSearchCandidate{{CandidateID: "candidate-1", Provider: "subtitlecat"}},
			})
		case "/v1/subtitles/preview":
			if body["search_session_id"] != "session-1" || body["candidate_id"] != "candidate-1" {
				t.Fatalf("unexpected selection: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(SubtitleCandidatePreview{
				SubtitleSearchCandidate: SubtitleSearchCandidate{CandidateID: "candidate-1", Provider: "subtitlecat"},
				MediaID:                 "media-1", ContentSample: "字幕预览",
			})
		case "/v1/subtitles/apply":
			_ = json.NewEncoder(w).Encode(SubtitleApplyResult{
				MediaID: "media-1", Status: "success", Source: "subtitlecat", Filename: "subtitle.srt", Count: 1,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := newResourcePipelineHTTPClient(config.ResourceImportConfig{
		PipelineURL: server.URL, PipelineToken: "secret", SearchTimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	search, err := client.SearchSubtitles(t.Context(), "admin-id", "media-1", 20)
	if err != nil {
		t.Fatal(err)
	}
	if search.SessionID != "session-1" || len(search.Items) != 1 {
		t.Fatalf("unexpected search response: %#v", search)
	}
	preview, err := client.PreviewSubtitle(t.Context(), "admin-id", "media-1", "session-1", "candidate-1")
	if err != nil {
		t.Fatal(err)
	}
	if preview.ContentSample != "字幕预览" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	applied, err := client.ApplySubtitle(t.Context(), "admin-id", "media-1", "session-1", "candidate-1")
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != "success" || applied.Filename != "subtitle.srt" {
		t.Fatalf("unexpected apply response: %#v", applied)
	}
}
