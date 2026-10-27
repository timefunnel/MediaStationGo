package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type transferFixture struct {
	*workerFixture
	sources                                                                          *ProbeWorkerSources
	directories                                                                      map[string][]probe115Entry
	nextDirectory                                                                    int
	cookieIdentity                                                                   string
	receiveLostAck, mkdirLostAck, hideFile, wrongHash, duplicateSource, nestedSource bool
	shareDenied, receiveDenied                                                       bool
	receiveStarted                                                                   chan struct{}
}

const syntheticProbeCookie = "UID=42_A1_synthetic; CID=synthetic-cid; SEID=synthetic-seid"

func newTransferFixture(t *testing.T) *transferFixture {
	t.Helper()
	f := &transferFixture{workerFixture: newWorkerFixture(t, false), directories: map[string][]probe115Entry{"100": {}}, nextDirectory: 300, cookieIdentity: "42"}
	f.sources = &ProbeWorkerSources{Shares: map[string]ProbeWorkerShareSource{"source-a": {RootID: "root-a", ShareURL: "https://115.com/s/syntheticshare?password=x1y2", DirectoryID: "0"}}}
	f.plan.Batch.Roots[0].SourceID = "source-a"
	f.plan.Batch.Limits = DefaultProbeWorkerTransferLimits()
	f.options.SourcesFile = filepath.Join(filepath.Dir(f.options.StateFile), "sources.json")
	if err := WriteProbeWorkerJSON(f.options.SourcesFile, f.sources); err != nil {
		t.Fatal(err)
	}
	if err := WriteProbeWorkerJSON(f.options.CredentialsFile, probeWorkerCredentials{UserID: "42", AccessToken: "synthetic-access-token", Cookie: syntheticProbeCookie}); err != nil {
		t.Fatal(err)
	}
	f.replan(t)
	f.deps.shareBase, f.deps.cookieBase = f.server.URL+"/webapi", f.server.URL
	f.sharedComplete = true
	previous := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files/uploadinfo", "/webapi/share/snap", "/webapi/share/receive", "/open/folder/add", "/open/ufile/files", "/open/ufile/downurl":
		default:
			previous.ServeHTTP(w, r)
			return
		}
		f.mu.Lock()
		f.calls[r.URL.Path]++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/open/") {
			if r.Header.Get("Authorization") != "Bearer synthetic-access-token" || r.Header.Get("Cookie") != "" {
				t.Error("open API credentials crossed roles")
			}
		} else if r.Header.Get("Authorization") != "" {
			t.Error("token leaked to share API")
		}
		switch r.URL.Path {
		case "/files/uploadinfo":
			if r.Header.Get("Cookie") != syntheticProbeCookie {
				t.Error("Cookie identity missing")
			}
			fmt.Fprintf(w, `{"errno":0,"user_id":%q}`, f.cookieIdentity)
		case "/webapi/share/snap":
			if f.shareDenied {
				io.WriteString(w, `{"state":false,"errno":403}`)
				return
			}
			if r.Header.Get("Cookie") != "" {
				t.Error("Cookie leaked to anonymous share listing")
			}
			if r.URL.Query().Get("share_code") != "syntheticshare" || r.URL.Query().Get("receive_code") != "x1y2" {
				t.Error("wrong source share")
			}
			if f.nestedSource && r.URL.Query().Get("cid") == "0" {
				io.WriteString(w, `{"state":true,"data":{"count":1,"list":[{"fid":"","cid":"500","n":"folder"}]}}`)
				return
			}
			rows := []map[string]any{}
			for i, name := range []string{"a", "b"} {
				if f.duplicateSource {
					name = "a"
				}
				rows = append(rows, map[string]any{"fid": fmt.Sprint(401 + i), "n": name + ".mkv", "s": "1000", "sha1": strings.Repeat(strings.ToUpper(name), 40)})
			}
			json.NewEncoder(w).Encode(map[string]any{"state": true, "data": map[string]any{"count": 2, "list": rows}})
		case "/open/folder/add":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			parent, name := r.Form.Get("pid"), r.Form.Get("file_name")
			if parent != "100" || !strings.HasPrefix(name, "msg-probe-") {
				t.Error("write outside dedicated parent")
			}
			var state probeWorkerState
			if err := ReadProbeWorkerJSON(f.options.StateFile, &state); err != nil {
				t.Error(err)
			}
			intent := false
			for _, slot := range state.Transfers {
				intent = intent || slot.Name == name && slot.Phase == "creating"
			}
			if !intent {
				t.Error("mkdir sent without durable intent")
			}
			f.nextDirectory++
			id := strconv.Itoa(f.nextDirectory)
			f.directories[parent] = append(f.directories[parent], probe115Entry{ID: id, Parent: parent, Category: "0", Name: name})
			f.directories[id] = []probe115Entry{}
			if f.mkdirLostAck {
				w.WriteHeader(503)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"state": true, "data": map[string]string{"file_id": id}})
		case "/open/ufile/files":
			id := r.URL.Query().Get("cid")
			rows, found := f.directories[id]
			if !found {
				t.Errorf("unexpected directory %s", id)
			}
			if f.hideFile && id != "100" {
				rows = []probe115Entry{}
			}
			json.NewEncoder(w).Encode(map[string]any{"state": true, "count": len(rows), "data": rows})
		case "/webapi/share/receive":
			if f.receiveStarted != nil {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				close(f.receiveStarted)
				<-r.Context().Done()
				return
			}
			if f.receiveDenied {
				io.WriteString(w, `{"state":false,"errno":4100024}`)
				return
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Header.Get("Cookie") != syntheticProbeCookie || r.Form.Get("share_code") != "syntheticshare" || r.Form.Get("receive_code") != "x1y2" {
				t.Error("wrong transfer credentials or source")
			}
			id, target := r.Form.Get("file_id"), r.Form.Get("cid")
			if len(f.directories[target]) != 0 {
				t.Error("receive into nonempty staging")
			}
			var state probeWorkerState
			if err := ReadProbeWorkerJSON(f.options.StateFile, &state); err != nil {
				t.Error(err)
			}
			intent := false
			for _, slot := range state.Transfers {
				intent = intent || slot.DirectoryID == target && slot.Phase == "receiving"
			}
			if !intent {
				t.Error("receive sent without durable intent")
			}
			name := "a"
			if id == "402" {
				name = "b"
			} else if id != "401" {
				t.Error("transferred an unselected source ID")
			}
			hash := strings.Repeat(strings.ToUpper(name), 40)
			if f.wrongHash {
				hash = strings.Repeat("C", 40)
			}
			fileID := "601"
			if name == "b" {
				fileID = "602"
			}
			f.directories[target] = []probe115Entry{{ID: fileID, Parent: target, Category: "1", Name: name + ".mkv", Size: 1000, SHA1: hash, PickCode: fileID}}
			if f.receiveLostAck {
				w.WriteHeader(503)
				return
			}
			io.WriteString(w, `{"state":true,"data":{}}`)
		case "/open/ufile/downurl":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			id := r.Form.Get("pick_code")
			hash := strings.Repeat("A", 40)
			if id == "602" {
				hash = strings.Repeat("B", 40)
			}
			json.NewEncoder(w).Encode(map[string]any{"state": true, "data": map[string]any{id: map[string]any{"file_size": 1000, "sha1": hash, "pick_code": id, "url": map[string]string{"url": f.server.URL + "/media?token=synthetic-signed-secret"}}}})
		}
	})
	return f
}

func (f *transferFixture) replan(t *testing.T) {
	t.Helper()
	var err error
	f.manifest.Digest = ""
	f.manifest.Digest, err = probeExchangeDigest(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	f.plan, err = CreateProbeWorkerPlanWithSources(f.manifest, f.plan.Batch, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	f.options.ApprovedDigest = f.plan.Digest
}

func TestProbeWorkerAutomaticTransferThenSharedOrProbe(t *testing.T) {
	for _, shared := range []bool{true, false} {
		t.Run(fmt.Sprint(shared), func(t *testing.T) {
			f := newTransferFixture(t)
			f.sharedComplete = shared
			bundle, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
			if err != nil {
				t.Fatal(err)
			}
			if bundle.Stats.ReceiveRequests != 2 || bundle.Stats.MkdirRequests != 2 || bundle.Stats.CookieIdentities != 1 || bundle.Stats.ShareRequests != 1 || bundle.SourcesDigest != f.plan.SourcesDigest {
				t.Fatalf("bad transfer stats: %+v", bundle.Stats)
			}
			for _, row := range bundle.Results {
				if row.Status != "completed" || row.Transfer == nil || row.Transfer.SHA1 != row.SHA1 || row.Transfer.SizeBytes != row.SizeBytes {
					t.Fatalf("unverified transfer: %+v", row)
				}
			}
			if shared && (bundle.Stats.ProbeCalls != 0 || bundle.Stats.LinkRequests != 0) {
				t.Fatal("shared hit unnecessarily read media")
			}
			if !shared && (bundle.Stats.ProbeCalls != 2 || bundle.Stats.LinkRequests != 2) {
				t.Fatal("probe stage was not reached after transfer")
			}
			data, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			planData, err := json.Marshal(f.plan)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"syntheticshare", "x1y2", "synthetic-seid", "synthetic-access-token", "synthetic-signed-secret"} {
				if bytes.Contains(data, []byte(secret)) || bytes.Contains(planData, []byte(secret)) {
					t.Fatal("private transfer input leaked")
				}
			}
		})
	}
}

func TestProbeWorkerTransferUncertainWritesAreReadReconciledWithoutReplay(t *testing.T) {
	for _, kind := range []string{"receive", "mkdir", "visibility"} {
		t.Run(kind, func(t *testing.T) {
			f := newTransferFixture(t)
			f.receiveLostAck, f.mkdirLostAck, f.hideFile = kind == "receive", kind == "mkdir", kind == "visibility"
			bundle, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
			if err == nil || bundle.Stats.SharedRequests != 0 || bundle.Stats.LinkRequests != 0 {
				t.Fatal("uncertain write continued")
			}
			if kind == "visibility" {
				f.replan(t)
				if _, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps); err == nil || f.calls["/webapi/share/receive"] != 1 {
					t.Fatal("invisible copy resubmitted")
				}
			}
			f.receiveLostAck, f.mkdirLostAck, f.hideFile = false, false, false
			f.replan(t)
			bundle, err = runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
			if err != nil {
				t.Fatal(err)
			}
			if f.calls["/webapi/share/receive"] != 2 || f.calls["/open/folder/add"] != 2 || bundle.Results[0].Status != "completed" {
				t.Fatalf("uncertain write replayed: %+v", f.calls)
			}
			f.replan(t)
			if _, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps); err != nil {
				t.Fatal(err)
			}
			if f.calls["/webapi/share/receive"] != 2 || f.calls["/open/folder/add"] != 2 {
				t.Fatal("completed copy transferred again")
			}
		})
	}
}

func TestProbeWorkerTransferRejectsWrongAccountSourceAndBudgets(t *testing.T) {
	for _, kind := range []string{"cookie", "identity", "source", "duplicate", "size", "hash", "budget", "receive_budget", "mkdir_budget", "share_denied", "receive_denied", "occupied"} {
		t.Run(kind, func(t *testing.T) {
			f := newTransferFixture(t)
			switch kind {
			case "cookie":
				if err := WriteProbeWorkerJSON(f.options.CredentialsFile, probeWorkerCredentials{UserID: "42", AccessToken: "synthetic-access-token", Cookie: "UID=99_A1_fake; CID=c; SEID=s"}); err != nil {
					t.Fatal(err)
				}
			case "identity":
				f.cookieIdentity = "99"
			case "source":
				s := f.sources.Shares["source-a"]
				s.ShareURL = "https://115.com/s/changedshare"
				f.sources.Shares["source-a"] = s
				if err := WriteProbeWorkerJSON(f.options.SourcesFile, f.sources); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				f.duplicateSource = true
			case "size":
				f.manifest.Items[0].SizeBytes++
				f.replan(t)
			case "hash":
				f.wrongHash = true
			case "budget":
				f.plan.Batch.Limits.ControlRequests = 2
				f.replan(t)
			case "receive_budget":
				f.plan.Batch.Limits.ReceiveRequests = 1
				f.replan(t)
			case "mkdir_budget":
				f.plan.Batch.Limits.MkdirRequests = 1
				f.replan(t)
			case "share_denied":
				f.shareDenied = true
			case "receive_denied":
				f.receiveDenied = true
			case "occupied":
				key := probeWorkerTransferKey(f.plan.Batch.Roots[0], "a.mkv")
				name := probeWorkerStagingName(&probeWorkerState{ManifestDigest: f.manifest.Digest, BatchID: f.plan.Batch.ID}, key)
				f.directories["100"] = []probe115Entry{{ID: "999", Parent: "100", Category: "0", Name: name}}
			}
			_, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
			if err == nil {
				t.Fatal("unsafe transfer accepted")
			}
			if kind == "receive_budget" || kind == "mkdir_budget" || kind == "hash" || kind == "receive_denied" {
				if f.calls["/webapi/share/receive"] != 1 {
					t.Fatal("failure expanded transfers")
				}
			} else if f.calls["/webapi/share/receive"] != 0 || f.calls["/open/folder/add"] != 0 {
				t.Fatal("invalid source/account reached writes")
			}
			if f.calls["/open/ufile/downurl"] != 0 || f.probeCalls != 0 {
				t.Fatal("failed transfer reached probe")
			}
		})
	}
}

func TestProbeWorkerTransferCancellationStopsCurrentWrite(t *testing.T) {
	f := newTransferFixture(t)
	f.receiveStarted = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := runProbeWorker(ctx, f.manifest, f.plan, f.options, f.deps); done <- err }()
	select {
	case <-f.receiveStarted:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("transfer never started")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled transfer succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled transfer did not exit")
	}
	if f.calls["/webapi/share/receive"] != 1 || f.calls["/shared"] != 0 || f.probeCalls != 0 {
		t.Fatal("cancelled transfer expanded work")
	}
}

func TestProbeWorkerTransferFailedResumeDoesNotPublishOldMappings(t *testing.T) {
	f := newTransferFixture(t)
	if _, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps); err != nil {
		t.Fatal(err)
	}
	f.identity = "99"
	f.replan(t)
	bundle, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps)
	if err == nil || bundle.Stats.CookieIdentities != 0 {
		t.Fatal("wrong token account reached transfer stage")
	}
	for _, row := range bundle.Results {
		if row.Status != "pending" || row.FileID != "" || row.Metadata != nil || row.Transfer != nil {
			t.Fatal("failed round published stale verified files")
		}
	}
}

func TestProbeWorkerTransferTraversesOnlySelectedSharePaths(t *testing.T) {
	f := newTransferFixture(t)
	f.nestedSource = true
	for i := range f.manifest.Items {
		f.manifest.Items[i].RelativePath = "folder/" + f.manifest.Items[i].RelativePath
	}
	f.replan(t)
	if _, err := runProbeWorker(t.Context(), f.manifest, f.plan, f.options, f.deps); err != nil {
		t.Fatal(err)
	}
	if f.calls["/webapi/share/snap"] != 2 {
		t.Fatal("source directories not reused")
	}
}

func TestProbeWorkerSourceValidationAndPlanPinning(t *testing.T) {
	f := newTransferFixture(t)
	if _, err := CreateProbeWorkerPlan(f.manifest, f.plan.Batch); err == nil {
		t.Fatal("automatic plan accepted without source config")
	}
	for _, raw := range []string{"http://115.com/s/abc", "https://115.com.evil.invalid/s/abc", "https://user:pass@115.com/s/abc", "https://115.com/s/abc?password=bad%0Acode", "https://115.com/s/abc#/list/share/4"} {
		if _, err := parseProbeWorkerShare(ProbeWorkerShareSource{ShareURL: raw, DirectoryID: "0"}); err == nil {
			t.Fatalf("unsafe source accepted: %q", raw)
		}
	}
	f.plan.SourcesDigest = ""
	f.plan.Digest = ""
	var err error
	f.plan.Digest, err = probeExchangeDigest(f.plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.plan.Check(f.manifest); err == nil {
		t.Fatal("automatic plan without source digest accepted")
	}
}
