package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

func (p *cloudDrive2Provider) listOpenListAPI(ctx context.Context, dir string) ([]FileEntry, error) {
	token, err := p.openListAPIToken(ctx)
	if err != nil {
		return nil, err
	}
	const pageSize = 500
	target := normalizeCloudDAVPath(dir)
	out := make([]FileEntry, 0, pageSize)
	for pageNum := 1; ; pageNum++ {
		payload := map[string]any{
			"path":     target,
			"password": "",
			"page":     pageNum,
			"per_page": pageSize,
			"refresh":  false,
		}
		body, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.openListAPIURL("/api/fs/list"), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", p.ua)
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, decorateDAVTransportError(p.name, p.openListAPIURL("/api/fs/list"), err)
		}
		var decoded openListListResponse
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&decoded)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, p.openListAPIStatusError("list", target, resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("%s: decode api list: %w", p.name, decodeErr)
		}
		if decoded.Code != 0 && decoded.Code != 200 {
			msg := strings.TrimSpace(decoded.Message)
			if msg == "" {
				msg = fmt.Sprintf("code %d", decoded.Code)
			}
			return nil, fmt.Errorf("%s: api list %s failed: %s", p.name, target, msg)
		}
		for _, item := range decoded.Data.Content {
			name := strings.TrimSpace(item.Name)
			if name == "" || name == "." || name == "/" {
				continue
			}
			out = append(out, FileEntry{
				ID:    joinOpenListAPIPath(target, name),
				Name:  name,
				IsDir: item.IsDir,
				Size:  item.Size,
			})
		}
		total := decoded.Data.Total
		if total > 0 {
			if len(out) >= total || len(decoded.Data.Content) == 0 {
				break
			}
			continue
		}
		if len(decoded.Data.Content) == 0 || len(decoded.Data.Content) < pageSize {
			break
		}
	}
	return out, nil
}

func (p *cloudDrive2Provider) resolveOpenListAPIDirect(ctx context.Context, fileRef string) (*DirectLink, error) {
	token, err := p.openListAPIToken(ctx)
	if err != nil {
		return nil, err
	}
	target := normalizeCloudDAVPath(fileRef)
	decoded, err := p.openListAPIGet(ctx, token, target)
	if err != nil {
		return nil, err
	}
	if openListGetNeedsParentWarmup(decoded) {
		if err := p.warmOpenListAPIParent(ctx, target); err != nil {
			return nil, fmt.Errorf("%s: api get %s failed: %s; parent list warmup failed: %w", p.name, fileRef, openListAPIMessage(decoded.Code, decoded.Message), err)
		}
		decoded, err = p.openListAPIGet(ctx, token, target)
		if err != nil {
			return nil, err
		}
	}
	if decoded.Code != 0 && decoded.Code != 200 {
		return nil, fmt.Errorf("%s: api get %s failed: %s", p.name, fileRef, openListAPIMessage(decoded.Code, decoded.Message))
	}
	return p.openListDirectLinkFromGet(ctx, fileRef, decoded)
}

func (p *cloudDrive2Provider) openListAPIGet(ctx context.Context, token, fileRef string) (openListGetResponse, error) {
	payload, _ := json.Marshal(map[string]string{"path": fileRef, "password": ""})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.openListAPIURL("/api/fs/get"), bytes.NewReader(payload))
	if err != nil {
		return openListGetResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.ua)
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return openListGetResponse{}, decorateDAVTransportError(p.name, p.openListAPIURL("/api/fs/get"), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openListGetResponse{}, p.openListAPIStatusError("get", fileRef, resp.StatusCode)
	}
	var decoded openListGetResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&decoded); err != nil {
		return openListGetResponse{}, fmt.Errorf("%s: decode api get: %w", p.name, err)
	}
	return decoded, nil
}

func (p *cloudDrive2Provider) warmOpenListAPIParent(ctx context.Context, fileRef string) error {
	parent := path.Dir(strings.TrimRight(normalizeCloudDAVPath(fileRef), "/"))
	if parent == "." || parent == "" {
		parent = "/"
	}
	_, err := p.listOpenListAPI(ctx, parent)
	return err
}

func openListGetNeedsParentWarmup(decoded openListGetResponse) bool {
	if decoded.Code == 0 || decoded.Code == 200 {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(decoded.Message))
	return decoded.Code == 430004 ||
		(decoded.Code == 500 && strings.Contains(msg, "430004")) ||
		strings.Contains(msg, "文件不存在") ||
		strings.Contains(msg, "不存在或已删除") ||
		strings.Contains(msg, "not exist")
}

func openListAPIMessage(code int, msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = fmt.Sprintf("code %d", code)
	}
	return msg
}

func (p *cloudDrive2Provider) openListDirectLinkFromGet(ctx context.Context, fileRef string, decoded openListGetResponse) (*DirectLink, error) {
	raw := firstNonEmpty(decoded.Data.RawURL, decoded.Data.URL)
	if raw == "" {
		return nil, fmt.Errorf("%s: api get %s returned empty raw_url", p.name, fileRef)
	}
	resolved, err := p.resolveOpenListPlaybackURL(raw)
	if err != nil {
		return nil, err
	}
	headers := normalizeOpenListPlaybackHeaders(decoded.Data.Header)
	if len(headers) > 0 {
		return &DirectLink{URL: resolved, Headers: headers, Proxy: true}, nil
	}
	if directURL, ok := p.resolveOpenListCDNRedirect(ctx, resolved); ok {
		return &DirectLink{URL: directURL, Headers: nil, Proxy: false}, nil
	}
	if sameURLHost(resolved, p.apiBase) {
		return &DirectLink{URL: resolved, Headers: nil, Proxy: true}, nil
	}
	return &DirectLink{URL: resolved, Headers: nil, Proxy: false}, nil
}

func (p *cloudDrive2Provider) resolveOpenListCDNRedirect(ctx context.Context, rawURL string) (string, bool) {
	if p.apiBase == nil || !sameURLHost(rawURL, p.apiBase) {
		return rawURL, true
	}
	location, _, err := p.firstHTTPRedirectLocation(ctx, rawURL, nil)
	if err == nil && location != "" {
		return location, true
	}
	return "", false
}

func (p *cloudDrive2Provider) openListAPIStatusError(action, target string, status int) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("%s: api %s %s returned http %d；请检查 OpenList Token 或用户名密码，并确认填写的是 OpenList 服务地址而不是 /dav 地址", p.name, action, target, status)
	}
	return fmt.Errorf("%s: api %s %s returned http %d", p.name, action, target, status)
}

func (p *cloudDrive2Provider) hasOpenListAPICredentials() bool {
	return strings.TrimSpace(p.token) != "" || (strings.TrimSpace(p.username) != "" && p.password != "")
}

func (p *cloudDrive2Provider) openListAPIToken(ctx context.Context) (string, error) {
	if token := strings.TrimSpace(p.token); token != "" {
		return token, nil
	}
	if strings.TrimSpace(p.username) == "" || p.password == "" {
		return "", nil
	}
	payload, _ := json.Marshal(map[string]string{
		"username": p.username,
		"password": p.password,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.openListAPIURL("/api/auth/login"), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.ua)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", decorateDAVTransportError(p.name, p.openListAPIURL("/api/auth/login"), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s: api login returned http %d", p.name, resp.StatusCode)
	}
	var decoded openListLoginResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&decoded); err != nil {
		return "", fmt.Errorf("%s: decode api login: %w", p.name, err)
	}
	if decoded.Code != 0 && decoded.Code != 200 {
		msg := strings.TrimSpace(decoded.Message)
		if msg == "" {
			msg = fmt.Sprintf("code %d", decoded.Code)
		}
		return "", fmt.Errorf("%s: api login failed: %s", p.name, msg)
	}
	token := strings.TrimSpace(decoded.Data.Token)
	if token == "" {
		return "", fmt.Errorf("%s: api login returned empty token", p.name)
	}
	p.token = token
	return token, nil
}

func (p *cloudDrive2Provider) resolveOpenListPlaybackURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%s: empty playback URL", p.name)
	}
	if strings.HasPrefix(raw, "//") {
		if p.apiBase == nil || p.apiBase.Scheme == "" {
			return "", fmt.Errorf("%s: protocol-relative playback URL without API base", p.name)
		}
		raw = p.apiBase.Scheme + ":" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s: invalid playback URL: %w", p.name, err)
	}
	if u.IsAbs() {
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", fmt.Errorf("%s: unsupported playback URL scheme %q", p.name, u.Scheme)
		}
		return u.String(), nil
	}
	if p.apiBase == nil {
		return "", fmt.Errorf("%s: relative playback URL without API base", p.name)
	}
	base := *p.apiBase
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	return base.ResolveReference(u).String(), nil
}
