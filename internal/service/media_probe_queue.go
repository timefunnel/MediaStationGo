package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

const mediaProbeQueueSetting = "media_probe.queue"

type MediaProbeQueueConfig struct {
	AutoCollect     bool     `json:"auto_collect"`
	LibraryIDs      []string `json:"library_ids"`
	IntervalSeconds int      `json:"interval_seconds"`
	TimeoutSeconds  int      `json:"timeout_seconds"`
	BatchSize       int      `json:"batch_size"`
	RunSeconds      int      `json:"run_seconds"`
}

func defaultMediaProbeQueueConfig() MediaProbeQueueConfig {
	return MediaProbeQueueConfig{AutoCollect: true, LibraryIDs: []string{}, IntervalSeconds: 10, TimeoutSeconds: 30, BatchSize: 10, RunSeconds: 600}
}

type MediaProbeQueueItem struct {
	model.MediaProbeJob
	Title         string                   `json:"title"`
	LibraryID     string                   `json:"library_id"`
	SeasonNum     int                      `json:"season_num"`
	EpisodeNum    int                      `json:"episode_num"`
	EpisodeEndNum int                      `json:"episode_end_num"`
	EpisodeTitle  string                   `json:"episode_title,omitempty"`
	FileName      string                   `gorm:"-" json:"file_name,omitempty"`
	SourcePath    string                   `json:"-"`
	Metadata      MediaProbeQueueMetadata  `gorm:"embedded" json:"-"`
	Result        *MediaProbeQueueMetadata `gorm:"-" json:"result,omitempty"`
}

type MediaProbeQueueMetadata struct {
	MediaProbeVersion int     `json:"-"`
	DurationSec       int     `json:"duration_sec"`
	Width             int     `json:"width"`
	Height            int     `json:"height"`
	VideoCodec        string  `json:"video_codec"`
	AudioCodec        string  `json:"audio_codec"`
	Container         string  `json:"container"`
	BitRate           int64   `json:"bit_rate"`
	FrameRate         float64 `json:"frame_rate"`
	VideoRange        string  `json:"video_range"`
	VideoBitDepth     int     `json:"video_bit_depth"`
	AudioChannels     int     `json:"audio_channels"`
	AudioSampleRate   int     `json:"audio_sample_rate"`
}

type MediaProbeQueueSnapshot struct {
	Config       MediaProbeQueueConfig `json:"config"`
	Running      bool                  `json:"running"`
	Counts       map[string]int64      `json:"counts"`
	Items        []MediaProbeQueueItem `json:"items"`
	Total        int64                 `json:"total"`
	Message      string                `json:"message"`
	CollectError string                `json:"collect_error,omitempty"`
	RiskAlert    *MediaProbeQueueItem  `json:"risk_alert,omitempty"`
}

type MediaProbeQueuePlan struct {
	ID            string                `json:"id"`
	ExpiresAt     time.Time             `json:"expires_at"`
	Config        MediaProbeQueueConfig `json:"config"`
	Count         int                   `json:"count"`
	LocalCount    int                   `json:"local_count"`
	OpenListCount int                   `json:"openlist_count"`
	RemoteCount   int                   `json:"remote_count"`
	Concurrency   int                   `json:"concurrency"`
	Targets       []string              `json:"targets"`
	Notice        string                `json:"notice"`
	RetryMediaID  string                `json:"retry_media_id,omitempty"`
	jobs          []model.MediaProbeJob
}

// MediaProbeQueueService automatically discovers missing metadata using only
// MSG's database. Remote work requires a confirmed, finite execution plan.
type MediaProbeQueueService struct {
	repo   *repository.Container
	stream *StreamService
	probe  mediaTrackProber
	log    *zap.Logger

	mu           sync.Mutex
	collectMu    sync.Mutex
	startOnce    sync.Once
	running      bool
	cancel       context.CancelFunc
	plan         *MediaProbeQueuePlan
	message      string
	collectError string
	lifetime     context.Context
}

func NewMediaProbeQueueService(repo *repository.Container, stream *StreamService, probe mediaTrackProber, log *zap.Logger) *MediaProbeQueueService {
	return &MediaProbeQueueService{repo: repo, stream: stream, probe: probe, log: log, lifetime: context.Background(), message: "已暂停；自动入队不会访问云盘"}
}

func (s *MediaProbeQueueService) Config(ctx context.Context) (MediaProbeQueueConfig, error) {
	value, err := s.repo.Setting.Get(ctx, mediaProbeQueueSetting)
	if err != nil {
		return MediaProbeQueueConfig{}, err
	}
	cfg := defaultMediaProbeQueueConfig()
	if value != "" {
		if err := json.Unmarshal([]byte(value), &cfg); err != nil {
			return cfg, fmt.Errorf("文件信息队列配置损坏: %w", err)
		}
	}
	if err := validateMediaProbeQueueConfig(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func validateMediaProbeQueueConfig(cfg *MediaProbeQueueConfig) error {
	if cfg.IntervalSeconds < 5 || cfg.IntervalSeconds > 3600 {
		return errors.New("任务间隔应为 5～3600 秒")
	}
	if cfg.TimeoutSeconds < 5 || cfg.TimeoutSeconds > 60 {
		return errors.New("单项超时应为 5～60 秒")
	}
	if cfg.BatchSize < 1 || cfg.BatchSize > 100 {
		return errors.New("每轮数量应为 1～100")
	}
	if cfg.RunSeconds < 10 || cfg.RunSeconds > 3600 {
		return errors.New("本轮最长运行时间应为 10～3600 秒")
	}
	if len(cfg.LibraryIDs) > 100 {
		return errors.New("媒体库范围最多 100 项")
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range cfg.LibraryIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return errors.New("媒体库范围不能包含空值或重复项")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	cfg.LibraryIDs = ids
	return nil
}

func (s *MediaProbeQueueService) SaveConfig(ctx context.Context, cfg MediaProbeQueueConfig) error {
	if err := validateMediaProbeQueueConfig(&cfg); err != nil {
		return err
	}
	s.collectMu.Lock()
	defer s.collectMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return errors.New("请先暂停本轮，再修改队列配置")
	}
	if len(cfg.LibraryIDs) > 0 {
		var count int64
		if err := s.repo.DB.WithContext(ctx).Model(&model.Library{}).Where("id IN ?", cfg.LibraryIDs).Count(&count).Error; err != nil {
			return err
		}
		if int(count) != len(cfg.LibraryIDs) {
			return errors.New("媒体库范围包含不存在或已删除的媒体库")
		}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := s.repo.Setting.Set(ctx, mediaProbeQueueSetting, string(data)); err != nil {
		return err
	}
	s.plan = nil
	return nil
}

func mediaProbeFingerprint(m *model.Media) string {
	data, _ := json.Marshal([]any{m.Path, m.STRMURL, m.SizeBytes, mediaProbeMetadataVersion})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Collect never resolves URLs, lists cloud directories or invokes ffprobe.
func (s *MediaProbeQueueService) Collect(ctx context.Context) (int64, error) {
	s.collectMu.Lock()
	defer s.collectMu.Unlock()
	cfg, err := s.Config(ctx)
	if err != nil {
		return 0, err
	}
	query := s.repo.DB.WithContext(ctx).Model(&model.Media{}).
		Select("id", "library_id", "path", "strm_url", "size_bytes").
		Where("media_probe_version < ? OR COALESCE(media_probe_version, 0) = 0 OR COALESCE(duration_sec, 0) <= 0 OR COALESCE(width, 0) <= 0 OR COALESCE(height, 0) <= 0 OR TRIM(COALESCE(video_codec, '')) = '' OR TRIM(COALESCE(audio_codec, '')) = ''", mediaProbeMetadataVersion)
	if len(cfg.LibraryIDs) > 0 {
		query = query.Where("library_id IN ?", cfg.LibraryIDs)
	}
	var added int64
	var batch []model.Media
	err = query.FindInBatches(&batch, 250, func(tx *gorm.DB, _ int) error {
		now := time.Now()
		jobs := make([]model.MediaProbeJob, 0, len(batch))
		for _, media := range batch {
			jobs = append(jobs, model.MediaProbeJob{MediaID: media.ID, Fingerprint: mediaProbeFingerprint(&media), Status: "pending", CreatedAt: now, UpdatedAt: now})
		}
		result := s.repo.DB.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "media_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"fingerprint", "status", "attempts", "error", "failure_kind", "http_status", "provider_code", "updated_at", "started_at", "finished_at"}),
			Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "media_probe_jobs.fingerprint <> excluded.fingerprint AND media_probe_jobs.status <> 'running'"}}},
		}).Create(&jobs)
		added += result.RowsAffected
		return result.Error
	}).Error
	return added, err
}

func (s *MediaProbeQueueService) Snapshot(ctx context.Context, status string, page int) (*MediaProbeQueueSnapshot, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return nil, err
	}
	if page < 1 || page > 100000 {
		return nil, errors.New("页码无效")
	}
	if status != "" && !validMediaProbeStatus(status) {
		return nil, errors.New("队列状态无效")
	}
	snap := &MediaProbeQueueSnapshot{Config: cfg, Counts: map[string]int64{}, Items: []MediaProbeQueueItem{}}
	var counts []struct {
		Status string
		Count  int64
	}
	if err := s.repo.DB.WithContext(ctx).Model(&model.MediaProbeJob{}).Select("status, COUNT(*) AS count").Group("status").Scan(&counts).Error; err != nil {
		return nil, err
	}
	for _, row := range counts {
		snap.Counts[row.Status] = row.Count
	}
	query := s.repo.DB.WithContext(ctx).Table("media_probe_jobs").Joins("LEFT JOIN media ON media.id = media_probe_jobs.media_id")
	if status != "" {
		query = query.Where("media_probe_jobs.status = ?", status)
	}
	if err := query.Count(&snap.Total).Error; err != nil {
		return nil, err
	}
	if err := query.Select(mediaProbeQueueItemColumns).Order("media_probe_jobs.updated_at DESC, media_probe_jobs.media_id").Offset((page - 1) * 50).Limit(50).Scan(&snap.Items).Error; err != nil {
		return nil, err
	}
	for i := range snap.Items {
		prepareMediaProbeQueueItem(&snap.Items[i])
	}
	// A warning must remain visible even when the failing row is on another page.
	var alerts []MediaProbeQueueItem
	if err := s.repo.DB.WithContext(ctx).Table("media_probe_jobs").Joins("LEFT JOIN media ON media.id = media_probe_jobs.media_id").
		Where("media_probe_jobs.failure_kind IN ? AND media_probe_jobs.status IN ?", []string{"risk_control", "rate_limited"}, []string{"failed", "interrupted", "pending"}).
		Select(mediaProbeQueueItemColumns).Order("COALESCE(media_probe_jobs.finished_at, media_probe_jobs.updated_at) DESC").Limit(1).Scan(&alerts).Error; err != nil {
		return nil, err
	}
	if len(alerts) > 0 {
		prepareMediaProbeQueueItem(&alerts[0])
		snap.RiskAlert = &alerts[0]
	}
	s.mu.Lock()
	snap.Running, snap.Message, snap.CollectError = s.running, s.message, s.collectError
	s.mu.Unlock()
	return snap, nil
}

const mediaProbeQueueItemColumns = `media_probe_jobs.*, COALESCE(media.title, '媒体已删除') AS title, COALESCE(media.library_id, '') AS library_id,
COALESCE(media.season_num, 0) AS season_num, COALESCE(media.episode_num, 0) AS episode_num,
COALESCE(media.episode_end_num, 0) AS episode_end_num, COALESCE(media.episode_title, '') AS episode_title,
COALESCE(media.path, '') AS source_path, COALESCE(media.media_probe_version, 0) AS media_probe_version,
COALESCE(media.duration_sec, 0) AS duration_sec, COALESCE(media.width, 0) AS width, COALESCE(media.height, 0) AS height,
COALESCE(media.video_codec, '') AS video_codec, COALESCE(media.audio_codec, '') AS audio_codec,
COALESCE(media.container, '') AS container, COALESCE(media.bit_rate, 0) AS bit_rate, COALESCE(media.frame_rate, 0) AS frame_rate,
COALESCE(media.video_range, '') AS video_range, COALESCE(media.video_bit_depth, 0) AS video_bit_depth,
COALESCE(media.audio_channels, 0) AS audio_channels, COALESCE(media.audio_sample_rate, 0) AS audio_sample_rate`

func prepareMediaProbeQueueItem(item *MediaProbeQueueItem) {
	source := strings.ReplaceAll(item.SourcePath, `\`, "/")
	if parsed, err := url.Parse(source); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		source = parsed.EscapedPath()
	}
	name := path.Base(source)
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	if name != "." && name != "/" {
		item.FileName = name
	}
	if item.Metadata.MediaProbeVersion >= mediaProbeMetadataVersion && (item.Status == "completed" || item.Status == "partial") {
		metadata := item.Metadata
		item.Result = &metadata
	}
}

func validMediaProbeStatus(status string) bool {
	switch status {
	case "pending", "running", "completed", "partial", "failed", "canceled", "interrupted":
		return true
	}
	return false
}

func (s *MediaProbeQueueService) CreatePlan(ctx context.Context) (*MediaProbeQueuePlan, error) {
	current, err := s.Config(ctx)
	if err != nil {
		return nil, err
	}
	if current.AutoCollect {
		if _, err := s.Collect(ctx); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil, errors.New("已有一轮正在运行")
	}
	cfg, err := s.Config(ctx)
	if err != nil {
		return nil, err
	}
	query := s.repo.DB.WithContext(ctx).Model(&model.MediaProbeJob{}).Joins("JOIN media ON media.id = media_probe_jobs.media_id AND media.deleted_at IS NULL").Where("media_probe_jobs.status = ?", "pending")
	if len(cfg.LibraryIDs) > 0 {
		query = query.Where("media.library_id IN ?", cfg.LibraryIDs)
	}
	var jobs []model.MediaProbeJob
	if err := query.Select("media_probe_jobs.*").Order("media_probe_jobs.created_at, media_probe_jobs.media_id").Limit(cfg.BatchSize).Find(&jobs).Error; err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, errors.New("当前范围没有待处理影片")
	}
	return s.preparePlan(ctx, cfg, jobs, "")
}

// Preview a single failed job without requeueing it or touching its source.
func (s *MediaProbeQueueService) CreateRetryPlan(ctx context.Context, mediaID string) (*MediaProbeQueuePlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil, errors.New("请先暂停本轮，再重试单条任务")
	}
	cfg, err := s.Config(ctx)
	if err != nil {
		return nil, err
	}
	var job model.MediaProbeJob
	if err := s.repo.DB.WithContext(ctx).First(&job, "media_id = ?", mediaID).Error; err != nil {
		return nil, err
	}
	if job.Status != "failed" {
		return nil, errors.New("仅失败任务支持单独重试")
	}
	var media model.Media
	if err := s.repo.DB.WithContext(ctx).First(&media, "id = ?", mediaID).Error; err != nil {
		return nil, err
	}
	if mediaProbeFingerprint(&media) != job.Fingerprint {
		return nil, errors.New("媒体文件已变化，请先刷新缺失信息队列")
	}
	if len(cfg.LibraryIDs) > 0 {
		included := false
		for _, id := range cfg.LibraryIDs {
			included = included || media.LibraryID == id
		}
		if !included {
			return nil, errors.New("此项不在当前媒体库范围，请先调整队列配置")
		}
	}
	return s.preparePlan(ctx, cfg, []model.MediaProbeJob{job}, mediaID)
}

func (s *MediaProbeQueueService) preparePlan(ctx context.Context, cfg MediaProbeQueueConfig, jobs []model.MediaProbeJob, retryMediaID string) (*MediaProbeQueuePlan, error) {
	plan := &MediaProbeQueuePlan{ID: uuid.NewString(), ExpiresAt: time.Now().Add(5 * time.Minute), Config: cfg, Concurrency: 1, Targets: []string{}, jobs: jobs, RetryMediaID: retryMediaID, Notice: "每项最多调用一次 MSG 取链流程；OpenList 正常为 1 次 /api/fs/link，430004 缓存恢复可能列父目录后再取链，不设置 refresh=true。底层 115 通常涉及文件查询和签发，Token 刷新可能增加请求；CDN 读取次数和流量由文件结构决定。并发 1，失败即停止本轮，无任务自动重试。真实访问 115/CDN 可能限流、封禁或链路失败。"}
	targets := map[string]bool{}
	for _, job := range plan.jobs {
		var media model.Media
		if err := s.repo.DB.WithContext(ctx).First(&media, "id = ?", job.MediaID).Error; err != nil {
			return nil, err
		}
		if typ, _, ok := parseCloudMediaPlaybackURL(media.STRMURL); ok {
			if typ == "openlist" {
				plan.OpenListCount++
			} else {
				plan.RemoteCount++
			}
			targets["云盘 "+typ+"：文件查询、直链签发及 CDN 媒体读取"] = true
		} else if probeHTTPMediaURL(&media) != "" {
			plan.RemoteCount++
			targets["HTTP 媒体读取"] = true
		} else {
			plan.LocalCount++
			targets["本地媒体读取"] = true
		}
	}
	for target := range targets {
		plan.Targets = append(plan.Targets, target)
	}
	sort.Strings(plan.Targets)
	plan.Count = len(plan.jobs)
	s.plan = plan
	copy := *plan
	return &copy, nil
}
