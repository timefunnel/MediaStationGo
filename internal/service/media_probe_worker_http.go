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
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errProbeWorkerBudget = errors.New("已达到批准的请求、流量或时间预算")

type probeWorkerGate struct {
	mu                     sync.Mutex
	limits                 ProbeWorkerLimits
	stats                  ProbeWorkerStats
	lastControl, lastMedia time.Time
	onRequest              func(ProbeWorkerStats) error
	wait                   func(context.Context, time.Duration) error
}

func (g *probeWorkerGate) take(ctx context.Context, kind string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	l, s := g.limits, &g.stats
	gap, last := time.Duration(l.ControlGapSeconds)*time.Second, &g.lastControl
	if kind == "media" {
		gap, last = time.Duration(l.MediaGapSeconds)*time.Second, &g.lastMedia
		if s.MediaRequests >= l.MediaRequests {
			return errProbeWorkerBudget
		}
	} else if kind == "shared" {
		if s.SharedRequests >= l.SharedRequests {
			return errProbeWorkerBudget
		}
		gap = 0
	} else {
		if s.ControlRequests >= l.ControlRequests {
			return errProbeWorkerBudget
		}
		switch kind {
		case "list":
			if s.ListRequests >= l.ListRequests {
				return errProbeWorkerBudget
			}
		case "link":
			if s.LinkRequests >= l.LinkRequests {
				return errProbeWorkerBudget
			}
		case "identity":
			if s.IdentityRequests >= 1 {
				return errProbeWorkerBudget
			}
		case "refresh":
			if s.RefreshRequests >= 1 {
				return errProbeWorkerBudget
			}
		case "share":
			if s.ShareRequests >= l.ShareRequests {
				return errProbeWorkerBudget
			}
		case "receive":
			if s.ReceiveRequests >= l.ReceiveRequests {
				return errProbeWorkerBudget
			}
		case "mkdir":
			if s.MkdirRequests >= l.MkdirRequests {
				return errProbeWorkerBudget
			}
		case "cookie_identity":
			if s.CookieIdentities >= 1 {
				return errProbeWorkerBudget
			}
		default:
			return errors.New("未知请求类别")
		}
	}
	wait := g.wait
	if wait == nil {
		wait = waitMediaProbeQueue
	}
	if err := wait(ctx, time.Until(last.Add(gap))); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	*last = time.Now()
	switch kind {
	case "media":
		s.MediaRequests++
	case "shared":
		s.SharedRequests++
	default:
		s.ControlRequests++
		switch kind {
		case "list":
			s.ListRequests++
		case "link":
			s.LinkRequests++
		case "identity":
			s.IdentityRequests++
		case "refresh":
			s.RefreshRequests++
		case "share":
			s.ShareRequests++
		case "receive":
			s.ReceiveRequests++
		case "mkdir":
			s.MkdirRequests++
		case "cookie_identity":
			s.CookieIdentities++
		}
	}
	if g.onRequest != nil {
		return g.onRequest(*s)
	}
	return nil
}

func (g *probeWorkerGate) snapshot() ProbeWorkerStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

func newProbeWorkerHTTPClient() *http.Client {
	// Direct, fresh HTTP/1 connections avoid proxy inheritance and transport
	// retries on stale pooled connections. Redirects are handled and counted by
	// the media reader; control redirects are rejected.
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
			DialContext: probeWorkerPublicDial, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}}}
}

func probeWorkerPublicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("上游地址无效")
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("上游 DNS 解析失败")
	}
	for _, value := range ips {
		if !value.IsGlobalUnicast() || value.IsPrivate() || value.IsLoopback() || value.IsLinkLocalUnicast() {
			return nil, errors.New("上游解析到了非公网地址")
		}
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp4", net.JoinHostPort(ips[0].String(), port))
}

type probeWorkerCredentials struct {
	UserID       string `json:"user_id"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
	Cookie       string `json:"cookie,omitempty"`
}

func readProbeWorkerCredentials(filename string) (*probeWorkerCredentials, error) {
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32<<10 {
		return nil, errors.New("凭据文件无效")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("凭据文件权限应为 0600 或更严格")
	}
	var c probeWorkerCredentials
	if err := ReadProbeWorkerJSON(filename, &c); err != nil {
		return nil, err
	}
	if !probeDecimalID(c.UserID) || c.AccessToken == "" || len(c.AccessToken) > 8192 || len(c.RefreshToken) > 8192 || strings.ContainsAny(c.AccessToken+c.RefreshToken, "\r\n") {
		return nil, errors.New("凭据文件缺少 B 用户 ID 或有效令牌")
	}
	if len(c.Cookie) > 16384 || strings.ContainsAny(c.Cookie, "\r\n\x00") {
		return nil, errors.New("B Cookie 格式无效")
	}
	return &c, nil
}

type probeWorkerFile struct {
	ID       string `json:"id"`
	PickCode string `json:"pick_code"`
	SHA1     string `json:"sha1"`
	Size     int64  `json:"size_bytes"`
}

type probeWireNumber int64

func (n *probeWireNumber) UnmarshalJSON(data []byte) error {
	var value json.Number
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("115 返回了无效数值")
	}
	v, err := strconv.ParseInt(value.String(), 10, 64)
	if err != nil {
		return errors.New("115 返回了无效整数")
	}
	*n = probeWireNumber(v)
	return nil
}

type probe115Client struct {
	validateURL    func(string) error
	client         *http.Client
	gate           *probeWorkerGate
	credentials    *probeWorkerCredentials
	base, authBase string
	ua             string
}

const probeWorkerUA = "MediaStationGo-MetadataWorker/1"

func (c *probe115Client) request(ctx context.Context, kind, endpoint string, form url.Values, dst any) error {
	method := http.MethodGet
	var body io.Reader
	if form != nil {
		method = http.MethodPost
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return errors.New("无法构造 115 请求")
	}
	req.Header.Set("User-Agent", c.ua)
	if kind != "refresh" {
		req.Header.Set("Authorization", "Bearer "+c.credentials.AccessToken)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if err := c.gate.take(ctx, kind); err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("115 %s 请求失败（无自动重试）: %s", kind, safeMediaProbeQueueError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("115 %s HTTP %d，已暂停", kind, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return errors.New("115 响应读取失败或超过大小限制")
	}
	if kind == "refresh" {
		var result struct {
			Code  int             `json:"code"`
			Data  json.RawMessage `json:"data"`
			Error string          `json:"error"`
		}
		if err := json.Unmarshal(data, &result); err != nil || result.Code != 0 || result.Error != "" || len(result.Data) == 0 {
			return errors.New("115 令牌刷新失败，已暂停")
		}
		if err := json.Unmarshal(result.Data, dst); err != nil {
			return errors.New("115 刷新响应结构无效")
		}
		return nil
	}
	var envelope struct {
		State bool            `json:"state"`
		Code  int             `json:"code"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return errors.New("115 响应结构无效")
	}
	if !envelope.State {
		return fmt.Errorf("115 %s 业务码 %d，已暂停且不刷新重放", kind, envelope.Code)
	}
	if kind == "list" {
		if err := json.Unmarshal(data, dst); err != nil {
			return errors.New("115 目录响应结构无效")
		}
	} else if err := json.Unmarshal(envelope.Data, dst); err != nil {
		return errors.New("115 响应 data 结构无效")
	}
	return nil
}

func (c *probe115Client) verifyAccount(ctx context.Context, expected string) error {
	var info struct {
		ID probeWireNumber `json:"user_id"`
	}
	if err := c.request(ctx, "identity", c.base+"/open/user/info", nil, &info); err != nil {
		return err
	}
	if strconv.FormatInt(int64(info.ID), 10) != expected || c.credentials.UserID != expected {
		return errors.New("115 实际账号与计划中的 B 账号不一致")
	}
	return nil
}

type probe115Entry struct {
	ID       string          `json:"fid"`
	Parent   string          `json:"pid"`
	Category string          `json:"fc"`
	Name     string          `json:"fn"`
	PickCode string          `json:"pc"`
	SHA1     string          `json:"sha1"`
	Size     probeWireNumber `json:"fs"`
}

func (c *probe115Client) list(ctx context.Context, directory string, offset int) ([]probe115Entry, int, error) {
	params := url.Values{"cid": {directory}, "offset": {strconv.Itoa(offset)}, "limit": {"1000"}, "show_dir": {"1"}, "cur": {"1"}, "o": {"file_name"}, "asc": {"1"}}
	var response struct {
		Count probeWireNumber `json:"count"`
		Data  []probe115Entry `json:"data"`
	}
	if err := c.request(ctx, "list", c.base+"/open/ufile/files?"+params.Encode(), nil, &response); err != nil {
		return nil, 0, err
	}
	if response.Count < 0 || response.Count > 10000000 || (len(response.Data) == 0 && int64(response.Count) > int64(offset)) {
		return nil, 0, errors.New("115 目录分页数量无效或中途变化")
	}
	return response.Data, int(response.Count), nil
}

func (c *probe115Client) link(ctx context.Context, file probeWorkerFile) (string, error) {
	var links map[string]struct {
		Size     probeWireNumber `json:"file_size"`
		SHA1     string          `json:"sha1"`
		PickCode string          `json:"pick_code"`
		URL      struct {
			URL string `json:"url"`
		} `json:"url"`
	}
	if err := c.request(ctx, "link", c.base+"/open/ufile/downurl", url.Values{"pick_code": {file.PickCode}}, &links); err != nil {
		return "", err
	}
	if len(links) != 1 {
		return "", errors.New("115 取链返回的文件数量不为一")
	}
	for id, value := range links {
		if id != file.ID || int64(value.Size) != file.Size || !strings.EqualFold(value.SHA1, file.SHA1) || value.PickCode != file.PickCode {
			return "", errors.New("B 取链返回的文件身份已变化")
		}
		validate := c.validateURL
		if validate == nil {
			validate = validateProbeMediaURL
		}
		if err := validate(value.URL.URL); err != nil {
			return "", err
		}
		return value.URL.URL, nil
	}
	return "", errors.New("115 未返回有效直链")
}

func validateProbeMediaURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return errors.New("B 返回的媒体地址格式无效")
	}
	host := strings.ToLower(u.Hostname())
	allowed := false
	for _, suffix := range []string{"115.com", "115cdn.net", "115cdn.com"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			allowed = true
		}
	}
	if !allowed || (u.Port() != "" && u.Port() != "443" && u.Port() != "80") {
		return errors.New("B 媒体地址不属于当前已支持的 115 CDN，停止并等待核对")
	}
	return nil
}

func (c *probe115Client) refresh(ctx context.Context, filename string) error {
	if c.credentials.RefreshToken == "" {
		return errors.New("未提供刷新令牌")
	}
	var tokens struct {
		Access  string          `json:"access_token"`
		Refresh string          `json:"refresh_token"`
		Expires probeWireNumber `json:"expires_in"`
	}
	if err := c.request(ctx, "refresh", c.authBase+"/open/refreshToken", url.Values{"refresh_token": {c.credentials.RefreshToken}}, &tokens); err != nil {
		return err
	}
	if tokens.Access == "" || tokens.Refresh == "" || tokens.Expires <= 0 {
		return errors.New("115 刷新结果不完整，必须重新核对授权")
	}
	updated := *c.credentials
	updated.AccessToken, updated.RefreshToken, updated.ExpiresAt = tokens.Access, tokens.Refresh, time.Now().Unix()+int64(tokens.Expires)
	if err := WriteProbeWorkerJSON(filename, updated); err != nil {
		return errors.New("新令牌保存失败，已停止；不能继续使用旧刷新令牌")
	}
	*c.credentials = updated
	return nil
}

func readProbeWorkerResponse(resp *http.Response, max int64) ([]byte, error) {
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("共享服务 HTTP %d，已暂停", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil || int64(len(data)) > max {
		return nil, errors.New("共享服务响应读取失败或超限")
	}
	return bytes.TrimSpace(data), nil
}
