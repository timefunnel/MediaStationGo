package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	MediaTitleRelationStandalone = "standalone"
	MediaTitleRelationVersion    = "version"
	MediaTitleRelationPart       = "part"
)

var ErrAITitleCleanupUnavailable = errors.New("AI 标题清洗未配置")

type MediaTitleCleanupSource struct {
	MediaID        string `json:"media_id"`
	CurrentTitle   string `json:"current_title"`
	SourceDirectory string `json:"source_directory"`
	DirectoryChain string `json:"directory_chain"`
	Filename       string `json:"filename"`
}

type MediaTitleCleanupGroup struct {
	SourceDirectory string                    `json:"source_directory"`
	Items           []MediaTitleCleanupSource `json:"items"`
}

type MediaTitleCleanupSuggestion struct {
	MediaID         string  `json:"media_id"`
	CurrentTitle    string  `json:"current_title,omitempty"`
	SourceDirectory string  `json:"source_directory,omitempty"`
	Filename        string  `json:"filename,omitempty"`
	Title           string  `json:"title"`
	Relation        string  `json:"relation"`
	GroupKey        string  `json:"group_key,omitempty"`
	Year            int     `json:"year,omitempty"`
	Confidence      float64 `json:"confidence"`
	Reason          string  `json:"reason,omitempty"`
}

type aiTitleCleanupResponse struct {
	Items []MediaTitleCleanupSuggestion `json:"items"`
}

func (a *AIService) CleanMediaTitles(ctx context.Context, groups []MediaTitleCleanupGroup) ([]MediaTitleCleanupSuggestion, error) {
	if len(groups) == 0 {
		return []MediaTitleCleanupSuggestion{}, nil
	}
	runtime := a.resolveRuntimeConfig(ctx)
	if !runtime.Enabled {
		return nil, ErrAITitleCleanupUnavailable
	}

	payload, err := json.Marshal(map[string]any{"groups": groups})
	if err != nil {
		return nil, err
	}
	const system = `你是媒体文件标题清洗器。只能依据输入的目录名、目录层级、当前标题和文件名做规范化，不得虚构作品、演员或外部元数据。
对每个 media_id 返回一条结果，并判断它与同目录其他文件的关系：
- standalone：独立作品，不应与其他文件聚合；
- version：同一完整作品的不同清晰度、编码或发行版本，group_key 相同且 title、year 必须相同；
- part：同一作品的分段、章节、花絮或连续短片，title 必须保留可区分的分段信息，不能当作多版本聚合。
目录名和文件名都可作为标题来源。优先去掉站点域名、广告前后缀、画质编码和无意义序号，但保留能区分作品或分段的文字。
当文件名仅为 mp4_ (2)、IMG_4595、纯数字、随机编号等无语义名称，而 source_directory 或 directory_chain 含有明确作品描述时，必须使用描述性目录名作为基础标题；括号或末尾数字可转为“第 N 段”，relation 使用 part。示例：目录“作品 A”、文件“mp4_ (2).mp4”应输出标题“作品 A 第 2 段”，不能原样保留 mp4_ (2)。
同一目录存在一个描述性主文件和多个无语义编号文件时，应结合目录上下文判断这些文件是否属于同一组分段；如果按分段处理，每条 title 必须带不同的分段标识。无法确定时保守使用 standalone，并降低 confidence。
只输出 JSON，不要代码块或解释。格式：{"items":[{"media_id":"...","title":"...","relation":"standalone|version|part","group_key":"仅 version 必填","year":0,"confidence":0.0,"reason":"简短依据"}]}`
	out, err := a.completeBatch(ctx, runtime, system, string(payload), 0)
	if err != nil {
		return nil, err
	}
	var response aiTitleCleanupResponse
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &response); err != nil {
		return nil, fmt.Errorf("AI 标题清洗返回了非法 JSON: %w", err)
	}
	return validateMediaTitleCleanupSuggestions(groups, response.Items)
}

func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```JSON")
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end >= start {
		return raw[start : end+1]
	}
	return raw
}

func validateMediaTitleCleanupSuggestions(groups []MediaTitleCleanupGroup, suggestions []MediaTitleCleanupSuggestion) ([]MediaTitleCleanupSuggestion, error) {
	expected := make(map[string]MediaTitleCleanupSource)
	order := make(map[string]int)
	for _, group := range groups {
		for _, item := range group.Items {
			if strings.TrimSpace(item.MediaID) == "" {
				return nil, errors.New("AI 标题清洗输入包含空 media_id")
			}
			order[item.MediaID] = len(order)
			expected[item.MediaID] = item
		}
	}
	if len(suggestions) != len(expected) {
		return nil, fmt.Errorf("AI 标题清洗结果数量不完整: got=%d want=%d", len(suggestions), len(expected))
	}

	seen := make(map[string]struct{}, len(suggestions))
	versionGroups := map[string][]MediaTitleCleanupSuggestion{}
	titleGroups := map[string][]MediaTitleCleanupSuggestion{}
	for i := range suggestions {
		item := &suggestions[i]
		source, ok := expected[strings.TrimSpace(item.MediaID)]
		if !ok {
			return nil, fmt.Errorf("AI 标题清洗返回了未知 media_id: %s", item.MediaID)
		}
		if _, exists := seen[item.MediaID]; exists {
			return nil, fmt.Errorf("AI 标题清洗重复返回 media_id: %s", item.MediaID)
		}
		seen[item.MediaID] = struct{}{}
		item.Title = strings.Join(strings.Fields(strings.TrimSpace(item.Title)), " ")
		if item.Title == "" || len([]rune(item.Title)) > 255 {
			return nil, fmt.Errorf("AI 标题清洗返回了无效标题: %s", item.MediaID)
		}
		item.Relation = strings.ToLower(strings.TrimSpace(item.Relation))
		switch item.Relation {
		case MediaTitleRelationStandalone, MediaTitleRelationPart:
			item.GroupKey = ""
		case MediaTitleRelationVersion:
			item.GroupKey = strings.ToLower(strings.TrimSpace(item.GroupKey))
			if item.GroupKey == "" {
				return nil, fmt.Errorf("AI 标题清洗的 version 缺少 group_key: %s", item.MediaID)
			}
			versionGroups[item.GroupKey] = append(versionGroups[item.GroupKey], *item)
		default:
			return nil, fmt.Errorf("AI 标题清洗返回了未知关系: %s", item.Relation)
		}
		if item.Year < 0 || item.Year > 2100 {
			return nil, fmt.Errorf("AI 标题清洗返回了无效年份: %s", item.MediaID)
		}
		if item.Confidence < 0 || item.Confidence > 1 {
			return nil, fmt.Errorf("AI 标题清洗返回了无效置信度: %s", item.MediaID)
		}
		item.Reason = strings.TrimSpace(item.Reason)
		item.CurrentTitle = source.CurrentTitle
		item.SourceDirectory = source.SourceDirectory
		item.Filename = source.Filename
		titleKey := normalizeMediaVersionText(item.Title) + fmt.Sprintf("|%d", item.Year)
		titleGroups[titleKey] = append(titleGroups[titleKey], *item)
	}

	for key, items := range versionGroups {
		if len(items) < 2 {
			return nil, fmt.Errorf("AI 标题清洗的 version 分组只有一个文件: %s", key)
		}
		for _, item := range items[1:] {
			if item.Title != items[0].Title || item.Year != items[0].Year {
				return nil, fmt.Errorf("AI 标题清洗的 version 分组标题或年份不一致: %s", key)
			}
		}
	}
	for key, items := range titleGroups {
		if len(items) < 2 {
			continue
		}
		groupKey := items[0].GroupKey
		for _, item := range items {
			if item.Relation != MediaTitleRelationVersion || item.GroupKey == "" || item.GroupKey != groupKey {
				return nil, fmt.Errorf("AI 标题清洗会把非版本文件聚合为同一标题: %s", key)
			}
		}
	}
	sort.SliceStable(suggestions, func(i, j int) bool {
		return order[suggestions[i].MediaID] < order[suggestions[j].MediaID]
	})
	return suggestions, nil
}
