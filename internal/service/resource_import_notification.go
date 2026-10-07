package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

func (s *ResourceImportService) SetNotifyChannels(notify *NotifyChannelService) {
	if s != nil {
		s.notify = notify
	}
}

func (s *ResourceImportService) notifyImportCompleted(ctx context.Context, job model.ResourceImportJob) error {
	if strings.TrimSpace(job.MediaID) == "" {
		// A manual replenishment can complete without importing any new episode.
		return nil
	}
	automatic := job.SubscriptionFollow && !job.ManualReplenish
	var event NotifyEvent
	if automatic {
		if strings.TrimSpace(job.SubscriptionID) == "" {
			return errors.New("automatic follow completion is missing subscription_id")
		}
		if s.subscriptionCompletionHandler == nil {
			return nil
		}
	} else if s.notify == nil {
		return nil
	} else {
		if strings.TrimSpace(job.CandidateTitle) == "" {
			return errors.New("resource import completion notification is missing candidate_title")
		}
		user, err := s.repos.User.FindByID(ctx, job.UserID)
		if err != nil {
			return fmt.Errorf("load resource import notification creator: %w", err)
		}
		if user == nil || strings.TrimSpace(user.Username) == "" {
			return errors.New("resource import completion notification creator is missing")
		}
		event = resourceImportCompletedNotification(job, user.Username)
	}

	// All completion notifications share one durable claim on the parent job.
	// Stale polls, restarts and enhancement retries must not queue another event.
	now := time.Now()
	claim := s.repos.DB.WithContext(ctx).Model(&model.ResourceImportJob{}).
		Where("id = ? AND status IN ? AND completion_notification_queued_at IS NULL", job.ID,
			[]string{ResourceImportStatusCompleted, ResourceImportStatusCompletedWithWarning}).
		Update("completion_notification_queued_at", now)
	if claim.Error != nil {
		return claim.Error
	}
	if claim.RowsAffected == 0 {
		return nil
	}
	if automatic {
		if err := s.subscriptionCompletionHandler(ctx, job); err != nil {
			resetErr := s.repos.DB.WithContext(ctx).Model(&model.ResourceImportJob{}).
				Where("id = ? AND completion_notification_queued_at = ?", job.ID, now).
				Update("completion_notification_queued_at", nil).Error
			return errors.Join(err, resetErr)
		}
		return nil
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.notify.BroadcastEvent(ctx, event)
	}()
	return nil
}

func resourceImportCompletedNotification(job model.ResourceImportJob, creatorUsername string) NotifyEvent {
	// Use the selected resource title shown in the import task list. MediaTitle
	// is the scanner's result and can lose release codes, separators or suffixes.
	title := strings.TrimSpace(job.CandidateTitle)
	body := fmt.Sprintf("任务：%s\n状态：入库完成。", title)
	if job.Status == ResourceImportStatusCompletedWithWarning {
		body = fmt.Sprintf("任务：%s\n状态：已入库完成，但有警告。\n警告：%s", title, job.PublicError)
	}
	return NotifyEvent{
		Type:    EventLibraryIngest,
		Title:   fmt.Sprintf("%s-入库-%s", strings.TrimSpace(creatorUsername), title),
		Message: body,
		Data:    map[string]interface{}{"title": title, "resource_title": strings.TrimSpace(job.CandidateTitle)},
	}
}
