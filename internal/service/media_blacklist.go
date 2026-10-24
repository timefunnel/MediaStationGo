package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BlacklistItem struct {
	model.MediaBlacklist
	Media *model.Media `json:"media"`
}

func (e *EmbyService) blacklistTarget(ctx context.Context, userID, mediaID string) (*model.MediaBlacklist, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(mediaID) == "" {
		return nil, errors.New("missing user or media")
	}
	m, err := e.repo.Media.FindByID(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	if m == nil || !UserDefaultMediaVisibility(ctx, e.repo, userID).Allows(m) {
		return nil, gorm.ErrRecordNotFound
	}
	// Reuse the ingest/browse calculator, including virtual series and parts.
	e.repo.Media.PrepareEmbyKeys(m)
	target := &model.MediaBlacklist{UserID: userID, MediaID: m.ID, Scope: "movie", LibraryID: m.LibraryID, TargetID: m.EmbyVersionKey}
	if m.SeriesID != "" || m.SeasonNum > 0 || m.EpisodeNum > 0 {
		target.Scope, target.LibraryID, target.TargetID = "series", "", m.EmbySeriesKey
	}
	if target.TargetID == "" {
		return nil, errors.New("media grouping identity unavailable")
	}
	return target, nil
}

func (e *EmbyService) SetMediaBlacklisted(ctx context.Context, userID, mediaID string, blocked bool) error {
	target, err := e.blacklistTarget(ctx, userID, mediaID)
	if err != nil {
		return err
	}
	if blocked {
		err = e.repo.DB.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "scope"}, {Name: "target_id"}, {Name: "library_id"}},
			DoUpdates: clause.Assignments(map[string]any{"deleted_at": nil, "updated_at": time.Now(), "media_id": mediaID}),
		}).Create(target).Error
	} else {
		err = e.blacklistTargetQuery(ctx, target).Delete(&model.MediaBlacklist{}).Error
	}
	if err == nil {
		e.invalidateBlacklist(ctx, userID)
	}
	return err
}

func (e *EmbyService) blacklistTargetQuery(ctx context.Context, target *model.MediaBlacklist) *gorm.DB {
	return e.repo.DB.WithContext(ctx).Model(&model.MediaBlacklist{}).
		Where("user_id = ? AND scope = ? AND target_id = ? AND library_id = ?", target.UserID, target.Scope, target.TargetID, target.LibraryID)
}

func (e *EmbyService) MediaBlacklisted(ctx context.Context, userID, mediaID string) (bool, error) {
	target, err := e.blacklistTarget(ctx, userID, mediaID)
	if err != nil {
		return false, err
	}
	var count int64
	err = e.blacklistTargetQuery(ctx, target).Count(&count).Error
	return count > 0, err
}

func (e *EmbyService) RemoveBlacklistItem(ctx context.Context, userID, id string) error {
	if err := e.repo.DB.WithContext(ctx).Where("user_id = ? AND id = ?", userID, id).Delete(&model.MediaBlacklist{}).Error; err != nil {
		return err
	}
	e.invalidateBlacklist(ctx, userID)
	return nil
}

func (e *EmbyService) ListMediaBlacklist(ctx context.Context, userID string) ([]BlacklistItem, error) {
	var records []model.MediaBlacklist
	if err := e.repo.DB.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC, id DESC").Find(&records).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.MediaID)
	}
	rows, err := e.repo.Media.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	visibility := UserDefaultMediaVisibility(ctx, e.repo, userID)
	byID := make(map[string]*model.Media, len(rows))
	for i := range rows {
		if visibility.Allows(&rows[i]) {
			byID[rows[i].ID] = &rows[i]
		}
	}
	items := make([]BlacklistItem, 0, len(records))
	for _, record := range records {
		items = append(items, BlacklistItem{MediaBlacklist: record, Media: byID[record.MediaID]})
	}
	return items, nil
}

func (e *EmbyService) invalidateBlacklist(ctx context.Context, userID string) {
	if e.cache != nil {
		e.cache.BumpRevision(ctx, "emby:blacklist:"+userID)
	}
}

func (e *EmbyService) userBlacklistCacheRevision(ctx context.Context, userID string) uint64 {
	if e == nil || e.cache == nil || userID == "" {
		return 0
	}
	return e.cache.Revision(ctx, "emby:blacklist:"+userID)
}

// Anti-joins run before grouping, counting and pagination. The composite index
// supplies account/scope/target lookups without loading blacklists into Go.
func (e *EmbyService) applyMediaBlacklist(q *gorm.DB, userID string) *gorm.DB {
	if strings.TrimSpace(userID) == "" {
		return q
	}
	return e.applySeriesBlacklist(q, userID, "media.emby_series_key").Where(`NOT EXISTS (SELECT 1 FROM media_blacklists b
WHERE b.user_id = ? AND b.scope = 'movie' AND b.target_id = media.emby_version_key
AND b.library_id = media.library_id AND b.deleted_at IS NULL)`, userID)
}

func (e *EmbyService) applySeriesBlacklist(q *gorm.DB, userID, seriesColumn string) *gorm.DB {
	if strings.TrimSpace(userID) == "" {
		return q
	}
	// seriesColumn is supplied only by internal callers, never request input.
	return q.Where(`NOT EXISTS (SELECT 1 FROM media_blacklists b
WHERE b.user_id = ? AND b.scope = 'series' AND b.target_id = `+seriesColumn+`
AND b.library_id = '' AND b.deleted_at IS NULL)`, userID)
}

func (e *EmbyService) visibleMediaByID(ctx context.Context, id, userID string) (*model.Media, error) {
	q := e.repo.DB.WithContext(ctx).Model(&model.Media{}).Where("media.id = ?", id)
	if err := e.ensureEmbyKeys(ctx, q); err != nil {
		return nil, err
	}
	q = e.applyUserMediaVisibility(ctx, q, userID)
	var rows []model.Media
	if err := q.Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}
