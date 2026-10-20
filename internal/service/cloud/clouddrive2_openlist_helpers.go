package cloud

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
)

func sortedHeaderNames(headers map[string]string) []string {
	if len(headers) == 0 {
		return nil
	}
	out := make([]string, 0, len(headers))
	for key := range headers {
		key = strings.TrimSpace(key)
		if key != "" {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func sameURLHost(raw string, base *url.URL) bool {
	if base == nil {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if !u.IsAbs() {
		return true
	}
	return strings.EqualFold(u.Host, base.Host)
}

func normalizeOpenListPlaybackHeaders(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(obj))
	for k, v := range obj {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		switch value := v.(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				out[key] = strings.TrimSpace(value)
			}
		case []any:
			parts := make([]string, 0, len(value))
			for _, item := range value {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("invalid value for header %s", key)
				}
				if strings.TrimSpace(s) != "" {
					parts = append(parts, strings.TrimSpace(s))
				}
			}
			if len(parts) > 0 {
				out[key] = strings.Join(parts, ", ")
			}
		default:
			return nil, fmt.Errorf("invalid value for header %s", key)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func isCloudVideoPlaybackCandidate(fileRef string) bool {
	switch strings.ToLower(path.Ext(strings.TrimSpace(fileRef))) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".mov", ".webm", ".wmv", ".ts", ".rmvb", ".rm", ".3gp", ".mpg", ".mpeg":
		return true
	default:
		return false
	}
}

type openListListResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Content []openListListItem `json:"content"`
		Total   int                `json:"total"`
	} `json:"data"`
}

type openListListItem struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"is_dir"`
}

type openListLinkResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		URL    string          `json:"url"`
		Header json.RawMessage `json:"header"`
	} `json:"data"`
}

type openListLoginResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Token string `json:"token"`
	} `json:"data"`
}

func joinOpenListAPIPath(dir, name string) string {
	dir = strings.TrimRight(normalizeCloudDAVPath(dir), "/")
	name = strings.Trim(strings.ReplaceAll(name, "\\", "/"), "/")
	if dir == "" || dir == "/" {
		return normalizeCloudDAVPath(name)
	}
	return normalizeCloudDAVPath(dir + "/" + name)
}
