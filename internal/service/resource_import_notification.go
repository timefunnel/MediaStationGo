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
	if automatic {
		if strings.TrimSpace(job.SubscriptionID) == "" {
			return errors.New("automatic follow completion is missing subscription_id")
		}
		if s.subscriptionCompletionHandler == nil {
			return nil
		}
	} else if s.notify == nil {
		return nil
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

	event := resourceImportCompletedNotification(job)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.notify.BroadcastEvent(ctx, event)
	}()
	return nil
}

func resourceImportCompletedNotification(job model.ResourceImportJob) NotifyEvent {
	title := strings.TrimSpace(job.MediaTitle)
	if title == "" {
		title = strings.TrimSpace(job.CandidateTitle)
	}
	body := fmt.Sprintf("任务：%s\n状态：入库完成。", title)
	if job.Status == ResourceImportStatusCompletedWithWarning {
		body = fmt.Sprintf("任务：%s\n状态：已入库完成，但有警告。\n警告：%s", title, job.PublicError)
	}
	return NotifyEvent{
		Type:    EventLibraryIngest,
		Title:   "MediaStationGo 入库完成",
		Message: body,
		Data:    map[string]interface{}{"title": title, "resource_title": strings.TrimSpace(job.CandidateTitle)},
	}
}
