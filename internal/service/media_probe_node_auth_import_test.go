package service

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProbeAccountImportCookieUsesSixRequestsWithoutHumanQR(t *testing.T) {
	f := newAccountFixture(t)
	code, view, body := accountCall(t, f, "/v1/account/import-cookie", map[string]any{"a_account_id": "7", "cookie": "UID=42_session; CID=new-cid; SEID=new-seid", "confirmed": true}, true)
	if code != 200 || view.Phase != "open_prepare" || view.Requests != 1 || view.RequestLimit != 6 || view.Image != "" || strings.Contains(body, "new-seid") {
		t.Fatalf("import: %d %s", code, body)
	}
	if _, err := os.Stat(f.node.credentials); !os.IsNotExist(err) {
		t.Fatal("partial import persisted credentials")
	}
	code, view, body = finishAccountTest(t, f, view.SessionID)
	if code != 200 || view.Phase != "ready" || view.Requests != 6 || view.Image != "" {
		t.Fatalf("complete: %d %s", code, body)
	}
	want := []string{"/files/uploadinfo", "/open/authDeviceCode", "/api/2.0/prompt.php", "/api/2.0/slogin.php", "/open/deviceCodeToToken", "/open/user/info"}
	if strings.Join(f.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected requests: %v", f.calls)
	}
	stored, err := readProbeWorkerCredentials(f.node.credentials)
	if err != nil || stored.UserID != "42" || stored.AccessToken != "new-access" {
		t.Fatalf("not saved: %v", err)
	}
}

func TestProbeAccountImportCookieRejectsUnsafeInputsBeforeRequests(t *testing.T) {
	for _, cookie := range []string{"", "UID=7_session; CID=cid; SEID=seid", "UID=42_session; CID=cid", "UID=42_session; CID=cid; SEID=seid; UID=43_session", "UID=42_session; CID=cid; SEID=se\nid", strings.Repeat("x", 16385)} {
		t.Run(cookie[:min(len(cookie), 20)], func(t *testing.T) {
			f := newAccountFixture(t)
			code, _, _ := accountCall(t, f, "/v1/account/import-cookie", map[string]any{"a_account_id": "7", "cookie": cookie, "confirmed": true}, true)
			if code != 409 || len(f.calls) != 0 {
				t.Fatalf("unsafe input contacted 115: %d %v", code, f.calls)
			}
		})
	}
	for _, confirmed := range []bool{false, true} {
		f := newAccountFixture(t)
		code, _, _ := accountCall(t, f, "/v1/account/import-cookie", map[string]any{"a_account_id": "7", "cookie": "UID=42_session; CID=cid; SEID=seid", "confirmed": confirmed}, confirmed == false)
		if code != map[bool]int{false: 409, true: 401}[confirmed] || len(f.calls) != 0 {
			t.Fatal("missing confirmation or bearer contacted 115")
		}
	}
}

func TestProbeAccountImportFailurePreservesExistingCredentials(t *testing.T) {
	for _, mismatch := range []string{"cookie", "token", "budget"} {
		t.Run(mismatch, func(t *testing.T) {
			f := newAccountFixture(t)
			old := probeWorkerCredentials{UserID: "43", Cookie: "UID=43_session; CID=old; SEID=old", AccessToken: "old-access"}
			if err := WriteProbeWorkerJSON(f.node.credentials, old); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(f.node.credentials)
			if mismatch == "cookie" {
				f.cookieActual = "44"
			}
			if mismatch == "token" {
				f.tokenID = "44"
			}
			code, view, _ := accountCall(t, f, "/v1/account/import-cookie", map[string]any{"a_account_id": "7", "cookie": "UID=42_session; CID=new-cid; SEID=new-seid", "confirmed": true}, true)
			if mismatch != "cookie" {
				if code != 200 {
					t.Fatal("import failed early")
				}
				if mismatch == "budget" {
					f.node.login.session.view.RequestLimit = 5
				}
				code, _, _ = finishAccountTest(t, f, view.SessionID)
			}
			if code != 409 {
				t.Fatal("failure was accepted")
			}
			after, _ := os.ReadFile(f.node.credentials)
			if !bytes.Equal(before, after) || f.node.login.session.candidate.Cookie != "" {
				t.Fatal("failed import altered old file or retained cookie")
			}
			calls := len(f.calls)
			accountCall(t, f, "/v1/account", nil, true)
			if len(f.calls) != calls {
				t.Fatal("state read retried import")
			}
		})
	}
}

func TestProbeAccountStatusWaitIsIndependentFromMediaHeaderTimeout(t *testing.T) {
	f := newAccountFixture(t)
	view := startAccountTest(t, f)
	f.status = 0
	f.statusDelay = 80 * time.Millisecond
	f.node.client.Timeout = 40 * time.Millisecond
	f.node.client.Transport.(*http.Transport).ResponseHeaderTimeout = 20 * time.Millisecond
	f.node.login.statusClient = newProbeAccountLogin().statusClient
	transport := f.node.login.statusClient.Transport.(*http.Transport)
	transport.DialContext = (&net.Dialer{}).DialContext // Local fixture only.
	if transport.ResponseHeaderTimeout != 60*time.Second || f.node.login.statusClient.Timeout != 65*time.Second {
		t.Fatal("invalid status wait policy")
	}
	code, next, body := pollAccountTest(t, f, view.SessionID)
	if code != 200 || next.Phase != "cookie_wait" || next.Image == "" || next.SessionID != view.SessionID {
		t.Fatalf("waiting QR was discarded: %d %s", code, body)
	}
	if len(f.calls) != 3 {
		t.Fatalf("unexpected status retries: %v", f.calls)
	}
	if f.node.client.Transport.(*http.Transport).ResponseHeaderTimeout != 20*time.Millisecond {
		t.Fatal("media transport policy changed")
	}
}

func TestProbeAccountStatusTimeoutIsExplicitAndDoesNotRetry(t *testing.T) {
	f := newAccountFixture(t)
	view := startAccountTest(t, f)
	f.statusDelay = 30 * time.Millisecond
	f.node.login.statusClient.Timeout = 5 * time.Millisecond
	code, _, body := pollAccountTest(t, f, view.SessionID)
	if code != 409 || !strings.Contains(body, "不表示二维码已过期") || strings.Contains(body, "cookie-sign") {
		t.Fatalf("unsafe or ambiguous timeout: %d %s", code, body)
	}
	if len(f.calls) != 3 {
		t.Fatal("timeout retried")
	}
	if f.node.login.session.view.Phase != "failed" {
		t.Fatal("timeout concealed")
	}
}
