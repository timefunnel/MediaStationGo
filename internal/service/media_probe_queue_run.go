package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func (s *MediaProbeQueueService) Boot(ctx context.Context) {
	s.startOnce.Do(func() {
		s.mu.Lock()
		s.lifetime = ctx
		s.mu.Unlock()
		// Recover before accepting execution plans; recovery must not race a
		// newly started run and relabel its active job as interrupted.
		if err := s.recoverInterrupted(ctx); err != nil {
			s.recordCollectError(err)
			return
		}
		go func() {
			s.collectAutomatically(ctx)
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.collectAutomatically(ctx)
				}
			}
		}()
	})
}

func (s *MediaProbeQueueService) recoverInterrupted(ctx context.Context) error {
	return s.repo.DB.WithContext(ctx).Model(&model.MediaProbeJob{}).Where("status = ?", "running").Updates(map[string]any{
		"status": "interrupted", "error": "服务重启中断；确认后可手动重新入队", "finished_at": time.Now(),
	}).Error
}

func (s *MediaProbeQueueService) recordCollectError(err error) {
	s.mu.Lock()
	if err == nil {
		s.collectError = ""
	} else {
		s.collectError = safeMediaProbeQueueError(err)
	}
	s.mu.Unlock()
	if err != nil && s.log != nil {
		s.log.Error("media probe queue collection failed", zap.String("error", safeMediaProbeQueueError(err)))
	}
}

func (s *MediaProbeQueueService) collectAutomatically(ctx context.Context) {
	cfg, err := s.Config(ctx)
	if err == nil && cfg.AutoCollect {
		_, err = s.Collect(ctx)
	}
	s.recordCollectError(err)
}

func (s *MediaProbeQueueService) Start(ctx context.Context, planID string, confirmed bool) error {
	if !confirmed {
		return errors.New("请先确认本轮调用范围和风险")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return errors.New("已有一轮正在运行")
	}
	if s.plan == nil || s.plan.ID != planID || time.Now().After(s.plan.ExpiresAt) {
		return errors.New("执行计划不存在或已过期，请重新预览")
	}
	if s.stream == nil || s.probe == nil {
		return errors.New("文件信息探测服务不可用")
	}
	cfg, err := s.Config(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(cfg, s.plan.Config) {
		return errors.New("配置已变化，请重新预览")
	}
	for _, expected := range s.plan.jobs {
		var current model.MediaProbeJob
		if err := s.repo.DB.WithContext(ctx).First(&current, "media_id = ?", expected.MediaID).Error; err != nil {
			return err
		}
		if current.Status != "pending" || current.Fingerprint != expected.Fingerprint {
			return errors.New("队列已变化，请重新预览")
		}
		var media model.Media
		if err := s.repo.DB.WithContext(ctx).First(&media, "id = ?", expected.MediaID).Error; err != nil {
			return err
		}
		if mediaProbeFingerprint(&media) != expected.Fingerprint {
			return errors.New("媒体文件已变化，请重新刷新队列")
		}
		if len(cfg.LibraryIDs) > 0 {
			included := false
			for _, id := range cfg.LibraryIDs {
				if id == media.LibraryID {
					included = true
					break
				}
			}
			if !included {
				return errors.New("媒体库范围已变化，请重新预览")
			}
		}
	}
	plan := *s.plan
	plan.jobs = append([]model.MediaProbeJob(nil), s.plan.jobs...)
	runCtx, cancel := context.WithTimeout(s.lifetime, time.Duration(cfg.RunSeconds)*time.Second)
	s.running = true
	s.cancel = cancel
	s.plan = nil
	s.message = "正在处理已确认的一轮"
	go s.run(runCtx, plan)
	return nil
}

// Pause cancels the active read as well as further dispatch; canceled reads
// require explicit requeue and are never silently retried on resume/restart.
func (s *MediaProbeQueueService) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.message = "正在停止当前任务"
	}
}

func (s *MediaProbeQueueService) run(ctx context.Context, plan MediaProbeQueuePlan) {
	message := "本轮完成；已暂停，等待下一次确认"
	defer func() {
		s.mu.Lock()
		if s.cancel != nil {
			s.cancel()
		}
		s.cancel = nil
		s.running = false
		s.message = message
		s.mu.Unlock()
	}()
	for index, job := range plan.jobs {
		if index > 0 {
			if err := waitMediaProbeQueue(ctx, time.Duration(plan.Config.IntervalSeconds)*time.Second); err != nil {
				message = "本轮已暂停或达到运行时限"
				return
			}
		}
		if ctx.Err() != nil {
			message = "本轮已暂停或达到运行时限"
			return
		}
		if err := s.runJob(ctx, job, plan.Config); err != nil {
			message = "本轮已停止：" + safeMediaProbeQueueError(err)
			return
		}
	}
}

func waitMediaProbeQueue(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *MediaProbeQueueService) runJob(ctx context.Context, job model.MediaProbeJob, cfg MediaProbeQueueConfig) error {
	// Share the playback queue's reservation. A phone playback probing this
	// same media wins; this queue waits and can then reuse the persisted result.
	for !s.stream.reserveExplicitMediaProbe(job.MediaID) {
		if err := waitMediaProbeQueue(ctx, time.Second); err != nil {
			return err
		}
	}
	backoff := time.Duration(0)
	defer func() { s.stream.finishPlaybackCloudProbe(job.MediaID, backoff) }()
	var media model.Media
	if err := s.repo.DB.WithContext(ctx).First(&media, "id = ?", job.MediaID).Error; err != nil {
		return s.finishJob(job, "failed", err)
	}
	if mediaProbeFingerprint(&media) != job.Fingerprint {
		return errors.New("媒体文件已变化，未进行探测；请刷新队列")
	}
	if !mediaTrackMetadataMissing(&media) {
		return s.finishJob(job, "completed", nil)
	}
	result := s.repo.DB.WithContext(ctx).Model(&model.MediaProbeJob{}).Where("media_id = ? AND fingerprint = ? AND status = ?", job.MediaID, job.Fingerprint, "pending").Updates(map[string]any{
		"status": "running", "attempts": gorm.Expr("attempts + 1"), "started_at": time.Now(), "finished_at": nil, "error": "",
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("任务状态已变化，未进行探测")
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	probe, inspected, err := s.stream.Inspect(probeCtx, job.MediaID, s.probe)
	if err == nil && mediaProbeFingerprint(inspected) != job.Fingerprint {
		err = errors.New("媒体文件在探测前发生变化，结果未写回")
	}
	if err == nil {
		err = s.repo.DB.WithContext(probeCtx).Transaction(func(tx *gorm.DB) error {
			var current model.Media
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", job.MediaID).Error; err != nil {
				return err
			}
			if mediaProbeFingerprint(&current) != job.Fingerprint {
				return errors.New("媒体文件在探测期间发生变化，结果未写回")
			}
			// This queue only collects technical information, without scheduling
			// unrelated artwork downloads outside the confirmed work scope.
			if err := persistMediaProbeResult(probeCtx, repository.New(tx), nil, nil, s.log, &current, probe); err != nil {
				return err
			}
			media = current
			return nil
		})
		if err == nil && s.stream.cache != nil {
			s.stream.cache.DeletePrefix(context.WithoutCancel(ctx), "media:")
		}
	}
	if err != nil {
		backoff = playbackCloudProbeFailureBackoff
		status := "failed"
		if ctx.Err() != nil {
			status = "interrupted"
		}
		return s.finishJob(job, status, err)
	}
	status := "completed"
	if mediaTrackMetadataMissing(&media) {
		status = "partial"
	}
	return s.finishJob(job, status, nil)
}

func (s *StreamService) reserveExplicitMediaProbe(mediaID string) bool {
	s.cloudTrackProbeMu.Lock()
	defer s.cloudTrackProbeMu.Unlock()
	if _, pending := s.cloudTrackProbePending[mediaID]; pending {
		return false
	}
	if s.cloudTrackProbePending == nil {
		s.cloudTrackProbePending = map[string]struct{}{}
	}
	s.cloudTrackProbePending[mediaID] = struct{}{}
	return true
}

func (s *MediaProbeQueueService) finishJob(job model.MediaProbeJob, status string, taskErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	detail := ""
	if taskErr != nil {
		detail = safeMediaProbeQueueError(taskErr)
	}
	result := s.repo.DB.WithContext(ctx).Model(&model.MediaProbeJob{}).Where("media_id = ? AND fingerprint = ?", job.MediaID, job.Fingerprint).Updates(map[string]any{
		"status": status, "error": detail, "finished_at": time.Now(),
	})
	if result.Error != nil {
		return fmt.Errorf("保存探测任务状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("任务已变化，无法保存探测状态")
	}
	return taskErr
}

func (s *MediaProbeQueueService) UpdateJob(ctx context.Context, mediaID, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return errors.New("请先暂停本轮，再操作队列任务")
	}
	var job model.MediaProbeJob
	if err := s.repo.DB.WithContext(ctx).First(&job, "media_id = ?", mediaID).Error; err != nil {
		return err
	}
	updates := map[string]any{}
	switch action {
	case "cancel":
		if job.Status != "pending" {
			return errors.New("只能取消待处理任务")
		}
		updates["status"] = "canceled"
		updates["finished_at"] = time.Now()
	case "retry":
		if job.Status != "failed" && job.Status != "partial" && job.Status != "interrupted" && job.Status != "canceled" {
			return errors.New("当前任务不支持重新入队")
		}
		updates["status"] = "pending"
		updates["error"] = ""
		updates["finished_at"] = nil
		updates["started_at"] = nil
	default:
		return errors.New("无效的队列操作")
	}
	result := s.repo.DB.WithContext(ctx).Model(&model.MediaProbeJob{}).Where("media_id = ? AND fingerprint = ? AND status = ?", mediaID, job.Fingerprint, job.Status).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("任务已变化，请刷新队列后重试操作")
	}
	s.plan = nil
	return nil
}

var mediaProbeURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

func safeMediaProbeQueueError(err error) string {
	text := mediaProbeURLPattern.ReplaceAllString(err.Error(), "[媒体地址已隐藏]")
	runes := []rune(text)
	if len(runes) > 1200 {
		text = string(runes[:1200])
	}
	return text
}
