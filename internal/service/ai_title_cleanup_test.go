package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
)

func TestCleanMediaTitlesRunsNormalizationBeforeRelationshipClassification(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		system, user := decodeAIRequestMessages(t, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(system, "标题标准化器"):
			calls.Add(1)
			writeAIContent(t, w, `{"items":[{"media_id":"a","title":"作品 A","year":2025,"confidence":0.96,"reason":"去除画质"},{"media_id":"b","title":"作品 A","year":2025,"confidence":0.91,"reason":"去除编码"}]}`)
		case strings.Contains(system, "关系聚合器"):
			if calls.Load() != 1 {
				t.Fatalf("relationship stage started before normalization barrier")
			}
			if !strings.Contains(user, `"standardized_title":"作品 A"`) {
				t.Fatalf("relationship input does not use normalized titles: %s", user)
			}
			calls.Add(1)
			writeAIContent(t, w, `{"items":[{"media_id":"a","relation":"version","group_key":"work-a","confidence":0.94,"reason":"同作品不同画质"},{"media_id":"b","relation":"version","group_key":"work-a","confidence":0.93,"reason":"同作品不同画质"}]}`)
		default:
			t.Fatalf("unexpected system prompt: %s", system)
		}
	}))
	defer server.Close()

	ai := newTestTitleCleanupAI(server.URL)
	groups := []MediaTitleCleanupGroup{{
		SourceDirectory: "作品 A",
		Items: []MediaTitleCleanupSource{
			{MediaID: "a", CurrentTitle: "A.1080p", SourceDirectory: "作品 A", Filename: "A.1080p.mkv"},
			{MediaID: "b", CurrentTitle: "A.2160p", SourceDirectory: "作品 A", Filename: "A.2160p.mkv"},
		},
	}}
	items, err := ai.CleanMediaTitles(t.Context(), groups)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(items) != 2 {
		t.Fatalf("calls=%d items=%#v", calls.Load(), items)
	}
	if items[0].Title != "作品 A" || items[0].Relation != MediaTitleRelationVersion || items[0].Confidence != 0.94 {
		t.Fatalf("unexpected suggestion: %#v", items[0])
	}
}

func TestCleanMediaTitlesCanJoinExistingPartGroup(t *testing.T) {
	const existingKey = "c01d2e3f4a5b6c7d8e9f001122334455"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		system, user := decodeAIRequestMessages(t, r)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(system, "标题标准化器") {
			writeAIContent(t, w, `{"items":[{"media_id":"part-3","title":"作品 A 第 3 段","year":0,"confidence":0.92,"reason":"目录与序号"}]}`)
			return
		}
		if !strings.Contains(user, existingKey) {
			t.Fatalf("existing group is missing from relationship input: %s", user)
		}
		writeAIContent(t, w, `{"items":[{"media_id":"part-3","relation":"part","group_key":"`+existingKey+`","group_title":"作品 A","part_index":3,"confidence":0.9,"reason":"续入现有分段"}]}`)
	}))
	defer server.Close()

	ai := newTestTitleCleanupAI(server.URL)
	groups := []MediaTitleCleanupGroup{{
		SourceDirectory: "作品 A",
		Items:           []MediaTitleCleanupSource{{MediaID: "part-3", CurrentTitle: "003", SourceDirectory: "作品 A", Filename: "003.mp4"}},
		ExistingGroups: []MediaTitleCleanupExistingGroup{{
			Relation: MediaTitleRelationPart, GroupKey: existingKey, GroupTitle: "作品 A",
			Items: []MediaTitleCleanupExistingItem{
				{MediaID: "part-1", Title: "作品 A 第 1 段", PartIndex: 1},
				{MediaID: "part-2", Title: "作品 A 第 2 段", PartIndex: 2},
			},
		}},
	}}
	items, err := ai.CleanMediaTitles(t.Context(), groups)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ExistingGroupKey != existingKey || items[0].PartIndex != 3 {
		t.Fatalf("existing group was not reused: %#v", items)
	}
}

func TestCleanMediaTitlesRetriesInvalidPartIndexesWithValidationFeedback(t *testing.T) {
	var relationCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		system, user := decodeAIRequestMessages(t, r)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(system, "标题标准化器") {
			writeAIContent(t, w, `{"items":[{"media_id":"a","title":"作品 A 第 1 段","year":0,"confidence":0.9,"reason":"文件名"},{"media_id":"b","title":"作品 A 第 2 段","year":0,"confidence":0.9,"reason":"文件名"}]}`)
			return
		}
		call := relationCalls.Add(1)
		if call == 1 {
			writeAIContent(t, w, `{"items":[{"media_id":"a","relation":"part","group_key":"work-a","group_title":"作品 A","part_index":1,"confidence":0.9,"reason":"分段"},{"media_id":"b","relation":"part","group_key":"work-a","group_title":"作品 A","part_index":3,"confidence":0.9,"reason":"分段"}]}`)
			return
		}
		if !strings.Contains(system, "上一轮输出未通过服务端校验") ||
			!strings.Contains(user, "part 分组序号不连续") ||
			!strings.Contains(user, `"previous_output"`) {
			t.Fatalf("correction request lacks validation context: system=%s user=%s", system, user)
		}
		writeAIContent(t, w, `{"items":[{"media_id":"a","relation":"part","group_key":"work-a","group_title":"作品 A","part_index":1,"confidence":0.9,"reason":"分段"},{"media_id":"b","relation":"part","group_key":"work-a","group_title":"作品 A","part_index":2,"confidence":0.9,"reason":"修正连续序号"}]}`)
	}))
	defer server.Close()

	items, err := newTestTitleCleanupAI(server.URL).CleanMediaTitles(t.Context(), []MediaTitleCleanupGroup{{
		SourceDirectory: "作品 A",
		Items: []MediaTitleCleanupSource{
			{MediaID: "a", Filename: "01.mp4"},
			{MediaID: "b", Filename: "02.mp4"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if relationCalls.Load() != 2 || len(items) != 2 || items[1].PartIndex != 2 {
		t.Fatalf("relation_calls=%d items=%#v", relationCalls.Load(), items)
	}
}

func TestCleanMediaTitlePromptsPrioritizeChineseDescriptionsOverNumericSuffixes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		system, _ := decodeAIRequestMessages(t, r)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(system, "标题标准化器") {
			for _, rule := range []string{
				"标题优先提取有语义的中文描述",
				"数字只是弱证据",
				"孤立数字 + 空格 + 中文描述",
				"“3月”、“12月新作”、“第3季”",
				"不得输出“第111段”",
				"不得用父目录影片标题覆盖它",
			} {
				if !strings.Contains(system, rule) {
					t.Fatalf("normalization prompt missing rule %q", rule)
				}
			}
			writeAIContent(t, w, `{"items":[{"media_id":"main","title":"玩偶姐姐 穿着可爱的黑色JK超诱惑黑丝","year":0,"confidence":0.9,"reason":"中文描述优先"},{"media_id":"ad","title":"私房猛药 提高硬度 延时不射","year":0,"confidence":0.8,"reason":"保留文件独立语义"}]}`)
			return
		}
		for _, rule := range []string{
			"按 candidates 的稳定顺序连续编为 1..N",
			"不得因副本编号跳号而拆成 standalone",
			"必须使用 standalone",
		} {
			if !strings.Contains(system, rule) {
				t.Fatalf("relationship prompt missing rule %q", rule)
			}
		}
		writeAIContent(t, w, `{"items":[{"media_id":"main","relation":"standalone","confidence":0.9,"reason":"主视频"},{"media_id":"ad","relation":"standalone","confidence":0.9,"reason":"独立广告"}]}`)
	}))
	defer server.Close()

	items, err := newTestTitleCleanupAI(server.URL).CleanMediaTitles(t.Context(), []MediaTitleCleanupGroup{{
		SourceDirectory: "玩偶姐姐hongkongdoll-111最新会员私信短片 穿着可爱的黑色JK超诱惑黑丝",
		Items: []MediaTitleCleanupSource{
			{MediaID: "main", Filename: "玩偶姐姐hongkongdoll-111.mp4"},
			{MediaID: "ad", Filename: "私房猛药，提高硬度，延时不射.mp4"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || strings.Contains(items[0].Title, "111") || items[1].Relation != MediaTitleRelationStandalone {
		t.Fatalf("unexpected suggestions: %#v", items)
	}
}

func TestCleanMediaTitlesStopsAfterBoundedValidationRetry(t *testing.T) {
	var relationCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		system, _ := decodeAIRequestMessages(t, r)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(system, "标题标准化器") {
			writeAIContent(t, w, `{"items":[{"media_id":"a","title":"作品 A 第 1 段","year":0,"confidence":0.9,"reason":"文件名"},{"media_id":"b","title":"作品 A 第 2 段","year":0,"confidence":0.9,"reason":"文件名"}]}`)
			return
		}
		relationCalls.Add(1)
		writeAIContent(t, w, `{"items":[{"media_id":"a","relation":"part","group_key":"work-a","group_title":"作品 A","part_index":1,"confidence":0.9,"reason":"分段"},{"media_id":"b","relation":"part","group_key":"work-a","group_title":"作品 A","part_index":3,"confidence":0.9,"reason":"分段"}]}`)
	}))
	defer server.Close()

	_, err := newTestTitleCleanupAI(server.URL).CleanMediaTitles(t.Context(), []MediaTitleCleanupGroup{{
		Items: []MediaTitleCleanupSource{{MediaID: "a", Filename: "01.mp4"}, {MediaID: "b", Filename: "02.mp4"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "纠错后仍未通过校验") || !strings.Contains(err.Error(), "序号不连续") {
		t.Fatalf("expected bounded validation error, got %v", err)
	}
	if relationCalls.Load() != mediaTitleCleanupMaxAttempts {
		t.Fatalf("relation calls=%d want=%d", relationCalls.Load(), mediaTitleCleanupMaxAttempts)
	}
}

func TestCleanMediaTitlesKeepsSameTitleStandaloneItemsSeparate(t *testing.T) {
	groups := []MediaTitleCleanupGroup{{Items: []MediaTitleCleanupSource{{MediaID: "a"}, {MediaID: "b"}}}}
	items, err := validateMediaTitleCleanupSuggestions(groups, []MediaTitleCleanupSuggestion{
		{MediaID: "a", Title: "同名", Relation: MediaTitleRelationStandalone, Confidence: 0.7},
		{MediaID: "b", Title: "同名", Relation: MediaTitleRelationStandalone, Confidence: 0.7},
	})
	if err != nil || len(items) != 2 {
		t.Fatalf("standalone items must remain separate: items=%#v err=%v", items, err)
	}
}

func TestCleanMediaTitlesRunsEachStageWithBoundedConcurrency(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	var relationshipCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		system, user := decodeAIRequestMessages(t, r)
		current := active.Add(1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		active.Add(-1)
		id := cleanupRequestMediaID(t, user)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(system, "标题标准化器") {
			writeAIContent(t, w, `{"items":[{"media_id":"`+id+`","title":"作品 `+id+`","year":0,"confidence":0.9,"reason":"清洗"}]}`)
			return
		}
		relationshipCalls.Add(1)
		writeAIContent(t, w, `{"items":[{"media_id":"`+id+`","relation":"standalone","confidence":0.9,"reason":"独立"}]}`)
	}))
	defer server.Close()

	ai := newTestTitleCleanupAI(server.URL)
	groups := make([]MediaTitleCleanupGroup, 3)
	for i, id := range []string{"a", "b", "c"} {
		groups[i] = MediaTitleCleanupGroup{SourceDirectory: id, Items: []MediaTitleCleanupSource{{MediaID: id, Filename: id + ".mp4"}}}
	}
	var mu sync.Mutex
	progress := map[string]int{}
	items, err := ai.CleanMediaTitlesWithProgress(t.Context(), groups, func(stage string, done, total int) {
		mu.Lock()
		defer mu.Unlock()
		if done == 0 {
			progress[stage] = 0
			return
		}
		if total != 3 || done <= progress[stage] {
			t.Fatalf("progress stage=%s value=%d/%d previous=%d", stage, done, total, progress[stage])
		}
		progress[stage] = done
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || progress[mediaTitleCleanupStageCleaning] != 3 || progress[mediaTitleCleanupStageGrouping] != 3 {
		t.Fatalf("items=%d progress=%#v", len(items), progress)
	}
	if relationshipCalls.Load() != 3 || maxActive.Load() < 2 || maxActive.Load() > 3 {
		t.Fatalf("relationship_calls=%d max_active=%d", relationshipCalls.Load(), maxActive.Load())
	}
}

func TestCleanMediaTitlesRejectsUnknownStageFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		system, _ := decodeAIRequestMessages(t, r)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(system, "标题标准化器") {
			writeAIContent(t, w, `{"items":[{"media_id":"a","title":"作品 A","year":0,"confidence":0.9,"relation":"part"}]}`)
			return
		}
		t.Fatal("relationship stage must not run after invalid normalization output")
	}))
	defer server.Close()

	ai := newTestTitleCleanupAI(server.URL)
	_, err := ai.CleanMediaTitles(t.Context(), []MediaTitleCleanupGroup{{
		Items: []MediaTitleCleanupSource{{MediaID: "a", Filename: "a.mp4"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected strict unknown-field error, got %v", err)
	}
}

func newTestTitleCleanupAI(apiBase string) *AIService {
	return NewAIService(&config.Config{AI: config.AIConfig{
		Enabled: true, APIBase: apiBase, APIKey: "test", Model: "test",
	}}, zap.NewNop(), nil)
}

func decodeAIRequestMessages(t *testing.T, r *http.Request) (string, string) {
	t.Helper()
	var request struct {
		Stream      bool    `json:"stream"`
		Temperature float64 `json:"temperature"`
		Messages    []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Stream || request.Temperature != 0 || len(request.Messages) < 2 {
		t.Fatalf("unexpected AI request: %#v", request)
	}
	return request.Messages[0].Content, request.Messages[1].Content
}

func writeAIContent(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(body)
}

func cleanupRequestMediaID(t *testing.T, user string) string {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(user), &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"items", "candidates"} {
		var items []struct {
			MediaID string `json:"media_id"`
		}
		if raw, ok := payload[key]; ok && json.Unmarshal(raw, &items) == nil && len(items) > 0 {
			return items[0].MediaID
		}
	}
	t.Fatalf("media id not found in request: %s", user)
	return ""
}
