package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"go.uber.org/zap"
)

type campaignFixture struct {
	service                  *MediaProbeCampaignService
	node                     *ProbeNode
	aCalls, bCalls, runs     atomic.Int32
	failShare, wrongIdentity atomic.Bool
}

func newCampaignFixture(t *testing.T, count int) *campaignFixture {
	t.Helper()
	ids := []string{}
	for i := 0; i < count; i++ {
		ids = append(ids, fmt.Sprintf("m%02d", i))
	}
	queue, _ := newExchangeFixture(t, ids...)
	if err := queue.repo.DB.AutoMigrate(&model.MediaProbeCampaign{}, &model.StorageConfig{}); err != nil {
		t.Fatal(err)
	}
	root := model.LibraryRoot{Base: model.Base{ID: "root-a"}, LibraryID: "library-a", Path: "cloud://openlist/Synthetic", Enabled: true}
	if err := queue.repo.DB.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	crypto := NewCryptoService("synthetic-campaign-key", zap.NewNop())
	storage := NewStorageConfigService(zap.NewNop(), queue.repo, crypto)
	if _, err := storage.Save(t.Context(), StorageInput{Type: "cloud115", Config: map[string]any{"cookie": "UID=7_test; CID=source-cid; SEID=source-secret"}}); err != nil {
		t.Fatal(err)
	}
	f := &campaignFixture{}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.aCalls.Add(1)
		if r.Header.Get("Cookie") != "UID=7_test; CID=source-cid; SEID=source-secret" || r.Header.Get("Authorization") != "" {
			t.Error("wrong A credentials")
		}
		switch r.URL.Path {
		case "/files/uploadinfo":
			if f.wrongIdentity.Load() {
				io.WriteString(w, `{"user_id":"8","errno":0}`)
			} else {
				io.WriteString(w, `{"user_id":"7","errno":0}`)
			}
		case "/share/send":
			if err := r.ParseForm(); err != nil || r.Form.Get("file_ids") != "700" {
				t.Error("unexpected A source range")
			}
			if f.failShare.Load() {
				io.WriteString(w, `{"state":false,"errno":911}`)
				return
			}
			io.WriteString(w, `{"state":true,"data":{"share_code":"syntheticshare","receive_code":"x1y2"}}`)
		default:
			t.Errorf("unexpected A request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(source.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.bCalls.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "Bearer synthetic-access-token" {
			t.Error("A credentials leaked to B upstream")
		}
		switch r.URL.Path {
		case "/open/user/info":
			io.WriteString(w, `{"state":true,"data":{"user_id":"42"}}`)
		case "/open/folder/add":
			if err := r.ParseForm(); err != nil || r.Form.Get("pid") != "0" || !strings.HasPrefix(r.Form.Get("file_name"), "msg-probe-") {
				t.Error("invalid staging creation")
			}
			io.WriteString(w, `{"state":true,"data":{"file_id":"100"}}`)
		default:
			t.Errorf("unexpected B request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	credentials := filepath.Join(dir, "credentials.json")
	if err := WriteProbeWorkerJSON(credentials, probeWorkerCredentials{UserID: "42", AccessToken: "synthetic-access-token", Cookie: syntheticProbeCookie}); err != nil {
		t.Fatal(err)
	}
	node, err := NewProbeNode(strings.Repeat("n", 32), credentials, dir, os.Args[0], "test")
	if err != nil {
		t.Fatal(err)
	}
	node.client, node.base, node.wait = upstream.Client(), upstream.URL, func(context.Context, time.Duration) error { return nil }
	node.run = func(_ context.Context, m *ProbeExchangeManifest, plan *ProbeWorkerPlan, options ProbeWorkerRunOptions) (*ProbeExchangeBundle, error) {
		f.runs.Add(1)
		if options.CredentialsFile != credentials || plan.Batch.AccountID != "42" {
			t.Error("worker credential boundary broken")
		}
		b := exchangeTestBundle(m)
		b.BatchID = plan.Batch.ID
		b.PlanDigest = plan.Digest
		b.SourcesDigest = plan.SourcesDigest
		b.Results = nil
		for _, id := range plan.Batch.MediaIDs {
			for _, item := range m.Items {
				if item.MediaID == id {
					b.Results = append(b.Results, ProbeExchangeResult{MediaID: id, Fingerprint: item.Fingerprint, FileID: "601", SHA1: strings.Repeat("A", 40), SizeBytes: item.SizeBytes, Status: "completed", Source: "shared",
						Metadata: &ProbeResult{DurationSec: 120, Width: 1920, Height: 1080, VideoCodec: "hevc", AudioCodec: "aac", BitRate: 800000},
						Transfer: &ProbeExchangeTransfer{SourceID: "root-a", SourceFileID: "401", SHA1: strings.Repeat("A", 40), SizeBytes: item.SizeBytes, DirectoryID: "301"}})
				}
			}
		}
		return b, nil
	}
	f.node = node
	nodeServer := httptest.NewServer(node)
	t.Cleanup(nodeServer.Close)
	pipeline := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/media-probe/roots" || r.Header.Get("Authorization") != "Bearer pipeline-secret" {
			t.Error("unexpected pipeline request")
		}
		io.WriteString(w, `{"roots":[{"root_id":"root-a","library_id":"library-a","path":"cloud://openlist/Synthetic","directory_id":"700"}]}`)
	}))
	t.Cleanup(pipeline.Close)
	cfg := &config.Config{MediaProbeWorker: config.MediaProbeWorkerConfig{URL: nodeServer.URL, Token: strings.Repeat("n", 32)}, ResourceImport: config.ResourceImportConfig{PipelineURL: pipeline.URL, PipelineToken: "pipeline-secret", SearchTimeoutSeconds: 5}}
	f.service = NewMediaProbeCampaignService(queue, storage, crypto, cfg, t.Context())
	f.service.aClient, f.service.aBase, f.service.wait = source.Client(), source.URL, func(context.Context, time.Duration) error { return nil }
	return f
}

func waitCampaign(t *testing.T, f *campaignFixture) *ProbeCampaignView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view, err := f.service.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if view.Status != "running" {
			return view
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("campaign did not finish")
	return nil
}

func TestProbeCampaignSelectLibraryInitializesTransfersAndImports(t *testing.T) {
	f := newCampaignFixture(t, 11)
	plan, err := f.service.Plan(t.Context(), []string{"library-a"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.aCalls.Load() != 0 || f.bCalls.Load() != 0 || plan.Files != 11 || plan.Batches != 2 || plan.SourceRequests != 2 || plan.ControlRequests != 165 {
		t.Fatalf("planning contacted 115 or budget wrong: %+v", plan)
	}
	if err := f.service.Start(t.Context(), plan.ID, plan.Digest, true); err != nil {
		t.Fatal(err)
	}
	view := waitCampaign(t, f)
	if view.Status != "completed" || view.FinishedBatches != 2 || view.Counts["completed"] != 11 || f.aCalls.Load() != 2 || f.bCalls.Load() != 2 || f.runs.Load() != 2 {
		t.Fatalf("incomplete campaign: %+v A=%d B=%d runs=%d", view, f.aCalls.Load(), f.bCalls.Load(), f.runs.Load())
	}
	var media []model.Media
	if err := f.service.queue.repo.DB.Find(&media).Error; err != nil {
		t.Fatal(err)
	}
	for _, m := range media {
		if m.DurationSec != 120 || m.BitRate != 800000 || !strings.HasPrefix(m.Path, "cloud://openlist/Synthetic/") {
			t.Fatalf("bad import: %+v", m)
		}
	}
	var row model.MediaProbeCampaign
	if err := f.service.queue.repo.DB.First(&row, "id = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(row.Payload, encPrefix) || strings.Contains(row.Payload, "syntheticshare") || strings.Contains(row.Payload, "source-secret") {
		t.Fatal("private checkpoint not encrypted")
	}
	data, _ := json.Marshal(view)
	if strings.Contains(string(data), "x1y2") || strings.Contains(string(data), "syntheticshare") {
		t.Fatal("public view exposed share")
	}
	var saved ProbeNodeBatch
	if err := ReadProbeWorkerJSON(filepath.Join(f.node.directory, plan.ID+"-1-request.json"), &saved); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(saved)
	request := httptest.NewRequest(http.MethodPost, "/v1/batch", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("n", 32))
	recorder := httptest.NewRecorder()
	f.node.ServeHTTP(recorder, request)
	if recorder.Code != 200 || f.runs.Load() != 2 {
		t.Fatalf("completed batch replayed: %d", recorder.Code)
	}
	saved.Batch.Roots[0].DirectoryID = "999"
	body, _ = json.Marshal(saved)
	request = httptest.NewRequest(http.MethodPost, "/v1/batch", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("n", 32))
	recorder = httptest.NewRecorder()
	f.node.ServeHTTP(recorder, request)
	if recorder.Code != 409 || f.runs.Load() != 2 {
		t.Fatal("accepted directory outside campaign")
	}
}

func TestProbeCampaignSmallSampleIsFixedAndRunsOnlyOneBatch(t *testing.T) {
	f := newCampaignFixture(t, 13)
	if err := f.service.queue.repo.DB.Model(&model.Media{}).Where("id = ?", "m00").Updates(map[string]any{
		"duration_sec": 120, "width": 1920, "height": 1080, "video_codec": "hevc", "audio_codec": "aac", "bit_rate": 800000,
	}).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := f.service.Plan(t.Context(), []string{"library-a"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MaxFiles != 5 || plan.Files != 5 || plan.Batches != 1 || plan.ControlRequests != 85 || f.aCalls.Load() != 0 || f.bCalls.Load() != 0 {
		t.Fatalf("sample plan expanded or contacted 115: %+v", plan)
	}
	state, err := f.service.load(t.Context(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Manifest.Items) != 5 {
		t.Fatalf("manifest contains more than the approved sample: %d", len(state.Manifest.Items))
	}
	for index, item := range state.Manifest.Items {
		if item.MediaID != fmt.Sprintf("m%02d", index+1) {
			t.Fatalf("sample included a complete or out-of-order file: %s", item.MediaID)
		}
	}
	if err := f.service.queue.repo.DB.Create(&model.Media{Base: model.Base{ID: "new-before-start"}, LibraryID: "library-a", Title: "Synthetic", Path: "cloud://openlist/Synthetic/!new.mkv", SizeBytes: 12000000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.service.Start(t.Context(), plan.ID, plan.Digest, true); err != nil {
		t.Fatal(err)
	}
	view := waitCampaign(t, f)
	if view.Status != "completed" || view.FinishedBatches != 1 || view.Counts["completed"] != 5 || f.runs.Load() != 1 {
		t.Fatalf("sample did not stop after one batch: %+v runs=%d", view, f.runs.Load())
	}
	var unchanged int64
	if err := f.service.queue.repo.DB.Model(&model.Media{}).Where("duration_sec = 0").Count(&unchanged).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged != 8 {
		t.Fatalf("records outside the fixed sample were changed: %d remain unchanged", unchanged)
	}
}

func TestProbeCampaignRejectsInvalidSampleSizeBeforePlanning(t *testing.T) {
	f := newCampaignFixture(t, 2)
	for _, limit := range []int{-1, 11} {
		if _, err := f.service.Plan(t.Context(), nil, limit); err == nil {
			t.Fatalf("invalid sample size accepted: %d", limit)
		}
	}
	var plans int64
	if err := f.service.queue.repo.DB.Model(&model.MediaProbeCampaign{}).Count(&plans).Error; err != nil {
		t.Fatal(err)
	}
	if plans != 0 || f.aCalls.Load() != 0 || f.bCalls.Load() != 0 {
		t.Fatal("invalid sample created a plan or contacted 115")
	}
}

func TestProbeCampaignStopsOnShareOrIdentityFailureWithoutBWork(t *testing.T) {
	for _, kind := range []string{"share", "identity", "root_changed"} {
		t.Run(kind, func(t *testing.T) {
			f := newCampaignFixture(t, 1)
			plan, err := f.service.Plan(t.Context(), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			f.failShare.Store(kind == "share")
			f.wrongIdentity.Store(kind == "identity")
			if kind == "root_changed" {
				f.service.queue.repo.DB.Model(&model.LibraryRoot{}).Where("id = ?", "root-a").Update("path", "cloud://openlist/Changed")
			}
			if err := f.service.Start(t.Context(), plan.ID, plan.Digest, true); err != nil {
				t.Fatal(err)
			}
			view := waitCampaign(t, f)
			if view.Status != "stopped" || view.Error == "" || f.bCalls.Load() != 0 || f.runs.Load() != 0 {
				t.Fatalf("continued after source failure: %+v", view)
			}
			calls := f.aCalls.Load()
			if err := f.service.Start(t.Context(), plan.ID, plan.Digest, true); err == nil || f.aCalls.Load() != calls {
				t.Fatal("failed plan replayed")
			}
		})
	}
}

func TestProbeCampaignRejectsSameAccountAndWrongApprovalWithout115(t *testing.T) {
	f := newCampaignFixture(t, 1)
	plan, err := f.service.Plan(t.Context(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Start(t.Context(), plan.ID, "wrong-digest", true); err == nil {
		t.Fatal("wrong approval accepted")
	}
	if err := f.service.Start(t.Context(), plan.ID, plan.Digest, false); err == nil {
		t.Fatal("unconfirmed campaign started")
	}
	if _, err := f.service.storage.Save(t.Context(), StorageInput{Type: "cloud115", Config: map[string]any{"cookie": syntheticProbeCookie}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Plan(t.Context(), nil, 0); err == nil {
		t.Fatal("same A and B accepted")
	}
	if f.aCalls.Load() != 0 || f.bCalls.Load() != 0 {
		t.Fatal("invalid plan contacted 115")
	}
}

func TestProbeCampaignFailureRetainsAppliedBatchAndNextPlanSkipsIt(t *testing.T) {
	f := newCampaignFixture(t, 11)
	previous := f.node.run
	f.node.run = func(ctx context.Context, m *ProbeExchangeManifest, p *ProbeWorkerPlan, o ProbeWorkerRunOptions) (*ProbeExchangeBundle, error) {
		if strings.HasSuffix(p.Batch.ID, "-2") {
			return nil, errors.New("synthetic transfer failure")
		}
		return previous(ctx, m, p, o)
	}
	plan, err := f.service.Plan(t.Context(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Start(t.Context(), plan.ID, plan.Digest, true); err != nil {
		t.Fatal(err)
	}
	view := waitCampaign(t, f)
	if view.Status != "stopped" || view.FinishedBatches != 1 || view.Counts["completed"] != 10 {
		t.Fatalf("lost completed batch or ignored failure: %+v", view)
	}
	next, err := f.service.Plan(t.Context(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if next.Files != 1 || next.Batches != 1 {
		t.Fatalf("completed rows scheduled again: %+v", next)
	}
}

func TestProbeCampaignRestartDoesNotResumeCloudOperations(t *testing.T) {
	f := newCampaignFixture(t, 1)
	plan, err := f.service.Plan(t.Context(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.service.load(t.Context(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.View.Status = "running"
	if err := f.service.save(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	view, err := f.service.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "interrupted" || f.aCalls.Load() != 0 || f.bCalls.Load() != 0 {
		t.Fatal("restart replayed cloud work")
	}
}

func TestProbeNodeUncertainStagingNeverRetriesWrite(t *testing.T) {
	f := newCampaignFixture(t, 1)
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open/user/info" {
			io.WriteString(w, `{"state":true,"data":{"user_id":"42"}}`)
			return
		}
		writes.Add(1)
		w.WriteHeader(503)
	}))
	defer server.Close()
	f.node.client, f.node.base = server.Client(), server.URL
	credentials, err := readProbeWorkerCredentials(f.node.credentials)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.node.staging(t.Context(), "uncertain", credentials); err == nil {
		t.Fatal("uncertain mkdir succeeded")
	}
	if _, err := f.node.staging(t.Context(), "uncertain", credentials); err == nil || writes.Load() != 1 {
		t.Fatal("uncertain mkdir replayed")
	}
}

func TestProbeNodeRejectsUnauthenticatedRequest(t *testing.T) {
	f := newCampaignFixture(t, 1)
	recorder := httptest.NewRecorder()
	f.node.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/staging", strings.NewReader(`{"id":"a","account_id":"42"}`)))
	if recorder.Code != 401 || f.bCalls.Load() != 0 {
		t.Fatal("unauthenticated cloud request")
	}
}

func TestProbeCampaignSourceKeepsExistingEgress(t *testing.T) {
	proxy := &url.URL{Scheme: "http", Host: "synthetic-proxy.invalid:8080"}
	storage := &StorageConfigService{client: &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}, Timeout: 120 * time.Second}}
	client := newProbeCampaignSourceClient(storage)
	transport := client.Transport.(*http.Transport)
	actual, err := transport.Proxy(httptest.NewRequest(http.MethodGet, "https://webapi.115.com/files/uploadinfo", nil))
	if err != nil || actual.String() != proxy.String() || !transport.DisableKeepAlives || client.Timeout > 30*time.Second {
		t.Fatal("A egress changed or request became unbounded")
	}
}

func TestProbeCampaignMatchesDecodedLibraryRoot(t *testing.T) {
	f := newCampaignFixture(t, 1)
	if err := f.service.queue.repo.DB.Model(&model.LibraryRoot{}).Where("id = ?", "root-a").Update("path", "cloud://openlist/%53ynthetic").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Plan(t.Context(), nil, 0); err != nil {
		t.Fatalf("same encoded root rejected: %v", err)
	}
	for _, pair := range [][2]string{
		{"cloud://openlist/115%2F%E7%94%B2", "cloud://openlist/115/甲"},
		{"cloud://openlist/Synthetic/", "cloud://openlist/Synthetic"},
	} {
		if !probeCampaignSameRootPath(pair[0], pair[1]) {
			t.Fatal("equivalent root paths differ")
		}
	}
	for _, other := range []string{"cloud://openlist/Synthetic2", "cloud://other/Synthetic", "cloud://openlist/Synthetic/child", "cloud://openlist/Synthetic?x=1", "cloud://openlist/%xx"} {
		if probeCampaignSameRootPath("cloud://openlist/Synthetic", other) {
			t.Fatalf("different root accepted: %s", other)
		}
	}
	if f.aCalls.Load() != 0 || f.bCalls.Load() != 0 {
		t.Fatal("root matching contacted 115")
	}
}

func TestProbeCampaignAccountChangeStopsBeforeAShare(t *testing.T) {
	f := newCampaignFixture(t, 1)
	plan, err := f.service.Plan(t.Context(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteProbeWorkerJSON(f.node.credentials, probeWorkerCredentials{UserID: "43", AccessToken: "changed-access", Cookie: "UID=43_session; CID=changed-cid; SEID=changed-seid"}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Start(t.Context(), plan.ID, plan.Digest, true); err != nil {
		t.Fatal(err)
	}
	view := waitCampaign(t, f)
	if view.Status != "stopped" || view.Error == "" || f.aCalls.Load() != 0 || f.bCalls.Load() != 0 || f.runs.Load() != 0 {
		t.Fatalf("account change did not stop before cloud calls: %+v", view)
	}
}

func TestProbeCampaignAccountProxyIsIndependentAndDoesNotUse115ForStatus(t *testing.T) {
	f := newCampaignFixture(t, 1)
	f.service.pipeline = nil
	view, err := f.service.Account(t.Context())
	if err != nil || !view.Available || view.AccountID != "42" || !view.Configured {
		t.Fatalf("account status depends on pipeline: %+v %v", view, err)
	}
	for _, action := range []string{"start", "import-cookie", "logout", "unknown"} {
		if _, err := f.service.AccountAction(t.Context(), action, "", false, ""); err == nil {
			t.Fatalf("unconfirmed action accepted: %s", action)
		}
	}
	if f.aCalls.Load() != 0 || f.bCalls.Load() != 0 {
		t.Fatal("status or rejected actions contacted 115")
	}
}

func TestProbeCampaignCookieImportForwardsOnlyBToPrivateNode(t *testing.T) {
	f := newCampaignFixture(t, 1)
	calls := 0
	cookie := "UID=42_session; CID=import-cid; SEID=import-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/account/import-cookie" || r.Header.Get("Authorization") != "Bearer "+f.service.nodeToken || r.Header.Get("Cookie") != "" {
			t.Error("wrong private credential boundary")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["cookie"] != cookie || payload["a_account_id"] != "7" || payload["confirmed"] != true || len(payload) != 3 {
			t.Error("wrong import payload")
		}
		io.WriteString(w, `{"phase":"open_prepare","configured":false,"request_limit":6,"requests":1}`)
	}))
	defer server.Close()
	f.service.nodeURL = server.URL
	for _, value := range []string{"UID=7_session; CID=cid; SEID=seid", strings.Repeat("x", 16385)} {
		if _, err := f.service.AccountAction(t.Context(), "import-cookie", "", true, value); err == nil {
			t.Fatal("unsafe import accepted")
		}
	}
	if calls != 0 {
		t.Fatal("unsafe import reached node")
	}
	view, err := f.service.AccountAction(t.Context(), "import-cookie", "", true, cookie)
	if err != nil || !view.Available || view.RequestLimit != 6 || calls != 1 || f.aCalls.Load() != 0 || f.bCalls.Load() != 0 {
		t.Fatalf("invalid proxy: %+v %v", view, err)
	}
}
