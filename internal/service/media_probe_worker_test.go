package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type workerFixture struct {
	mu              sync.Mutex
	server          *httptest.Server
	manifest        *ProbeExchangeManifest
	plan            *ProbeWorkerPlan
	options         ProbeWorkerRunOptions
	deps            probeWorkerDependencies
	calls           map[string]int
	waits           []time.Duration
	sharedStatus    int
	sharedComplete  bool
	sharedWrongSize bool
	sharedInvalid   bool
	identity        string
	businessCode    int
	duplicateName   bool
	nested          bool
	probeCalls      int
	probeErr        error
}

func newWorkerFixture(t *testing.T, duplicateContent bool) *workerFixture {
	t.Helper()
	f := &workerFixture{calls: map[string]int{}, sharedStatus: 200, identity: "42"}
	f.manifest = &ProbeExchangeManifest{Version: ProbeExchangeVersion, ID: "synthetic-manifest", CreatedAt: time.Now().UTC(), Items: []ProbeExchangeItem{}}
	for _, name := range []string{"a", "b"} {
		f.manifest.Items = append(f.manifest.Items, ProbeExchangeItem{MediaID: name, LibraryID: "library-a", RootID: "root-a", RelativePath: name + ".mkv", SizeBytes: 1000, Fingerprint: strings.Repeat(name, 64), Metadata: &ProbeResult{}})
	}
	var err error
	f.manifest.Digest, err = probeExchangeDigest(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	batch := ProbeWorkerBatch{ID: "batch-a", AccountID: "42", Provenance: "synthetic original copy", OriginalCopy: true, MediaIDs: []string{"a", "b"}, Roots: []ProbeWorkerRoot{{RootID: "root-a", DirectoryID: "100"}}, Limits: DefaultProbeWorkerLimits()}
	f.plan, err = CreateProbeWorkerPlan(f.manifest, batch)
	if err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls[r.URL.Path]++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/shared" && r.URL.Path != "/media" && r.Header.Get("Authorization") != "Bearer synthetic-access-token" {
			t.Errorf("missing synthetic authorization")
		}
		switch r.URL.Path {
		case "/open/user/info":
			fmt.Fprintf(w, `{"state":true,"data":{"user_id":%q}}`, f.identity)
		case "/open/ufile/files":
			if f.businessCode != 0 {
				fmt.Fprintf(w, `{"state":false,"code":%d}`, f.businessCode)
				return
			}
			parent := "100"
			if f.nested && r.URL.Query().Get("cid") == "100" {
				io.WriteString(w, `{"state":true,"count":1,"data":[{"fid":"101","pid":"100","fc":"0","fn":"folder"}]}`)
				return
			}
			if f.nested {
				parent = "101"
			}
			rows := []map[string]any{}
			for index, name := range []string{"a", "b"} {
				hash := strings.Repeat(strings.ToUpper(name), 40)
				if duplicateContent {
					hash = strings.Repeat("A", 40)
				}
				if f.duplicateName {
					name = "a"
				}
				rows = append(rows, map[string]any{"fid": fmt.Sprint(201 + index), "pid": parent, "fc": "1", "fn": name + ".mkv", "pc": fmt.Sprint(201 + index), "sha1": hash, "fs": "1000"})
			}
			json.NewEncoder(w).Encode(map[string]any{"state": true, "count": 2, "data": rows})
		case "/open/ufile/downurl":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			id := r.Form.Get("pick_code")
			hash := strings.Repeat("A", 40)
			if id == "202" && !duplicateContent {
				hash = strings.Repeat("B", 40)
			}
			json.NewEncoder(w).Encode(map[string]any{"state": true, "data": map[string]any{id: map[string]any{"file_size": 1000, "sha1": hash, "pick_code": id, "url": map[string]string{"url": f.server.URL + "/media?token=synthetic-signed-secret"}}}})
		case "/shared":
			w.WriteHeader(f.sharedStatus)
			if f.sharedStatus != 200 {
				return
			}
			var req struct {
				SHA1s []string `json:"sha1s"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			rows := []map[string]any{}
			for _, hash := range req.SHA1s {
				var data any
				if f.sharedComplete {
					size := 1000
					if f.sharedWrongSize {
						size++
					}
					payload := fmt.Sprintf(`{"MediaSourceInfo":{"Size":%d,"RunTimeTicks":1200000000,"Container":"mkv","Bitrate":1000000,"MediaStreams":[{"Type":"Video","Codec":"hevc","Width":1920,"Height":1080,"BitDepth":10,"VideoRangeType":"HDR10"},{"Type":"Audio","Codec":"aac","Channels":2,"SampleRate":48000}]}}`, size)
					var buffer bytes.Buffer
					writer := gzip.NewWriter(&buffer)
					writer.Write([]byte(payload))
					writer.Close()
					data = base64.StdEncoding.EncodeToString(buffer.Bytes())
				}
				if f.sharedInvalid {
					data = "not-base64"
				}
				rows = append(rows, map[string]any{"sha1": hash, "data": data})
			}
			json.NewEncoder(w).Encode(rows)
		case "/media":
			if r.Header.Get("Authorization") != "" {
				t.Error("115 control credential leaked to media host")
			}
			if r.Header.Get("User-Agent") != probeWorkerUA {
				t.Error("UA changed between link and read")
			}
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
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(f.server.Close)
	client := f.server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	f.deps = probeWorkerDependencies{control: client, media: client, shared: client, base: f.server.URL, authBase: f.server.URL, sharedURL: f.server.URL + "/shared",
		validateURL: func(raw string) error {
			if !strings.HasPrefix(raw, f.server.URL+"/") {
				return errors.New("nonfixture media URL")
			}
			return nil
		},
		wait: func(ctx context.Context, d time.Duration) error {
			f.mu.Lock()
			f.waits = append(f.waits, d)
			f.mu.Unlock()
			return ctx.Err()
		},
		probe: func(ctx context.Context, localURL string) (*ProbeResult, error) {
			f.probeCalls++
			if f.probeErr != nil {
				return nil, f.probeErr
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, localURL, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Range", "bytes=0-9")
			resp, err := client.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != 206 {
				return nil, fmt.Errorf("local read HTTP %d", resp.StatusCode)
			}
			if _, err := io.ReadAll(resp.Body); err != nil {
				return nil, err
			}
			return &ProbeResult{DurationSec: 120, Width: 1920, Height: 1080, VideoCodec: "hevc", AudioCodec: "aac"}, nil
		}}
	dir := t.TempDir()
	f.options = ProbeWorkerRunOptions{CredentialsFile: filepath.Join(dir, "secret.json"), StateFile: filepath.Join(dir, "state.json"), ApprovedDigest: f.plan.Digest, ToolVersion: "synthetic"}
	if err := WriteProbeWorkerJSON(f.options.CredentialsFile, probeWorkerCredentials{UserID: "42", AccessToken: "synthetic-access-token", RefreshToken: "synthetic-refresh-token"}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *workerFixture) regenerate(t *testing.T) {
	t.Helper()
	var err error
	f.manifest.Digest = ""
	f.manifest.Digest, err = probeExchangeDigest(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	f.plan, err = CreateProbeWorkerPlan(f.manifest, f.plan.Batch)
	if err != nil {
		t.Fatal(err)
	}
	f.options.ApprovedDigest = f.plan.Digest
}

func TestProbeWorkerDeduplicatesReadsAndNeverLeaksCredentials(t *testing.T) {
	f := newWorkerFixture(t, true)
	bundle, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Stats.ControlRequests != 3 || bundle.Stats.LinkRequests != 1 || bundle.Stats.ProbeCalls != 1 || bundle.Stats.MediaRequests != 1 || bundle.Stats.MediaBytes != 10 || f.probeCalls != 1 || len(bundle.Results) != 2 {
		t.Fatalf("unexpected stats: %+v", bundle.Stats)
	}
	for _, row := range bundle.Results {
		if row.Status != "completed" || !row.EstimatedBitRate || row.Metadata.Width != 1920 {
			t.Fatalf("result=%+v", row)
		}
	}
	data, _ := json.Marshal(bundle)
	for _, secret := range []string{"synthetic-access-token", "synthetic-refresh-token", "synthetic-signed-secret", f.server.URL} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("secret leaked into result")
		}
	}
	if len(f.waits) < 2 || f.waits[1] < 4*time.Second {
		t.Fatalf("control rate limiter not applied: %+v", f.waits)
	}
	if _, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps); err == nil {
		t.Fatal("one-time plan replayed")
	}
	if _, err := os.Stat(f.options.CredentialsFile + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("credential lock left behind")
	}
}

func TestProbeWorkerSharedHitAvoidsAllLinksAndReads(t *testing.T) {
	f := newWorkerFixture(t, true)
	f.sharedComplete = true
	bundle, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Stats.SharedLookups != 1 || bundle.Stats.SharedHits != 1 || bundle.Stats.LinkRequests != 0 || bundle.Stats.ProbeCalls != 0 || f.probeCalls != 0 {
		t.Fatalf("shared hit read media: %+v", bundle.Stats)
	}
	if bundle.Results[0].Metadata.VideoRange != "HDR10" || bundle.Results[0].Source != "shared" {
		t.Fatalf("bad shared parse: %+v", bundle.Results[0])
	}
}

func TestProbeWorkerKnownDurationEstimatesBitrateWithoutMediaRead(t *testing.T) {
	f := newWorkerFixture(t, true)
	for index := range f.manifest.Items {
		f.manifest.Items[index].Metadata = &ProbeResult{DurationSec: 120, Width: 1920, Height: 1080, VideoCodec: "hevc", AudioCodec: "aac"}
	}
	f.regenerate(t)
	bundle, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Stats.LinkRequests != 0 || bundle.Stats.SharedRequests != 0 || bundle.Stats.ProbeCalls != 0 || !bundle.Results[0].EstimatedBitRate || bundle.Results[0].Metadata.BitRate != 66 {
		t.Fatalf("derived bitrate fetched media: %+v", bundle)
	}
}

func TestProbeWorkerStopsOnSharedErrorsWithoutProbeFallback(t *testing.T) {
	for _, kind := range []string{"http", "size", "encoding"} {
		t.Run(kind, func(t *testing.T) {
			f := newWorkerFixture(t, false)
			switch kind {
			case "http":
				f.sharedStatus = 503
			case "size":
				f.sharedComplete = true
				f.sharedWrongSize = true
			case "encoding":
				f.sharedInvalid = true
			}
			bundle, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
			if err == nil || bundle.Stats.LinkRequests != 0 || bundle.Stats.ProbeCalls != 0 || f.probeCalls != 0 || bundle.StopReason == "" {
				t.Fatalf("shared error expanded work: %+v %v", bundle, err)
			}
		})
	}
}

func TestProbeWorkerDoesNotRefreshOrRetryBusinessFailure(t *testing.T) {
	f := newWorkerFixture(t, false)
	f.businessCode = 40140116
	bundle, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
	if err == nil || bundle.Stats.ControlRequests != 2 || bundle.Stats.RefreshRequests != 0 || bundle.Stats.ListRequests != 1 || f.calls["/open/ufile/files"] != 1 || bundle.Stats.SharedRequests != 0 {
		t.Fatalf("business failure retried: %+v %v", bundle.Stats, err)
	}
}

func TestProbeWorkerMappingAccountAndListBudgetStopBeforeProbe(t *testing.T) {
	for _, kind := range []string{"account", "duplicate", "budget"} {
		t.Run(kind, func(t *testing.T) {
			f := newWorkerFixture(t, false)
			switch kind {
			case "account":
				f.identity = "999"
			case "duplicate":
				f.duplicateName = true
			case "budget":
				f.nested = true
				f.plan.Batch.Limits.ListRequests = 1
				for i := range f.manifest.Items {
					f.manifest.Items[i].RelativePath = "folder/" + f.manifest.Items[i].RelativePath
				}
				f.regenerate(t)
			}
			bundle, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
			if err == nil || bundle.Stats.LinkRequests != 0 || bundle.Stats.SharedRequests != 0 || f.probeCalls != 0 {
				t.Fatalf("invalid mapping continued: %+v %v", bundle.Stats, err)
			}
			if kind == "budget" && f.calls["/open/ufile/files"] != 1 {
				t.Fatal("list budget exceeded")
			}
			if kind == "account" && bundle.Stats.ListRequests != 0 {
				t.Fatal("wrong account listed")
			}
		})
	}
}

func TestProbeWorkerFailureStopsNextFileAndExplicitResumeReusesCheckpoint(t *testing.T) {
	f := newWorkerFixture(t, false)
	f.probeErr = errors.New("synthetic probe failure https://private.invalid/?token=secret")
	bundle, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
	if err == nil || bundle.Stats.ProbeCalls != 1 || bundle.Stats.LinkRequests != 1 || strings.Contains(bundle.StopReason, "token=secret") {
		t.Fatalf("failure did not stop: %+v %v", bundle, err)
	}
	f.probeErr = nil
	f.regenerate(t)
	bundle, err = runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Stats.ListRequests != 0 || bundle.Stats.ProbeCalls != 2 || f.calls["/open/ufile/files"] != 1 {
		t.Fatalf("checkpoint lost: %+v", bundle.Stats)
	}
}

func TestProbeWorkerCanceledBeforeDispatchMakesNoRequest(t *testing.T) {
	f := newWorkerFixture(t, false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	bundle, err := runProbeWorker(ctx, f.manifest, f.plan, f.options, f.deps)
	if err == nil || bundle.Stats.ControlRequests != 0 || len(f.calls) != 0 {
		t.Fatalf("canceled run dispatched: %+v %v", bundle, err)
	}
}

func TestProbeWorkerRejectsUnsafeURLAndMalformedJSON(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/file", "https://115cdn.net.evil.invalid/file", "https://user:pass@a.115cdn.net/file", "ftp://a.115cdn.net/file", "https://a.115cdn.net:8443/file"} {
		if validateProbeMediaURL(raw) == nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
	if err := validateProbeMediaURL("https://a.115cdn.net/file?signature=synthetic"); err != nil {
		t.Fatal(err)
	}
	for _, contents := range []string{`{"value":1}garbage`, `{"value":1} {}`, `{"unknown":1}`} {
		filename := filepath.Join(t.TempDir(), "bad.json")
		os.WriteFile(filename, []byte(contents), 0600)
		var dst struct {
			Value int `json:"value"`
		}
		if ReadProbeWorkerJSON(filename, &dst) == nil {
			t.Fatal("malformed/unknown JSON accepted")
		}
	}
}
