package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

const (
	MediaTitleRelationStandalone = "standalone"
	MediaTitleRelationVersion    = "version"
	MediaTitleRelationPart       = "part"

	mediaTitleCleanupStageCleaning = "cleaning"
	mediaTitleCleanupStageGrouping = "grouping"
)

var ErrAITitleCleanupUnavailable = errors.New("AI 标题清洗未配置")

type MediaTitleCleanupSource struct {
	MediaID         string `json:"media_id"`
	CurrentTitle    string `json:"current_title"`
	SourceDirectory string `json:"source_directory"`
	DirectoryChain  string `json:"directory_chain"`
	Filename        string `json:"filename"`
}

type MediaTitleCleanupExistingItem struct {
	MediaID   string `json:"media_id"`
	Title     string `json:"title"`
	Year      int    `json:"year,omitempty"`
	Filename  string `json:"filename,omitempty"`
	PartIndex int    `json:"part_index,omitempty"`
}

type MediaTitleCleanupExistingGroup struct {
	Relation   string                          `json:"relation"`
	GroupKey   string                          `json:"group_key"`
	GroupTitle string                          `json:"group_title,omitempty"`
	Items      []MediaTitleCleanupExistingItem `json:"items"`
}

type MediaTitleCleanupGroup struct {
	SourceDirectory string                           `json:"source_directory"`
	Items           []MediaTitleCleanupSource        `json:"items"`
	ExistingGroups  []MediaTitleCleanupExistingGroup `json:"existing_groups,omitempty"`
	DirectoryKey    string                           `json:"-"`
}

type MediaTitleCleanupSuggestion struct {
	MediaID          string  `json:"media_id"`
	CurrentTitle     string  `json:"current_title,omitempty"`
	SourceDirectory  string  `json:"source_directory,omitempty"`
	Filename         string  `json:"filename,omitempty"`
	Title            string  `json:"title"`
	Relation         string  `json:"relation"`
	GroupKey         string  `json:"group_key,omitempty"`
	ExistingGroupKey string  `json:"existing_group_key,omitempty"`
	GroupTitle       string  `json:"group_title,omitempty"`
	PartIndex        int     `json:"part_index,omitempty"`
	Year             int     `json:"year,omitempty"`
	Confidence       float64 `json:"confidence"`
	Reason           string  `json:"reason,omitempty"`
}

type mediaTitleNormalization struct {
	MediaID    string
	Title      string
	Year       int
	Confidence float64
	Reason     string
}

type mediaTitleRelationDecision struct {
	MediaID    string
	Relation   string
	GroupKey   string
	GroupTitle string
	PartIndex  int
	Confidence float64
	Reason     string
}

type aiTitleNormalizationItem struct {
	MediaID    string   `json:"media_id"`
	Title      string   `json:"title"`
	Year       *int     `json:"year"`
	Confidence *float64 `json:"confidence"`
	Reason     string   `json:"reason"`
}

type aiTitleNormalizationResponse struct {
	Items *[]aiTitleNormalizationItem `json:"items"`
}

type aiTitleRelationItem struct {
	MediaID    string   `json:"media_id"`
	Relation   string   `json:"relation"`
	GroupKey   *string  `json:"group_key,omitempty"`
	GroupTitle *string  `json:"group_title,omitempty"`
	PartIndex  *int     `json:"part_index,omitempty"`
	Confidence *float64 `json:"confidence"`
	Reason     string   `json:"reason"`
}

type aiTitleRelationResponse struct {
	Items *[]aiTitleRelationItem `json:"items"`
}

type mediaTitleGroupingCandidate struct {
	MediaID           string `json:"media_id"`
	StandardizedTitle string `json:"standardized_title"`
	Year              int    `json:"year"`
	SourceDirectory   string `json:"source_directory"`
	DirectoryChain    string `json:"directory_chain"`
	Filename          string `json:"filename"`
}

func (a *AIService) CleanMediaTitles(ctx context.Context, groups []MediaTitleCleanupGroup) ([]MediaTitleCleanupSuggestion, error) {
	return a.CleanMediaTitlesWithProgress(ctx, groups, nil)
}

// CleanMediaTitlesWithProgress first normalizes every title, waits for the
// whole stage to pass validation, and only then classifies relationships.
func (a *AIService) CleanMediaTitlesWithProgress(
	ctx context.Context,
	groups []MediaTitleCleanupGroup,
	onProgress func(stage string, completed, total int),
) ([]MediaTitleCleanupSuggestion, error) {
	if len(groups) == 0 {
		return []MediaTitleCleanupSuggestion{}, nil
	}
	runtime := a.resolveRuntimeConfig(ctx)
	if !runtime.Enabled {
		return nil, ErrAITitleCleanupUnavailable
	}

	normalizedBatches, err := runOrderedTitleCleanupStage(ctx, len(groups), func(stageCtx context.Context, index int) ([]mediaTitleNormalization, error) {
		return a.normalizeMediaTitleBatch(stageCtx, runtime, groups[index])
	}, func(completed, total int) {
		if onProgress != nil {
			onProgress(mediaTitleCleanupStageCleaning, completed, total)
		}
	})
	if err != nil {
		return nil, err
	}

	normalizedByID := make(map[string]mediaTitleNormalization)
	for _, batch := range normalizedBatches {
		for _, item := range batch {
			if _, exists := normalizedByID[item.MediaID]; exists {
				return nil, fmt.Errorf("AI 标题清洗重复返回 media_id: %s", item.MediaID)
			}
			normalizedByID[item.MediaID] = item
		}
	}
	if len(normalizedByID) != mediaTitleCleanupItemCount(groups) {
		return nil, fmt.Errorf("AI 标题清洗结果数量不完整: got=%d want=%d", len(normalizedByID), mediaTitleCleanupItemCount(groups))
	}
	if onProgress != nil {
		onProgress(mediaTitleCleanupStageGrouping, 0, len(groups))
	}

	relationBatches, err := runOrderedTitleCleanupStage(ctx, len(groups), func(stageCtx context.Context, index int) ([]mediaTitleRelationDecision, error) {
		return a.classifyMediaTitleBatch(stageCtx, runtime, groups[index], normalizedByID)
	}, func(completed, total int) {
		if onProgress != nil {
			onProgress(mediaTitleCleanupStageGrouping, completed, total)
		}
	})
	if err != nil {
		return nil, err
	}

	out := make([]MediaTitleCleanupSuggestion, 0, len(normalizedByID))
	for groupIndex, decisions := range relationBatches {
		group := groups[groupIndex]
		sources := make(map[string]MediaTitleCleanupSource, len(group.Items))
		existingKeys := make(map[string]string, len(group.ExistingGroups))
		for _, source := range group.Items {
			sources[source.MediaID] = source
		}
		for _, existing := range group.ExistingGroups {
			existingKeys[existing.Relation+"\x00"+strings.ToLower(strings.TrimSpace(existing.GroupKey))] = existing.GroupKey
		}
		for _, decision := range decisions {
			normalized, ok := normalizedByID[decision.MediaID]
			if !ok {
				return nil, fmt.Errorf("AI 关系聚合返回了未知 media_id: %s", decision.MediaID)
			}
			source := sources[decision.MediaID]
			groupKey := strings.ToLower(strings.TrimSpace(decision.GroupKey))
			existingGroupKey := ""
			if groupKey != "" {
				if persisted, exists := existingKeys[decision.Relation+"\x00"+groupKey]; exists {
					groupKey = persisted
					existingGroupKey = persisted
				} else {
					groupKey = fmt.Sprintf("g%d:%s", groupIndex, groupKey)
				}
			}
			out = append(out, MediaTitleCleanupSuggestion{
				MediaID:          decision.MediaID,
				CurrentTitle:     source.CurrentTitle,
				SourceDirectory:  source.SourceDirectory,
				Filename:         source.Filename,
				Title:            normalized.Title,
				Relation:         decision.Relation,
				GroupKey:         groupKey,
				ExistingGroupKey: existingGroupKey,
				GroupTitle:       decision.GroupTitle,
				PartIndex:        decision.PartIndex,
				Year:             normalized.Year,
				Confidence:       minFloat64(normalized.Confidence, decision.Confidence),
				Reason:           joinMediaTitleCleanupReasons(normalized.Reason, decision.Reason),
			})
		}
	}
	return validateMediaTitleCleanupSuggestions(groups, out)
}

func runOrderedTitleCleanupStage[T any](
	ctx context.Context,
	count int,
	worker func(context.Context, int) ([]T, error),
	onProgress func(completed, total int),
) ([][]T, error) {
	const concurrency = 3
	results := make([][]T, count)
	if count == 0 {
		return results, nil
	}
	stageCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	completed := 0
	workerCount := minInt(concurrency, count)
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range work {
				items, err := worker(stageCtx, index)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
					cancel()
				}
				if err == nil {
					results[index] = items
					completed++
					if onProgress != nil {
						onProgress(completed, count)
					}
				}
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}()
	}
sendLoop:
	for index := 0; index < count; index++ {
		select {
		case work <- index:
		case <-stageCtx.Done():
			break sendLoop
		}
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (a *AIService) normalizeMediaTitleBatch(
	ctx context.Context,
	runtime aiRuntimeConfig,
	group MediaTitleCleanupGroup,
) ([]mediaTitleNormalization, error) {
	payload, err := json.Marshal(map[string]any{
		"source_directory": group.SourceDirectory,
		"items":            group.Items,
	})
	if err != nil {
		return nil, err
	}
	const system = `你是媒体文件标题标准化器。只做标题和年份清洗，不判断文件之间的关系。
只能依据输入的目录名、目录层级、当前标题和文件名，不得虚构作品、演员或外部元数据。
优先去掉站点域名、广告前后缀、画质、编码、发布组和无意义噪声，但保留作品名以及能区分分段的文字。
文件名无语义而目录名有明确描述时，以描述性目录名为标题基础；括号或末尾数字可规范为“第 N 段”。
每个 media_id 必须返回且只能返回一次。year 不确定时返回 0。只输出纯 JSON。
格式：{"items":[{"media_id":"...","title":"...","year":0,"confidence":0.0,"reason":"简短依据"}]}`
	raw, err := a.completeBatch(ctx, runtime, system, string(payload), 0)
	if err != nil {
		return nil, err
	}
	var response aiTitleNormalizationResponse
	if err := decodeStrictAIJSON(raw, &response); err != nil {
		return nil, fmt.Errorf("AI 标题清洗返回了非法 JSON: %w", err)
	}
	if response.Items == nil {
		return nil, errors.New("AI 标题清洗缺少 items")
	}
	expected := make(map[string]MediaTitleCleanupSource, len(group.Items))
	for _, source := range group.Items {
		expected[source.MediaID] = source
	}
	if len(*response.Items) != len(expected) {
		return nil, fmt.Errorf("AI 标题清洗结果数量不完整: got=%d want=%d", len(*response.Items), len(expected))
	}
	seen := make(map[string]struct{}, len(expected))
	out := make([]mediaTitleNormalization, 0, len(expected))
	for _, wire := range *response.Items {
		id := strings.TrimSpace(wire.MediaID)
		if _, ok := expected[id]; !ok {
			return nil, fmt.Errorf("AI 标题清洗返回了未知 media_id: %s", id)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("AI 标题清洗重复返回 media_id: %s", id)
		}
		seen[id] = struct{}{}
		title := strings.Join(strings.Fields(strings.TrimSpace(wire.Title)), " ")
		if title == "" || len([]rune(title)) > 255 {
			return nil, fmt.Errorf("AI 标题清洗返回了无效标题: %s", id)
		}
		if wire.Year == nil || *wire.Year < 0 || *wire.Year > 2100 {
			return nil, fmt.Errorf("AI 标题清洗返回了无效年份: %s", id)
		}
		if wire.Confidence == nil || *wire.Confidence < 0 || *wire.Confidence > 1 {
			return nil, fmt.Errorf("AI 标题清洗返回了无效置信度: %s", id)
		}
		out = append(out, mediaTitleNormalization{
			MediaID: id, Title: title, Year: *wire.Year,
			Confidence: *wire.Confidence, Reason: strings.TrimSpace(wire.Reason),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return mediaTitleCleanupSourceOrder(group.Items, out[i].MediaID) < mediaTitleCleanupSourceOrder(group.Items, out[j].MediaID)
	})
	return out, nil
}

func (a *AIService) classifyMediaTitleBatch(
	ctx context.Context,
	runtime aiRuntimeConfig,
	group MediaTitleCleanupGroup,
	normalizedByID map[string]mediaTitleNormalization,
) ([]mediaTitleRelationDecision, error) {
	candidates := make([]mediaTitleGroupingCandidate, 0, len(group.Items))
	for _, source := range group.Items {
		normalized, ok := normalizedByID[source.MediaID]
		if !ok {
			return nil, fmt.Errorf("关系聚合缺少已清洗标题: %s", source.MediaID)
		}
		candidates = append(candidates, mediaTitleGroupingCandidate{
			MediaID: source.MediaID, StandardizedTitle: normalized.Title, Year: normalized.Year,
			SourceDirectory: source.SourceDirectory, DirectoryChain: source.DirectoryChain, Filename: source.Filename,
		})
	}
	payload, err := json.Marshal(map[string]any{
		"source_directory": group.SourceDirectory,
		"candidates":       candidates,
		"existing_groups":  group.ExistingGroups,
	})
	if err != nil {
		return nil, err
	}
	const system = `你是媒体关系聚合器。输入中的 candidates 已完成标题标准化，你不得修改或重新输出标题、年份。
只判断每个 candidate 与同目录候选或 existing_groups 的关系：
- standalone：独立作品；
- version：同一完整作品的不同清晰度、编码或发行版本，组内标准标题和年份必须相同；
- part：同一作品的连续分段、章节或花絮，group_title 为共同作品名，part_index 从 1 开始且组内唯一。
existing_groups 是只读的历史聚合结果。候选属于历史组时，必须原样复用该组 group_key；不得为同一作品创建第二个组。
新组只有至少两个 candidate 明确属于同一作品时才能建立。无法确定时使用 standalone 并降低 confidence。
每个 candidate media_id 必须返回且只能返回一次。只输出纯 JSON，不得输出 title 或 year 字段。
格式：{"items":[{"media_id":"...","relation":"standalone|version|part","group_key":"仅 version/part 填写","group_title":"仅 part 填写","part_index":1,"confidence":0.0,"reason":"简短依据"}]}`
	raw, err := a.completeBatch(ctx, runtime, system, string(payload), 0)
	if err != nil {
		return nil, err
	}
	var response aiTitleRelationResponse
	if err := decodeStrictAIJSON(raw, &response); err != nil {
		return nil, fmt.Errorf("AI 关系聚合返回了非法 JSON: %w", err)
	}
	if response.Items == nil {
		return nil, errors.New("AI 关系聚合缺少 items")
	}
	expected := make(map[string]struct{}, len(group.Items))
	for _, source := range group.Items {
		expected[source.MediaID] = struct{}{}
	}
	if len(*response.Items) != len(expected) {
		return nil, fmt.Errorf("AI 关系聚合结果数量不完整: got=%d want=%d", len(*response.Items), len(expected))
	}
	seen := make(map[string]struct{}, len(expected))
	out := make([]mediaTitleRelationDecision, 0, len(expected))
	for _, wire := range *response.Items {
		id := strings.TrimSpace(wire.MediaID)
		if _, ok := expected[id]; !ok {
			return nil, fmt.Errorf("AI 关系聚合返回了未知 media_id: %s", id)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("AI 关系聚合重复返回 media_id: %s", id)
		}
		seen[id] = struct{}{}
		if wire.Confidence == nil || *wire.Confidence < 0 || *wire.Confidence > 1 {
			return nil, fmt.Errorf("AI 关系聚合返回了无效置信度: %s", id)
		}
		item := mediaTitleRelationDecision{
			MediaID: id, Relation: strings.ToLower(strings.TrimSpace(wire.Relation)),
			Confidence: *wire.Confidence, Reason: strings.TrimSpace(wire.Reason),
		}
		if wire.GroupKey != nil {
			item.GroupKey = strings.ToLower(strings.TrimSpace(*wire.GroupKey))
		}
		if wire.GroupTitle != nil {
			item.GroupTitle = strings.Join(strings.Fields(strings.TrimSpace(*wire.GroupTitle)), " ")
		}
		if wire.PartIndex != nil {
			item.PartIndex = *wire.PartIndex
		}
		switch item.Relation {
		case MediaTitleRelationStandalone:
			if item.GroupKey != "" || item.GroupTitle != "" || item.PartIndex != 0 {
				return nil, fmt.Errorf("AI 关系聚合的 standalone 携带了分组字段: %s", id)
			}
		case MediaTitleRelationVersion:
			if item.GroupKey == "" || item.GroupTitle != "" || item.PartIndex != 0 {
				return nil, fmt.Errorf("AI 关系聚合的 version 字段无效: %s", id)
			}
		case MediaTitleRelationPart:
			if item.GroupKey == "" || item.GroupTitle == "" || item.PartIndex <= 0 {
				return nil, fmt.Errorf("AI 关系聚合的 part 缺少有效分组或序号: %s", id)
			}
		default:
			return nil, fmt.Errorf("AI 关系聚合返回了未知关系: %s", item.Relation)
		}
		out = append(out, item)
	}
	return out, nil
}

func decodeStrictAIJSON(raw string, target any) error {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		firstLineEnd := strings.IndexByte(raw, '\n')
		if firstLineEnd < 0 || !strings.HasSuffix(raw, "```") {
			return errors.New("代码块格式不完整")
		}
		fence := strings.TrimSpace(raw[:firstLineEnd])
		if fence != "```" && !strings.EqualFold(fence, "```json") {
			return fmt.Errorf("不支持的代码块类型: %s", fence)
		}
		raw = strings.TrimSpace(raw[firstLineEnd+1 : len(raw)-3])
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON 后存在额外内容")
		}
		return err
	}
	return nil
}

func validateMediaTitleCleanupSuggestions(groups []MediaTitleCleanupGroup, suggestions []MediaTitleCleanupSuggestion) ([]MediaTitleCleanupSuggestion, error) {
	expected := make(map[string]MediaTitleCleanupSource)
	order := make(map[string]int)
	existing := make(map[string]MediaTitleCleanupExistingGroup)
	for _, group := range groups {
		for _, item := range group.Items {
			if strings.TrimSpace(item.MediaID) == "" {
				return nil, errors.New("AI 标题清洗输入包含空 media_id")
			}
			if _, duplicate := expected[item.MediaID]; duplicate {
				return nil, fmt.Errorf("AI 标题清洗输入重复 media_id: %s", item.MediaID)
			}
			order[item.MediaID] = len(order)
			expected[item.MediaID] = item
		}
		for _, contextGroup := range group.ExistingGroups {
			key := contextGroup.Relation + "\x00" + strings.ToLower(strings.TrimSpace(contextGroup.GroupKey))
			existing[key] = contextGroup
		}
	}
	if len(suggestions) != len(expected) {
		return nil, fmt.Errorf("AI 标题清洗结果数量不完整: got=%d want=%d", len(suggestions), len(expected))
	}

	seen := make(map[string]struct{}, len(suggestions))
	versionGroups := map[string][]MediaTitleCleanupSuggestion{}
	partGroups := map[string][]MediaTitleCleanupSuggestion{}
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
		item.GroupKey = strings.ToLower(strings.TrimSpace(item.GroupKey))
		item.ExistingGroupKey = strings.ToLower(strings.TrimSpace(item.ExistingGroupKey))
		switch item.Relation {
		case MediaTitleRelationStandalone:
			if item.GroupKey != "" || item.ExistingGroupKey != "" || item.GroupTitle != "" || item.PartIndex != 0 {
				return nil, fmt.Errorf("AI 标题清洗的 standalone 携带了分组字段: %s", item.MediaID)
			}
		case MediaTitleRelationVersion:
			if item.GroupKey == "" || item.GroupTitle != "" || item.PartIndex != 0 {
				return nil, fmt.Errorf("AI 标题清洗的 version 字段无效: %s", item.MediaID)
			}
			versionGroups[item.GroupKey] = append(versionGroups[item.GroupKey], *item)
		case MediaTitleRelationPart:
			item.GroupTitle = strings.Join(strings.Fields(strings.TrimSpace(item.GroupTitle)), " ")
			if item.GroupKey == "" || item.GroupTitle == "" || item.PartIndex <= 0 {
				return nil, fmt.Errorf("AI 标题清洗的 part 缺少有效分组或序号: %s", item.MediaID)
			}
			partGroups[item.GroupKey] = append(partGroups[item.GroupKey], *item)
		default:
			return nil, fmt.Errorf("AI 标题清洗返回了未知关系: %s", item.Relation)
		}
		if item.ExistingGroupKey != "" {
			if item.ExistingGroupKey != item.GroupKey {
				return nil, fmt.Errorf("AI 标题清洗的历史组键不一致: %s", item.MediaID)
			}
			if _, ok := existing[item.Relation+"\x00"+item.ExistingGroupKey]; !ok {
				return nil, fmt.Errorf("AI 标题清洗引用了未知历史组: %s", item.MediaID)
			}
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
	}

	for key, items := range versionGroups {
		contextGroup, hasExisting := existing[MediaTitleRelationVersion+"\x00"+key]
		if len(items) < 2 && !hasExisting {
			return nil, fmt.Errorf("AI 标题清洗的 version 分组只有一个文件: %s", key)
		}
		title, year := items[0].Title, items[0].Year
		if hasExisting && len(contextGroup.Items) > 0 {
			title, year = contextGroup.Items[0].Title, contextGroup.Items[0].Year
		}
		for _, item := range items {
			if item.Title != title || item.Year != year {
				return nil, fmt.Errorf("AI 标题清洗的 version 分组标题或年份不一致: %s", key)
			}
		}
	}
	for key, items := range partGroups {
		contextGroup, hasExisting := existing[MediaTitleRelationPart+"\x00"+key]
		total := len(items)
		indices := make(map[int]struct{}, len(items))
		groupTitle := items[0].GroupTitle
		year := items[0].Year
		if hasExisting {
			total += len(contextGroup.Items)
			groupTitle = contextGroup.GroupTitle
			if len(contextGroup.Items) > 0 {
				year = contextGroup.Items[0].Year
			}
			for _, contextItem := range contextGroup.Items {
				if contextItem.PartIndex > 0 {
					indices[contextItem.PartIndex] = struct{}{}
				}
			}
		}
		if total < 2 {
			return nil, fmt.Errorf("AI 标题清洗的 part 分组只有一个文件: %s", key)
		}
		for _, item := range items {
			if item.GroupTitle != groupTitle || item.Year != year {
				return nil, fmt.Errorf("AI 标题清洗的 part 分组标题或年份不一致: %s", key)
			}
			if _, exists := indices[item.PartIndex]; exists {
				return nil, fmt.Errorf("AI 标题清洗的 part 分组序号重复: %s", key)
			}
			indices[item.PartIndex] = struct{}{}
		}
		for index := 1; index <= total; index++ {
			if _, ok := indices[index]; !ok {
				return nil, fmt.Errorf("AI 标题清洗的 part 分组序号不连续: %s", key)
			}
		}
	}
	sort.SliceStable(suggestions, func(i, j int) bool {
		return order[suggestions[i].MediaID] < order[suggestions[j].MediaID]
	})
	return suggestions, nil
}

func mediaTitleCleanupItemCount(groups []MediaTitleCleanupGroup) int {
	total := 0
	for _, group := range groups {
		total += len(group.Items)
	}
	return total
}

func mediaTitleCleanupSourceOrder(items []MediaTitleCleanupSource, mediaID string) int {
	for index, item := range items {
		if item.MediaID == mediaID {
			return index
		}
	}
	return len(items)
}

func minFloat64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func joinMediaTitleCleanupReasons(titleReason, relationReason string) string {
	titleReason = strings.TrimSpace(titleReason)
	relationReason = strings.TrimSpace(relationReason)
	switch {
	case titleReason == "":
		return relationReason
	case relationReason == "":
		return titleReason
	default:
		return "标题：" + titleReason + "；关系：" + relationReason
	}
}
