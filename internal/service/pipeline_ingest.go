package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	PipelineIngestStatusRunning   = "running"
	PipelineIngestStatusCompleted = "completed"
	PipelineIngestStatusFailed    = "failed"
)

type PipelineIngestService struct {
	log         *zap.Logger
	repos       *repository.Container
	scanner     *ScannerService
	maintenance *PipelineMaintenanceService
	tasks       *TaskTrackerService

	mu     sync.Mutex
	jobs   map[string]*PipelineIngestJob
	recent []string
	now    func() time.Time
}

func NewPipelineIngestService(log *zap.Logger, repos *repository.Container, scanner *ScannerService, maintenance *PipelineMaintenanceService, tasks *TaskTrackerService) *PipelineIngestService {
	return &PipelineIngestService{
		log:         log,
		repos:       repos,
		scanner:     scanner,
		maintenance: maintenance,
		tasks:       tasks,
		jobs:        make(map[string]*PipelineIngestJob),
		now:         time.Now,
	}
}

type PipelineIngestRequest struct {
	PipelineMaintenanceTarget
	Title                     string   `json:"title,omitempty"`
	Queries                   []string `json:"queries,omitempty"`
	TargetOpenListPaths       []string `json:"target_openlist_paths,omitempty"`
	RequireTargetPath         bool     `json:"require_target_path,omitempty"`
	PruneDeletedOpenListPaths []string `json:"prune_deleted_openlist_paths,omitempty"`
	Scan                      bool     `json:"scan"`
	RepairMovieExtras         bool     `json:"repair_movie_extras,omitempty"`
	RepairEpisodeVisibility   bool     `json:"repair_episode_visibility,omitempty"`
}

type PipelineIngestJob struct {
	ID         string                `json:"id"`
	Status     string                `json:"status"`
	Stage      string                `json:"stage,omitempty"`
	Message    string                `json:"message,omitempty"`
	Error      string                `json:"error,omitempty"`
	Request    PipelineIngestRequest `json:"request"`
	Result     PipelineIngestResult  `json:"result"`
	StartedAt  time.Time             `json:"started_at"`
	UpdatedAt  time.Time             `json:"updated_at"`
	FinishedAt *time.Time            `json:"finished_at,omitempty"`
}

type PipelineIngestResult struct {
	DeletedMediaPrune *PipelineDeletedMediaPruneResult `json:"deleted_media_prune,omitempty"`
	Scan              *PipelineIngestScanResult        `json:"scan,omitempty"`
	Media             *PipelineIngestMediaResult       `json:"media,omitempty"`
	MovieExtras       *PipelineRepairResult            `json:"movie_extras,omitempty"`
	EpisodeVisibility *PipelineRepairResult            `json:"episode_visibility,omitempty"`
}

type PipelineIngestScanResult struct {
	LibraryID  string `json:"library_id,omitempty"`
	Visited    int    `json:"visited"`
	Added      int    `json:"added"`
	Updated    int    `json:"updated"`
	Skipped    int    `json:"skipped"`
	Probed     int    `json:"probed"`
	Removed    int64  `json:"removed"`
	ErrorCount int    `json:"error_count"`
}

type PipelineIngestMediaResult struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Path      string `json:"path,omitempty"`
	MatchMode string `json:"match_mode,omitempty"`
	MatchPath string `json:"match_path,omitempty"`
}

func (s *PipelineIngestService) Start(ctx context.Context, req PipelineIngestRequest) (PipelineIngestJob, error) {
	if s == nil || s.repos == nil || s.repos.DB == nil {
		return PipelineIngestJob{}, errors.New("pipeline ingest service unavailable")
	}
	if s.maintenance == nil {
		return PipelineIngestJob{}, errors.New("pipeline maintenance service unavailable")
	}
	req.Category = normalizePipelineCategory(req.Category)
	if strings.TrimSpace(req.Category) == "" {
		return PipelineIngestJob{}, errors.New("category is required")
	}
	if strings.TrimSpace(req.LibraryID) == "" || strings.TrimSpace(req.RootID) == "" {
		return PipelineIngestJob{}, errors.New("library_id and root_id are required")
	}
	if strings.TrimSpace(req.RootOpenListPath) == "" {
		return PipelineIngestJob{}, errors.New("root_openlist_path is required")
	}
	req.Queries = pipelineCompactStrings(append(req.Queries, req.Title))
	req.TargetOpenListPaths = pipelineCompactOpenListPaths(req.TargetOpenListPaths)
	req.PruneDeletedOpenListPaths = pipelineCompactOpenListPaths(req.PruneDeletedOpenListPaths)

	now := s.currentTime()
	job := &PipelineIngestJob{
		ID:        uuid.NewString(),
		Status:    PipelineIngestStatusRunning,
		Stage:     "queued",
		Message:   "queued",
		Request:   req,
		StartedAt: now,
		UpdatedAt: now,
	}
	s.storeJob(job)

	go s.run(context.Background(), job.ID)
	return s.Get(job.ID)
}

func (s *PipelineIngestService) Get(id string) (PipelineIngestJob, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return PipelineIngestJob{}, errors.New("job id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok || job == nil {
		return PipelineIngestJob{}, errors.New("pipeline ingest job not found")
	}
	return clonePipelineIngestJob(*job), nil
}

func (s *PipelineIngestService) run(ctx context.Context, id string) {
	job, err := s.Get(id)
	if err != nil {
		return
	}
	var task *TaskHandle
	if s.tasks != nil {
		task = s.tasks.Start(TaskKindScan, "pipeline ingest", TaskUpdate{
			Stage:      "queued",
			SourcePath: job.Request.RootOpenListPath,
			Message:    "pipeline ingest queued",
		})
	}
	finishTask := func(err error, stage, message string, metrics map[string]int64) {
		if task != nil {
			task.Finish(err, TaskUpdate{Stage: stage, Message: message, Metrics: metrics})
		}
	}

	if err := s.runJob(ctx, id, task); err != nil {
		s.failJob(id, err)
		finishTask(err, "failed", "pipeline ingest failed", nil)
		return
	}
	s.completeJob(id)
	finishTask(nil, "completed", "pipeline ingest completed", nil)
}

func (s *PipelineIngestService) runJob(ctx context.Context, id string, task *TaskHandle) error {
	job, err := s.Get(id)
	if err != nil {
		return err
	}
	req := job.Request
	target, err := s.maintenance.resolveTarget(ctx, req.PipelineMaintenanceTarget)
	if err != nil {
		return err
	}
	if len(req.PruneDeletedOpenListPaths) > 0 {
		s.updateJob(id, "prune_deleted", "pruning stale deleted media", nil)
		if task != nil {
			task.Update(TaskUpdate{Stage: "prune_deleted", Message: "pruning stale deleted media"})
		}
		result, err := s.maintenance.PruneDeletedMedia(ctx, req.PipelineMaintenanceTarget, req.PruneDeletedOpenListPaths)
		if err != nil {
			return err
		}
		s.updateJobResult(id, func(resultOut *PipelineIngestResult) {
			resultOut.DeletedMediaPrune = &result
		})
	}
	if req.Scan {
		if s.scanner == nil {
			return errors.New("scanner service unavailable")
		}
		scanMessage := "scanning library root"
		if len(req.TargetOpenListPaths) > 0 {
			scanMessage = "scanning target path"
		}
		s.updateJob(id, "scan", scanMessage, nil)
		if task != nil {
			task.Update(TaskUpdate{Stage: "scan", Message: scanMessage})
		}
		finish, ok := s.scanner.TryBeginLocalScan("pipeline-ingest:" + target.LibraryID + ":" + target.RootID)
		if !ok {
			return errors.New("library root scan already running")
		}
		scanResult, scanErr := s.scanForPipelineIngest(ctx, target, req)
		finish()
		if scanResult != nil {
			summary := pipelineIngestScanSummary(scanResult)
			s.updateJobResult(id, func(resultOut *PipelineIngestResult) {
				resultOut.Scan = &summary
			})
		}
		if scanErr != nil {
			return scanErr
		}
	}

	s.updateJob(id, "find_media", "finding ingested media", nil)
	media, matchMode, matchPath, err := s.findMedia(ctx, target, req)
	if err != nil {
		return err
	}
	mediaResult := PipelineIngestMediaResult{
		ID:        media.ID,
		Title:     pipelineMediaDisplayTitle(media),
		Path:      media.Path,
		MatchMode: matchMode,
		MatchPath: matchPath,
	}
	s.updateJobResult(id, func(resultOut *PipelineIngestResult) {
		resultOut.Media = &mediaResult
	})

	if req.RepairMovieExtras {
		s.updateJob(id, "repair_movie_extras", "repairing movie extras", nil)
		result, err := s.maintenance.RepairMovieExtras(ctx, media.ID, req.PipelineMaintenanceTarget)
		if err != nil {
			return err
		}
		s.updateJobResult(id, func(resultOut *PipelineIngestResult) {
			resultOut.MovieExtras = &result
		})
	}
	if req.RepairEpisodeVisibility {
		s.updateJob(id, "repair_episode_visibility", "repairing episode visibility", nil)
		result, err := s.maintenance.RepairEpisodeVisibility(ctx, media.ID, req.PipelineMaintenanceTarget)
		if err != nil {
			return err
		}
		s.updateJobResult(id, func(resultOut *PipelineIngestResult) {
			resultOut.EpisodeVisibility = &result
		})
	}
	return nil
}

func (s *PipelineIngestService) scanForPipelineIngest(ctx context.Context, target pipelineResolvedTarget, req PipelineIngestRequest) (*ScanResult, error) {
	if len(req.TargetOpenListPaths) > 0 {
		res, handled, err := s.scanner.ScanLibraryRootOpenListTargets(ctx, target.LibraryID, target.RootID, req.TargetOpenListPaths)
		if handled || err != nil {
			return res, err
		}
	}
	return s.scanner.ScanLibraryRoot(ctx, target.LibraryID, target.RootID)
}

func (s *PipelineIngestService) findMedia(ctx context.Context, target pipelineResolvedTarget, req PipelineIngestRequest) (model.Media, string, string, error) {
	if len(req.TargetOpenListPaths) > 0 {
		rows, err := s.findMediaByOpenListPaths(ctx, target, req.TargetOpenListPaths)
		if err != nil {
			return model.Media{}, "", "", err
		}
		if len(rows) > 0 {
			row := choosePipelineIngestMedia(rows, target.RootOpenListPath)
			return row, "path", pipelineMatchedOpenListPath(row.Path, req.TargetOpenListPaths), nil
		}
		if req.RequireTargetPath {
			return model.Media{}, "", "", errors.New("MediaStationGo media not found after root scan")
		}
	}
	for _, query := range req.Queries {
		rows, err := s.findMediaByQuery(ctx, target, query)
		if err != nil {
			return model.Media{}, "", "", err
		}
		if len(rows) > 0 {
			row := choosePipelineIngestMedia(rows, target.RootOpenListPath)
			return row, "query", "", nil
		}
	}
	return model.Media{}, "", "", errors.New("MediaStationGo media not found after root scan")
}

func (s *PipelineIngestService) findMediaByOpenListPaths(ctx context.Context, target pipelineResolvedTarget, openListPaths []string) ([]model.Media, error) {
	cloudPaths := pipelineCompactCloudPaths(openListPaths)
	if len(cloudPaths) == 0 {
		return nil, nil
	}
	query := s.repos.DB.WithContext(ctx).Model(&model.Media{}).
		Where("library_id = ? AND library_root_id = ?", target.LibraryID, target.RootID)
	parts := make([]string, 0, len(cloudPaths))
	args := make([]any, 0, len(cloudPaths)*2)
	for _, cloudPath := range cloudPaths {
		parts = append(parts, "(path = ? OR path LIKE ?)")
		args = append(args, cloudPath, strings.TrimRight(cloudPath, "/")+"/%")
	}
	var rows []model.Media
	err := query.Where("("+strings.Join(parts, " OR ")+")", args...).
		Order("updated_at DESC, created_at DESC").
		Limit(200).
		Find(&rows).Error
	return rows, err
}

func (s *PipelineIngestService) findMediaByQuery(ctx context.Context, target pipelineResolvedTarget, query string) ([]model.Media, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	like := "%" + strings.ToLower(query) + "%"
	var rows []model.Media
	err := s.repos.DB.WithContext(ctx).Model(&model.Media{}).
		Where("library_id = ? AND library_root_id = ?", target.LibraryID, target.RootID).
		Where("(LOWER(COALESCE(title, '')) LIKE ? OR LOWER(COALESCE(original_name, '')) LIKE ? OR LOWER(COALESCE(path, '')) LIKE ? OR LOWER(COALESCE(relative_path, '')) LIKE ?)", like, like, like, like).
		Order("updated_at DESC, created_at DESC").
		Limit(50).
		Find(&rows).Error
	return rows, err
}

func choosePipelineIngestMedia(rows []model.Media, rootOpenListPath string) model.Media {
	if len(rows) == 0 {
		return model.Media{}
	}
	candidates := append([]model.Media(nil), rows...)
	sort.SliceStable(candidates, func(i, j int) bool {
		iExtra := pipelineMovieMediaRowLooksLikeExtra(candidates[i], rootOpenListPath)
		jExtra := pipelineMovieMediaRowLooksLikeExtra(candidates[j], rootOpenListPath)
		if iExtra != jExtra {
			return !iExtra
		}
		if candidates[i].SizeBytes != candidates[j].SizeBytes {
			return candidates[i].SizeBytes > candidates[j].SizeBytes
		}
		if !candidates[i].UpdatedAt.Equal(candidates[j].UpdatedAt) {
			return candidates[i].UpdatedAt.After(candidates[j].UpdatedAt)
		}
		return candidates[i].ID > candidates[j].ID
	})
	return candidates[0]
}

func (s *PipelineIngestService) storeJob(job *PipelineIngestJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = clonePipelineIngestJob(*job).ptr()
	s.recent = append([]string{job.ID}, s.recent...)
	if len(s.recent) > 100 {
		for _, oldID := range s.recent[100:] {
			delete(s.jobs, oldID)
		}
		s.recent = s.recent[:100]
	}
}

func (s *PipelineIngestService) updateJob(id, stage, message string, mutate func(*PipelineIngestJob)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job := s.jobs[id]; job != nil {
		if stage != "" {
			job.Stage = stage
		}
		if message != "" {
			job.Message = message
		}
		job.UpdatedAt = s.currentTime()
		if mutate != nil {
			mutate(job)
		}
	}
}

func (s *PipelineIngestService) updateJobResult(id string, mutate func(*PipelineIngestResult)) {
	s.updateJob(id, "", "", func(job *PipelineIngestJob) {
		mutate(&job.Result)
	})
}

func (s *PipelineIngestService) completeJob(id string) {
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	if job := s.jobs[id]; job != nil {
		job.Status = PipelineIngestStatusCompleted
		job.Stage = "completed"
		job.Message = "completed"
		job.UpdatedAt = now
		job.FinishedAt = &now
	}
}

func (s *PipelineIngestService) failJob(id string, err error) {
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	if job := s.jobs[id]; job != nil {
		job.Status = PipelineIngestStatusFailed
		job.Stage = "failed"
		job.Message = "failed"
		job.Error = err.Error()
		job.UpdatedAt = now
		job.FinishedAt = &now
	}
}

func (s *PipelineIngestService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func clonePipelineIngestJob(job PipelineIngestJob) PipelineIngestJob {
	job.Request.Queries = append([]string(nil), job.Request.Queries...)
	job.Request.TargetOpenListPaths = append([]string(nil), job.Request.TargetOpenListPaths...)
	job.Request.PruneDeletedOpenListPaths = append([]string(nil), job.Request.PruneDeletedOpenListPaths...)
	return job
}

func (job PipelineIngestJob) ptr() *PipelineIngestJob {
	out := job
	return &out
}

func pipelineIngestScanSummary(res *ScanResult) PipelineIngestScanResult {
	if res == nil {
		return PipelineIngestScanResult{}
	}
	return PipelineIngestScanResult{
		LibraryID:  res.LibraryID,
		Visited:    res.Visited,
		Added:      res.Added,
		Updated:    res.Updated,
		Skipped:    res.Skipped,
		Probed:     res.Probed,
		Removed:    res.Removed,
		ErrorCount: res.ErrorCount,
	}
}

func pipelineCompactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func pipelineCompactOpenListPaths(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = pipelineNormalizeOpenListPath(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func pipelineCompactCloudPaths(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		cloudPath := pipelineOpenListPathToCloudPath(value)
		if cloudPath == pipelineOpenListCloudPrefix || seen[cloudPath] {
			continue
		}
		seen[cloudPath] = true
		out = append(out, cloudPath)
	}
	return out
}

func pipelineMatchedOpenListPath(mediaPath string, candidates []string) string {
	openListPath := pipelineCloudPathToOpenListPath(mediaPath)
	for _, candidate := range candidates {
		if pipelinePathIsSameOrChild(openListPath, candidate) {
			return candidate
		}
	}
	return ""
}

func pipelineMediaDisplayTitle(media model.Media) string {
	for _, value := range []string{media.Title, media.OriginalName, media.EpisodeTitle, media.Path} {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return media.ID
}
