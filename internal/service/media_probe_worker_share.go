package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Source links are private configuration, never copied into plans or results.
type ProbeWorkerShareSource struct {
	RootID         string `json:"root_id"`
	RelativePrefix string `json:"relative_prefix"`
	ShareURL       string `json:"share_url"`
	DirectoryID    string `json:"share_directory_id"`
}

type ProbeWorkerSources struct {
	Shares map[string]ProbeWorkerShareSource `json:"shares"`
}

type probeWorkerShare struct {
	RootID, RelativePrefix, Code, Password, DirectoryID string
}

func probeWorkerHasTransfers(batch ProbeWorkerBatch) bool {
	for _, root := range batch.Roots {
		if root.SourceID != "" {
			return true
		}
	}
	return false
}

func ProbeWorkerSourcesTemplate(m *ProbeExchangeManifest) ProbeWorkerSources {
	s := ProbeWorkerSources{Shares: map[string]ProbeWorkerShareSource{}}
	for _, item := range m.Items {
		s.Shares[item.RootID] = ProbeWorkerShareSource{RootID: item.RootID, DirectoryID: "0"}
	}
	return s
}

func readProbeWorkerSources(filename string) (*ProbeWorkerSources, error) {
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("来源文件无效、过大或权限不私密")
	}
	var sources ProbeWorkerSources
	if err := ReadProbeWorkerJSON(filename, &sources); err != nil {
		return nil, err
	}
	return &sources, nil
}

// Plan reads only this source file, never the account token/Cookie file.
func ReadProbeWorkerSources(filename string) (*ProbeWorkerSources, error) {
	return readProbeWorkerSources(filename)
}

func parseProbeWorkerShare(source ProbeWorkerShareSource) (probeWorkerShare, error) {
	u, err := url.Parse(source.ShareURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (strings.ToLower(u.Hostname()) != "115.com" && strings.ToLower(u.Hostname()) != "115cdn.com" && strings.ToLower(u.Hostname()) != "anxia.com") {
		return probeWorkerShare{}, errors.New("来源必须是完整的 HTTPS 115 分享链接")
	}
	code := strings.TrimPrefix(u.Path, "/s/")
	if !strings.HasPrefix(u.Path, "/s/") || !probeShareCode(code) {
		return probeWorkerShare{}, errors.New("来源分享码无效")
	}
	password := u.Query().Get("password")
	if password == "" {
		password = u.Query().Get("pwd")
	}
	directory := source.DirectoryID
	if strings.HasPrefix(u.Fragment, "/list/share/") {
		fragmentID := strings.TrimPrefix(u.Fragment, "/list/share/")
		if directory != "" && directory != fragmentID {
			return probeWorkerShare{}, errors.New("来源分享目录与链接中的目录不一致")
		}
		directory = fragmentID
	} else if u.Fragment != "" {
		if password != "" && password != u.Fragment {
			return probeWorkerShare{}, errors.New("来源链接包含冲突的提取码")
		}
		password = u.Fragment
	}
	if directory == "" {
		directory = "0"
	}
	if !probeDecimalID(directory) || (password != "" && !probeShareCode(password)) {
		return probeWorkerShare{}, errors.New("分享目录或提取码格式无效")
	}
	return probeWorkerShare{source.RootID, source.RelativePrefix, code, password, directory}, nil
}

func probeShareCode(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func probeWorkerSourcesDigest(batch ProbeWorkerBatch, sources *ProbeWorkerSources) (string, error) {
	selected := map[string]probeWorkerShare{}
	for _, root := range batch.Roots {
		if root.SourceID == "" {
			continue
		}
		if sources == nil {
			return "", errors.New("自动转存必须指定私密来源文件 -sources")
		}
		source, exists := sources.Shares[root.SourceID]
		if !exists || source.RootID != root.RootID || source.RelativePrefix != root.RelativePrefix {
			return "", errors.New("来源别名、库根或相对前缀与批次不一致")
		}
		share, err := parseProbeWorkerShare(source)
		if err != nil {
			return "", err
		}
		selected[root.SourceID] = share
	}
	if len(selected) == 0 {
		return "", nil
	}
	return probeExchangeDigest(selected)
}

type probeShareEntry struct {
	ID, Name, SHA1 string
	Size           int64
	Directory      bool
}

type probeShareID probeWireNumber

func (id *probeShareID) UnmarshalJSON(data []byte) error {
	// Directory entries may explicitly have an empty fid, as well as omit it.
	if string(data) == `""` {
		*id = 0
		return nil
	}
	var number probeWireNumber
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*id = probeShareID(number)
	return nil
}

type probeShareClient struct {
	client                     *http.Client
	gate                       *probeWorkerGate
	cookie, base, identityBase string
}

// Match the established web share client; the OpenAPI/media UA remains fixed
// independently for download-link and Range consistency.
const probeWorkerShareUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36"

// Uses the same snap/receive protocol as pipeline.Share115Client. No page
// warmup, CookieJar, redirect following, login refresh or request replay.
func (c *probeShareClient) request(ctx context.Context, kind, endpoint string, form url.Values, share *probeWorkerShare) ([]byte, error) {
	method := http.MethodGet
	var body io.Reader
	if form != nil {
		method, body = http.MethodPost, strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, errors.New("无法构造分享请求")
	}
	req.Header.Set("User-Agent", probeWorkerShareUA)
	if share != nil {
		req.Header.Set("Referer", "https://115cdn.com/s/"+share.Code+"?password="+url.QueryEscape(share.Password)+"&")
	}
	if kind == "receive" {
		req.Header.Set("Origin", "https://115cdn.com")
	}
	if kind == "create_share" {
		req.Header.Set("Referer", "https://webapi.115.com")
	}
	if kind == "receive" || kind == "cookie_identity" || kind == "create_share" {
		req.Header.Set("Cookie", c.cookie)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	}
	if err := c.gate.take(ctx, kind); err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("分享 %s 连接失败，已暂停；未自动重试", kind)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("分享 %s HTTP %d，已暂停", kind, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return nil, errors.New("分享响应读取失败或超过限制，已暂停")
	}
	if kind != "cookie_identity" {
		var envelope struct {
			State bool            `json:"state"`
			Code  probeWireNumber `json:"code"`
			Errno probeWireNumber `json:"errno"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			return nil, fmt.Errorf("分享 %s 响应结构无效，已暂停", kind)
		}
		if !envelope.State || envelope.Code != 0 || envelope.Errno != 0 {
			return nil, fmt.Errorf("分享 %s 业务码 %d/%d 未确认成功，已暂停且不重放", kind, envelope.Code, envelope.Errno)
		}
	}
	return data, nil
}

func validateProbeWorkerCookie(cookie, userID string) error {
	values := map[string]string{}
	for _, field := range strings.Split(cookie, ";") {
		if strings.TrimSpace(field) == "" {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok || key == "" || value == "" || values[key] != "" {
			return errors.New("B Cookie 缺失、重复或格式无效")
		}
		values[key] = value
	}
	if strings.SplitN(values["UID"], "_", 2)[0] != userID || values["CID"] == "" || values["SEID"] == "" {
		return errors.New("自动转存需要 B 本人的完整 Cookie，UID 必须与 B 账号一致")
	}
	return nil
}

func (c *probeShareClient) verifyAccount(ctx context.Context, expected string) error {
	data, err := c.request(ctx, "cookie_identity", c.identityBase+"/files/uploadinfo", nil, nil)
	if err != nil {
		return err
	}
	var result struct {
		ID    probeWireNumber `json:"user_id"`
		Errno probeWireNumber `json:"errno"`
		State json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.Errno != 0 || strconv.FormatInt(int64(result.ID), 10) != expected || (len(result.State) > 0 && string(result.State) != "true" && string(result.State) != "1") {
		return errors.New("Cookie 的实际身份无效或不匹配，未转存")
	}
	return nil
}

func (c *probeShareClient) list(ctx context.Context, share probeWorkerShare, directory string) ([]probeShareEntry, error) {
	all, seen := []probeShareEntry{}, map[string]bool{}
	offset, total := 0, int64(-1)
	for {
		query := url.Values{"share_code": {share.Code}, "receive_code": {share.Password}, "cid": {directory}, "offset": {strconv.Itoa(offset)}, "limit": {"1000"}, "asc": {"1"}, "o": {"file_name"}, "format": {"json"}}
		data, err := c.request(ctx, "share", c.base+"/share/snap?"+query.Encode(), nil, &share)
		if err != nil {
			return nil, err
		}
		var response struct {
			Data struct {
				Count *probeWireNumber `json:"count"`
				List  []struct {
					FID  probeShareID    `json:"fid"`
					CID  probeShareID    `json:"cid"`
					Name string          `json:"n"`
					Size probeWireNumber `json:"s"`
					SHA1 string          `json:"sha1"`
				} `json:"list"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, errors.New("来源分享清单格式无效")
		}
		if response.Data.Count != nil {
			n := int64(*response.Data.Count)
			if n < 0 || n > 10000000 || total >= 0 && total != n {
				return nil, errors.New("来源分享分页数量变化或无效")
			}
			total = n
		}
		for _, row := range response.Data.List {
			id, directoryEntry := row.FID, row.FID == 0
			if directoryEntry {
				id = row.CID
			}
			key := strconv.FormatInt(int64(id), 10)
			if id <= 0 || seen[key] || row.Name == "" || row.Name == "." || row.Name == ".." || strings.ContainsAny(row.Name, "/\\\x00\r\n") {
				return nil, errors.New("来源分享包含无效或重复条目")
			}
			seen[key] = true
			all = append(all, probeShareEntry{ID: key, Name: row.Name, SHA1: strings.ToUpper(row.SHA1), Size: int64(row.Size), Directory: directoryEntry})
		}
		offset += len(response.Data.List)
		if total >= 0 {
			if int64(offset) > total || len(response.Data.List) == 0 && int64(offset) < total {
				return nil, errors.New("来源分享分页缺项或越界")
			}
			if int64(offset) == total {
				return all, nil
			}
		} else if len(response.Data.List) < 1000 {
			return all, nil
		}
	}
}

func (c *probeShareClient) receive(ctx context.Context, share probeWorkerShare, sourceID, target string) error {
	_, err := c.request(ctx, "receive", c.base+"/share/receive", url.Values{"share_code": {share.Code}, "receive_code": {share.Password}, "file_id": {sourceID}, "cid": {target}}, &share)
	return err // Success is accepted only after independent B SHA1/size verification.
}
