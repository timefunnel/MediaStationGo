package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testWorkerGate() *probeWorkerGate {
	return &probeWorkerGate{limits: DefaultProbeWorkerLimits(), wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}
}

func TestProbeWorkerReadHardLimitsBodyBytesAndRequests(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		start, end, err := probeWorkerRange(r.Header.Get("Range"), 1000)
		if err != nil {
			t.Error(err)
			w.WriteHeader(416)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/1000", start, end))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(206)
		w.Write(bytes.Repeat([]byte("x"), int(end-start+1)))
	}))
	defer upstream.Close()
	gate := testWorkerGate()
	gate.limits.MediaBytes = 8
	gate.limits.MediaBytesFile = 8
	guard := &probeWorkerReadGuard{ctx: t.Context(), gate: gate, client: upstream.Client(), file: probeWorkerFile{Size: 1000}, url: upstream.URL, route: "/private", validateURL: func(string) error { return nil }}
	local := httptest.NewServer(guard)
	defer local.Close()
	for index := 0; index < 2; index++ {
		req, _ := http.NewRequest(http.MethodGet, local.URL+"/private", nil)
		req.Header.Set("Range", "bytes=0-999")
		resp, err := local.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 && (resp.StatusCode != 206 || len(data) != 8) {
			t.Fatalf("unexpected bounded read: %d %d", resp.StatusCode, len(data))
		}
		if index == 1 && resp.StatusCode != 429 {
			t.Fatalf("byte limit did not stop next request: %d", resp.StatusCode)
		}
	}
	if gate.snapshot().MediaBytes != 8 || gate.snapshot().MediaRequests != 1 || upstreamCalls.Load() != 1 {
		t.Fatalf("over budget: %+v upstream=%d", gate.snapshot(), upstreamCalls.Load())
	}
}

func TestProbeWorkerCountsRedirectAndRefusesRangeIgnored(t *testing.T) {
	for _, kind := range []string{"redirect", "ignored"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if kind == "redirect" {
					w.Header().Set("Location", "/again")
					w.WriteHeader(302)
					return
				}
				w.Header().Set("Content-Length", "1000")
				w.WriteHeader(200)
				w.Write(bytes.Repeat([]byte("x"), 1000))
			}))
			defer upstream.Close()
			client := upstream.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			gate := testWorkerGate()
			gate.limits.MediaRequestsFile = 1
			gate.limits.MediaBytesFile = 10
			guard := &probeWorkerReadGuard{ctx: t.Context(), gate: gate, client: client, file: probeWorkerFile{Size: 1000}, url: upstream.URL, route: "/private", validateURL: func(string) error { return nil }}
			request := httptest.NewRequest(http.MethodGet, "http://localhost/private", nil)
			request.Header.Set("Range", "bytes=0-9")
			recorder := httptest.NewRecorder()
			guard.ServeHTTP(recorder, request)
			if guard.err == nil || calls.Load() != 1 || gate.snapshot().MediaRequests != 1 || gate.snapshot().MediaBytes != 0 {
				t.Fatalf("unexpected extra read: %v %+v calls=%d", guard.err, gate.snapshot(), calls.Load())
			}
		})
	}
}

func TestProbeWorkerCancelInterruptsCurrentMediaRead(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer upstream.Close()
	ctx, cancel := context.WithCancel(t.Context())
	gate := testWorkerGate()
	finished := make(chan error, 1)
	go func() {
		_, err := runProbeWorkerRead(ctx, probeWorkerFile{Size: 1000}, upstream.URL, gate, upstream.Client(), func(string) error { return nil }, func(ctx context.Context, raw string) (*ProbeResult, error) {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
			resp, err := upstream.Client().Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			return nil, err
		})
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("media read did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("canceled probe succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not interrupt active read")
	}
	if gate.snapshot().MediaRequests != 1 {
		t.Fatal("canceled read retried")
	}
}

func TestProbeWorkerRangeValidation(t *testing.T) {
	for _, test := range []struct {
		value      string
		start, end int64
	}{{"", 0, 99}, {"bytes=0-", 0, 99}, {"bytes=90-999", 90, 99}, {"bytes=-10", 90, 99}, {"bytes=1-2", 1, 2}} {
		start, end, err := probeWorkerRange(test.value, 100)
		if err != nil || start != test.start || end != test.end {
			t.Fatalf("range %s: %d %d %v", test.value, start, end, err)
		}
	}
	for _, value := range []string{"bytes=100-", "bytes=-0", "bytes=2-1", "bytes=1-2,3-4", "items=0-1", "bytes=garbage-2"} {
		if _, _, err := probeWorkerRange(value, 100); err == nil {
			t.Fatalf("bad range accepted: %s", value)
		}
	}
}

func TestProbeWorkerBudgetIsConsumedBeforeNetworkAndCancelStopsWait(t *testing.T) {
	gate := testWorkerGate()
	gate.limits.ListRequests = 1
	dispatched := 0
	gate.onRequest = func(ProbeWorkerStats) error { return errors.New("synthetic checkpoint failure") }
	if err := gate.take(t.Context(), "list"); err == nil {
		dispatched++
	}
	if dispatched != 0 || gate.snapshot().ListRequests != 1 {
		t.Fatal("network dispatched without durable budget")
	}
	gate.onRequest = nil
	if err := gate.take(t.Context(), "list"); !errors.Is(err, errProbeWorkerBudget) {
		t.Fatalf("request budget exceeded: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := gate.take(ctx, "identity"); err == nil || gate.snapshot().IdentityRequests != 0 {
		t.Fatal("canceled wait consumed a request")
	}
}

func TestProbeWorkerSharedParserRejectsSizeAndScalarMismatch(t *testing.T) {
	// Exercise the parser's strict identity and scalar validation separately
	// from the network fixture. A movie-title match never substitutes for size.
	_, err := parseProbeWorkerEmby([]byte(`{"MediaSourceInfo":{"Size":100,"RunTimeTicks":10000000}}`), 101)
	if err == nil || !strings.Contains(err.Error(), "大小") {
		t.Fatal("shared size mismatch accepted")
	}
	_, err = parseProbeWorkerEmby([]byte(`{"MediaSourceInfo":{"Size":100,"MediaStreams":[{"Type":"Video","Width":999999999,"Codec":"hevc"}]}}`), 100)
	if err == nil {
		t.Fatal("out of range technical data accepted")
	}
}

func TestProbeWorkerSuccessfulEarlyReadDoesNotBecomeCancellationFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Header().Set("Content-Range", "bytes 0-999/1000")
		w.WriteHeader(206)
		w.Write([]byte("0123456789"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	gate := testWorkerGate()
	probe := func(ctx context.Context, raw string) (*ProbeResult, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		resp, err := upstream.Client().Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		buffer := make([]byte, 10)
		if _, err := io.ReadFull(resp.Body, buffer); err != nil {
			return nil, err
		}
		return &ProbeResult{DurationSec: 120}, nil
	}
	result, err := runProbeWorkerRead(t.Context(), probeWorkerFile{Size: 1000}, upstream.URL, gate, upstream.Client(), func(string) error { return nil }, probe)
	if err != nil || result.DurationSec != 120 {
		t.Fatalf("normal early close treated as a failed probe: %+v %v", result, err)
	}
}
