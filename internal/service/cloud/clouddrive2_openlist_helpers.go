package cloud

import (
	"path"
	"strings"
)

func isCloudVideoPlaybackCandidate(fileRef string) bool {
	switch strings.ToLower(path.Ext(strings.TrimSpace(fileRef))) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".mov", ".webm", ".ts", ".rmvb", ".rm", ".3gp", ".mpg", ".mpeg":
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
