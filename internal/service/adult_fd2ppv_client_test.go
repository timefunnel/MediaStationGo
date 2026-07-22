package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

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
			call := targetCalls.Add(1)
			hasMember := false
			hasFreshClearance := false
			for _, cookie := range request.Cookies {
				hasMember = hasMember || (cookie.Name == "member" && cookie.Value == "member-token")
				hasFreshClearance = hasFreshClearance || (cookie.Name == "cf_clearance" && cookie.Value == "fresh")
			}
			if !hasMember {
				t.Fatalf("authenticated request did not carry member cookie")
			}
			if call == 1 {
				response = `<img src="/image/logo2.png" alt="FD2PPV">`
				cookies = append(cookies, map[string]string{"name": "cf_clearance", "value": "fresh", "domain": "127.0.0.1", "path": "/"})
			} else {
				sawFreshCookies.Store(hasFreshClearance)
				time.Sleep(40 * time.Millisecond)
				response = fd2PPVAuthenticatedTestHTML(`<h1 class="work-title">3780016</h1><div class="work-brief">cached detail</div>`)
				cookies = append(cookies, map[string]string{"name": "cf_clearance", "value": "fresh", "domain": "127.0.0.1", "path": "/"})
			}
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

	body, err := provider.fetchFD2PPVText(context.Background(), targetURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "cached detail") || loginCalls.Load() != 1 || targetCalls.Load() != 2 || !sawFreshCookies.Load() {
		t.Fatalf("login=%d target=%d fresh=%v body=%q", loginCalls.Load(), targetCalls.Load(), sawFreshCookies.Load(), body[:80])
	}
	if _, err := provider.fetchFD2PPVText(context.Background(), targetURL); err != nil {
		t.Fatal(err)
	}
	if targetCalls.Load() != 2 {
		t.Fatalf("cached target calls = %d, want 2", targetCalls.Load())
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
	if targetCalls.Load() != 3 {
		t.Fatalf("singleflight target calls = %d, want 3", targetCalls.Load())
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
