package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type accountFixture struct {
	node                            *ProbeNode
	cookieID, cookieActual, tokenID string
	status                          int
	failPath                        string
	calls                           []string
	challenge                       string
	clock                           time.Time
	waits                           []time.Duration
}

func newAccountFixture(t *testing.T) *accountFixture {
	t.Helper()
	f := &accountFixture{cookieID: "42", cookieActual: "42", tokenID: "42", status: 2, clock: time.Now()}
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.URL.Path)
		cookie, token := r.Header.Get("Cookie"), r.Header.Get("Authorization")
		if r.URL.Path == "/files/uploadinfo" {
			if !strings.Contains(cookie, "UID="+f.cookieID+"_") || token != "" {
				t.Error("wrong Cookie boundary")
			}
		} else if r.URL.Path == "/open/user/info" {
			if cookie != "" || token != "Bearer new-access" {
				t.Error("wrong Token boundary")
			}
		} else if cookie != "" || token != "" {
			t.Error("credentials leaked to QR endpoint")
		}
		if r.URL.Path == f.failPath {
			w.WriteHeader(429)
			io.WriteString(w, "private-upstream-details")
			return
		}
		switch r.URL.Path {
		case "/api/1.0/web/1.0/token/":
			io.WriteString(w, `{"state":1,"data":{"uid":"cookie-qr","time":"123","sign":"cookie-sign"}}`)
		case "/api/1.0/web/1.0/qrcode":
			if r.URL.Query().Get("uid") == "" {
				t.Error("missing QR uid")
			}
			w.Write(picture.Bytes())
		case "/get/status/":
			if r.URL.Query().Get("sign") == "" || r.URL.Query().Get("time") != "123" {
				t.Error("missing QR signature")
			}
			fmt.Fprintf(w, `{"state":true,"data":{"status":%d}}`, f.status)
		case "/app/1.0/web/1.0/login/qrcode/":
			r.ParseForm()
			if r.Method != "POST" || r.Form.Get("account") != "cookie-qr" || r.Form.Get("app") != "web" {
				t.Error("wrong cookie exchange")
			}
			fmt.Fprintf(w, `{"state":true,"data":{"cookie":{"UID":"%s_session","CID":"new-cid","SEID":"new-seid"}}}`, f.cookieID)
		case "/files/uploadinfo":
			fmt.Fprintf(w, `{"user_id":%s,"errno":0}`, f.cookieActual)
		case "/open/authDeviceCode":
			r.ParseForm()
			f.challenge = r.Form.Get("code_challenge")
			if r.Method != "POST" || r.Form.Get("client_id") != "100197303" || r.Form.Get("code_challenge_method") != "sha256" {
				t.Error("invalid PKCE device request")
			}
			io.WriteString(w, `{"code":0,"data":{"uid":"open-qr","time":123,"sign":"open-sign"}}`)
		case "/open/deviceCodeToToken":
			r.ParseForm()
			verifier := r.Form.Get("code_verifier")
			sum := sha256.Sum256([]byte(verifier))
			if r.Method != "POST" || r.Form.Get("uid") != "open-qr" || len(verifier) < 43 || f.challenge != base64.StdEncoding.EncodeToString(sum[:]) {
				t.Error("invalid PKCE token exchange")
			}
			io.WriteString(w, `{"code":0,"data":{"access_token":"new-access","refresh_token":"new-refresh","expires_in":7200}}`)
		case "/open/user/info":
			fmt.Fprintf(w, `{"state":true,"data":{"user_id":"%s"}}`, f.tokenID)
		default:
			t.Errorf("unexpected network request: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	directory := t.TempDir()
	node, err := NewProbeNode(strings.Repeat("b", 32), filepath.Join(directory, "private", "b.json"), filepath.Join(directory, "state"), os.Args[0], "test")
	if err != nil {
		t.Fatal(err)
	}
	f.node = node
	node.client = server.Client()
	node.base = server.URL
	node.login.qrBase, node.login.passportBase, node.login.webBase = server.URL, server.URL, server.URL
	node.login.now = func() time.Time { return f.clock }
	node.wait = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d > 0 {
			f.waits = append(f.waits, d)
			f.clock = f.clock.Add(d)
		}
		return nil
	}
	t.Cleanup(func() {
		if node.login.session != nil && node.login.session.timer != nil {
			node.login.session.timer.Stop()
		}
	})
	return f
}

func accountCall(t *testing.T, f *accountFixture, path string, payload any, authorized bool) (int, ProbeAccountView, string) {
	t.Helper()
	method := "POST"
	var body io.Reader
	if payload == nil {
		method = "GET"
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	r := httptest.NewRequest(method, path, body)
	if authorized {
		r.Header.Set("Authorization", "Bearer "+f.node.token)
	}
	w := httptest.NewRecorder()
	f.node.ServeHTTP(w, r)
	var view ProbeAccountView
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("account response must not be cached")
	}
	return w.Code, view, w.Body.String()
}

func startAccountTest(t *testing.T, f *accountFixture) ProbeAccountView {
	t.Helper()
	code, view, body := accountCall(t, f, "/v1/account/start", map[string]any{"a_account_id": "7", "confirmed": true}, true)
	if code != 200 || view.Phase != "cookie_wait" || !strings.HasPrefix(view.Image, "data:image/png;base64,") {
		t.Fatalf("start: %d %s", code, body)
	}
	return view
}

func pollAccountTest(t *testing.T, f *accountFixture, id string) (int, ProbeAccountView, string) {
	t.Helper()
	return accountCall(t, f, "/v1/account/poll", map[string]any{"session_id": id}, true)
}

func TestProbeAccountTwoGrantsSaveOnlyVerifiedB(t *testing.T) {
	f := newAccountFixture(t)
	code, view, _ := accountCall(t, f, "/v1/status", nil, true)
	if code != 200 || view.Configured || len(f.calls) != 0 {
		t.Fatal("unconfigured node must start without contacting 115")
	}
	view = startAccountTest(t, f)
	code, view, body := pollAccountTest(t, f, view.SessionID)
	if code != 200 || view.Phase != "open_wait" || view.Configured || view.PendingAccountID != "42" || view.AccountID != "" {
		t.Fatalf("first grant: %d %s", code, body)
	}
	if _, err := os.Stat(f.node.credentials); !os.IsNotExist(err) {
		t.Fatal("partial grant must remain in RAM")
	}
	code, view, body = pollAccountTest(t, f, view.SessionID)
	if code != 200 || view.Phase != "ready" || view.AccountID != "42" || !view.Configured || view.Image != "" || view.Requests != 10 || len(f.calls) != 10 {
		t.Fatalf("second grant: %d %s calls=%d", code, body, len(f.calls))
	}
	credentials, err := readProbeWorkerCredentials(f.node.credentials)
	if err != nil || credentials.UserID != "42" || credentials.AccessToken != "new-access" || credentials.RefreshToken != "new-refresh" || credentials.Cookie == "" || credentials.ExpiresAt <= f.clock.Unix() {
		t.Fatalf("credentials not saved: %v", err)
	}
	for _, secret := range []string{"new-access", "new-refresh", "new-seid", "cookie-sign", "open-sign", "code_verifier"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked %s", secret)
		}
	}
	if f.node.login.session.candidate.Cookie != "" || f.node.login.session.verifier != "" {
		t.Fatal("grant secrets retained after save")
	}
	for _, gap := range f.waits {
		if gap < 5*time.Second {
			t.Fatalf("request gap %s", gap)
		}
	}
	before := len(f.calls)
	pollAccountTest(t, f, view.SessionID)
	if len(f.calls) != before {
		t.Fatal("completed poll replayed 115")
	}
}

func TestProbeAccountRejectsWrongAccountsPreservesExistingB(t *testing.T) {
	for _, scenario := range []string{"A", "cookie identity", "token identity"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAccountFixture(t)
			old := probeWorkerCredentials{UserID: "90", AccessToken: "old-access", RefreshToken: "old-refresh", Cookie: "UID=90_session; CID=old-cid; SEID=old-seid"}
			if err := WriteProbeWorkerJSON(f.node.credentials, old); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(f.node.credentials)
			if scenario == "A" {
				f.cookieID = "7"
			} else if scenario == "cookie identity" {
				f.cookieActual = "43"
			} else {
				f.tokenID = "43"
			}
			view := startAccountTest(t, f)
			code, next, body := pollAccountTest(t, f, view.SessionID)
			if scenario == "token identity" {
				if code != 200 || next.AccountID != "90" || next.PendingAccountID != "42" {
					t.Fatalf("saved B identity changed before commit: %s", body)
				}
				code, _, body = pollAccountTest(t, f, view.SessionID)
			}
			if code == 200 {
				t.Fatalf("wrong account accepted: %s", body)
			}
			after, _ := os.ReadFile(f.node.credentials)
			if !bytes.Equal(before, after) {
				t.Fatal("old B login changed on failure")
			}
			_, failed, _ := accountCall(t, f, "/v1/account", nil, true)
			if failed.Phase != "failed" || !failed.Configured || failed.AccountID != "90" || failed.PendingAccountID != "" || failed.Image != "" {
				t.Fatalf("incorrect failure state: %+v", failed)
			}
			if f.node.login.session.candidate.Cookie != "" || f.node.login.session.verifier != "" {
				t.Fatal("failed grant retained secrets")
			}
			if scenario == "A" && len(f.calls) != 4 {
				t.Fatalf("A scan must stop before identity or open grant, calls=%d", len(f.calls))
			}
		})
	}
}

func TestProbeAccountBudgetDenialExpiryAndHTTPFailureStop(t *testing.T) {
	for _, scenario := range []string{"poll budget", "total budget", "expired", "denied", "HTTP failure", "canceled context"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAccountFixture(t)
			view := startAccountTest(t, f)
			switch scenario {
			case "poll budget":
				f.status = 0
				for i := 0; i < probeAccountMaxPolls; i++ {
					code, _, body := pollAccountTest(t, f, view.SessionID)
					if code != 200 {
						t.Fatalf("poll %d: %s", i, body)
					}
				}
			case "total budget":
				f.node.login.session.view.Requests = probeAccountMaxRequests
			case "expired":
				f.clock = f.clock.Add(6 * time.Minute)
			case "denied":
				f.status = -2
			case "HTTP failure":
				f.failPath = "/get/status/"
			case "canceled context":
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				_, err := f.node.pollAccount(ctx, view.SessionID)
				if err == nil {
					t.Fatal("canceled context accepted")
				}
			}
			before := len(f.calls)
			code, next, body := pollAccountTest(t, f, view.SessionID)
			if code == 200 && next.Phase != "failed" {
				t.Fatalf("grant not stopped: %s", body)
			}
			if strings.Contains(body, "private-upstream-details") {
				t.Fatal("upstream error body leaked")
			}
			if scenario == "denied" || scenario == "HTTP failure" {
				before++
			}
			if len(f.calls) != before {
				t.Fatalf("unexpected retry: %d != %d", len(f.calls), before)
			}
			before = len(f.calls)
			pollAccountTest(t, f, view.SessionID)
			if len(f.calls) != before {
				t.Fatal("failed grant automatically retried")
			}
			if f.node.login.session.view.Phase != "failed" || f.node.login.session.candidate.Cookie != "" {
				t.Fatal("failure did not clear partial authorization")
			}
		})
	}
}

func TestProbeAccountGuardsCancellationAndLocalLogout(t *testing.T) {
	f := newAccountFixture(t)
	for _, req := range []struct {
		path string
		body any
		auth bool
	}{
		{"/v1/account/start", map[string]any{"a_account_id": "7", "confirmed": true}, false},
		{"/v1/account/start", map[string]any{"a_account_id": "7"}, true},
		{"/v1/account/start", map[string]any{"a_account_id": "0", "confirmed": true}, true},
		{"/v1/account/poll", map[string]any{"session_id": "wrong"}, true},
	} {
		code, _, _ := accountCall(t, f, req.path, req.body, req.auth)
		if code == 200 {
			t.Fatal("invalid authorization request accepted")
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("invalid requests contacted upstream")
	}
	view := startAccountTest(t, f)
	code, _, _ := accountCall(t, f, "/v1/account/start", map[string]any{"a_account_id": "7", "confirmed": true}, true)
	if code == 200 || len(f.calls) != 2 {
		t.Fatal("parallel grant accepted")
	}
	code, _, _ = accountCall(t, f, "/v1/staging", map[string]any{"id": "blocked", "account_id": "42"}, true)
	if code == 200 || len(f.calls) != 2 {
		t.Fatal("task ran while authorizing")
	}
	code, _, _ = accountCall(t, f, "/v1/account/cancel", map[string]any{"session_id": "wrong"}, true)
	if code == 200 {
		t.Fatal("wrong session canceled")
	}
	code, view, _ = accountCall(t, f, "/v1/account/cancel", map[string]any{"session_id": view.SessionID}, true)
	if code != 200 || view.Phase != "logged_out" || len(f.calls) != 2 {
		t.Fatal("cancel contacted 115")
	}
	if err := WriteProbeWorkerJSON(f.node.credentials, probeWorkerCredentials{UserID: "42", AccessToken: "synthetic", Cookie: syntheticProbeCookie}); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(f.node.directory, "keep.json")
	if err := os.WriteFile(sentinel, []byte("existing-task"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, _ = accountCall(t, f, "/v1/account/logout", map[string]any{}, true)
	if code == 200 {
		t.Fatal("unconfirmed logout accepted")
	}
	code, view, _ = accountCall(t, f, "/v1/account/logout", map[string]any{"confirmed": true}, true)
	if code != 200 || view.Configured || len(f.calls) != 2 {
		t.Fatal("local logout contacted 115")
	}
	if _, err := os.Stat(f.node.credentials); !os.IsNotExist(err) {
		t.Fatal("B credentials not removed")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "existing-task" {
		t.Fatal("logout affected task state")
	}
}
