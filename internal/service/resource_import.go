package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	ResourceImportStatusQueued               = "queued"
	ResourceImportStatusRunning              = "running"
	ResourceImportStatusCompleted            = "completed"
	ResourceImportStatusCompletedWithWarning = "completed_with_warning"
	ResourceImportStatusFailed               = "failed"
	ResourceImportStatusCanceled             = "canceled"
	resourceSearchLimit                      = 100
	resourceSearchSessionTTL                 = 15 * time.Minute
)

var resourceImportFinalStatuses = []string{
	ResourceImportStatusCompleted,
	ResourceImportStatusCompletedWithWarning,
	ResourceImportStatusFailed,
	ResourceImportStatusCanceled,
}

var ErrResourceImportDeleteNotAllowed = errors.New("only failed resource import tasks can be deleted")

type ResourceImportService struct {
	cfg    config.ResourceImportConfig
	log    *zap.Logger
	repos  *repository.Container
	client resourcePipelineClient
	ctx    context.Context

	globalSem chan struct{}
	mu        sync.Mutex
	userSems  map[string]chan struct{}
	executing map[string]struct{}
	closed    bool
}

type ResourceSearchInput struct {
	Query            string `json:"query"`
	Source           string `json:"source,omitempty"`
	Page             int    `json:"page,omitempty"`
	PageSize         int    `json:"page_size,omitempty"`
	RootID           string `json:"root_id,omitempty"`
	ResultQuery      string `json:"result_query,omitempty"`
	SourceFilter     string `json:"source_filter,omitempty"`
	ResolutionFilter string `json:"resolution_filter,omitempty"`
	SubtitleFilter   string `json:"subtitle_filter,omitempty"`
	SortBy           string `json:"sort_by,omitempty"`
}

type ResourceSearchCapabilities struct {
	Sources   []string `json:"sources,omitempty"`
	Pansou    bool     `json:"pansou,omitempty"`
	BT4G      bool     `json:"bt4g,omitempty"`
	LLMRerank bool     `json:"llm_rerank,omitempty"`
}

type ResourceSearchCandidate struct {
	Index        int    `json:"index"`
	Title        string `json:"title"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	SizeText     string `json:"size_text,omitempty"`
	Source       string `json:"source,omitempty"`
	Seeders      int    `json:"seeders,omitempty"`
	Resolution   string `json:"resolution,omitempty"`
	Subtitle     string `json:"subtitle,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	Summary      string `json:"summary,omitempty"`
	CandidateID  string `json:"-"`
}

type ResourceSearchRoot struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

type ResourceSearchResponse struct {
	SessionID       string                     `json:"session_id"`
	Query           string                     `json:"query"`
	Page            int                        `json:"page"`
	PageSize        int                        `json:"page_size"`
	Total           int                        `json:"total"`
	UnfilteredTotal int                        `json:"unfiltered_total"`
	TotalPages      int                        `json:"total_pages"`
	Roots           []ResourceSearchRoot       `json:"roots,omitempty"`
	Capabilities    ResourceSearchCapabilities `json:"capabilities,omitempty"`
	Facets          ResourceSearchFacets       `json:"facets"`
	Results         []ResourceSearchCandidate  `json:"results"`
}

type ResourceSearchFacets struct {
	Sources     []string `json:"sources"`
	Resolutions []string `json:"resolutions"`
}

type ResourceSearchError struct {
	StatusCode   int
	Code         string
	Message      string
	Capabilities ResourceSearchCapabilities
}

func (e *ResourceSearchError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "resource search failed"
	}
	return e.Message
}

func (e *ResourceSearchError) HTTPStatus() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

type ResourceImportCreateInput struct {
	SearchSessionID string `json:"search_session_id"`
	CandidateIndex  int    `json:"candidate_index"`
	RootID          string `json:"root_id"`
	SubscriptionID  string `json:"subscription_id,omitempty"`
	ForceDuplicate  bool   `json:"force_duplicate,omitempty"`
	UpgradeMediaID  string `json:"upgrade_media_id,omitempty"`
	KeepOldVersion  *bool  `json:"keep_old_version,omitempty"`
}

type ResourceImportDuplicate struct {
	CanForce bool   `json:"can_force"`
	MediaID  string `json:"media_id,omitempty"`
	Title    string `json:"title,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type ResourceImportDuplicateError struct {
	Message   string
	Duplicate ResourceImportDuplicate
}

func (e *ResourceImportDuplicateError) Error() string { return e.Message }

type resourcePipelineStateError struct {
	Status string
	Stage  string
}

func (e *resourcePipelineStateError) Error() string {
	return fmt.Sprintf("media-pipeline import returned an invalid state: status=%q stage=%q", e.Status, e.Stage)
}

type ResourceImportTask struct {
	ID              string     `json:"id"`
	UserID          string     `json:"user_id,omitempty"`
	CreatorUsername string     `json:"creator_username,omitempty"`
	SubscriptionID  string     `json:"subscription_id,omitempty"`
	LibraryID       string     `json:"library_id"`
	LibraryName     string     `json:"library_name,omitempty"`
	RootID          string     `json:"root_id"`
	RootName        string     `json:"root_name,omitempty"`
	SearchSessionID string     `json:"search_session_id,omitempty"`
	CandidateIndex  int        `json:"candidate_index"`
	CandidateTitle  string     `json:"candidate_title,omitempty"`
	Source          string     `json:"source,omitempty"`
	Status          string     `json:"status"`
	Stage           string     `json:"stage,omitempty"`
	Progress        int        `json:"progress"`
	Message         string     `json:"message,omitempty"`
	Error           string     `json:"error,omitempty"`
	PipelineJobID   string     `json:"pipeline_job_id,omitempty"`
	MediaID         string     `json:"media_id,omitempty"`
	MediaTitle      string     `json:"media_title,omitempty"`
	UpgradeMediaID  string     `json:"upgrade_media_id,omitempty"`
	KeepOldVersion  bool       `json:"keep_old_version"`
	CancelRequested bool       `json:"cancel_requested"`
	Attempt         int        `json:"attempt"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
}

type ResourceImportListFilter struct {
	LibraryID string
	UserID    string
	Status    string
	Page      int
	PageSize  int
}

type ResourceImportListResult struct {
	Items    []ResourceImportTask `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
}

type storedResourceSearch struct {
	PipelineSessionID string                     `json:"pipeline_session_id"`
	Capabilities      ResourceSearchCapabilities `json:"capabilities"`
	Candidates        []storedResourceCandidate  `json:"candidates"`
}

type storedResourceCandidate struct {
	ResourceSearchCandidate
	CandidateID string `json:"candidate_id"`
}

func NewResourceImportService(cfg config.ResourceImportConfig, log *zap.Logger, repos *repository.Container, ctx context.Context) (*ResourceImportService, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	client, err := newResourcePipelineHTTPClient(cfg)
	if err != nil {
		return nil, err
	}
	return newResourceImportServiceWithClient(cfg, log, repos, ctx, client), nil
}

func newResourceImportServiceWithClient(cfg config.ResourceImportConfig, log *zap.Logger, repos *repository.Container, ctx context.Context, client resourcePipelineClient) *ResourceImportService {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	if cfg.MaxConcurrentPerUser <= 0 {
		cfg.MaxConcurrentPerUser = 2
	}
	if cfg.PollSeconds <= 0 {
		cfg.PollSeconds = 5
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &ResourceImportService{
		cfg:       cfg,
		log:       log,
		repos:     repos,
		client:    client,
		ctx:       ctx,
		globalSem: make(chan struct{}, maxConcurrent),
		userSems:  make(map[string]chan struct{}),
		executing: make(map[string]struct{}),
	}
}

func (s *ResourceImportService) Search(ctx context.Context, userID string, library model.Library, root model.LibraryRoot, in ResourceSearchInput) (ResourceSearchResponse, error) {
	if s == nil || s.client == nil || s.repos == nil || s.repos.DB == nil {
		return ResourceSearchResponse{}, errors.New("resource import service unavailable")
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return ResourceSearchResponse{}, errors.New("query is required")
	}
	if len(query) > 512 {
		return ResourceSearchResponse{}, errors.New("query is too long")
	}
	if err := validateResourceSearchView(in); err != nil {
		return ResourceSearchResponse{}, err
	}
	source := strings.ToLower(strings.TrimSpace(in.Source))
	if source == "" {
		source = "default"
	}
	if source != "default" && source != "pansou" && source != "bt4g" {
		return ResourceSearchResponse{}, errors.New("unsupported resource search source")
	}
	category, _, _ := resourceTargetMetadata(library.Type)
	if _, err := resourceRootOpenListPath(root.Path); err != nil {
		return ResourceSearchResponse{}, err
	}
	if err := s.repos.DB.WithContext(ctx).Unscoped().Where("expires_at <= ?", time.Now()).Delete(&model.ResourceSearchSession{}).Error; err != nil {
		return ResourceSearchResponse{}, err
	}

	if cached, ok, err := s.findCachedSearch(ctx, userID, library.ID, root.ID, query, source); err != nil {
		return ResourceSearchResponse{}, err
	} else if ok {
		var stored storedResourceSearch
		if err := json.Unmarshal([]byte(cached.ResultsJSON), &stored); err != nil {
			return ResourceSearchResponse{}, errors.New("cached resource search session data is invalid")
		}
		if storedResourceSearchValid(stored) {
			return resourceSearchPage(cached, stored, root, in), nil
		}
		if err := s.repos.DB.WithContext(ctx).Unscoped().Delete(&cached).Error; err != nil {
			return ResourceSearchResponse{}, err
		}
		if s.log != nil {
			s.log.Warn("discarded invalid resource search cache", zap.String("session_id", cached.ID))
		}
	}

	pipeline, err := s.client.Search(ctx, resourcePipelineSearchRequest{
		OwnerID:  userID,
		Query:    query,
		Category: category,
		Source:   source,
		Limit:    resourceSearchLimit,
	})
	if err != nil {
		var pipelineErr *resourcePipelineError
		if errors.As(err, &pipelineErr) {
			return ResourceSearchResponse{}, &ResourceSearchError{
				StatusCode:   pipelineErr.StatusCode,
				Code:         pipelineErr.Code,
				Message:      pipelineErr.Message,
				Capabilities: pipelineErr.Capabilities,
			}
		}
		return ResourceSearchResponse{}, err
	}
	if strings.TrimSpace(pipeline.SessionID) == "" {
		return ResourceSearchResponse{}, errors.New("media-pipeline search returned no session_id")
	}
	candidates, err := normalizeResourceCandidates(pipeline.Items)
	if err != nil {
		return ResourceSearchResponse{}, err
	}
	stored := storedResourceSearch{
		PipelineSessionID: pipeline.SessionID,
		Capabilities:      pipeline.Capabilities,
		Candidates:        candidates,
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return ResourceSearchResponse{}, err
	}
	expiresAt := time.Now().Add(resourceSearchSessionTTL)
	if pipeline.ExpiresAt > 0 {
		pipelineExpiry := time.Unix(pipeline.ExpiresAt, 0)
		if pipelineExpiry.Before(expiresAt) {
			expiresAt = pipelineExpiry
		}
	}
	record := model.ResourceSearchSession{
		UserID: userID, LibraryID: library.ID, LibraryRootID: root.ID,
		Query: query, Source: source, ResultsJSON: string(encoded), ExpiresAt: expiresAt,
	}
	if err := s.repos.DB.WithContext(ctx).Create(&record).Error; err != nil {
		return ResourceSearchResponse{}, err
	}
	return resourceSearchPage(record, stored, root, in), nil
}

func (s *ResourceImportService) Create(ctx context.Context, userID string, library model.Library, root model.LibraryRoot, in ResourceImportCreateInput) (ResourceImportTask, error) {
	if s == nil || s.client == nil || s.repos == nil || s.repos.DB == nil {
		return ResourceImportTask{}, errors.New("resource import service unavailable")
	}
	session, stored, err := s.loadOwnedSearch(ctx, userID, library.ID, root.ID, in.SearchSessionID)
	if err != nil {
		return ResourceImportTask{}, err
	}
	if in.CandidateIndex < 0 || in.CandidateIndex >= len(stored.Candidates) {
		return ResourceImportTask{}, errors.New("candidate_index is out of range")
	}
	storedCandidate := stored.Candidates[in.CandidateIndex]
	candidate := storedCandidate.ResourceSearchCandidate
	category, provider, mediaType := resourceTargetMetadata(library.Type)
	rootOpenListPath, err := resourceRootOpenListPath(root.Path)
	if err != nil {
		return ResourceImportTask{}, err
	}
	upgradeMediaID := strings.TrimSpace(in.UpgradeMediaID)
	if err := s.validateUpgradeTarget(ctx, library, root, upgradeMediaID); err != nil {
		return ResourceImportTask{}, err
	}
	keepOldVersion := true
	if upgradeMediaID != "" && in.KeepOldVersion != nil {
		keepOldVersion = *in.KeepOldVersion
	}
	if upgradeMediaID != "" && !keepOldVersion {
		allowed, err := userCanManageMediaVersion(ctx, s.repos, userID, false, upgradeMediaID)
		if err != nil {
			return ResourceImportTask{}, err
		}
		if !allowed {
			return ResourceImportTask{}, fmt.Errorf("%w: 只有管理员或该片源的入库用户可以在升级成功后移除旧版本", ErrMediaVersionForbidden)
		}
	}
	idempotencyKey := resourceImportIdempotencyKey(
		userID, library.ID, root.ID, session.ID, in.CandidateIndex, in.SubscriptionID, in.ForceDuplicate, upgradeMediaID, keepOldVersion,
	)
	if existing, found, err := s.findJobByIdempotencyKey(ctx, idempotencyKey); err != nil {
		return ResourceImportTask{}, err
	} else if found {
		return s.taskDTO(ctx, existing, false)
	}

	pipelineTask, err := s.client.CreateImport(ctx, userID, idempotencyKey, resourcePipelineCreateRequest{
		SearchSessionID:  stored.PipelineSessionID,
		CandidateID:      storedCandidate.CandidateID,
		Category:         category,
		LibraryID:        library.ID,
		RootID:           root.ID,
		RootOpenListPath: rootOpenListPath,
		Provider:         provider,
		MediaType:        mediaType,
		ForceDuplicate:   in.ForceDuplicate,
		UpgradeMediaID:   upgradeMediaID,
		KeepOldVersion:   keepOldVersion,
	})
	if err != nil {
		var pipelineErr *resourcePipelineError
		if errors.As(err, &pipelineErr) && pipelineErr.StatusCode == 409 && pipelineErr.Duplicate != nil {
			return ResourceImportTask{}, &ResourceImportDuplicateError{Message: pipelineErr.Message, Duplicate: *pipelineErr.Duplicate}
		}
		return ResourceImportTask{}, err
	}
	if strings.TrimSpace(pipelineTask.ID) == "" {
		return ResourceImportTask{}, errors.New("media-pipeline import returned no task id")
	}
	candidateJSON, err := json.Marshal(candidate)
	if err != nil {
		return ResourceImportTask{}, err
	}
	now := time.Now()
	status, stage := mapPipelineImportState(pipelineTask)
	if status == "" || stage == "" {
		_, _ = s.client.CancelImport(context.Background(), userID, pipelineTask.ID)
		return ResourceImportTask{}, errors.New("media-pipeline import returned an invalid status or stage")
	}
	record := model.ResourceImportJob{
		UserID: userID, SubscriptionID: strings.TrimSpace(in.SubscriptionID), LibraryID: library.ID, LibraryRootID: root.ID,
		SearchSessionID: session.ID, CandidateIndex: in.CandidateIndex,
		CandidateJSON: string(candidateJSON), CandidateTitle: candidate.Title,
		CandidateSource: candidate.Source, CandidateSize: candidate.SizeBytes,
		Attempt: 1, IdempotencyKey: idempotencyKey, ForceDuplicate: in.ForceDuplicate,
		UpgradeMediaID: upgradeMediaID, KeepOldVersion: keepOldVersion,
		Status: status, Stage: stage, Message: safePipelineMessage(pipelineTask.Message),
		PipelineJobID: pipelineTask.ID, MediaID: pipelineTask.MsgMediaID,
		MediaTitle: pipelineTask.MsgMediaTitle, CancelRequested: pipelineTask.CancelRequested,
	}
	if status == ResourceImportStatusRunning {
		record.StartedAt = &now
	}
	if resourceImportStatusFinal(status) {
		record.FinishedAt = &now
	}
	if err := s.repos.DB.WithContext(ctx).Create(&record).Error; err != nil {
		if existing, found, lookupErr := s.findJobByIdempotencyKey(ctx, idempotencyKey); lookupErr == nil && found {
			return s.taskDTO(ctx, existing, false)
		}
		_, cancelErr := s.client.CancelImport(context.Background(), userID, pipelineTask.ID)
		if cancelErr != nil && s.log != nil {
			s.log.Error("orphan pipeline import cancel failed", zap.String("pipeline_job_id", pipelineTask.ID), zap.Error(cancelErr))
		}
		return ResourceImportTask{}, err
	}
	s.schedule(record.ID)
	return s.taskDTO(ctx, record, false)
}

func (s *ResourceImportService) validateUpgradeTarget(ctx context.Context, library model.Library, root model.LibraryRoot, mediaID string) error {
	if mediaID == "" {
		return nil
	}
	if s.repos.Media == nil {
		return errors.New("upgrade_media_id 无效：媒体服务不可用")
	}
	media, err := s.repos.Media.FindByID(ctx, mediaID)
	if err != nil {
		return err
	}
	if media == nil {
		return errors.New("upgrade_media_id 无效：目标作品不存在")
	}
	if media.LibraryID != library.ID {
		return errors.New("upgrade_media_id 无效：目标作品不属于当前媒体库")
	}
	if media.LibraryRootID != "" && media.LibraryRootID != root.ID {
		return errors.New("upgrade_media_id 无效：目标作品不属于当前入库目录")
	}
	return nil
}

func (s *ResourceImportService) Recover(ctx context.Context) (int, error) {
	if s == nil || s.repos == nil || s.repos.DB == nil {
		return 0, nil
	}
	var jobs []model.ResourceImportJob
	err := s.repos.DB.WithContext(ctx).
		Where("status NOT IN ?", resourceImportFinalStatuses).
		Order("created_at ASC").Find(&jobs).Error
	if err != nil {
		return 0, err
	}
	for i := range jobs {
		if strings.TrimSpace(jobs[i].PipelineJobID) == "" {
			_ = s.repos.DB.WithContext(ctx).Model(&model.ResourceImportJob{}).Where("id = ?", jobs[i].ID).Updates(map[string]any{
				"status": ResourceImportStatusFailed, "stage": "failed",
				"public_error": "任务恢复失败：缺少 pipeline_job_id", "error": "missing pipeline_job_id during recovery", "finished_at": time.Now(),
			}).Error
			continue
		}
		s.schedule(jobs[i].ID)
	}
	return len(jobs), nil
}

func (s *ResourceImportService) Get(ctx context.Context, requesterID string, isAdmin bool, id string) (ResourceImportTask, error) {
	job, err := s.loadOwnedJob(ctx, requesterID, isAdmin, id)
	if err != nil {
		return ResourceImportTask{}, err
	}
	return s.taskDTO(ctx, job, isAdmin)
}

func (s *ResourceImportService) List(ctx context.Context, requesterID string, isAdmin bool, filter ResourceImportListFilter) (ResourceImportListResult, error) {
	if s == nil || s.repos == nil || s.repos.DB == nil {
		return ResourceImportListResult{}, errors.New("resource import service unavailable")
	}
	page, pageSize := normalizeResourceImportPage(filter.Page, filter.PageSize)
	query := s.repos.DB.WithContext(ctx).Model(&model.ResourceImportJob{})
	if !isAdmin {
		query = query.Where("user_id = ?", requesterID)
	} else if strings.TrimSpace(filter.UserID) != "" {
		query = query.Where("user_id = ?", strings.TrimSpace(filter.UserID))
	}
	if strings.TrimSpace(filter.LibraryID) != "" {
		query = query.Where("library_id = ?", strings.TrimSpace(filter.LibraryID))
	}
	if strings.TrimSpace(filter.Status) != "" {
		query = query.Where("status = ?", strings.TrimSpace(filter.Status))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return ResourceImportListResult{}, err
	}
	var rows []model.ResourceImportJob
	if err := query.Order("updated_at DESC, created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return ResourceImportListResult{}, err
	}
	items := make([]ResourceImportTask, 0, len(rows))
	for i := range rows {
		item, err := s.taskDTO(ctx, rows[i], isAdmin)
		if err != nil {
			return ResourceImportListResult{}, err
		}
		items = append(items, item)
	}
	return ResourceImportListResult{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *ResourceImportService) Cancel(ctx context.Context, requesterID string, isAdmin bool, id string) (ResourceImportTask, error) {
	job, err := s.loadOwnedJob(ctx, requesterID, isAdmin, id)
	if err != nil {
		return ResourceImportTask{}, err
	}
	if resourceImportStatusFinal(job.Status) {
		return ResourceImportTask{}, errors.New("resource import task is already final")
	}
	if err := s.repos.DB.WithContext(ctx).Model(&model.ResourceImportJob{}).Where("id = ?", job.ID).Update("cancel_requested", true).Error; err != nil {
		return ResourceImportTask{}, err
	}
	task, err := s.client.CancelImport(ctx, job.UserID, job.PipelineJobID)
	if err != nil {
		return ResourceImportTask{}, err
	}
	if err := s.applyPipelineTask(ctx, &job, task); err != nil {
		return ResourceImportTask{}, err
	}
	return s.taskDTO(ctx, job, isAdmin)
}

func (s *ResourceImportService) Retry(ctx context.Context, requesterID string, isAdmin bool, id string) (ResourceImportTask, error) {
	job, err := s.loadOwnedJob(ctx, requesterID, isAdmin, id)
	if err != nil {
		return ResourceImportTask{}, err
	}
	if job.Status != ResourceImportStatusFailed && job.Status != ResourceImportStatusCanceled && job.Status != ResourceImportStatusCompletedWithWarning {
		return ResourceImportTask{}, errors.New("resource import task is not retryable")
	}
	task, err := s.client.RetryImport(ctx, job.UserID, job.PipelineJobID)
	if err != nil {
		return ResourceImportTask{}, err
	}
	job.Attempt++
	job.CancelRequested = false
	job.FinishedAt = nil
	if err := s.applyPipelineTask(ctx, &job, task); err != nil {
		return ResourceImportTask{}, err
	}
	s.schedule(job.ID)
	return s.taskDTO(ctx, job, isAdmin)
}

func (s *ResourceImportService) DeleteFailed(ctx context.Context, requesterID string, isAdmin bool, id string) error {
	job, err := s.loadOwnedJob(ctx, requesterID, isAdmin, id)
	if err != nil {
		return err
	}
	if job.Status != ResourceImportStatusFailed {
		return ErrResourceImportDeleteNotAllowed
	}
	result := s.repos.DB.WithContext(ctx).Unscoped().
		Where("id = ? AND status = ?", job.ID, ResourceImportStatusFailed).
		Delete(&model.ResourceImportJob{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrResourceImportDeleteNotAllowed
	}
	return nil
}

func (s *ResourceImportService) schedule(id string) {
	if s == nil || strings.TrimSpace(id) == "" {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if _, ok := s.executing[id]; ok {
		s.mu.Unlock()
		return
	}
	s.executing[id] = struct{}{}
	s.mu.Unlock()
	go s.monitor(id)
}

func (s *ResourceImportService) monitor(id string) {
	defer func() {
		s.mu.Lock()
		delete(s.executing, id)
		s.mu.Unlock()
	}()
	ctx := s.ctx
	job, err := s.loadOwnedJob(ctx, "", true, id)
	if err != nil {
		return
	}
	if !s.acquire(ctx, job.UserID) {
		return
	}
	defer s.release(job.UserID)
	ticker := time.NewTicker(time.Duration(s.cfg.PollSeconds) * time.Second)
	defer ticker.Stop()
	invalidStateCount := 0
	for {
		waitForStateRecovery := false
		job, err = s.loadOwnedJob(ctx, "", true, id)
		if err != nil || resourceImportStatusFinal(job.Status) {
			return
		}
		var child resourcePipelineTask
		if job.CancelRequested {
			child, err = s.client.CancelImport(ctx, job.UserID, job.PipelineJobID)
		} else {
			child, err = s.client.GetImport(ctx, job.UserID, job.PipelineJobID)
		}
		if err != nil {
			invalidStateCount = 0
			var pipelineErr *resourcePipelineError
			if job.CancelRequested && errors.As(err, &pipelineErr) && pipelineErr.StatusCode == 409 {
				_ = s.repos.DB.WithContext(ctx).Model(&model.ResourceImportJob{}).Where("id = ?", job.ID).Updates(map[string]any{
					"cancel_requested": false,
					"message":          "任务已进入媒体库同步阶段，无法取消；已转存的115文件会保留",
				}).Error
			}
			if s.log != nil {
				s.log.Warn("resource import poll failed", zap.String("job_id", job.ID), zap.Error(err))
			}
		} else if applyErr := s.applyPipelineTask(ctx, &job, child); applyErr != nil {
			if s.log != nil {
				s.log.Error(
					"resource import state persistence failed",
					zap.String("job_id", job.ID),
					zap.String("pipeline_status", child.Status),
					zap.String("pipeline_stage", child.Stage),
					zap.Error(applyErr),
				)
			}
			var stateErr *resourcePipelineStateError
			if errors.As(applyErr, &stateErr) {
				invalidStateCount++
				if invalidStateCount < 3 {
					waitForStateRecovery = true
				} else {
					_ = s.repos.DB.WithContext(ctx).Model(&model.ResourceImportJob{}).Where("id = ?", job.ID).Updates(map[string]any{
						"status": ResourceImportStatusFailed, "stage": "failed",
						"public_error": applyErr.Error(), "error": applyErr.Error(), "finished_at": time.Now(),
					}).Error
					return
				}
			} else {
				invalidStateCount = 0
			}
		} else {
			invalidStateCount = 0
		}
		if !waitForStateRecovery && resourceImportStatusFinal(job.Status) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *ResourceImportService) acquire(ctx context.Context, userID string) bool {
	s.mu.Lock()
	userSem := s.userSems[userID]
	if userSem == nil {
		userSem = make(chan struct{}, s.cfg.MaxConcurrentPerUser)
		s.userSems[userID] = userSem
	}
	s.mu.Unlock()
	select {
	case s.globalSem <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	select {
	case userSem <- struct{}{}:
		return true
	case <-ctx.Done():
		<-s.globalSem
		return false
	}
}

func (s *ResourceImportService) release(userID string) {
	s.mu.Lock()
	userSem := s.userSems[userID]
	s.mu.Unlock()
	if userSem != nil {
		<-userSem
	}
	<-s.globalSem
}

func (s *ResourceImportService) applyPipelineTask(ctx context.Context, job *model.ResourceImportJob, child resourcePipelineTask) error {
	if job == nil {
		return errors.New("resource import job is nil")
	}
	status, stage := mapPipelineImportState(child)
	if status == "" || stage == "" {
		return &resourcePipelineStateError{Status: child.Status, Stage: child.Stage}
	}
	if (status == ResourceImportStatusCompleted || status == ResourceImportStatusCompletedWithWarning) && strings.TrimSpace(child.MsgMediaID) == "" {
		status, stage = ResourceImportStatusFailed, "failed"
		child.Error = "media-pipeline completed without msg_media_id"
	}
	now := time.Now()
	updates := map[string]any{
		"status": status, "stage": stage, "message": safePipelineMessage(child.Message),
		"public_error": safePipelineMessage(child.Error), "error": child.Error,
		"media_id": strings.TrimSpace(child.MsgMediaID), "media_title": strings.TrimSpace(child.MsgMediaTitle),
		"cancel_requested": child.CancelRequested, "attempt": job.Attempt,
	}
	if status == ResourceImportStatusRunning && job.StartedAt == nil {
		updates["started_at"] = now
		job.StartedAt = &now
	}
	if resourceImportStatusFinal(status) {
		updates["finished_at"] = now
		job.FinishedAt = &now
	}
	if child.Result != nil {
		if encoded, err := json.Marshal(child.Result); err == nil {
			updates["result_json"] = string(encoded)
		}
	}
	if err := s.repos.DB.WithContext(ctx).Model(&model.ResourceImportJob{}).Where("id = ?", job.ID).Updates(updates).Error; err != nil {
		return err
	}
	job.Status, job.Stage = status, stage
	job.Message, job.PublicError, job.Error = safePipelineMessage(child.Message), safePipelineMessage(child.Error), child.Error
	job.MediaID, job.MediaTitle, job.CancelRequested = strings.TrimSpace(child.MsgMediaID), strings.TrimSpace(child.MsgMediaTitle), child.CancelRequested
	return nil
}

func (s *ResourceImportService) findCachedSearch(ctx context.Context, userID, libraryID, rootID, query, source string) (model.ResourceSearchSession, bool, error) {
	var row model.ResourceSearchSession
	err := s.repos.DB.WithContext(ctx).
		Where("user_id = ? AND library_id = ? AND library_root_id = ? AND query = ? AND source = ? AND expires_at > ?", userID, libraryID, rootID, query, source, time.Now()).
		Order("created_at DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ResourceSearchSession{}, false, nil
	}
	return row, err == nil, err
}

func (s *ResourceImportService) loadOwnedSearch(ctx context.Context, userID, libraryID, rootID, id string) (model.ResourceSearchSession, storedResourceSearch, error) {
	var row model.ResourceSearchSession
	err := s.repos.DB.WithContext(ctx).
		Where("id = ? AND user_id = ? AND library_id = ? AND library_root_id = ?", strings.TrimSpace(id), userID, libraryID, rootID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, storedResourceSearch{}, errors.New("resource search session not found")
	}
	if err != nil {
		return row, storedResourceSearch{}, err
	}
	if !row.ExpiresAt.After(time.Now()) {
		return row, storedResourceSearch{}, errors.New("resource search session expired")
	}
	var stored storedResourceSearch
	if err := json.Unmarshal([]byte(row.ResultsJSON), &stored); err != nil {
		return row, stored, errors.New("resource search session data is invalid")
	}
	if !storedResourceSearchValid(stored) {
		return row, stored, errors.New("resource search session data is invalid; search again")
	}
	return row, stored, nil
}

func (s *ResourceImportService) findJobByIdempotencyKey(ctx context.Context, key string) (model.ResourceImportJob, bool, error) {
	var row model.ResourceImportJob
	err := s.repos.DB.WithContext(ctx).Where("idempotency_key = ?", key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, false, nil
	}
	return row, err == nil, err
}

func (s *ResourceImportService) loadOwnedJob(ctx context.Context, requesterID string, isAdmin bool, id string) (model.ResourceImportJob, error) {
	var row model.ResourceImportJob
	query := s.repos.DB.WithContext(ctx).Where("id = ?", strings.TrimSpace(id))
	if !isAdmin {
		query = query.Where("user_id = ?", requesterID)
	}
	err := query.First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, errors.New("resource import task not found")
	}
	return row, err
}

func (s *ResourceImportService) taskDTO(ctx context.Context, job model.ResourceImportJob, includeCreator bool) (ResourceImportTask, error) {
	item := ResourceImportTask{
		ID: job.ID, SubscriptionID: job.SubscriptionID, LibraryID: job.LibraryID, RootID: job.LibraryRootID,
		SearchSessionID: job.SearchSessionID, CandidateIndex: job.CandidateIndex,
		CandidateTitle: job.CandidateTitle, Source: job.CandidateSource,
		Status: job.Status, Stage: job.Stage, Progress: resourceImportProgress(job.Status, job.Stage),
		Message: job.Message, Error: job.PublicError,
		MediaID: job.MediaID, MediaTitle: job.MediaTitle, UpgradeMediaID: job.UpgradeMediaID,
		KeepOldVersion:  job.KeepOldVersion,
		CancelRequested: job.CancelRequested,
		Attempt:         job.Attempt, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
		StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
	}
	if s.repos != nil && s.repos.Library != nil {
		if library, err := s.repos.Library.FindByID(ctx, job.LibraryID); err != nil {
			return item, err
		} else if library != nil {
			item.LibraryName = library.Name
			for _, root := range library.Roots {
				if root.ID == job.LibraryRootID {
					item.RootName = root.Name
					break
				}
			}
		}
	}
	if includeCreator {
		item.UserID = job.UserID
		item.PipelineJobID = job.PipelineJobID
		if s.repos != nil && s.repos.User != nil {
			user, err := s.repos.User.FindByID(ctx, job.UserID)
			if err != nil {
				return item, err
			}
			if user != nil {
				item.CreatorUsername = user.Username
			}
		}
	}
	return item, nil
}

func resourceSearchPage(record model.ResourceSearchSession, stored storedResourceSearch, root model.LibraryRoot, in ResourceSearchInput) ResourceSearchResponse {
	page, pageSize := in.Page, in.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > resourceSearchLimit {
		pageSize = resourceSearchLimit
	}
	candidates := filterResourceSearchCandidates(stored.Candidates, in)
	sortResourceSearchCandidates(candidates, in.SortBy)
	total := len(candidates)
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if end > total {
		end = total
	}
	results := make([]ResourceSearchCandidate, 0, end-start)
	for _, candidate := range candidates[start:end] {
		results = append(results, candidate.ResourceSearchCandidate)
	}
	return ResourceSearchResponse{
		SessionID: record.ID, Query: record.Query, Page: page, PageSize: pageSize,
		Total: total, UnfilteredTotal: len(stored.Candidates), TotalPages: totalPages,
		Roots:        []ResourceSearchRoot{{ID: root.ID, Name: root.Name, Path: root.Path, Enabled: root.Enabled}},
		Capabilities: stored.Capabilities, Facets: resourceSearchFacets(stored.Candidates), Results: results,
	}
}

func validateResourceSearchView(in ResourceSearchInput) error {
	if len([]rune(strings.TrimSpace(in.ResultQuery))) > 200 {
		return errors.New("result_query is too long")
	}
	resolution := strings.ToLower(strings.TrimSpace(in.ResolutionFilter))
	if resolution != "" && resolution != "all" && resolution != "2160p" && resolution != "1080p" && resolution != "720p" && resolution != "other" {
		return errors.New("unsupported resolution_filter")
	}
	subtitle := strings.ToLower(strings.TrimSpace(in.SubtitleFilter))
	if subtitle != "" && subtitle != "all" && subtitle != "chinese" && subtitle != "with_subtitle" {
		return errors.New("unsupported subtitle_filter")
	}
	sortBy := strings.ToLower(strings.TrimSpace(in.SortBy))
	if sortBy != "" && sortBy != "relevance" && sortBy != "size_desc" && sortBy != "size_asc" && sortBy != "seeders_desc" && sortBy != "resolution_desc" {
		return errors.New("unsupported sort_by")
	}
	return nil
}

func filterResourceSearchCandidates(candidates []storedResourceCandidate, in ResourceSearchInput) []storedResourceCandidate {
	query := strings.ToLower(strings.TrimSpace(in.ResultQuery))
	source := strings.ToLower(strings.TrimSpace(in.SourceFilter))
	resolution := strings.ToLower(strings.TrimSpace(in.ResolutionFilter))
	subtitle := strings.ToLower(strings.TrimSpace(in.SubtitleFilter))
	out := make([]storedResourceCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if query != "" && !strings.Contains(strings.ToLower(resourceCandidateSearchText(candidate.ResourceSearchCandidate)), query) {
			continue
		}
		if source != "" && source != "all" && !strings.EqualFold(strings.TrimSpace(candidate.Source), source) {
			continue
		}
		if resolution != "" && resolution != "all" && resourceCandidateResolution(candidate.ResourceSearchCandidate) != resolution {
			continue
		}
		if subtitle == "chinese" && !resourceCandidateHasChineseSubtitle(candidate.ResourceSearchCandidate) {
			continue
		}
		if subtitle == "with_subtitle" && !resourceCandidateHasSubtitle(candidate.ResourceSearchCandidate) {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func sortResourceSearchCandidates(candidates []storedResourceCandidate, sortBy string) {
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "size_desc":
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].SizeBytes > candidates[j].SizeBytes })
	case "size_asc":
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].SizeBytes < candidates[j].SizeBytes })
	case "seeders_desc":
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Seeders > candidates[j].Seeders })
	case "resolution_desc":
		sort.SliceStable(candidates, func(i, j int) bool {
			return resourceResolutionScore(resourceCandidateResolution(candidates[i].ResourceSearchCandidate)) >
				resourceResolutionScore(resourceCandidateResolution(candidates[j].ResourceSearchCandidate))
		})
	}
}

func resourceSearchFacets(candidates []storedResourceCandidate) ResourceSearchFacets {
	sources := make(map[string]string)
	resolutions := make(map[string]bool)
	for _, candidate := range candidates {
		if source := strings.TrimSpace(candidate.Source); source != "" {
			key := strings.ToLower(source)
			if _, exists := sources[key]; !exists {
				sources[key] = source
			}
		}
		resolutions[resourceCandidateResolution(candidate.ResourceSearchCandidate)] = true
	}
	sourceValues := make([]string, 0, len(sources))
	for _, source := range sources {
		sourceValues = append(sourceValues, source)
	}
	sort.Slice(sourceValues, func(i, j int) bool { return strings.ToLower(sourceValues[i]) < strings.ToLower(sourceValues[j]) })
	resolutionValues := make([]string, 0, 4)
	for _, value := range []string{"2160p", "1080p", "720p", "other"} {
		if resolutions[value] {
			resolutionValues = append(resolutionValues, value)
		}
	}
	return ResourceSearchFacets{Sources: sourceValues, Resolutions: resolutionValues}
}

func resourceCandidateSearchText(candidate ResourceSearchCandidate) string {
	return strings.Join([]string{
		candidate.Title, candidate.Source, candidate.Resolution, candidate.Subtitle,
		candidate.ResourceType, candidate.Summary, candidate.SizeText,
	}, " ")
}

func resourceCandidateResolution(candidate ResourceSearchCandidate) string {
	text := strings.ToLower(resourceCandidateSearchText(candidate))
	switch {
	case strings.Contains(text, "2160") || strings.Contains(text, "4k") || strings.Contains(text, "uhd"):
		return "2160p"
	case strings.Contains(text, "1080") || strings.Contains(text, "fullhd") || strings.Contains(text, "full hd"):
		return "1080p"
	case strings.Contains(text, "720"):
		return "720p"
	default:
		return "other"
	}
}

func resourceResolutionScore(value string) int {
	switch value {
	case "2160p":
		return 4
	case "1080p":
		return 3
	case "720p":
		return 2
	default:
		return 1
	}
}

func resourceCandidateHasSubtitle(candidate ResourceSearchCandidate) bool {
	if strings.TrimSpace(candidate.Subtitle) != "" {
		return true
	}
	text := strings.ToLower(resourceCandidateSearchText(candidate))
	return strings.Contains(text, "subtitle") || strings.Contains(text, "subbed") || strings.Contains(text, "字幕") || strings.Contains(text, "中字")
}

func resourceCandidateHasChineseSubtitle(candidate ResourceSearchCandidate) bool {
	text := strings.ToLower(resourceCandidateSearchText(candidate))
	for _, marker := range []string{"中文字幕", "中字", "简中", "繁中", "中文", "chinese", "chs", "cht", "zh-cn", "zh-tw"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func normalizeResourceCandidates(items []map[string]any) ([]storedResourceCandidate, error) {
	if len(items) > resourceSearchLimit {
		items = items[:resourceSearchLimit]
	}
	out := make([]storedResourceCandidate, 0, len(items))
	for index, raw := range items {
		title := resourceString(raw, "title", "name")
		candidateID := resourceString(raw, "candidate_id")
		if title == "" || candidateID == "" {
			return nil, errors.New("media-pipeline search returned an invalid candidate")
		}
		out = append(out, storedResourceCandidate{
			ResourceSearchCandidate: ResourceSearchCandidate{
				Index: index, Title: title,
				SizeBytes:    resourceInt64(raw, "size_bytes", "size"),
				SizeText:     resourceString(raw, "size_text", "size_label"),
				Source:       resourceString(raw, "source", "indexer", "source_name"),
				Seeders:      int(resourceInt64(raw, "seeders", "seed")),
				Resolution:   resourceString(raw, "resolution", "quality"),
				Subtitle:     resourceString(raw, "subtitle", "subtitles"),
				ResourceType: resourceString(raw, "resource_type", "type"),
				Summary:      truncateResourceText(resourceString(raw, "summary", "description"), 600),
			},
			CandidateID: candidateID,
		})
	}
	return out, nil
}

func storedResourceSearchValid(stored storedResourceSearch) bool {
	if strings.TrimSpace(stored.PipelineSessionID) == "" {
		return false
	}
	for _, candidate := range stored.Candidates {
		if strings.TrimSpace(candidate.CandidateID) == "" {
			return false
		}
	}
	return true
}

func resourceTargetMetadata(libraryType string) (category, provider, mediaType string) {
	switch strings.ToLower(strings.TrimSpace(libraryType)) {
	case "adult":
		return "adult", "adult", "adult"
	case "anime":
		return "anime", "tmdb", "anime"
	case "tv", "series", "show":
		return "tv", "tmdb", "tv"
	case "movie":
		return "movie", "tmdb", "movie"
	default:
		return "other", "tmdb", "movie"
	}
}

func resourceRootOpenListPath(value string) (string, error) {
	raw := strings.TrimSpace(value)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "cloud" || parsed.Host != "openlist" {
		return "", errors.New("当前仅支持 OpenList 云盘媒体库入库")
	}
	cloudPath := "cloud://openlist" + parsed.EscapedPath()
	openListPath := pipelineCloudPathToOpenListPath(cloudPath)
	if openListPath == "" {
		return "", errors.New("OpenList 媒体库目录无效")
	}
	return openListPath, nil
}

func resourceImportIdempotencyKey(userID, libraryID, rootID, sessionID string, candidateIndex int, subscriptionID string, force bool, upgradeMediaID string, keepOldVersion bool) string {
	raw := strings.Join([]string{
		userID, libraryID, rootID, sessionID, strconv.Itoa(candidateIndex), strings.TrimSpace(subscriptionID), strconv.FormatBool(force),
		strings.TrimSpace(upgradeMediaID), strconv.FormatBool(keepOldVersion),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return "msg-resource-import:" + hex.EncodeToString(sum[:])
}

func mapPipelineImportState(task resourcePipelineTask) (string, string) {
	status := strings.ToLower(strings.TrimSpace(task.Status))
	stage := strings.ToLower(strings.TrimSpace(task.Stage))
	switch status {
	case ResourceImportStatusQueued:
		return ResourceImportStatusQueued, mapPipelineActiveStage(stage)
	case ResourceImportStatusRunning:
		return ResourceImportStatusRunning, mapPipelineActiveStage(stage)
	case ResourceImportStatusCompleted:
		return ResourceImportStatusCompleted, "completed"
	case ResourceImportStatusCompletedWithWarning:
		return ResourceImportStatusCompletedWithWarning, "completed"
	case "cancelled", ResourceImportStatusCanceled:
		return ResourceImportStatusCanceled, "canceled"
	case ResourceImportStatusFailed:
		return ResourceImportStatusFailed, "failed"
	default:
		return "", ""
	}
}

func mapPipelineActiveStage(stage string) string {
	if mapped := mapPipelineImportStage(stage); mapped != "" {
		return mapped
	}
	if strings.TrimSpace(stage) != "" {
		return "running"
	}
	return ""
}

func mapPipelineImportStage(stage string) string {
	switch stage {
	case "queued":
		return "duplicate_check"
	case "starting", "submitted":
		return "submitting"
	case "waiting_download":
		return "transferring"
	case "syncing":
		return "preparing_openlist"
	case "scanning":
		return "scanning"
	case "scraping":
		return "scraping"
	case "subtitles":
		return "matching_subtitle"
	case "removing_old_version":
		return "finalizing_upgrade"
	case "completed", "completed_with_warning":
		return "completed"
	case "failed", "canceled", "cancelled":
		return stage
	default:
		return ""
	}
}

func resourceImportProgress(status, stage string) int {
	if status == ResourceImportStatusCompleted || status == ResourceImportStatusCompletedWithWarning {
		return 100
	}
	switch stage {
	case "duplicate_check":
		return 5
	case "submitting":
		return 15
	case "transferring":
		return 35
	case "preparing_openlist":
		return 55
	case "scanning":
		return 70
	case "scraping":
		return 85
	case "matching_subtitle":
		return 95
	case "finalizing_upgrade":
		return 98
	case "running":
		return 25
	default:
		return 0
	}
}

func resourceImportStatusFinal(status string) bool {
	for _, value := range resourceImportFinalStatuses {
		if status == value {
			return true
		}
	}
	return false
}

func normalizeResourceImportPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}

func resourceString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok && value != nil {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func resourceInt64(values map[string]any, keys ...string) int64 {
	for _, key := range keys {
		value, ok := values[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return int64(typed)
		case int64:
			return typed
		case int:
			return int64(typed)
		case json.Number:
			parsed, _ := typed.Int64()
			return parsed
		default:
			parsed, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(value)), 10, 64)
			return parsed
		}
	}
	return 0
}

func truncateResourceText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func safePipelineMessage(value string) string {
	return truncateResourceText(value, 1000)
}
