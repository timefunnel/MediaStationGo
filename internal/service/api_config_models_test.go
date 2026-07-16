package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func TestDiscoverOpenAICompatibleModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-live" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-reasoner","owned_by":"deepseek"},{"id":"deepseek-chat","owned_by":"deepseek"},{"id":"deepseek-chat"}]}`))
	}))
	defer server.Close()

	db := newServiceTestDB(t, &model.APIConfig{})
	repos := repository.New(db)
	svc := NewAPIConfigService(zap.NewNop(), repos, NewCryptoService("test-secret", zap.NewNop()))
	items, err := svc.DiscoverModels(t.Context(), "openai", AIModelDiscoveryInput{
		APIKey: "sk-live", BaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "deepseek-chat" || items[1].ID != "deepseek-reasoner" {
		t.Fatalf("models = %#v", items)
	}
}

func TestAIModelsEndpointAcceptsVersionedAndCompletionBaseURLs(t *testing.T) {
	for input, want := range map[string]string{
		"https://api.openai.com/v1":                  "https://api.openai.com/v1/models",
		"https://api.deepseek.com/":                  "https://api.deepseek.com/models",
		"https://example.test/v1/chat/completions":   "https://example.test/v1/models",
	} {
		if got := aiModelsEndpoint(input); got != want {
			t.Fatalf("aiModelsEndpoint(%q) = %q, want %q", input, got, want)
		}
	}
}
