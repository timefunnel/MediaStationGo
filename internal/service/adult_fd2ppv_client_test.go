package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/helper"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func TestFD2PPVAuthenticatedFetchRetriesIncompletePageAndReusesCache(t *testing.T) {
	const (
		username = "fd2-user"
		password = "fd2-password"
	)
	var loginCalls atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fetch/login.php" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		loginCalls.Add(1)
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["user"] != username || payload["password"] != password || payload["_csrf"] != "csrf-token" {
			t.Fatalf("unexpected login payload keys or values")
		}
		http.SetCookie(w, &http.Cookie{Name: "member", Value: "member-token", Path: "/", HttpOnly: true, Secure: false})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"script":"bG9jYXRpb24ucmVsb2FkKCk7"}`))
	}))
	defer site.Close()

	originalBaseURL := fd2PPVBaseURL
	fd2PPVBaseURL = site.URL
	t.Cleanup(func() { fd2PPVBaseURL = originalBaseURL })

	var targetCalls atomic.Int32
	var sawFreshCookies atomic.Bool
	flare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Cmd     string `json:"cmd"`
			URL     string `json:"url"`
			Cookies []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"cookies"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Cmd != "request.get" {
			t.Fatalf("cmd = %q", request.Cmd)
		}
		response := ""
		cookies := []map[string]string{}
		switch request.URL {
		case site.URL + "/":
			response = `<html><input type="hidden" name="_csrf" value="csrf-token"></html>`
			cookies = append(cookies, map[string]string{"name": "cf_clearance", "value": "initial", "domain": "127.0.0.1", "path": "/"})
		default:
			targetCalls.Add(1)
			hasMember := false
			for _, cookie := range request.Cookies {
				hasMember = hasMember || (cookie.Name == "member" && cookie.Value == "member-token")
			}
			if !hasMember {
				t.Fatalf("authenticated request did not carry member cookie")
			}
			response = `<img src="/image/logo2.png" alt="FD2PPV">`
			cookies = append(cookies, map[string]string{"name": "cf_clearance", "value": "fresh", "domain": "127.0.0.1", "path": "/"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"status":    200,
				"response":  response,
				"userAgent": "test-browser",
				"cookies":   cookies,
			},
		})
	}))
	defer flare.Close()

	provider := newConfiguredFD2PPVTestProvider(t, username, password)
	provider.client = site.Client()
	provider.SetFlareSolverr(flare.URL, 5)
	targetURL := site.URL + "/articles/3780016"
	var directCalls atomic.Int32
	provider.fd2ppv.direct = fd2PPVDirectFetcherFunc(func(
		_ context.Context,
		_ string,
		_ string,
		cookies []helper.FlareSolverrCookie,
	) (fd2PPVDirectFetchResult, error) {
		call := directCalls.Add(1)
		hasFreshClearance := false
		for _, cookie := range cookies {
			hasFreshClearance = hasFreshClearance || (cookie.Name == "cf_clearance" && cookie.Value == "fresh")
		}
		if call == 1 {
			return fd2PPVDirectFetchResult{
				body:       "cloudflare challenge",
				statusCode: http.StatusForbidden,
			}, nil
		}
		if hasFreshClearance {
			sawFreshCookies.Store(true)
		}
		time.Sleep(40 * time.Millisecond)
		return fd2PPVDirectFetchResult{
			body:       fd2PPVAuthenticatedTestHTML(`<h1 class="work-title">3780016</h1><div class="work-brief">cached detail</div>`),
			statusCode: http.StatusOK,
		}, nil
	})

	body, err := provider.fetchFD2PPVText(context.Background(), targetURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "cached detail") || loginCalls.Load() != 1 || targetCalls.Load() != 1 || directCalls.Load() != 2 || !sawFreshCookies.Load() {
		t.Fatalf("login=%d target=%d direct=%d fresh=%v body=%q", loginCalls.Load(), targetCalls.Load(), directCalls.Load(), sawFreshCookies.Load(), body[:80])
	}
	if _, err := provider.fetchFD2PPVText(context.Background(), targetURL); err != nil {
		t.Fatal(err)
	}
	if targetCalls.Load() != 1 || directCalls.Load() != 2 {
		t.Fatalf("cached target=%d direct=%d, want 1 and 2", targetCalls.Load(), directCalls.Load())
	}

	provider.ForgetFD2PPVCache()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := provider.fetchFD2PPVText(context.Background(), targetURL)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if targetCalls.Load() != 1 || directCalls.Load() != 3 {
		t.Fatalf("singleflight target=%d direct=%d, want 1 and 3", targetCalls.Load(), directCalls.Load())
	}
}

func TestFD2PPVDirectTransportErrorDoesNotInvokeTargetChallenge(t *testing.T) {
	const targetURL = "https://fd2ppv.cc/articles/3780016"
	var targetChallengeCalls atomic.Int32
	flare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.URL == targetURL {
			targetChallengeCalls.Add(1)
		}
		response := `<html><input type="hidden" name="_csrf" value="csrf-token"></html>`
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"status":    200,
				"response":  response,
				"userAgent": fd2PPVChrome133UserAgent,
				"cookies":   []map[string]string{},
			},
		})
	}))
	defer flare.Close()

	loginSite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "member", Value: "member-token", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"script":"bG9jYXRpb24ucmVsb2FkKCk7"}`))
	}))
	defer loginSite.Close()
	originalBaseURL := fd2PPVBaseURL
	fd2PPVBaseURL = loginSite.URL
	t.Cleanup(func() { fd2PPVBaseURL = originalBaseURL })

	provider := newConfiguredFD2PPVTestProvider(t, "fd2-user", "fd2-password")
	provider.client = loginSite.Client()
	provider.SetFlareSolverr(flare.URL, 5)
	provider.fd2ppv.direct = fd2PPVDirectFetcherFunc(func(
		context.Context,
		string,
		string,
		[]helper.FlareSolverrCookie,
	) (fd2PPVDirectFetchResult, error) {
		return fd2PPVDirectFetchResult{}, errors.New("dial failed")
	})

	_, err := provider.fetchFD2PPVText(context.Background(), targetURL)
	if err == nil || !strings.Contains(err.Error(), "fd2ppv direct request: dial failed") {
		t.Fatalf("error = %v", err)
	}
	if targetChallengeCalls.Load() != 0 {
		t.Fatalf("target challenge calls = %d, want 0", targetChallengeCalls.Load())
	}
}

func TestFD2PPVFetchErrorsAreNotCached(t *testing.T) {
	var flareCalls atomic.Int32
	flare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flareCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "message": "challenge failed"})
	}))
	defer flare.Close()

	provider := newConfiguredFD2PPVTestProvider(t, "fd2-user", "fd2-password")
	provider.SetFlareSolverr(flare.URL, 5)
	for range 2 {
		if _, err := provider.fetchFD2PPVText(context.Background(), "https://fd2ppv.cc/articles/3780016"); err == nil {
			t.Fatal("expected fetch error")
		}
	}
	if flareCalls.Load() != 2 {
		t.Fatalf("flare calls = %d, want 2", flareCalls.Load())
	}
}

func newConfiguredFD2PPVTestProvider(t *testing.T, username, password string) *AdultProvider {
	t.Helper()
	db := newServiceTestDB(t, &model.APIConfig{})
	apiConfig := NewAPIConfigService(zap.NewNop(), repository.New(db), NewCryptoService("test-secret", zap.NewNop()))
	baseURL := "https://fd2ppv.cc"
	enabled := true
	if _, err := apiConfig.Update(t.Context(), "fd2ppv", APIConfigPatch{
		APIKey:  &password,
		BaseURL: &baseURL,
		Extra:   &username,
		Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	return NewAdultProvider(zap.NewNop(), apiConfig)
}

func fd2PPVAuthenticatedTestHTML(content string) string {
	return fmt.Sprintf(`<html><body><button data-url="logout">logout</button>%s%s</body></html>`, content, strings.Repeat("x", 4096))
}

type fd2PPVDirectFetcherFunc func(
	ctx context.Context,
	targetURL string,
	userAgent string,
	cookies []helper.FlareSolverrCookie,
) (fd2PPVDirectFetchResult, error)

func (f fd2PPVDirectFetcherFunc) fetch(
	ctx context.Context,
	targetURL string,
	userAgent string,
	cookies []helper.FlareSolverrCookie,
) (fd2PPVDirectFetchResult, error) {
	return f(ctx, targetURL, userAgent, cookies)
}
