package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/service/cloud"
)

type cloudResolveTestRoundTripper func(*http.Request) (*http.Response, error)

func (f cloudResolveTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestCloudResolveHotCacheRefreshesInBackground(t *testing.T) {
	var resolves atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/fs/list" {
			writeOpenListCacheList(w)
			return
		}
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

func TestCloudResolveCachePrunesLeastRecentlyUsedEntries(t *testing.T) {
	storage := &StorageConfigService{resolveCache: make(map[string]cloudResolveCacheEntry)}
	now := time.Now()
	for i := 0; i < cloudResolveCacheMaxEntries; i++ {
		key := fmt.Sprintf("openlist\x00/file-%04d\x00Player", i)
		storage.resolveCache[key] = cloudResolveCacheEntry{
			link:      &cloud.DirectLink{URL: fmt.Sprintf("https://cdn.example/%d", i)},
			expiresAt: now.Add(time.Minute), staleUntil: now.Add(time.Minute), lastHit: now.Add(time.Duration(i) * time.Millisecond),
		}
	}
	oldest := "openlist\x00/file-0000\x00Player"
	storage.storeResolvedLinkLocked("openlist\x00/new-file\x00Player", &cloud.DirectLink{URL: "https://cdn.example/new"}, time.Minute, time.Minute)
	if len(storage.resolveCache) != cloudResolveCacheMaxEntries {
		t.Fatalf("cache size=%d, want %d", len(storage.resolveCache), cloudResolveCacheMaxEntries)
	}
	if _, ok := storage.resolveCache[oldest]; ok {
		t.Fatal("least recently used cache entry was not pruned")
	}
}

func TestCloudResolveCacheRefreshAtCapacityDoesNotEvict(t *testing.T) {
	storage := &StorageConfigService{resolveCache: make(map[string]cloudResolveCacheEntry)}
	now := time.Now()
	for i := 0; i < cloudResolveCacheMaxEntries; i++ {
		key := fmt.Sprintf("key-%04d", i)
		storage.resolveCache[key] = cloudResolveCacheEntry{
			link:      &cloud.DirectLink{URL: fmt.Sprintf("https://cdn.example/%d", i)},
			expiresAt: now.Add(time.Minute), staleUntil: now.Add(time.Minute), lastHit: now,
		}
	}
	storage.storeResolvedLinkLocked("key-0000", &cloud.DirectLink{URL: "https://cdn.example/refreshed"}, time.Minute, time.Minute)
	if len(storage.resolveCache) != cloudResolveCacheMaxEntries || storage.resolveCache["key-0000"].link.URL != "https://cdn.example/refreshed" {
		t.Fatalf("cache refresh changed capacity or missed replacement")
	}
}

func TestCloudResolveReturnsStaleLinkAndRefreshesInBackground(t *testing.T) {
	var resolves atomic.Int32
	expiry := time.Now().Add(time.Hour).Unix()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/fs/list" {
			writeOpenListCacheList(w)
			return
		}
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
		if r.URL.Path == "/api/fs/list" {
			writeOpenListCacheList(w)
			return
		}
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

func TestCloudResolveColdMissUsesBoundedWorkerDeadline(t *testing.T) {
	started := make(chan struct{})
	_, storage := newStorageUploadTestService(t)
	storage.client = &http.Client{
		Transport: cloudResolveTestRoundTripper(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/api/fs/list":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"code":200,"data":{"content":[],"total":0}}`)),
					Request:    r,
				}, nil
			case "/api/fs/get":
				select {
				case <-started:
				default:
					close(started)
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			default:
				return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
			}
		}),
	}

	if _, err := storage.Save(t.Context(), StorageInput{
		Type: "openlist",
		Config: map[string]any{
			"server": "http://openlist.test",
			"token":  "token",
		},
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err := storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cold resolve error = %v, want context deadline exceeded", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("OpenList get request did not start")
	}
	if elapsed >= cloudResolveColdMaxDuration+500*time.Millisecond {
		t.Fatalf("cold resolve took %s, want less than %s", elapsed, cloudResolveColdMaxDuration+500*time.Millisecond)
	}
	key := storage.resolveCacheKey("openlist", "/Movies/f1.mkv", "Player/1")
	storage.resolveMu.Lock()
	_, inFlight := storage.resolveFlight[key]
	storage.resolveMu.Unlock()
	if inFlight {
		t.Fatal("cold resolve left a single-flight request behind")
	}
}

func TestStorageConfigSaveNotifiesChangeHandler(t *testing.T) {
	_, storage := newStorageUploadTestService(t)
	var changed string
	storage.SetChangeHandler(func(typ string) {
		changed = typ
	})
	if _, err := storage.Save(t.Context(), StorageInput{
		Type: "openlist",
		Config: map[string]any{
			"server": "http://openlist.test",
			"token":  "token",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if changed != "openlist" {
		t.Fatalf("changed=%q, want openlist", changed)
	}
}

func TestCloudResolveContinuesAfterOwnerContextCanceled(t *testing.T) {
	var resolves atomic.Int32
	getStarted := make(chan struct{})
	releaseGet := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/list":
			writeOpenListCacheList(w)
		case "/api/fs/get":
			if resolves.Add(1) == 1 {
				close(getStarted)
			}
			<-releaseGet
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"code":200,"data":{"raw_url":"http://cdn.local/movie.mkv"}}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	_, storage := newStorageUploadTestService(t)
	if _, err := storage.Save(t.Context(), StorageInput{Type: "openlist", Config: map[string]any{"server": upstream.URL, "token": "token"}}); err != nil {
		t.Fatal(err)
	}

	ownerCtx, cancelOwner := context.WithCancel(t.Context())
	ownerErr := make(chan error, 1)
	go func() {
		_, err := storage.CloudResolve(ownerCtx, "openlist", "/Movies/f1.mkv", "Player/1")
		ownerErr <- err
	}()
	select {
	case <-getStarted:
	case <-time.After(time.Second):
		t.Fatal("cold resolve did not start")
	}
	cancelOwner()
	if err := <-ownerErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("owner error = %v, want context canceled", err)
	}

	waiterResult := make(chan *cloud.DirectLink, 1)
	waiterErr := make(chan error, 1)
	go func() {
		link, err := storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
		waiterResult <- link
		waiterErr <- err
	}()
	time.Sleep(30 * time.Millisecond)
	if resolves.Load() != 1 {
		t.Fatalf("resolve calls = %d, want one shared flight", resolves.Load())
	}
	close(releaseGet)
	if err := <-waiterErr; err != nil {
		t.Fatal(err)
	}
	if link := <-waiterResult; link == nil || link.URL != "http://cdn.local/movie.mkv" {
		t.Fatalf("waiter link = %#v", link)
	}
	link, err := storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
	if err != nil || link == nil || link.URL != "http://cdn.local/movie.mkv" || resolves.Load() != 1 {
		t.Fatalf("cached link=%#v err=%v resolves=%d", link, err, resolves.Load())
	}
}

func TestCloudResolveKeepsDifferentUserAgentsIndependent(t *testing.T) {
	var resolves atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/list":
			writeOpenListCacheList(w)
		case "/api/fs/get":
			resolves.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"code":200,"data":{"raw_url":"http://cdn.local/%s.mkv"}}`, url.PathEscape(r.UserAgent()))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	_, storage := newStorageUploadTestService(t)
	if _, err := storage.Save(t.Context(), StorageInput{Type: "openlist", Config: map[string]any{"server": upstream.URL, "token": "token"}}); err != nil {
		t.Fatal(err)
	}
	first, err := storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/2")
	if err != nil {
		t.Fatal(err)
	}
	if first.URL == second.URL || resolves.Load() != 2 {
		t.Fatalf("first=%q second=%q resolves=%d, want UA-specific links", first.URL, second.URL, resolves.Load())
	}
}

func TestCloudResolveConfigChangePreventsOldFlightCacheWrite(t *testing.T) {
	var resolves atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/list":
			writeOpenListCacheList(w)
		case "/api/fs/get":
			call := resolves.Add(1)
			if call == 1 {
				close(firstStarted)
				<-releaseFirst
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"code":200,"data":{"raw_url":"http://cdn.local/%d.mkv"}}`, call)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	_, storage := newStorageUploadTestService(t)
	if _, err := storage.Save(t.Context(), StorageInput{Type: "openlist", Config: map[string]any{"server": upstream.URL, "token": "token-1"}}); err != nil {
		t.Fatal(err)
	}
	oldResult := make(chan error, 1)
	go func() {
		_, err := storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
		oldResult <- err
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("old flight did not start")
	}
	if _, err := storage.Save(t.Context(), StorageInput{Type: "openlist", Config: map[string]any{"server": upstream.URL, "token": "token-2"}}); err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)
	if err := <-oldResult; err == nil || !strings.Contains(err.Error(), "storage config changed") {
		t.Fatalf("old flight error = %v, want config changed", err)
	}
	link, err := storage.CloudResolve(t.Context(), "openlist", "/Movies/f1.mkv", "Player/1")
	if err != nil {
		t.Fatal(err)
	}
	if link.URL != "http://cdn.local/2.mkv" || resolves.Load() != 2 {
		t.Fatalf("link=%#v resolves=%d, old result was cached", link, resolves.Load())
	}
}

func writeOpenListCacheList(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, `{"code":200,"data":{"content":[],"total":0}}`)
}
