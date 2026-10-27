package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SourceID selects a private source share. DirectoryID is its B staging parent.
// With no SourceID, DirectoryID contains an existing unchanged source copy.
type ProbeWorkerRoot struct {
	RootID         string `json:"root_id"`
	RelativePrefix string `json:"relative_prefix"`
	DirectoryID    string `json:"b_directory_id"`
	SourceID       string `json:"source_id,omitempty"`
}

type ProbeWorkerLimits struct {
	ListRequests      int   `json:"list_requests"`
	LinkRequests      int   `json:"link_requests"`
	ControlRequests   int   `json:"control_requests"`
	MediaRequests     int   `json:"media_requests"`
	MediaRequestsFile int   `json:"media_requests_per_file"`
	MediaBytes        int64 `json:"media_bytes"`
	MediaBytesFile    int64 `json:"media_bytes_per_file"`
	ControlGapSeconds int   `json:"control_gap_seconds"`
	MediaGapSeconds   int   `json:"media_gap_seconds"`
	TaskGapSeconds    int   `json:"task_gap_seconds"`
	ProbeSeconds      int   `json:"probe_seconds"`
	RunSeconds        int   `json:"run_seconds"`
	SharedRequests    int   `json:"shared_requests"`
	ShareRequests     int   `json:"share_requests,omitempty"`
	ReceiveRequests   int   `json:"receive_requests,omitempty"`
	MkdirRequests     int   `json:"mkdir_requests,omitempty"`
}

func DefaultProbeWorkerTransferLimits() ProbeWorkerLimits {
	l := DefaultProbeWorkerLimits()
	l.ControlRequests, l.ListRequests, l.RunSeconds = 80, 50, 900
	l.ShareRequests, l.ReceiveRequests, l.MkdirRequests = 20, 10, 10
	return l
}

func DefaultProbeWorkerLimits() ProbeWorkerLimits {
	return ProbeWorkerLimits{ListRequests: 20, LinkRequests: 10, ControlRequests: 32,
		MediaRequests: 60, MediaRequestsFile: 6, MediaBytes: 320 << 20, MediaBytesFile: 32 << 20,
		ControlGapSeconds: 5, MediaGapSeconds: 1, TaskGapSeconds: 10, ProbeSeconds: 30, RunSeconds: 600, SharedRequests: 1}
}

type ProbeWorkerBatch struct {
	ID               string            `json:"id"`
	AccountID        string            `json:"b_account_id"`
	Provenance       string            `json:"provenance"`
	OriginalCopy     bool              `json:"confirmed_original_copy"`
	MediaIDs         []string          `json:"media_ids"`
	Roots            []ProbeWorkerRoot `json:"roots"`
	Limits           ProbeWorkerLimits `json:"limits"`
	RefreshBeforeRun bool              `json:"refresh_before_run"`
}

type ProbeWorkerPlan struct {
	Version        int              `json:"version"`
	ID             string           `json:"id"`
	ManifestID     string           `json:"manifest_id"`
	ManifestDigest string           `json:"manifest_digest"`
	CreatedAt      time.Time        `json:"created_at"`
	ExpiresAt      time.Time        `json:"expires_at"`
	Batch          ProbeWorkerBatch `json:"batch"`
	Targets        []string         `json:"targets"`
	Notice         string           `json:"notice"`
	Digest         string           `json:"digest"`
	SourcesDigest  string           `json:"sources_digest,omitempty"`
}

type ProbeWorkerStats struct {
	ControlRequests  int   `json:"control_requests"`
	ListRequests     int   `json:"list_requests"`
	LinkRequests     int   `json:"link_requests"`
	RefreshRequests  int   `json:"refresh_requests"`
	IdentityRequests int   `json:"identity_requests"`
	MediaRequests    int   `json:"media_requests"`
	MediaBytes       int64 `json:"media_bytes"`
	SharedRequests   int   `json:"shared_requests"`
	SharedHits       int   `json:"shared_hits"`
	SharedRawHits    int   `json:"shared_raw_hits"`
	SharedLookups    int   `json:"shared_lookups"`
	ExistingHits     int   `json:"existing_hits"`
	ProbeCalls       int   `json:"probe_calls"`
	ShareRequests    int   `json:"share_requests,omitempty"`
	ReceiveRequests  int   `json:"receive_requests,omitempty"`
	MkdirRequests    int   `json:"mkdir_requests,omitempty"`
	CookieIdentities int   `json:"cookie_identity_requests,omitempty"`
}

func CreateProbeWorkerPlan(m *ProbeExchangeManifest, batch ProbeWorkerBatch) (*ProbeWorkerPlan, error) {
	return CreateProbeWorkerPlanWithSources(m, batch, nil)
}

func CreateProbeWorkerPlanWithSources(m *ProbeExchangeManifest, batch ProbeWorkerBatch, sources *ProbeWorkerSources) (*ProbeWorkerPlan, error) {
	if err := m.CheckDigest(); err != nil {
		return nil, err
	}
	if err := validateProbeWorkerBatch(m, &batch); err != nil {
		return nil, err
	}
	sourceDigest, err := probeWorkerSourcesDigest(batch, sources)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	p := &ProbeWorkerPlan{Version: ProbeExchangeVersion, ID: uuid.NewString(), ManifestID: m.ID, ManifestDigest: m.Digest,
		CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute), Batch: batch,
		Targets:       []string{"proapi.115.com/open/user/info", "proapi.115.com/open/ufile/files", "proapi.115.com/open/ufile/downurl", "115server.ddsrem.com/emby_mediainfo_data/get", "B 直链指定的 115 CDN"},
		SourcesDigest: sourceDigest,
		Notice:        "仅处理指定 B 目录和清单。真实访问 115/CDN，可能限流、封禁、账号关联或链路失败。并发 1，失败即停，无自动重试、OpenList refresh、创建 A 分享或删除。请求、响应体字节、间隔和时限见 batch.limits。"}
	if sourceDigest != "" {
		p.Targets = append(p.Targets, "webapi.115.com/files/uploadinfo（核对 B Cookie 身份一次）", "115cdn.com/webapi/share/snap（指定来源清单）", "115cdn.com/webapi/share/receive（仅将选中文件转存到 B）", "proapi.115.com/open/folder/add（B 专用文件暂存目录）")
		p.Notice += "本轮包含自动转存和建目录；来源分享只从私密配置读取，不输出链接。转存结果不明时仅核对，不重复提交。"
	}
	if batch.RefreshBeforeRun {
		p.Targets = append(p.Targets, "passportapi.115.com/open/refreshToken（最多一次，计入控制请求总量）")
	}
	p.Digest, err = probeExchangeDigest(p)
	return p, err
}

func (p ProbeWorkerPlan) Check(m *ProbeExchangeManifest) error {
	expected := p.Digest
	p.Digest = ""
	actual, err := probeExchangeDigest(p)
	if err != nil || p.Version != ProbeExchangeVersion || expected == "" || expected != actual || p.ID == "" || p.ManifestID != m.ID || p.ManifestDigest != m.Digest {
		return errors.New("执行计划与任务清单不一致或校验和无效")
	}
	if time.Now().After(p.ExpiresAt) || p.CreatedAt.After(time.Now().Add(time.Minute)) || p.ExpiresAt.Sub(p.CreatedAt) > 30*time.Minute {
		return errors.New("执行计划已过期，请重新生成并审阅")
	}
	if probeWorkerHasTransfers(p.Batch) != (probeSHA256RE.MatchString(p.SourcesDigest)) || (!probeWorkerHasTransfers(p.Batch) && p.SourcesDigest != "") {
		return errors.New("自动转存计划缺少有效来源配置摘要")
	}
	return validateProbeWorkerBatch(m, &p.Batch)
}

func validateProbeWorkerBatch(m *ProbeExchangeManifest, b *ProbeWorkerBatch) error {
	if b.ID == "" || !probeDecimalID(b.AccountID) || b.AccountID == "0" || !b.OriginalCopy || strings.TrimSpace(b.Provenance) == "" || len(b.Provenance) > 1024 || strings.Contains(b.Provenance, "://") {
		return errors.New("请指定 B 账号 ID、批次、原样来源确认和来源说明（不填写分享链接）")
	}
	l := b.Limits
	if len(b.MediaIDs) < 1 || len(b.MediaIDs) > 100 || len(b.Roots) == 0 || len(b.Roots) > 100 ||
		l.ListRequests < 1 || l.ListRequests > 1000 || l.LinkRequests < 1 || l.LinkRequests > 100 ||
		l.ControlRequests < 1 || l.ControlRequests > 1102 || l.MediaRequests < 1 || l.MediaRequests > 2000 ||
		l.MediaRequestsFile < 1 || l.MediaRequestsFile > 20 || l.MediaBytesFile < 1 || l.MediaBytesFile > 256<<20 ||
		l.MediaBytes < l.MediaBytesFile || l.MediaBytes > 25<<30 || l.ControlGapSeconds < 5 || l.ControlGapSeconds > 3600 ||
		l.MediaGapSeconds < 1 || l.MediaGapSeconds > 3600 || l.TaskGapSeconds < 10 || l.TaskGapSeconds > 3600 ||
		l.ProbeSeconds < 5 || l.ProbeSeconds > 30 || l.RunSeconds < 10 || l.RunSeconds > 3600 || l.SharedRequests < 1 || l.SharedRequests > 2 {
		return errors.New("批次或预算超出支持范围；每轮最多 100 项、并发固定为 1")
	}
	for _, root := range b.Roots {
		if root.RootID == "" || !probeDecimalID(root.DirectoryID) || root.DirectoryID == "0" {
			return errors.New("请指定 B 的专用批次目录，禁止以账号根目录作为遍历起点")
		}
		if root.RelativePrefix != "" {
			if err := validateProbeRelativePath(root.RelativePrefix); err != nil {
				return err
			}
		}
		if root.SourceID != "" && (len(root.SourceID) > 128 || strings.TrimSpace(root.SourceID) != root.SourceID || strings.ContainsAny(root.SourceID, "/\\\x00\r\n") || strings.Contains(root.SourceID, ":")) {
			return errors.New("来源别名无效，不能填写分享链接")
		}
	}
	if probeWorkerHasTransfers(*b) && (l.ShareRequests < 1 || l.ShareRequests > 80 || l.ReceiveRequests < 1 || l.ReceiveRequests > 100 || l.MkdirRequests < 1 || l.MkdirRequests > 100 || l.ControlRequests > 80 || l.RunSeconds > 900) {
		return errors.New("自动转存需显式分享/转存/建目录预算；本版每轮控制请求最多 80 次、15 分钟")
	}
	seen := map[string]bool{}
	items := map[string]ProbeExchangeItem{}
	for _, item := range m.Items {
		items[item.MediaID] = item
	}
	for _, id := range b.MediaIDs {
		item, found := items[id]
		if !found || seen[id] {
			return errors.New("指定媒体重复或不属于任务清单")
		}
		seen[id] = true
		if _, _, err := probeWorkerBinding(item, b.Roots); err != nil {
			return err
		}
	}
	return nil
}

func probeWorkerBinding(item ProbeExchangeItem, roots []ProbeWorkerRoot) (int, string, error) {
	matched, relative := -1, ""
	for index, root := range roots {
		if root.RootID != item.RootID {
			continue
		}
		value := item.RelativePath
		if root.RelativePrefix != "" {
			if !strings.HasPrefix(value, root.RelativePrefix+"/") {
				continue
			}
			value = strings.TrimPrefix(value, root.RelativePrefix+"/")
		}
		if matched >= 0 {
			return -1, "", errors.New("同一媒体匹配多个 B 目录绑定，请消除重叠")
		}
		matched, relative = index, value
	}
	if matched < 0 {
		return -1, "", fmt.Errorf("媒体 %s 没有对应的 B 转存目录", item.MediaID)
	}
	return matched, relative, nil
}

func probeDecimalID(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func ProbeWorkerBatchTemplate(m *ProbeExchangeManifest) ProbeWorkerBatch {
	b := ProbeWorkerBatch{ID: uuid.NewString(), Limits: DefaultProbeWorkerTransferLimits(), MediaIDs: []string{}, Roots: []ProbeWorkerRoot{}}
	perLibrary, roots := map[string]int{}, map[string]bool{}
	for _, item := range m.Items {
		if probeExchangeBasicComplete(item.Metadata) && item.Metadata.BitRate > 0 {
			continue
		}
		if len(b.MediaIDs) < 10 && perLibrary[item.LibraryID] < 2 {
			b.MediaIDs = append(b.MediaIDs, item.MediaID)
			perLibrary[item.LibraryID]++
		}
		if !roots[item.RootID] {
			b.Roots = append(b.Roots, ProbeWorkerRoot{RootID: item.RootID, DirectoryID: "SET_B_DIRECTORY_ID", SourceID: item.RootID})
			roots[item.RootID] = true
		}
	}
	return b
}

// Read/write artifacts are private and are never accepted as credential files
// implicitly. The command requires separate explicit file paths for each role.
func ReadProbeWorkerJSON(filename string, dst any) error {
	f, err := os.Open(filename)
	if err != nil {
		return errors.New("无法打开指定 JSON 文件")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > ProbeExchangeMaxBytes {
		return errors.New("JSON 文件类型无效或超过 64 MiB")
	}
	decoder := json.NewDecoder(io.LimitReader(f, ProbeExchangeMaxBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return errors.New("JSON 文件结构无效或包含未知字段")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("JSON 文件包含额外内容")
	}
	return nil
}

func WriteProbeWorkerJSON(filename string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > ProbeExchangeMaxBytes {
		return errors.New("JSON 输出超过 64 MiB")
	}
	return writeProbeWorkerPrivate(filename, append(data, '\n'))
}

func writeProbeWorkerPrivate(filename string, data []byte) error {
	dir := path.Dir(strings.ReplaceAll(filename, "\\", "/"))
	tmp, err := os.CreateTemp(dir, ".probe-write-*")
	if err != nil {
		return errors.New("无法创建私密结果文件")
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filename)
}
