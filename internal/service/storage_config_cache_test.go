package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/service/cloud"
)

func TestCloudResolveHotCacheRefreshesInBackground(t *testing.T) {
	var resolves atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/get" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		n := resolves.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":200,"data":{"raw_url":"http://cdn.local/%d.mkv"}}`, n)
	}))
	defer upstream.Close()

	_, storage := newStorageUploadTestService(t)
	if _, err := storage.Save(t.Context(), StorageInput{
		Type: "openlist",
		Config: map[string]any{
			"server": upstream.URL,
			"token":  "token",
		},
	}); err != nil {
		t.Fatal(err)
	}

	link, err := storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
	if err != nil {
		t.Fatal(err)
	}
	if link.URL != "http://cdn.local/1.mkv" || resolves.Load() != 1 {
		t.Fatalf("first resolve link=%#v resolves=%d", link, resolves.Load())
	}
	for i := 0; i < cloudResolveHotHitThreshold-1; i++ {
		link, err = storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
		if err != nil {
			t.Fatal(err)
		}
		if link.URL != "http://cdn.local/1.mkv" || resolves.Load() != 1 {
			t.Fatalf("cached resolve link=%#v resolves=%d", link, resolves.Load())
		}
	}

	key := storage.resolveCacheKey("openlist", "/Movies/f1.mkv", "Player/1")
	storage.resolveMu.Lock()
	entry := storage.resolveCache[key]
	entry.hits = cloudResolveHotHitThreshold
	entry.expiresAt = time.Now().Add(5 * time.Second)
	storage.resolveCache[key] = entry
	storage.resolveMu.Unlock()

	link, err = storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
	if err != nil {
		t.Fatal(err)
	}
	if link.URL != "http://cdn.local/1.mkv" {
		t.Fatalf("hot hit should return cached link immediately, got %s", link.URL)
	}
	deadline := time.Now().Add(2 * time.Second)
	for resolves.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if resolves.Load() < 2 {
		t.Fatalf("background refresh did not run, resolves=%d", resolves.Load())
	}
	link, err = storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
	if err != nil {
		t.Fatal(err)
	}
	if link.URL != "http://cdn.local/2.mkv" {
		t.Fatalf("refreshed link = %s, want second URL", link.URL)
	}
}

func TestCloudResolveCacheTTLUsesShortTTLForCloudPlaybackLinks(t *testing.T) {
	for _, typ := range []string{"cloud115", "clouddrive2", "openlist"} {
		if got := cloudResolveCacheTTL(typ); got != 2*time.Minute {
			t.Fatalf("%s cloud resolve cache ttl = %v, want 2m", typ, got)
		}
	}
}

func TestCloudResolveCacheUses115DirectLinkExpiry(t *testing.T) {
	expires := time.Now().Add(time.Hour).Unix()
	link := &cloud.DirectLink{URL: fmt.Sprintf("https://cdnfhnfile.115cdn.net/hash/movie.mp4?t=%d&k=secret", expires)}
	ttl, staleTTL := cloudResolveCacheDurations("openlist", link)
	if ttl <= 2*time.Minute || ttl > 30*time.Minute {
		t.Fatalf("ttl = %v, want extended ttl within 30m", ttl)
	}
	if staleTTL <= ttl || staleTTL > 50*time.Minute {
		t.Fatalf("stale ttl = %v, ttl = %v, want stale window within 50m", staleTTL, ttl)
	}
}

func TestCloudResolveReturnsStaleLinkAndRefreshesInBackground(t *testing.T) {
	var resolves atomic.Int32
	expiry := time.Now().Add(time.Hour).Unix()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/get" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		n := resolves.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = fmt.Fprintf(w, `{"code":200,"data":{"raw_url":"https://cdnfhnfile.115cdn.net/hash/movie.mp4?t=%d"}}`, expiry)
			return
		}
		_, _ = fmt.Fprint(w, `{"code":500,"message":"failed get link"}`)
	}))
	defer upstream.Close()

	_, storage := newStorageUploadTestService(t)
	if _, err := storage.Save(t.Context(), StorageInput{
		Type: "openlist",
		Config: map[string]any{
			"server": upstream.URL,
			"token":  "token",
		},
	}); err != nil {
		t.Fatal(err)
	}

	link, err := storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
	if err != nil {
		t.Fatal(err)
	}
	if link.URL == "" || resolves.Load() != 1 {
		t.Fatalf("first resolve link=%#v resolves=%d", link, resolves.Load())
	}

	key := storage.resolveCacheKey("openlist", "/Movies/f1.mkv", "Player/1")
	storage.resolveMu.Lock()
	entry := storage.resolveCache[key]
	entry.expiresAt = time.Now().Add(-time.Second)
	entry.staleUntil = time.Now().Add(10 * time.Minute)
	entry.refreshAfter = time.Time{}
	storage.resolveCache[key] = entry
	storage.resolveMu.Unlock()

	link, err = storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
	if err != nil {
		t.Fatal(err)
	}
	if link.URL != entry.link.URL {
		t.Fatalf("stale link = %s, want %s", link.URL, entry.link.URL)
	}
	deadline := time.Now().Add(2 * time.Second)
	for resolves.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if resolves.Load() < 2 {
		t.Fatalf("background stale refresh did not run, resolves=%d", resolves.Load())
	}
}

func TestCloudResolveRetriesFastFailedGetLink(t *testing.T) {
	var resolves atomic.Int32
	expiry := time.Now().Add(time.Hour).Unix()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/get" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		n := resolves.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = fmt.Fprint(w, `{"code":500,"message":"failed get link"}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"code":200,"data":{"raw_url":"https://cdnfhnfile.115cdn.net/hash/movie.mp4?t=%d&%s"}}`, expiry, url.Values{"k": []string{"secret"}}.Encode())
	}))
	defer upstream.Close()

	_, storage := newStorageUploadTestService(t)
	if _, err := storage.Save(t.Context(), StorageInput{
		Type: "openlist",
		Config: map[string]any{
			"server": upstream.URL,
			"token":  "token",
		},
	}); err != nil {
		t.Fatal(err)
	}

	link, err := storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
	if err != nil {
		t.Fatal(err)
	}
	if resolves.Load() != 2 {
		t.Fatalf("resolves = %d, want retry once", resolves.Load())
	}
	if link.URL == "" {
		t.Fatal("empty resolved link")
	}
}
