package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
)

type resourcePipelineRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn resourcePipelineRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestResourcePipelineHTTPClientUsesDedicatedBT4GSearchTimeout(t *testing.T) {
	client, err := newResourcePipelineHTTPClient(config.ResourceImportConfig{
		PipelineURL:              "http://pipeline.example",
		PipelineToken:            "secret",
		SearchTimeoutSeconds:     30,
		BT4GSearchTimeoutSeconds: 70,
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.client.Timeout != 30*time.Second {
		t.Fatalf("default timeout = %s", client.client.Timeout)
	}
	if client.bt4gSearchClient.Timeout != 70*time.Second {
		t.Fatalf("BT4G timeout = %s", client.bt4gSearchClient.Timeout)
	}
	client.client.Transport = resourcePipelineSearchResponseTransport("normal")
	client.bt4gSearchClient.Transport = resourcePipelineSearchResponseTransport("bt4g")

	normal, err := client.Search(context.Background(), resourcePipelineSearchRequest{Query: "IPZZ-912"})
	if err != nil {
		t.Fatal(err)
	}
	bt4g, err := client.Search(context.Background(), resourcePipelineSearchRequest{Query: "IPZZ-912", Source: "BT4G"})
	if err != nil {
		t.Fatal(err)
	}
	if normal.SessionID != "normal" || bt4g.SessionID != "bt4g" {
		t.Fatalf("unexpected search client routing: normal=%q bt4g=%q", normal.SessionID, bt4g.SessionID)
	}
}

func resourcePipelineSearchResponseTransport(sessionID string) http.RoundTripper {
	return resourcePipelineRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"session_id":"` + sessionID + `","items":[]}`)),
			Request:    request,
		}, nil
	})
}

func TestResourcePipelineHTTPClientUsesBearerOwnerAndIdempotency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v1/search":
			var body resourcePipelineSearchRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.OwnerID != "user-a" || body.Category != "movie" || !body.SubscriptionFollow {
				t.Fatalf("search body = %+v", body)
			}
			_ = json.NewEncoder(w).Encode(resourcePipelineSearchResponse{SessionID: "session", Items: []map[string]any{}})
		case "/v1/manual-candidates":
			var body resourcePipelineManualRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.OwnerID != "user-a" || body.Category != "movie" || body.Input == "" || body.Title != "Manual task" {
				t.Fatalf("manual body = %+v", body)
			}
			_ = json.NewEncoder(w).Encode(resourcePipelineSearchResponse{SessionID: "manual-session", Items: []map[string]any{}})
		case "/v1/imports":
			if r.Header.Get("Idempotency-Key") != "idem" {
				t.Fatalf("idempotency key = %q", r.Header.Get("Idempotency-Key"))
			}
			var body resourcePipelineCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.OwnerID != "user-a" || body.SearchSessionID != "session" || body.CandidateID != "candidate" || body.UpgradeMediaID != "media-existing" {
				t.Fatalf("create body = %+v", body)
			}
			_ = json.NewEncoder(w).Encode(resourcePipelineTask{ID: "job", OwnerID: "user-a", Status: "queued", Stage: "queued"})
		case "/v1/imports/job":
			if r.URL.Query().Get("owner_id") != "user-a" {
				t.Fatalf("get owner = %q", r.URL.Query().Get("owner_id"))
			}
			_ = json.NewEncoder(w).Encode(resourcePipelineTask{ID: "job", OwnerID: "user-a", Status: "completed", Stage: "completed", MsgMediaID: "media"})
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
	if _, err := client.Search(context.Background(), resourcePipelineSearchRequest{OwnerID: "user-a", Query: "Movie", Category: "movie", Limit: 100, SubscriptionFollow: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PrepareManual(context.Background(), resourcePipelineManualRequest{OwnerID: "user-a", Title: "Manual task", Input: "https://115.com/s/test", Category: "movie"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateImport(context.Background(), "user-a", "idem", resourcePipelineCreateRequest{SearchSessionID: "session", CandidateID: "candidate", UpgradeMediaID: "media-existing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetImport(context.Background(), "user-a", "job"); err != nil {
		t.Fatal(err)
	}
}

func TestResourcePipelineHTTPClientDecodesNestedDuplicate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"duplicate_media","message":"duplicate","duplicate":{"can_force":true,"media_id":"media-existing","title":"Existing","reason":"title"}}}`))
	}))
	defer server.Close()
	client, err := newResourcePipelineHTTPClient(config.ResourceImportConfig{PipelineURL: server.URL, PipelineToken: "secret", SearchTimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateImport(context.Background(), "user-a", "idem", resourcePipelineCreateRequest{})
	pipelineErr, ok := err.(*resourcePipelineError)
	if !ok || pipelineErr.Duplicate == nil || !pipelineErr.Duplicate.CanForce || pipelineErr.Duplicate.MediaID != "media-existing" {
		t.Fatalf("duplicate error = %#v", err)
	}
}

func TestResourcePipelineHTTPClientDecodesSearchFailureCapabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"code":"search_failed","message":"BT4G timed out","capabilities":{"pansou":true}}}`))
	}))
	defer server.Close()
	client, err := newResourcePipelineHTTPClient(config.ResourceImportConfig{PipelineURL: server.URL, PipelineToken: "secret", SearchTimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(context.Background(), resourcePipelineSearchRequest{})
	pipelineErr, ok := err.(*resourcePipelineError)
	if !ok || pipelineErr.Code != "search_failed" || !pipelineErr.Capabilities.Pansou {
		t.Fatalf("search error = %#v", err)
	}
}
