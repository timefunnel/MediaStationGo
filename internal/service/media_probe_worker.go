package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type ProbeWorkerRunOptions struct {
	CredentialsFile string
	StateFile       string
	ReuseStateFile  string
	ApprovedDigest  string
	FFprobe         string
	ToolVersion     string
	SourcesFile     string
}

type probeWorkerCache struct {
	Metadata         *ProbeResult `json:"metadata"`
	Source           string       `json:"source"`
	EstimatedBitRate bool         `json:"estimated_bit_rate"`
}

type probeWorkerPage struct {
	Next        int             `json:"next"`
	Total       int             `json:"total"`
	Initialized bool            `json:"initialized"`
	Complete    bool            `json:"complete"`
	Entries     []probe115Entry `json:"entries"`
}

type probeWorkerState struct {
	Version        int                             `json:"version"`
	ManifestDigest string                          `json:"manifest_digest"`
	AccountID      string                          `json:"b_account_id"`
	BindingDigest  string                          `json:"binding_digest"`
	BatchID        string                          `json:"batch_id"`
	Pages          map[string]*probeWorkerPage     `json:"pages"`
	Files          map[string]probeWorkerFile      `json:"files"`
	Cache          map[string]probeWorkerCache     `json:"cache"`
	Bundle         ProbeExchangeBundle             `json:"bundle"`
	SourcesDigest  string                          `json:"sources_digest,omitempty"`
	Transfers      map[string]*probeWorkerTransfer `json:"transfers,omitempty"`
}

type probeWorkerDependencies struct {
	control, media, shared    *http.Client
	base, authBase, sharedURL string
	shareBase, cookieBase     string
	validateURL               func(string) error
	probe                     probeWorkerProber
	wait                      func(context.Context, time.Duration) error
}

func RunProbeWorker(ctx context.Context, manifest *ProbeExchangeManifest, plan *ProbeWorkerPlan, options ProbeWorkerRunOptions) (*ProbeExchangeBundle, error) {
	probe, err := newProbeWorkerProber(options.FFprobe)
	if err != nil {
		return nil, err
	}
	return runProbeWorker(ctx, manifest, plan, options, probeWorkerDependencies{
		control: newProbeWorkerHTTPClient(), media: newProbeWorkerHTTPClient(), shared: newProbeWorkerHTTPClient(),
		base: "https://proapi.115.com", authBase: "https://passportapi.115.com", sharedURL: "https://115server.ddsrem.com/emby_mediainfo_data/get",
		shareBase: "https://115cdn.com/webapi", cookieBase: "https://webapi.115.com",
		validateURL: validateProbeMediaURL, probe: probe})
}

func runProbeWorker(ctx context.Context, manifest *ProbeExchangeManifest, plan *ProbeWorkerPlan, options ProbeWorkerRunOptions, deps probeWorkerDependencies) (*ProbeExchangeBundle, error) {
	if manifest == nil || plan == nil {
		return nil, errors.New("缺少任务清单或执行计划")
	}
	if err := manifest.CheckDigest(); err != nil {
		return nil, err
	}
	if err := plan.Check(manifest); err != nil {
		return nil, err
	}
	if options.ApprovedDigest != plan.Digest || options.StateFile == "" {
		return nil, errors.New("必须审阅并明确批准精确计划摘要，且指定检查点文件")
	}
	credentials, err := readProbeWorkerCredentials(options.CredentialsFile)
	if err != nil {
		return nil, err
	}
	if credentials.UserID != plan.Batch.AccountID {
		return nil, errors.New("凭据文件不是计划中的 B 账号")
	}
	var sources *ProbeWorkerSources
	if probeWorkerHasTransfers(plan.Batch) {
		if err := validateProbeWorkerCookie(credentials.Cookie, plan.Batch.AccountID); err != nil {
			return nil, err
		}
		sources, err = readProbeWorkerSources(options.SourcesFile)
		if err != nil {
			return nil, err
		}
		digest, err := probeWorkerSourcesDigest(plan.Batch, sources)
		if err != nil {
			return nil, err
		}
		if digest != plan.SourcesDigest {
			return nil, errors.New("来源配置在计划批准后发生变化，请重新生成计划")
		}
	}
	if !plan.Batch.RefreshBeforeRun && credentials.ExpiresAt > 0 && credentials.ExpiresAt <= time.Now().Unix()+int64(plan.Batch.Limits.RunSeconds) {
		return nil, errors.New("访问令牌可能在本轮内到期，请单独安排明确的刷新范围")
	}
	bindingDigest, err := probeExchangeDigest(plan.Batch.Roots)
	if plan.SourcesDigest != "" {
		bindingDigest, err = probeExchangeDigest(struct {
			Roots    []ProbeWorkerRoot
			MediaIDs []string
			Sources  string
		}{plan.Batch.Roots, plan.Batch.MediaIDs, plan.SourcesDigest})
	}
	if err != nil {
		return nil, err
	}
	state := &probeWorkerState{Version: ProbeExchangeVersion, ManifestDigest: manifest.Digest, AccountID: credentials.UserID,
		BindingDigest: bindingDigest, BatchID: plan.Batch.ID, SourcesDigest: plan.SourcesDigest, Pages: map[string]*probeWorkerPage{}, Files: map[string]probeWorkerFile{}, Cache: map[string]probeWorkerCache{}}
	if _, err := os.Stat(options.StateFile); err == nil {
		if err := ReadProbeWorkerJSON(options.StateFile, state); err != nil {
			return nil, err
		}
		if state.Version != ProbeExchangeVersion || state.ManifestDigest != manifest.Digest || state.AccountID != credentials.UserID || state.BindingDigest != bindingDigest || state.BatchID != plan.Batch.ID || state.SourcesDigest != plan.SourcesDigest || state.Pages == nil || state.Files == nil || state.Cache == nil {
			return nil, errors.New("检查点不属于本轮来源、B 账号或目录绑定，请使用正确检查点")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("无法检查任务检查点")
	}
	if options.ReuseStateFile != "" {
		var previous probeWorkerState
		if err := ReadProbeWorkerJSON(options.ReuseStateFile, &previous); err != nil {
			return nil, err
		}
		if previous.Version != ProbeExchangeVersion || previous.ManifestDigest != manifest.Digest || previous.AccountID != credentials.UserID {
			return nil, errors.New("复用内容缓存不属于本任务清单及 B 账号")
		}
		for key, cached := range previous.Cache {
			if err := validateExchangeProbe(cached.Metadata); err != nil {
				return nil, err
			}
			if cached.Source != "existing" && cached.Source != "shared" && cached.Source != "ffprobe" {
				return nil, errors.New("复用缓存包含无效来源")
			}
			state.Cache[key] = cached
		}
	}
	if err := validateProbeWorkerState(state); err != nil {
		return nil, err
	}
	owner, err := os.OpenFile(options.CredentialsFile+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("账号凭据已被另一任务占用；异常退出后的锁需人工核对")
	}
	owner.Close()
	defer os.Remove(options.CredentialsFile + ".lock")
	marker, err := os.OpenFile(options.StateFile+"."+plan.ID+".started", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("本计划已经使用或无法写入启动标记；继续需新计划")
	}
	if _, err := marker.WriteString(plan.Digest); err != nil {
		marker.Close()
		return nil, errors.New("无法保存一次性计划启动标记")
	}
	if err := marker.Close(); err != nil {
		return nil, errors.New("无法关闭计划启动标记")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(plan.Batch.Limits.RunSeconds)*time.Second)
	defer cancel()
	state.Bundle = ProbeExchangeBundle{Version: ProbeExchangeVersion, AccountID: plan.Batch.AccountID, ManifestID: manifest.ID, ManifestDigest: manifest.Digest,
		PlanDigest: plan.Digest, BatchID: plan.Batch.ID, Provenance: plan.Batch.Provenance, ToolVersion: options.ToolVersion,
		CreatedAt: time.Now().UTC(), SourcesDigest: plan.SourcesDigest, Results: []ProbeExchangeResult{}}
	// Every round reconstructs mappings. A failure before identity/inventory
	// must not publish a previous round's files as freshly verified results.
	for _, id := range plan.Batch.MediaIDs {
		delete(state.Files, id)
	}
	items := map[string]ProbeExchangeItem{}
	for _, item := range manifest.Items {
		items[item.MediaID] = item
	}
	for _, id := range plan.Batch.MediaIDs {
		item := items[id]
		state.Bundle.Results = append(state.Bundle.Results, ProbeExchangeResult{MediaID: id, Fingerprint: item.Fingerprint, SizeBytes: item.SizeBytes, Status: "pending"})
	}
	gate := &probeWorkerGate{limits: plan.Batch.Limits, wait: deps.wait}
	save := func() error {
		state.Bundle.Stats = gate.snapshot()
		return WriteProbeWorkerJSON(options.StateFile, state)
	}
	gate.onRequest = func(stats ProbeWorkerStats) error {
		state.Bundle.Stats = stats
		return WriteProbeWorkerJSON(options.StateFile, state)
	}
	client := &probe115Client{client: deps.control, gate: gate, credentials: credentials, base: deps.base, authBase: deps.authBase, ua: probeWorkerUA, validateURL: deps.validateURL}
	finish := func(runErr error) (*ProbeExchangeBundle, error) {
		if runErr != nil {
			state.Bundle.StopReason = safeMediaProbeQueueError(runErr)
		}
		populateProbeWorkerResults(state)
		if err := save(); err != nil {
			return &state.Bundle, fmt.Errorf("检查点保存失败: %w", err)
		}
		return &state.Bundle, runErr
	}
	if err := save(); err != nil {
		return nil, err
	}
	if plan.Batch.RefreshBeforeRun {
		if err := client.refresh(ctx, options.CredentialsFile); err != nil {
			return finish(err)
		}
	}
	if err := client.verifyAccount(ctx, plan.Batch.AccountID); err != nil {
		return finish(err)
	}
	shareClient := &probeShareClient{client: deps.control, gate: gate, cookie: credentials.Cookie, base: deps.shareBase, identityBase: deps.cookieBase}
	if err := prepareProbeWorkerTransfers(ctx, client, shareClient, manifest, plan, sources, state, save); err != nil {
		return finish(err)
	}
	if err := inventoryProbeWorker(ctx, client, manifest, plan, state, save); err != nil {
		for index := range state.Bundle.Results {
			if _, found := state.Files[state.Bundle.Results[index].MediaID]; !found {
				state.Bundle.Results[index].Status = "mapping_conflict"
				state.Bundle.Results[index].Error = safeMediaProbeQueueError(err)
			}
		}
		return finish(err)
	}
	byContent := map[string]probeWorkerFile{}
	contentOrder := []string{}
	for _, id := range plan.Batch.MediaIDs {
		file := state.Files[id]
		key := probeWorkerContentKey(file)
		if _, found := byContent[key]; !found {
			byContent[key] = file
			contentOrder = append(contentOrder, key)
		}
		if previous, found := state.Cache[key]; found {
			merged, err := mergeProbeWorkerCache(previous, items[id].Metadata, file.Size, previous.Source)
			if err != nil {
				return finish(err)
			}
			state.Cache[key] = merged
		} else {
			merged, err := mergeProbeWorkerCache(probeWorkerCache{Metadata: &ProbeResult{}}, items[id].Metadata, file.Size, "existing")
			if err != nil {
				return finish(err)
			}
			state.Cache[key] = merged
		}
	}
	needed := map[string]probeWorkerFile{}
	for key, file := range byContent {
		cached := state.Cache[key]
		if probeWorkerNeedsProbe(cached.Metadata) {
			needed[key] = file
		} else {
			gate.mu.Lock()
			gate.stats.ExistingHits++
			gate.mu.Unlock()
		}
	}
	if len(needed) > 0 {
		shared, err := lookupProbeWorkerShared(ctx, deps.shared, gate, deps.sharedURL, needed)
		if err != nil {
			return finish(err)
		}
		for key, file := range needed {
			probe := shared[strings.ToUpper(file.SHA1)]
			if probe == nil {
				continue
			}
			gate.mu.Lock()
			gate.stats.SharedRawHits++
			gate.mu.Unlock()
			cached := state.Cache[key]
			merged, err := mergeProbeWorkerCache(cached, probe, file.Size, "shared")
			if err != nil {
				return finish(err)
			}
			cached = merged
			if !probeWorkerNeedsProbe(merged.Metadata) {
				gate.mu.Lock()
				gate.stats.SharedHits++
				gate.mu.Unlock()
			}
			state.Cache[key] = cached
		}
		if err := save(); err != nil {
			return finish(err)
		}
	}
	var previousProbeEnd time.Time
	for _, key := range contentOrder {
		cached := state.Cache[key]
		if !probeWorkerNeedsProbe(cached.Metadata) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		if !previousProbeEnd.IsZero() {
			wait := deps.wait
			if wait == nil {
				wait = waitMediaProbeQueue
			}
			if err := wait(ctx, time.Until(previousProbeEnd.Add(time.Duration(plan.Batch.Limits.TaskGapSeconds)*time.Second))); err != nil {
				return finish(err)
			}
		}
		file := byContent[key]
		stats := gate.snapshot()
		if stats.MediaBytes >= plan.Batch.Limits.MediaBytes || stats.MediaRequests >= plan.Batch.Limits.MediaRequests {
			return finish(errProbeWorkerBudget)
		}
		supported := true
		for _, id := range plan.Batch.MediaIDs {
			if probeWorkerContentKey(state.Files[id]) == key && !probeWorkerSupported(items[id].RelativePath) {
				supported = false
			}
		}
		if !supported {
			for index := range state.Bundle.Results {
				if probeWorkerContentKey(state.Files[state.Bundle.Results[index].MediaID]) == key {
					state.Bundle.Results[index].Status = "unsupported"
					state.Bundle.Results[index].Error = "首版不支持此容器的远程探测"
				}
			}
			continue
		}
		link, err := client.link(ctx, file)
		if err != nil {
			markProbeWorkerFailed(state, key, err)
			return finish(err)
		}
		gate.mu.Lock()
		gate.stats.ProbeCalls++
		gate.mu.Unlock()
		probe, err := runProbeWorkerRead(ctx, file, link, gate, deps.media, deps.validateURL, deps.probe)
		previousProbeEnd = time.Now()
		if err != nil {
			markProbeWorkerFailed(state, key, err)
			return finish(err)
		}
		merged, err := mergeProbeWorkerCache(cached, probe, file.Size, "ffprobe")
		if err != nil {
			markProbeWorkerFailed(state, key, err)
			return finish(err)
		}
		state.Cache[key] = merged
		if err := save(); err != nil {
			return finish(err)
		}
	}
	return finish(nil)
}

func probeWorkerContentKey(f probeWorkerFile) string {
	return fmt.Sprintf("%s:%d", strings.ToUpper(f.SHA1), f.Size)
}

func mergeProbeWorkerCache(cached probeWorkerCache, incoming *ProbeResult, size int64, source string) (probeWorkerCache, error) {
	if err := validateExchangeProbe(cached.Metadata); err != nil {
		return probeWorkerCache{}, err
	}
	current := *cached.Metadata
	if cached.EstimatedBitRate && incoming != nil && incoming.BitRate > 0 {
		current.BitRate = 0
		cached.EstimatedBitRate = false
	}
	merged, err := mergeExchangeProbe(&current, incoming)
	if err != nil {
		return probeWorkerCache{}, err
	}
	cached.Metadata, cached.Source = merged, source
	if merged.BitRate == 0 && merged.DurationSec > 0 {
		merged.BitRate = effectiveMediaBitRate(0, size, merged.DurationSec)
		cached.EstimatedBitRate = merged.BitRate > 0
	}
	return cached, nil
}

func validateProbeWorkerState(state *probeWorkerState) error {
	if err := validateProbeWorkerTransfers(state); err != nil {
		return err
	}
	for key, cached := range state.Cache {
		parts := strings.Split(key, ":")
		if len(parts) != 2 || !probeSHA1RE.MatchString(parts[0]) {
			return errors.New("检查点内容缓存身份无效")
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || size <= 0 {
			return errors.New("检查点缓存大小无效")
		}
		if err := validateExchangeProbe(cached.Metadata); err != nil {
			return err
		}
		if cached.Source != "existing" && cached.Source != "shared" && cached.Source != "ffprobe" {
			return errors.New("检查点缓存来源无效")
		}
	}
	for _, file := range state.Files {
		if !probeDecimalID(file.ID) || file.ID == "0" || !probeSHA1RE.MatchString(file.SHA1) || file.PickCode == "" || strings.ContainsAny(file.PickCode, "\r\n") || file.Size <= 0 {
			return errors.New("检查点文件身份无效")
		}
	}
	for key, page := range state.Pages {
		parts := strings.SplitN(key, ":", 3)
		if len(parts) != 3 || !probeDecimalID(parts[1]) || parts[1] == "0" || page == nil || page.Next < 0 || page.Total < page.Next || page.Total > 10000000 || len(page.Entries) > page.Next || (page.Complete && (!page.Initialized || page.Next != page.Total)) {
			return errors.New("检查点目录分页状态无效")
		}
		seen := map[string]bool{}
		for _, entry := range page.Entries {
			if !probeDecimalID(entry.ID) || entry.ID == "0" || entry.ID == parts[1] || entry.Parent != parts[1] || seen[entry.ID] || (entry.Category != "0" && entry.Category != "1") || strings.ContainsAny(entry.Name, "/\\\x00\r\n") || entry.Name == "" || entry.Name == "." || entry.Name == ".." {
				return errors.New("检查点目录条目无效")
			}
			seen[entry.ID] = true
		}
	}
	return nil
}
func probeWorkerNeedsProbe(p *ProbeResult) bool {
	return !probeExchangeBasicComplete(p) || p.BitRate <= 0
}

func probeWorkerSupported(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".mp4", ".mkv", ".webm", ".avi", ".mov", ".m4v", ".ts", ".m2ts", ".mpeg", ".mpg", ".flv":
		return true
	}
	return false
}

func markProbeWorkerFailed(state *probeWorkerState, key string, err error) {
	for index := range state.Bundle.Results {
		row := &state.Bundle.Results[index]
		if probeWorkerContentKey(state.Files[row.MediaID]) == key {
			row.Status, row.Error = "failed", safeMediaProbeQueueError(err)
		}
	}
}

func populateProbeWorkerResults(state *probeWorkerState) {
	for index := range state.Bundle.Results {
		row := &state.Bundle.Results[index]
		file, mapped := state.Files[row.MediaID]
		if !mapped {
			continue
		}
		row.FileID, row.SHA1 = file.ID, strings.ToUpper(file.SHA1)
		for _, slot := range state.Transfers {
			if slot.Phase == "verified" && slot.FileID == file.ID {
				row.Transfer = &ProbeExchangeTransfer{SourceID: slot.SourceID, SourceFileID: slot.Source.ID, SHA1: slot.Source.SHA1, SizeBytes: slot.Source.Size, DirectoryID: slot.DirectoryID}
				break
			}
		}
		cached, found := state.Cache[probeWorkerContentKey(file)]
		if !found || row.Status == "failed" || row.Status == "unsupported" || row.Status == "mapping_conflict" {
			continue
		}
		if len(probeResultUpdates(cached.Metadata)) == 0 {
			continue
		}
		row.Metadata, row.Source, row.EstimatedBitRate = cached.Metadata, cached.Source, cached.EstimatedBitRate
		row.UnknownFields = probeExchangeUnknownFields(cached.Metadata)
		row.Status = "partial"
		if !probeWorkerNeedsProbe(cached.Metadata) {
			row.Status = "completed"
		}
	}
}

func inventoryProbeWorker(ctx context.Context, client *probe115Client, manifest *ProbeExchangeManifest, plan *ProbeWorkerPlan, state *probeWorkerState, save func() error) error {
	items := map[string]ProbeExchangeItem{}
	for _, item := range manifest.Items {
		items[item.MediaID] = item
	}
	for bindingIndex, binding := range plan.Batch.Roots {
		// Automatic copies were already mapped and verified above.
		if binding.SourceID != "" {
			continue
		}
		wanted := map[string][]string{}
		for _, id := range plan.Batch.MediaIDs {
			index, relative, err := probeWorkerBinding(items[id], plan.Batch.Roots)
			if err != nil {
				return err
			}
			if index == bindingIndex {
				wanted[relative] = append(wanted[relative], id)
			}
		}
		if len(wanted) == 0 {
			continue
		}
		type directory struct{ id, prefix string }
		directories := []directory{{id: binding.DirectoryID}}
		for len(directories) > 0 {
			dir := directories[0]
			directories = directories[1:]
			pageKey := fmt.Sprintf("%d:%s:%s", bindingIndex, dir.id, dir.prefix)
			desiredNames := map[string]bool{}
			for relative := range wanted {
				if strings.HasPrefix(relative, dir.prefix) {
					desiredNames[strings.SplitN(strings.TrimPrefix(relative, dir.prefix), "/", 2)[0]] = true
				}
			}
			page := state.Pages[pageKey]
			if page == nil {
				page = &probeWorkerPage{}
				state.Pages[pageKey] = page
			}
			for !page.Complete {
				entries, total, err := client.list(ctx, dir.id, page.Next)
				if err != nil {
					return err
				}
				if page.Initialized && page.Total != total {
					return errors.New("B 目录内容在分页期间发生变化，停止映射")
				}
				if len(entries)+page.Next > total {
					return errors.New("B 目录分页长度超过总数")
				}
				seen := map[string]bool{}
				for _, entry := range page.Entries {
					seen[entry.ID] = true
				}
				for _, entry := range entries {
					if !probeDecimalID(entry.ID) || entry.ID == "0" || entry.ID == dir.id || entry.Parent != dir.id || seen[entry.ID] || entry.Name == "" || strings.ContainsAny(entry.Name, "/\\\x00\r\n") || entry.Name == "." || entry.Name == ".." || (entry.Category != "0" && entry.Category != "1") {
						return errors.New("B 目录响应存在无效、重复或越界条目")
					}
					seen[entry.ID] = true
				}
				for _, entry := range entries {
					if desiredNames[entry.Name] {
						page.Entries = append(page.Entries, entry)
					}
				}
				page.Total, page.Initialized = total, true
				page.Next += len(entries)
				page.Complete = page.Next >= total
				if err := save(); err != nil {
					return err
				}
			}
			byName := map[string][]probe115Entry{}
			for _, entry := range page.Entries {
				byName[entry.Name] = append(byName[entry.Name], entry)
			}
			nextNames := map[string]bool{}
			for relative, mediaIDs := range wanted {
				if !strings.HasPrefix(relative, dir.prefix) {
					continue
				}
				rest := strings.TrimPrefix(relative, dir.prefix)
				parts := strings.SplitN(rest, "/", 2)
				matches := byName[parts[0]]
				if len(matches) != 1 {
					return errors.New("B 中缺少指定文件、目录或存在同名冲突")
				}
				entry := matches[0]
				if len(parts) == 2 {
					if entry.Category != "0" {
						return errors.New("B 路径层级与原始转存目录不一致")
					}
					if !nextNames[entry.Name] {
						directories = append(directories, directory{id: entry.ID, prefix: dir.prefix + entry.Name + "/"})
						nextNames[entry.Name] = true
					}
					continue
				}
				if entry.Category != "1" || !probeSHA1RE.MatchString(entry.SHA1) || entry.PickCode == "" || entry.Size <= 0 {
					return errors.New("B 文件缺少可靠的内容身份")
				}
				for _, id := range mediaIDs {
					if int64(entry.Size) != items[id].SizeBytes {
						return errors.New("B 转存文件大小与主库快照不一致")
					}
					state.Files[id] = probeWorkerFile{ID: entry.ID, PickCode: entry.PickCode, SHA1: strings.ToUpper(entry.SHA1), Size: int64(entry.Size)}
				}
			}
			if err := save(); err != nil {
				return err
			}
		}
	}
	for _, id := range plan.Batch.MediaIDs {
		if _, found := state.Files[id]; !found {
			return errors.New("B 文件映射不完整")
		}
	}
	return nil
}
