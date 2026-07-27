package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func TestSubtitleTranslationUsesConfiguredEncryptedProvider(t *testing.T) {
	var authorization string
	var requestedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		requestedModel = payload.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"translations\":[{\"id\":7,\"text\":\"你好\"}]}"}}]}`))
	}))
	defer server.Close()

	db := newServiceTestDB(t, &model.APIConfig{})
	apiConfig := NewAPIConfigService(
		zap.NewNop(), repository.New(db), NewCryptoService("test-secret", zap.NewNop()),
	)
	key := "cloud-secret"
	baseURL := server.URL + "/v1"
	modelID := "deepseek-chat"
	enabled := true
	if _, err := apiConfig.Update(t.Context(), "deepseek", APIConfigPatch{
		APIKey: &key, BaseURL: &baseURL, Model: &modelID, Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	service := NewSubtitleService(zap.NewNop(), repository.New(db)).SetAPIConfig(apiConfig)
	result, err := service.TranslateSegments(
		context.Background(),
		"deepseek",
		modelID,
		[]SubtitleTranslationSegment{{ID: 7, Text: "hello"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer "+key || requestedModel != modelID {
		t.Fatalf("authorization=%q model=%q", authorization, requestedModel)
	}
	if len(result.Translations) != 1 || result.Translations[0].Text != "你好" {
		t.Fatalf("result = %#v", result)
	}
}

func TestSubtitleTranslationRejectsUnconfiguredModel(t *testing.T) {
	db := newServiceTestDB(t, &model.APIConfig{})
	apiConfig := NewAPIConfigService(
		zap.NewNop(), repository.New(db), NewCryptoService("test-secret", zap.NewNop()),
	)
	key := "cloud-secret"
	baseURL := "https://example.invalid/v1"
	modelID := "configured-model"
	if _, err := apiConfig.Update(t.Context(), "siliconflow", APIConfigPatch{
		APIKey: &key, BaseURL: &baseURL, Model: &modelID,
	}); err != nil {
		t.Fatal(err)
	}
	service := NewSubtitleService(zap.NewNop(), repository.New(db)).SetAPIConfig(apiConfig)
	_, err := service.TranslateSegments(
		t.Context(), "siliconflow", "client-supplied-model",
		[]SubtitleTranslationSegment{{ID: 0, Text: "hello"}},
	)
	if err == nil {
		t.Fatal("expected mismatched model to be rejected")
	}
}
