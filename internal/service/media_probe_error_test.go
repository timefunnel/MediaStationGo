package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type probeResponseCodeError int

func (e probeResponseCodeError) Error() string     { return "upstream response rejected" }
func (e probeResponseCodeError) ResponseCode() int { return int(e) }

func TestMediaProbeFailureClassificationDoesNotGuessRiskFromTimeoutOr403(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		kind string
		http int
		code string
	}{
		{"provider code", fmt.Errorf("resolve: %w", probeResponseCodeError(40140117)), "risk_control", 0, "40140117"},
		{"nested provider code", errors.New(`OpenList error: {"errno":40140117,"error":"签名刷新过于频繁"}`), "risk_control", 0, "40140117"},
		{"explicit risk", errors.New("115: 访问过于频繁，请稍后再试"), "risk_control", 0, ""},
		{"http429", errors.New("HTTP error 429 Too Many Requests"), "rate_limited", 429, ""},
		{"http403", errors.New("HTTP error 403 Forbidden"), "access_denied", 403, ""},
		{"OpenList token403", errors.New("OpenList: api link denied (403)；需要管理员 Token"), "access_denied", 0, "403"},
		{"http401", errors.New("returned http 401"), "unauthorized", 401, ""},
		{"timeout", fmt.Errorf("read: %w", context.DeadlineExceeded), "timeout", 0, ""},
		{"canceled", context.Canceled, "canceled", 0, ""},
		{"unknown kill", errors.New("signal: killed"), "probe_failed", 0, ""},
		{"filename is not a code", errors.New("read /synthetic/40140117.mkv failed"), "probe_failed", 0, ""},
		{"signed URL is not a code", errors.New("read https://synthetic.invalid/40140117?token=abc failed"), "probe_failed", 0, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classifyMediaProbeFailure(test.err)
			if got.Kind != test.kind || got.HTTPStatus != test.http || got.ProviderCode != test.code {
				t.Fatalf("classification=%+v", got)
			}
		})
	}
}

func TestMediaProbeStderrHelper(t *testing.T) {
	if os.Getenv("MSG_PROBE_STDERR_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stderr, "HTTP error 429 Too Many Requests")
	fmt.Fprintln(os.Stderr, "https://synthetic.invalid/media?token=URL_SECRET")
	fmt.Fprintln(os.Stderr, "Authorization: Bearer AUTH_SECRET")
	fmt.Fprintln(os.Stderr, "Cookie: UID=COOKIE_SECRET; SEID=SESSION_SECRET")
	fmt.Fprintln(os.Stderr, strings.Repeat("diagnostic ", 300))
	os.Exit(1)
}

func TestMediaProbeCommandKeepsHTTPFailureButRedactsDiagnostics(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestMediaProbeStderrHelper$")
	command.Env = append(os.Environ(), "MSG_PROBE_STDERR_HELPER=1")
	_, err := command.Output()
	if err == nil {
		t.Fatal("helper should fail")
	}
	wrapped := mediaProbeCommandFailure(t.Context(), "remote ffprobe", err)
	failure := classifyMediaProbeFailure(wrapped)
	if failure.Kind != "rate_limited" || failure.HTTPStatus != 429 || !strings.Contains(wrapped.Error(), "HTTP error 429") {
		t.Fatalf("failure=%+v error=%s", failure, wrapped)
	}
	for _, secret := range []string{"URL_SECRET", "AUTH_SECRET", "COOKIE_SECRET", "SESSION_SECRET", "synthetic.invalid"} {
		if strings.Contains(wrapped.Error(), secret) {
			t.Fatalf("diagnostic exposes %s", secret)
		}
	}
	if len([]rune(safeMediaProbeQueueError(wrapped))) > 1200 {
		t.Fatal("diagnostic is not bounded")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatal("missing command diagnostics")
	}
	exitErr.Stderr = []byte(strings.Repeat("diagnostic ", 300) + "\nHTTP error 429 Too Many Requests")
	if classifyMediaProbeFailure(mediaProbeCommandFailure(t.Context(), "ffprobe", err)).Kind != "rate_limited" {
		t.Fatal("classification lost evidence beyond the display limit")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if classifyMediaProbeFailure(mediaProbeCommandFailure(ctx, "ffprobe", errors.New("signal: killed"))).Kind != "canceled" {
		t.Fatal("cancel lost")
	}
	deadline, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	if classifyMediaProbeFailure(mediaProbeCommandFailure(deadline, "ffprobe", errors.New("signal: killed"))).Kind != "timeout" {
		t.Fatal("deadline lost")
	}
}

func TestMediaProbeRiskRecordSurvivesReloadAndIsVisibleOutsideFailurePage(t *testing.T) {
	queue, probe := newExplicitProbeQueue(t)
	seedExplicitProbeMedia(t, queue, "a", "b")
	core, logs := observer.New(zap.WarnLevel)
	queue.log = zap.New(core)
	probe.failure = errors.New("HTTP error 429 Too Many Requests https://synthetic.invalid/?token=PRIVATE")
	startExplicitQueue(t, queue)
	waitExplicitQueueStopped(t, queue)
	reloaded := NewMediaProbeQueueService(queue.repo, queue.stream, probe, zap.NewNop())
	snapshot, err := reloaded.Snapshot(t.Context(), "pending", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 1 || snapshot.RiskAlert == nil || snapshot.RiskAlert.Status != "failed" || snapshot.RiskAlert.FailureKind != "rate_limited" || snapshot.RiskAlert.HTTPStatus != 429 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if probe.count() != 1 || snapshot.Running {
		t.Fatal("risk caused extra reads")
	}
	if logs.Len() != 1 || logs.All()[0].ContextMap()["failure_kind"] != "rate_limited" {
		t.Fatalf("logs=%v", logs.All())
	}
	if strings.Contains(snapshot.RiskAlert.Error, "PRIVATE") || strings.Contains(logs.All()[0].ContextMap()["error"].(string), "PRIVATE") {
		t.Fatal("signed address was recorded")
	}
	previous := snapshot.RiskAlert
	if err := reloaded.UpdateJob(t.Context(), previous.MediaID, "retry"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = reloaded.Snapshot(t.Context(), "pending", 1)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RiskAlert == nil || snapshot.RiskAlert.Error != previous.Error || snapshot.RiskAlert.FinishedAt == nil || !snapshot.RiskAlert.FinishedAt.Equal(*previous.FinishedAt) || probe.count() != 1 {
		t.Fatal("requeue lost the previous warning or started a new read")
	}
}

func TestMediaProbeRedactsStructuredCredentialsBeforeRecording(t *testing.T) {
	err := errors.New(`failure {"Authorization":"Bearer AUTH_SECRET","Cookie":"UID=COOKIE_SECRET; SEID=SESSION_SECRET","access_token":"ACCESS_SECRET"}`)
	text := safeMediaProbeQueueError(err)
	for _, secret := range []string{"AUTH_SECRET", "COOKIE_SECRET", "SESSION_SECRET", "ACCESS_SECRET"} {
		if strings.Contains(text, secret) {
			t.Fatalf("exposed %s in %s", secret, text)
		}
	}
}
