package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func openListAPIMessage(code int, msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = fmt.Sprintf("code %d", code)
	}
	return msg
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
