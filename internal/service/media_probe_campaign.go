package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"gorm.io/gorm"
)

type ProbeCampaignRoot struct {
	RootID      string `json:"root_id"`
	LibraryID   string `json:"library_id"`
	Path        string `json:"path"`
	DirectoryID string `json:"directory_id"`
}

type ProbeCampaignPlan struct {
	ID              string    `json:"id"`
	Digest          string    `json:"digest"`
	ExpiresAt       time.Time `json:"expires_at"`
	LibraryIDs      []string  `json:"library_ids"`
	AccountID       string    `json:"b_account_id"`
	Files           int       `json:"files"`
	MaxFiles        int       `json:"max_files,omitempty"`
	Batches         int       `json:"batches"`
	SourceRequests  int       `json:"a_requests_max"`
	ControlRequests int       `json:"control_requests_max"`
	MediaRequests   int       `json:"media_requests_max"`
	MediaBytes      int64     `json:"media_bytes_max"`
	RunSeconds      int       `json:"run_seconds_max"`
	Notice          string    `json:"notice"`
	Targets         []string  `json:"targets"`
}

type ProbeCampaignView struct {
	Configured      bool               `json:"configured"`
	Plan            *ProbeCampaignPlan `json:"plan,omitempty"`
	Status          string             `json:"status"`
	Phase           string             `json:"phase,omitempty"`
	FinishedBatches int                `json:"finished_batches"`
	Counts          map[string]int     `json:"counts"`
	Error           string             `json:"error,omitempty"`
	AStats          ProbeWorkerStats   `json:"a_stats"`
	BStats          ProbeWorkerStats   `json:"b_stats"`
}

type probeCampaignState struct {
	View        ProbeCampaignView     `json:"view"`
	Manifest    ProbeExchangeManifest `json:"manifest"`
	Roots       []ProbeCampaignRoot   `json:"roots"`
	Sources     ProbeWorkerSources    `json:"sources"`
	AAccountID  string                `json:"a_account_id"`
	DirectoryID string                `json:"directory_id"`
}

// The controller owns A setup and imports. Only the B node reads media.
// All share URLs/checkpoints are encrypted using MSG's existing CryptoService.
type MediaProbeCampaignService struct {
	mu                 sync.Mutex
	queue              *MediaProbeQueueService
	storage            *StorageConfigService
	crypto             *CryptoService
	nodeURL, nodeToken string
	pipeline           *resourcePipelineHTTPClient
	setupError         error
	client             *http.Client
	aClient            *http.Client
	aBase              string
	active             string
	cancel             context.CancelFunc
	lifetime           context.Context
	wait               func(context.Context, time.Duration) error
}

func NewMediaProbeCampaignService(queue *MediaProbeQueueService, storage *StorageConfigService, crypto *CryptoService, cfg *config.Config, lifetime context.Context) *MediaProbeCampaignService {
	var pipeline *resourcePipelineHTTPClient
	var setupError error
	if cfg.MediaProbeWorker.URL != "" || cfg.MediaProbeWorker.Token != "" {
		pipeline, setupError = newResourcePipelineHTTPClient(cfg.ResourceImport)
	}
	return &MediaProbeCampaignService{queue: queue, storage: storage, crypto: crypto,
		nodeURL: strings.TrimRight(cfg.MediaProbeWorker.URL, "/"), nodeToken: cfg.MediaProbeWorker.Token, pipeline: pipeline, setupError: setupError,
		client: newResourcePipelineHTTPTransport(16 * time.Minute), aClient: newProbeCampaignSourceClient(storage), aBase: "https://webapi.115.com", lifetime: lifetime}
}

func newProbeCampaignSourceClient(storage *StorageConfigService) *http.Client {
	// Keep A's existing proxy/egress choice. Only B uses the independent node's
	// fixed direct transport. Fresh HTTP/1 connections prevent stale replay.
	base := storage.client
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if source, ok := transport.(*http.Transport); ok {
		copy := source.Clone()
		copy.DisableKeepAlives = true
		copy.ForceAttemptHTTP2 = false
		copy.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		transport = copy
	}
	timeout := 30 * time.Second
	if base.Timeout > 0 && base.Timeout < timeout {
		timeout = base.Timeout
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (s *MediaProbeCampaignService) nodeConfigured() bool {
	u, err := url.Parse(s.nodeURL)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	secure := u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && (ip.IsLoopback() || ip.IsPrivate())))
	return u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && secure && len(s.nodeToken) >= 32
}

func (s *MediaProbeCampaignService) configured() bool {
	return s.nodeConfigured() && s.pipeline != nil && s.crypto != nil && s.crypto.aead != nil
}

func (s *MediaProbeCampaignService) node(ctx context.Context, endpoint string, request, response any) error {
	var body io.Reader
	method := http.MethodGet
	if request != nil {
		data, err := json.Marshal(request)
		if err != nil {
			return err
		}
		body, method = bytes.NewReader(data), http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, s.nodeURL+endpoint, body)
	if err != nil {
		return errors.New("独立节点地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+s.nodeToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return errors.New("独立节点连接中断，已停止；未自动重试")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, ProbeExchangeMaxBytes+1))
	if err != nil || len(data) > ProbeExchangeMaxBytes {
		return errors.New("独立节点响应读取失败或超出限制")
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		if failure.Error != "" {
			return errors.New(safeMediaProbeQueueError(errors.New(failure.Error)))
		}
		return fmt.Errorf("独立节点 HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(data, response); err != nil {
		return errors.New("独立节点响应结构无效")
	}
	return nil
}

func (s *MediaProbeCampaignService) load(ctx context.Context, id string) (*probeCampaignState, error) {
	var row model.MediaProbeCampaign
	q := s.queue.repo.DB.WithContext(ctx)
	if id != "" {
		q = q.Where("id = ?", id)
	}
	if err := q.Order("created_at DESC, id DESC").First(&row).Error; err != nil {
		return nil, err
	}
	var state probeCampaignState
	if err := json.Unmarshal([]byte(s.crypto.Decrypt(row.Payload)), &state); err != nil {
		return nil, errors.New("补齐任务检查点无法解密")
	}
	if state.View.Plan == nil || state.View.Plan.ID != row.ID {
		return nil, errors.New("补齐任务检查点身份无效")
	}
	return &state, nil
}

func (s *MediaProbeCampaignService) save(ctx context.Context, state *probeCampaignState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	payload := s.crypto.Encrypt(string(data))
	result := s.queue.repo.DB.WithContext(ctx).Model(&model.MediaProbeCampaign{}).Where("id = ?", state.View.Plan.ID).Update("payload", payload)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("补齐任务检查点不存在，未保存")
	}
	return nil
}

// Boot records interruption, never replays cloud writes on process restart.
func (s *MediaProbeCampaignService) Boot(ctx context.Context) error {
	var rows []model.MediaProbeCampaign
	if err := s.queue.repo.DB.WithContext(ctx).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		state, err := s.load(ctx, row.ID)
		if err != nil {
			return err
		}
		if state.View.Status == "running" {
			state.View.Status, state.View.Error = "interrupted", "服务重启，任务已停止；未自动重放云盘操作"
			if err := s.save(ctx, state); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *MediaProbeCampaignService) Snapshot(ctx context.Context) (*ProbeCampaignView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load(ctx, "")
	if errors.Is(err, gorm.ErrRecordNotFound) {
		view := &ProbeCampaignView{Configured: s.configured(), Status: "idle", Counts: map[string]int{}}
		if s.setupError != nil {
			view.Error = s.setupError.Error()
		}
		return view, nil
	}
	if err != nil {
		return nil, err
	}
	state.View.Configured = s.configured()
	if s.setupError != nil {
		state.View.Error = s.setupError.Error()
	}
	return &state.View, nil
}

func probeCookieAccount(cookie string) string {
	for _, part := range strings.Split(cookie, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && key == "UID" {
			return strings.SplitN(value, "_", 2)[0]
		}
	}
	return ""
}

func (s *MediaProbeCampaignService) sourceAccount(ctx context.Context) (string, string, error) {
	view, err := s.storage.Get(ctx, "cloud115")
	if err != nil {
		return "", "", err
	}
	if view == nil {
		return "", "", errors.New("请先维护系统已有 A 账号登录")
	}
	cookie := strr(view.Config["cookie"])
	id := probeCookieAccount(cookie)
	if !probeDecimalID(id) || id == "0" {
		return "", "", errors.New("系统 A 账号 Cookie 无效")
	}
	if err := validateProbeWorkerCookie(cookie, id); err != nil {
		return "", "", errors.New("系统 A 账号登录信息不完整")
	}
	return id, cookie, nil
}

// Planning reads the MSG database and trusted service configuration only.
// Node /status reads its credential file; pipeline /roots reads its config.
func (s *MediaProbeCampaignService) Plan(ctx context.Context, libraryIDs []string, maxFiles int) (*ProbeCampaignPlan, error) {
	if maxFiles < 0 || maxFiles > 10 {
		return nil, errors.New("小范围测试上限必须为 1 到 10；0 表示完整所选范围")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != "" {
		return nil, errors.New("请先暂停正在运行的独立补齐任务")
	}
	if !s.configured() {
		if s.setupError != nil {
			return nil, s.setupError
		}
		return nil, errors.New("独立补齐节点尚未由管理员配置")
	}
	if len(libraryIDs) > 100 {
		return nil, errors.New("媒体库范围过大")
	}
	aID, _, err := s.sourceAccount(ctx)
	if err != nil {
		return nil, err
	}
	var status struct {
		AccountID  string `json:"account_id"`
		Configured bool   `json:"configured"`
	}
	if err := s.node(ctx, "/v1/status", nil, &status); err != nil {
		return nil, err
	}
	if !status.Configured || !probeDecimalID(status.AccountID) || status.AccountID == "0" || status.AccountID == aID {
		return nil, errors.New("B 账号未登录或与 A 是同一账号")
	}
	manifest, err := s.queue.exportExchangeManifest(ctx, libraryIDs, maxFiles)
	if err != nil {
		return nil, err
	}
	needed := map[string]bool{}
	files := 0
	for _, item := range manifest.Items {
		if !probeExchangeBasicComplete(item.Metadata) || item.Metadata.BitRate <= 0 {
			files++
			needed[item.RootID] = true
		}
	}
	if len(needed) > 5 {
		return nil, errors.New("单任务最多 5 个库根，请缩小媒体库范围")
	}
	var configured struct {
		Roots []ProbeCampaignRoot `json:"roots"`
	}
	if len(needed) > 0 {
		query := url.Values{}
		for rootID := range needed {
			query.Add("root_id", rootID)
		}
		if err := s.pipeline.doJSON(ctx, http.MethodGet, "/v1/media-probe/roots?"+query.Encode(), nil, "", &configured); err != nil {
			return nil, err
		}
	}
	roots := []ProbeCampaignRoot{}
	for id := range needed {
		var root model.LibraryRoot
		if err := s.queue.repo.DB.WithContext(ctx).First(&root, "id = ?", id).Error; err != nil {
			return nil, errors.New("所选库根已变化，请先同步媒体库")
		}
		matches := []ProbeCampaignRoot{}
		for _, candidate := range configured.Roots {
			if candidate.RootID == id && candidate.LibraryID == root.LibraryID && probeCampaignSameRootPath(candidate.Path, root.Path) && probeDecimalID(candidate.DirectoryID) && candidate.DirectoryID != "0" {
				matches = append(matches, candidate)
			}
		}
		if len(matches) != 1 {
			return nil, errors.New("入库服务与 MSG 库根配置不一致，需要管理员修复现有映射")
		}
		roots = append(roots, matches[0])
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].RootID < roots[j].RootID })
	batches := (files + 9) / 10
	plan := &ProbeCampaignPlan{ID: manifest.ID, ExpiresAt: time.Now().UTC().Add(30 * time.Minute), LibraryIDs: append([]string{}, libraryIDs...), AccountID: status.AccountID,
		Files: files, MaxFiles: maxFiles, Batches: batches, SourceRequests: len(roots) + 1, ControlRequests: len(roots) + 4 + 80*batches,
		MediaRequests: 60 * batches, MediaBytes: int64(batches) * (320 << 20), RunSeconds: 180 + 910*batches,
		Targets: []string{"A：webapi.115.com/files/uploadinfo、/share/send", "B：proapi.115.com/open/user/info、/open/folder/add、/open/ufile/files、/open/ufile/downurl", "B：webapi.115.com/files/uploadinfo、115cdn.com/webapi/share/snap、/share/receive", "B Token 刷新：passportapi.115.com/open/refreshToken（每批最多一次）", "共享信息：115server.ddsrem.com；媒体读取：B 直链指定的 115 CDN"},
		Notice:  "A 仅核验身份并为所选库根创建来源分享；B 自动建目录、按缺口转存、查询共享信息、限量探测并逐批回填。并发 1；控制请求间隔至少 5 秒，文件间隔 10 秒，媒体请求间隔至少 1 秒；每批最多 15 分钟、共享查询 1 次。不刷新 OpenList、不自动重试或删除副本。会真实访问 115/CDN，存在限流、封禁、账号关联和链路失败风险。执行期间请保持来源文件不替换、不改名；异常立即停止。"}
	plan.Digest, err = probeExchangeDigest(struct {
		Plan     *ProbeCampaignPlan
		Manifest string
		Roots    []ProbeCampaignRoot
		A        string
	}{plan, manifest.Digest, roots, aID})
	if err != nil {
		return nil, err
	}
	state := probeCampaignState{View: ProbeCampaignView{Configured: true, Plan: plan, Status: "planned", Counts: map[string]int{}}, Manifest: *manifest, Roots: roots, AAccountID: aID, Sources: ProbeWorkerSources{Shares: map[string]ProbeWorkerShareSource{}}}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	row := model.MediaProbeCampaign{Base: model.Base{ID: plan.ID}, Payload: s.crypto.Encrypt(string(data))}
	if err := s.queue.repo.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	return plan, nil
}

func (s *MediaProbeCampaignService) Start(ctx context.Context, id, digest string, confirmed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != "" {
		return errors.New("独立补齐任务正在运行")
	}
	latest, err := s.load(ctx, "")
	if err != nil {
		return err
	}
	if id == "" || latest.View.Plan.ID != id {
		return errors.New("已有更新的任务预览，请使用最新范围")
	}
	state, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if !s.configured() || !confirmed || state.View.Plan.Digest != digest || state.View.Status != "planned" || time.Now().After(state.View.Plan.ExpiresAt) {
		return errors.New("请重新预览并确认有限任务范围")
	}
	s.queue.mu.Lock()
	if s.queue.running || s.queue.secondaryActive {
		s.queue.mu.Unlock()
		return errors.New("请先暂停已有文件信息任务")
	}
	s.queue.secondaryActive = true
	s.queue.mu.Unlock()
	state.View.Status, state.View.Phase = "running", "initializing"
	if err := s.save(ctx, state); err != nil {
		s.queue.mu.Lock()
		s.queue.secondaryActive = false
		s.queue.mu.Unlock()
		return err
	}
	work, cancel := context.WithTimeout(s.lifetime, time.Duration(state.View.Plan.RunSeconds)*time.Second)
	s.active, s.cancel = id, cancel
	go func() {
		err := s.execute(work, state)
		cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			state.View.Status, state.View.Error = "stopped", safeMediaProbeQueueError(err)
		} else {
			state.View.Status, state.View.Phase = "completed", "done"
		}
		if saveErr := s.save(context.Background(), state); saveErr != nil && s.queue.log != nil {
			s.queue.log.Error("save media probe campaign completion failed")
		}
		s.active, s.cancel = "", nil
		s.queue.mu.Lock()
		s.queue.secondaryActive = false
		s.queue.mu.Unlock()
	}()
	return nil
}

func (s *MediaProbeCampaignService) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *MediaProbeCampaignService) checkpoint(ctx context.Context, state *probeCampaignState, phase string) error {
	state.View.Phase = phase
	return s.save(ctx, state)
}

func (s *MediaProbeCampaignService) execute(ctx context.Context, state *probeCampaignState) error {
	if state.View.Plan.Files == 0 {
		return nil
	}
	s.queue.mu.Lock()
	running := s.queue.running
	s.queue.mu.Unlock()
	if running {
		return errors.New("请先暂停主账号文件信息队列")
	}
	if err := s.checkRoots(ctx, state); err != nil {
		return err
	}
	aID, cookie, err := s.sourceAccount(ctx)
	if err != nil {
		return err
	}
	if aID != state.AAccountID || aID == state.View.Plan.AccountID {
		return errors.New("来源账号在预览后变化")
	}
	var status struct {
		Configured bool   `json:"configured"`
		AccountID  string `json:"account_id"`
	}
	if err := s.node(ctx, "/v1/status", nil, &status); err != nil {
		return err
	}
	if !status.Configured || status.AccountID != state.View.Plan.AccountID {
		return errors.New("B 账号在预览后发生变化或正在授权，未创建 A 分享")
	}
	var uploaded struct {
		Digest string `json:"digest"`
	}
	if err := s.node(ctx, "/v1/manifest", &state.Manifest, &uploaded); err != nil {
		return err
	}
	if uploaded.Digest != state.Manifest.Digest {
		return errors.New("独立节点保存的清单不一致")
	}
	gate := &probeWorkerGate{limits: ProbeWorkerLimits{ControlRequests: 6, ShareRequests: 5, ControlGapSeconds: 5}, wait: s.wait}
	gate.onRequest = func(stats ProbeWorkerStats) error { state.View.AStats = stats; return s.save(ctx, state) }
	source := &probeShareClient{client: s.aClient, gate: gate, cookie: cookie, identityBase: s.aBase}
	if err := source.verifyAccount(ctx, aID); err != nil {
		return err
	}
	for _, root := range state.Roots {
		if err := s.checkRoots(ctx, state); err != nil {
			return err
		}
		if err := s.checkpoint(ctx, state, "creating_source_share"); err != nil {
			return err
		}
		data, err := source.request(ctx, "create_share", s.aBase+"/share/send", url.Values{"file_ids": {root.DirectoryID}, "is_asc": {"1"}, "order": {"file_name"}, "ignore_warn": {"1"}}, nil)
		if err != nil {
			return err
		}
		var response struct {
			Data struct {
				Code     string `json:"share_code"`
				Password string `json:"receive_code"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &response); err != nil || response.Data.Code == "" {
			return errors.New("A 创建分享未返回有效标识；不会重发")
		}
		shareURL := "https://115.com/s/" + response.Data.Code + "?password=" + url.QueryEscape(response.Data.Password)
		if _, err := parseProbeWorkerShare(ProbeWorkerShareSource{ShareURL: shareURL, DirectoryID: root.DirectoryID}); err != nil {
			return err
		}
		state.Sources.Shares[root.RootID] = ProbeWorkerShareSource{RootID: root.RootID, ShareURL: shareURL, DirectoryID: root.DirectoryID}
		if err := s.checkpoint(ctx, state, "source_ready"); err != nil {
			return err
		}
	}
	if err := s.checkpoint(ctx, state, "creating_b_staging"); err != nil {
		return err
	}
	var stage struct {
		ID    string           `json:"directory_id"`
		Stats ProbeWorkerStats `json:"stats"`
	}
	if err := s.node(ctx, "/v1/staging", map[string]string{"id": state.Manifest.ID, "account_id": state.View.Plan.AccountID}, &stage); err != nil {
		return err
	}
	if !probeDecimalID(stage.ID) || stage.ID == "0" {
		return errors.New("B 暂存目录无效")
	}
	state.DirectoryID = stage.ID
	state.View.BStats = stage.Stats
	template := ProbeWorkerBatch{ID: state.Manifest.ID, AccountID: state.View.Plan.AccountID, Provenance: "MSG managed source shares and verified B transfers", OriginalCopy: true, Limits: DefaultProbeWorkerTransferLimits()}
	for _, root := range state.Roots {
		template.Roots = append(template.Roots, ProbeWorkerRoot{RootID: root.RootID, SourceID: root.RootID, DirectoryID: stage.ID})
	}
	set, err := SplitProbeWorkerBatches(&state.Manifest, template, &state.Sources, 10)
	if err != nil {
		return err
	}
	if len(set.Batches) != state.View.Plan.Batches {
		return errors.New("任务分批数量与批准计划不符")
	}
	for index, batch := range set.Batches {
		if index > 0 {
			wait := s.wait
			if wait == nil {
				wait = waitMediaProbeQueue
			}
			if err := wait(ctx, 10*time.Second); err != nil {
				return err
			}
		}
		if err := s.checkpoint(ctx, state, "running_batch"); err != nil {
			return err
		}
		var bundle ProbeExchangeBundle
		if err := s.node(ctx, "/v1/batch", ProbeNodeBatch{ManifestID: state.Manifest.ID, ManifestDigest: state.Manifest.Digest, Batch: batch, Sources: state.Sources}, &bundle); err != nil {
			return err
		}
		if bundle.ManifestID != state.Manifest.ID || bundle.BatchID != batch.ID || bundle.AccountID != batch.AccountID {
			return errors.New("独立节点返回了其他任务的结果")
		}
		expected, err := probeWorkerSourcesDigest(batch, &state.Sources)
		if err != nil {
			return err
		}
		if bundle.SourcesDigest != expected || len(bundle.Results) != len(batch.MediaIDs) {
			return errors.New("独立节点结果来源或范围不一致")
		}
		addProbeCampaignStats(&state.View.BStats, bundle.Stats)
		allowed := map[string]bool{}
		for _, id := range batch.MediaIDs {
			allowed[id] = true
		}
		for _, result := range bundle.Results {
			if !allowed[result.MediaID] {
				return errors.New("结果超出本批范围或重复")
			}
			delete(allowed, result.MediaID)
		}
		if err := s.checkRoots(ctx, state); err != nil {
			return err
		}
		preview, err := s.queue.PreviewExchange(ctx, &bundle)
		if err != nil {
			return err
		}
		for _, change := range preview.Changes {
			if change.Status != "completed" && change.Status != "partial" && change.Status != "unchanged" {
				return fmt.Errorf("结果 %s，已停止回填", change.Status)
			}
		}
		if _, err := s.queue.ApplyExchange(ctx, &bundle, preview.Digest, true); err != nil {
			return err
		}
		for status, count := range preview.Counts {
			state.View.Counts[status] += count
		}
		state.View.FinishedBatches = index + 1
		if err := s.checkpoint(ctx, state, "batch_applied"); err != nil {
			return err
		}
	}
	return nil
}

func probeCampaignSameRootPath(a, b string) bool {
	left, e1 := exchangeSourcePath(a)
	right, e2 := exchangeSourcePath(b)
	return e1 == nil && e2 == nil && left == right
}

func addProbeCampaignStats(a *ProbeWorkerStats, b ProbeWorkerStats) {
	a.ControlRequests += b.ControlRequests
	a.ListRequests += b.ListRequests
	a.LinkRequests += b.LinkRequests
	a.RefreshRequests += b.RefreshRequests
	a.IdentityRequests += b.IdentityRequests
	a.MediaRequests += b.MediaRequests
	a.MediaBytes += b.MediaBytes
	a.SharedRequests += b.SharedRequests
	a.SharedHits += b.SharedHits
	a.SharedRawHits += b.SharedRawHits
	a.SharedLookups += b.SharedLookups
	a.ExistingHits += b.ExistingHits
	a.ProbeCalls += b.ProbeCalls
	a.ShareRequests += b.ShareRequests
	a.ReceiveRequests += b.ReceiveRequests
	a.MkdirRequests += b.MkdirRequests
	a.CookieIdentities += b.CookieIdentities
}

func (s *MediaProbeCampaignService) checkRoots(ctx context.Context, state *probeCampaignState) error {
	for _, expected := range state.Roots {
		var root model.LibraryRoot
		if err := s.queue.repo.DB.WithContext(ctx).First(&root, "id = ?", expected.RootID).Error; err != nil {
			return err
		}
		if root.LibraryID != expected.LibraryID || !probeCampaignSameRootPath(root.Path, expected.Path) {
			return errors.New("媒体库根在预览后发生变化，已停止")
		}
	}
	return nil
}
