package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
)

func TestCleanMediaTitlesValidatesStructuredResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if stream, ok := request["stream"].(bool); !ok || stream {
			t.Fatalf("stream = %#v, want false", request["stream"])
		}
		if temperature, ok := request["temperature"].(float64); !ok || temperature != 0 {
			t.Fatalf("temperature = %#v, want 0", request["temperature"])
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"content": "```json\n{\"items\":[{\"media_id\":\"a\",\"title\":\"作品 A\",\"relation\":\"version\",\"group_key\":\"work-a\",\"year\":2025,\"confidence\":0.96},{\"media_id\":\"b\",\"title\":\"作品 A\",\"relation\":\"version\",\"group_key\":\"work-a\",\"year\":2025,\"confidence\":0.91}]}\n```",
		}}}})
		_, _ = w.Write(body)
	}))
	defer server.Close()

	ai := NewAIService(&config.Config{AI: config.AIConfig{
		Enabled: true,
		APIBase: server.URL,
		APIKey:  "test-key",
		Model:   "test-model",
	}}, zap.NewNop(), nil)
	groups := []MediaTitleCleanupGroup{{
		SourceDirectory: "作品 A",
		Items: []MediaTitleCleanupSource{
			{MediaID: "a", CurrentTitle: "A.1080p", SourceDirectory: "作品 A", Filename: "A.1080p.mkv"},
			{MediaID: "b", CurrentTitle: "A.2160p", SourceDirectory: "作品 A", Filename: "A.2160p.mkv"},
		},
	}}
	items, err := ai.CleanMediaTitles(t.Context(), groups)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Title != "作品 A" || items[1].Relation != MediaTitleRelationVersion {
		t.Fatalf("suggestions = %#v", items)
	}
	if items[0].Filename != "A.1080p.mkv" || items[1].SourceDirectory != "作品 A" {
		t.Fatalf("source context missing: %#v", items)
	}
}

func TestValidateMediaTitleCleanupSuggestionsRejectsAccidentalMerge(t *testing.T) {
	groups := []MediaTitleCleanupGroup{{Items: []MediaTitleCleanupSource{{MediaID: "a"}, {MediaID: "b"}}}}
	_, err := validateMediaTitleCleanupSuggestions(groups, []MediaTitleCleanupSuggestion{
		{MediaID: "a", Title: "同名", Relation: MediaTitleRelationStandalone, Confidence: 0.7},
		{MediaID: "b", Title: "同名", Relation: MediaTitleRelationPart, Confidence: 0.7},
	})
	if err == nil {
		t.Fatal("duplicate non-version titles should be rejected")
	}
}

func TestCleanMediaTitlesRunsDirectoryGroupsConcurrently(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Groups []MediaTitleCleanupGroup `json:"groups"`
		}
		if len(request.Messages) < 2 || json.Unmarshal([]byte(request.Messages[1].Content), &payload) != nil || len(payload.Groups) != 1 {
			t.Fatalf("unexpected request: %#v", request)
		}
		current := active.Add(1)
		for current > maxActive.Load() && !maxActive.CompareAndSwap(maxActive.Load(), current) {
		}
		time.Sleep(80 * time.Millisecond)
		active.Add(-1)
		id := payload.Groups[0].Items[0].MediaID
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"content": `{"items":[{"media_id":"` + id + `","title":"作品 ` + id + `","relation":"standalone","confidence":0.9}]}`,
		}}}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	ai := NewAIService(&config.Config{AI: config.AIConfig{
		Enabled: true, APIBase: server.URL, APIKey: "test", Model: "test",
	}}, zap.NewNop(), nil)
	groups := make([]MediaTitleCleanupGroup, 3)
	for i, id := range []string{"a", "b", "c"} {
		groups[i] = MediaTitleCleanupGroup{SourceDirectory: id, Items: []MediaTitleCleanupSource{{MediaID: id, Filename: id + ".mp4"}}}
	}
	progress := 0
	items, err := ai.CleanMediaTitlesWithProgress(t.Context(), groups, func(done, total int) {
		if total != 3 || done <= progress {
			t.Fatalf("progress = %d/%d after %d", done, total, progress)
		}
		progress = done
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || progress != 3 || maxActive.Load() < 2 {
		t.Fatalf("items=%d progress=%d max_active=%d", len(items), progress, maxActive.Load())
	}
}
