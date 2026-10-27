package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProbeNode is a private service on the independent machine. Its API never
// accepts A credentials, arbitrary filenames, binaries or upstream endpoints.
type ProbeNode struct {
	mu                                             sync.Mutex
	token, credentials, directory, binary, version string
	run                                            func(context.Context, *ProbeExchangeManifest, *ProbeWorkerPlan, ProbeWorkerRunOptions) (*ProbeExchangeBundle, error)
	client                                         *http.Client
	base                                           string
	wait                                           func(context.Context, time.Duration) error
	login                                          *probeAccountLogin
}

type ProbeNodeBatch struct {
	ManifestID     string             `json:"manifest_id"`
	ManifestDigest string             `json:"manifest_digest"`
	Batch          ProbeWorkerBatch   `json:"batch"`
	Sources        ProbeWorkerSources `json:"sources"`
}

type probeNodeStage struct {
	AccountID   string           `json:"account_id"`
	Phase       string           `json:"phase"`
	DirectoryID string           `json:"directory_id"`
	Stats       ProbeWorkerStats `json:"stats"`
}

func NewProbeNode(token, credentials, directory, binary, version string) (*ProbeNode, error) {
	if len(token) < 32 || strings.ContainsAny(token, "\r\n") || directory == "" || credentials == "" {
		return nil, errors.New("独立节点需要至少 32 字符的服务密钥、B 凭据和状态目录")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(credentials); err == nil {
		if _, err := readProbeWorkerCredentials(credentials); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("B 凭据路径不可访问")
	}
	if err := os.MkdirAll(filepath.Dir(credentials), 0700); err != nil {
		return nil, err
	}
	if _, err := newProbeWorkerProber(binary); err != nil {
		return nil, err
	}
	return &ProbeNode{token: token, credentials: credentials, directory: directory, binary: binary, version: version,
		run: RunProbeWorker, client: newProbeWorkerHTTPClient(), base: "https://proapi.115.com", login: newProbeAccountLogin()}, nil
}

func (n *ProbeNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+n.token)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	respond := func(value any, err error) {
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			value = map[string]string{"error": err.Error()}
		}
		_ = json.NewEncoder(w).Encode(value)
	}
	if r.URL.Path == "/v1/account" || strings.HasPrefix(r.URL.Path, "/v1/account/") {
		n.serveAccount(w, r, respond)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/status" {
		if !n.mu.TryLock() {
			respond(nil, errors.New("独立节点已有任务运行"))
			return
		}
		defer n.mu.Unlock()
		view, err := n.accountView()
		if err != nil {
			respond(nil, err)
			return
		}
		if !view.Configured || n.accountActive() {
			respond(map[string]any{"configured": false, "account_id": view.AccountID}, nil)
			return
		}
		credentials, err := readProbeWorkerCredentials(n.credentials)
		if err != nil {
			respond(nil, err)
			return
		}
		if credentials.ExpiresAt > 0 && credentials.ExpiresAt <= time.Now().Unix()+900 && credentials.RefreshToken == "" {
			respond(nil, errors.New("B 访问令牌即将到期且没有刷新授权，请维护 B 登录"))
			return
		}
		respond(map[string]any{"account_id": credentials.UserID, "configured": true}, nil)
		return
	}
	if r.Method != http.MethodPost || (r.URL.Path != "/v1/staging" && r.URL.Path != "/v1/batch" && r.URL.Path != "/v1/manifest") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if !n.mu.TryLock() {
		respond(nil, errors.New("独立节点已有任务运行"))
		return
	}
	defer n.mu.Unlock()
	if n.accountActive() {
		respond(nil, errors.New("请先完成或取消 B 授权"))
		return
	}
	credentials, err := readProbeWorkerCredentials(n.credentials)
	if err != nil {
		respond(nil, err)
		return
	}
	decode := func(dst any) error {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, ProbeExchangeMaxBytes))
		d.DisallowUnknownFields()
		if err := d.Decode(dst); err != nil {
			return errors.New("节点任务结构无效或超出大小限制")
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return errors.New("节点任务包含额外内容")
		}
		return nil
	}
	if r.URL.Path == "/v1/manifest" {
		var manifest ProbeExchangeManifest
		if err := decode(&manifest); err != nil {
			respond(nil, err)
			return
		}
		if !validProbeNodeID(manifest.ID) {
			respond(nil, errors.New("任务清单标识无效"))
			return
		}
		if err := manifest.CheckDigest(); err != nil {
			respond(nil, err)
			return
		}
		filename := filepath.Join(n.directory, manifest.ID+"-manifest.json")
		var saved ProbeExchangeManifest
		if err := readOptionalProbeNodeJSON(filename, &saved); err == nil {
			if saved.Digest != manifest.Digest {
				respond(nil, errors.New("清单标识已绑定其他范围"))
				return
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			respond(nil, err)
			return
		} else if err := WriteProbeWorkerJSON(filename, &manifest); err != nil {
			respond(nil, err)
			return
		}
		respond(map[string]string{"digest": manifest.Digest}, nil)
		return
	}
	if r.URL.Path == "/v1/staging" {
		var req struct {
			ID        string `json:"id"`
			AccountID string `json:"account_id"`
		}
		if err := decode(&req); err != nil {
			respond(nil, err)
			return
		}
		if !validProbeNodeID(req.ID) || req.AccountID != credentials.UserID {
			respond(nil, errors.New("暂存任务或 B 账号不匹配"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		id, err := n.staging(ctx, req.ID, credentials)
		var stage probeNodeStage
		if err == nil {
			err = ReadProbeWorkerJSON(filepath.Join(n.directory, req.ID+"-staging.json"), &stage)
		}
		respond(struct {
			DirectoryID string           `json:"directory_id"`
			Stats       ProbeWorkerStats `json:"stats"`
		}{id, stage.Stats}, err)
		return
	}
	var req ProbeNodeBatch
	if err := decode(&req); err != nil {
		respond(nil, err)
		return
	}
	if !validProbeNodeID(req.ManifestID) || !validProbeNodeID(req.Batch.ID) || !strings.HasPrefix(req.Batch.ID, req.ManifestID+"-") || req.Batch.AccountID != credentials.UserID || len(req.Batch.MediaIDs) > 10 || req.Batch.Limits != DefaultProbeWorkerTransferLimits() || !probeWorkerHasTransfers(req.Batch) {
		respond(nil, errors.New("独立节点只接受本任务的默认有限自动转存批次"))
		return
	}
	var manifest ProbeExchangeManifest
	if err := ReadProbeWorkerJSON(filepath.Join(n.directory, req.ManifestID+"-manifest.json"), &manifest); err != nil {
		respond(nil, err)
		return
	}
	if manifest.Digest != req.ManifestDigest {
		respond(nil, errors.New("批次清单摘要不匹配"))
		return
	}
	batch := req.Batch
	batch.RefreshBeforeRun = credentials.ExpiresAt > 0 && credentials.ExpiresAt <= time.Now().Unix()+int64(batch.Limits.RunSeconds)+30
	plan, err := CreateProbeWorkerPlanWithSources(&manifest, batch, &req.Sources)
	if err != nil {
		respond(nil, err)
		return
	}
	// Only the directory created for THIS campaign can receive these files.
	var stage probeNodeStage
	if err := ReadProbeWorkerJSON(filepath.Join(n.directory, req.ManifestID+"-staging.json"), &stage); err != nil {
		respond(nil, err)
		return
	}
	for _, root := range req.Batch.Roots {
		if stage.Phase != "ready" || stage.AccountID != credentials.UserID || root.DirectoryID != stage.DirectoryID {
			respond(nil, errors.New("批次没有绑定到本任务管理的 B 暂存目录"))
			return
		}
	}
	resultPath := filepath.Join(n.directory, req.Batch.ID+"-result.json")
	requestPath := filepath.Join(n.directory, req.Batch.ID+"-request.json")
	var previous ProbeNodeBatch
	if err := readOptionalProbeNodeJSON(requestPath, &previous); err == nil {
		oldDigest, e1 := probeExchangeDigest(previous)
		newDigest, e2 := probeExchangeDigest(req)
		if e1 != nil || e2 != nil || oldDigest != newDigest {
			respond(nil, errors.New("批次 ID 已绑定其他范围"))
			return
		}
		var result ProbeExchangeBundle
		if err := ReadProbeWorkerJSON(resultPath, &result); err != nil {
			respond(nil, errors.New("批次已经开始但未保存完整结果，已停止；不会重复提交转存"))
			return
		}
		respond(&result, nil)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		respond(nil, err)
		return
	}
	if err := WriteProbeWorkerJSON(requestPath, &req); err != nil {
		respond(nil, err)
		return
	}
	sourcesPath := filepath.Join(n.directory, req.Batch.ID+"-sources.json")
	if err := WriteProbeWorkerJSON(sourcesPath, &req.Sources); err != nil {
		respond(nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.Batch.Limits.RunSeconds)*time.Second)
	defer cancel()
	options := ProbeWorkerRunOptions{CredentialsFile: n.credentials, StateFile: filepath.Join(n.directory, req.Batch.ID+"-state.json"), SourcesFile: sourcesPath,
		ApprovedDigest: plan.Digest, FFprobe: n.binary, ToolVersion: n.version}
	index, err := strconv.Atoi(strings.TrimPrefix(req.Batch.ID, req.ManifestID+"-"))
	if err != nil || index < 1 {
		respond(nil, errors.New("批次序号无效"))
		return
	}
	if index > 1 {
		options.ReuseStateFile = filepath.Join(n.directory, req.ManifestID+"-"+strconv.Itoa(index-1)+"-state.json")
	}
	result, err := n.run(ctx, &manifest, plan, options)
	if err == nil {
		err = WriteProbeWorkerJSON(resultPath, result)
	}
	respond(result, err)
}

func validProbeNodeID(id string) bool {
	return id != "" && len(id) <= 100 && strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") == ""
}

func (n *ProbeNode) staging(ctx context.Context, id string, credentials *probeWorkerCredentials) (string, error) {
	filename := filepath.Join(n.directory, id+"-staging.json")
	stage := probeNodeStage{AccountID: credentials.UserID, Phase: "new"}
	if err := readOptionalProbeNodeJSON(filename, &stage); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if stage.AccountID != credentials.UserID {
		return "", errors.New("暂存目录属于另一 B 账号")
	}
	if stage.Phase == "ready" {
		return stage.DirectoryID, nil
	}
	if stage.Phase != "new" {
		return "", errors.New("创建 B 暂存目录结果不明，已停止且不重发")
	}
	owner, err := os.OpenFile(n.credentials+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("B 凭据已被占用")
	}
	owner.Close()
	defer os.Remove(n.credentials + ".lock")
	gate := &probeWorkerGate{limits: DefaultProbeWorkerTransferLimits(), wait: n.wait}
	gate.onRequest = func(stats ProbeWorkerStats) error { stage.Stats = stats; return WriteProbeWorkerJSON(filename, &stage) }
	client := &probe115Client{client: n.client, base: n.base, credentials: credentials, gate: gate, ua: probeWorkerUA}
	if err := client.verifyAccount(ctx, credentials.UserID); err != nil {
		return "", err
	}
	stage.Phase = "creating"
	if err := WriteProbeWorkerJSON(filename, &stage); err != nil {
		return "", err
	}
	dir, err := client.mkdir(ctx, "0", "msg-probe-"+id)
	if err != nil {
		return "", err
	}
	stage.Phase, stage.DirectoryID = "ready", dir
	if err := WriteProbeWorkerJSON(filename, &stage); err != nil {
		return "", err
	}
	return dir, nil
}

func readOptionalProbeNodeJSON(filename string, dst any) error {
	if _, err := os.Lstat(filename); err != nil {
		return err
	}
	return ReadProbeWorkerJSON(filename, dst)
}
