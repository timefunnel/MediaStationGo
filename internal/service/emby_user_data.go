package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

// SetFavorite 把 mediaID 标为 userID 的收藏。
func (e *EmbyService) SetFavorite(ctx context.Context, userID, mediaID string, favorite bool) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(mediaID) == "" {
		return errors.New("missing user or media")
	}
	if _, found, err := e.FavoriteUserData(ctx, userID, mediaID); err != nil {
		return err
	} else if !found {
		return gorm.ErrRecordNotFound
	}
	var err error
	if favorite {
		// The unique key also covers soft-deleted rows. Restore that same row
		// when favoriting again instead of creating a conflicting second row.
		err = e.repo.DB.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "media_id"}},
			DoUpdates: clause.Assignments(map[string]any{"deleted_at": nil, "updated_at": time.Now()}),
		}).Create(&model.Favorite{UserID: userID, MediaID: mediaID}).Error
	} else {
		err = e.repo.DB.WithContext(ctx).
			Where("user_id = ? AND media_id = ?", userID, mediaID).
			Delete(&model.Favorite{}).Error
	}
	if err != nil {
		return err
	}
	if e.cache != nil {
		e.cache.BumpRevision(ctx, embyFavoriteCacheDomain(userID))
	}
	return nil
}

// FavoriteUserData supports the same physical and aggregate item identities
// as Item, without loading playback sources to answer a favorite action.
func (e *EmbyService) FavoriteUserData(ctx context.Context, userID, itemID string) (map[string]any, bool, error) {
	userData, found, err := e.MediaUserData(ctx, userID, itemID)
	if err != nil || found {
		return userData, found, err
	}
	group, found, err := e.findSeriesGroup(ctx, itemID, userID)
	if err != nil || !found {
		return nil, false, err
	}
	item, err := e.seriesItemWithFavorites(ctx, group, userID)
	if err != nil {
		return nil, false, err
	}
	return item["UserData"].(map[string]any), true, nil
}

// Read favorites by public item ID in one query. Aggregate series keep their
// own favorite row; their episodes have independent user state.
func (e *EmbyService) attachItemFavorites(ctx context.Context, userID string, items []map[string]any) error {
	if strings.TrimSpace(userID) == "" || len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item["Id"].(string))
	}
	var favorites []model.Favorite
	if err := e.repo.DB.WithContext(ctx).Select("media_id").Where("user_id = ? AND media_id IN ?", userID, ids).Find(&favorites).Error; err != nil {
		return err
	}
	byID := make(map[string]bool, len(favorites))
	for _, favorite := range favorites {
		byID[favorite.MediaID] = true
	}
	for _, item := range items {
		item["UserData"].(map[string]any)["IsFavorite"] = byID[item["Id"].(string)]
	}
	return nil
}

// MarkPlayed 把 mediaID 标为已看（写一个 100% 进度的 history 行）。
func (e *EmbyService) MarkPlayed(ctx context.Context, userID, mediaID string, played bool) error {
	if !played {
		return e.repo.DB.WithContext(ctx).
			Where("user_id = ? AND media_id = ?", userID, mediaID).
			Delete(&model.PlaybackHistory{}).Error
	}
	m, err := e.repo.Media.FindByID(ctx, mediaID)
	if err != nil || m == nil {
		return errors.New("media not found")
	}
	dur := int64(m.DurationSec) * 1000
	if dur <= 0 {
		dur = 1
	}
	return e.repo.History.Upsert(ctx, &model.PlaybackHistory{
		UserID:     userID,
		MediaID:    mediaID,
		PositionMs: dur,
		DurationMs: dur,
		WatchedAt:  time.Now(),
		Completed:  true,
	})
}

// RecordProgress 记录播放进度（来自 Emby 客户端的 /Sessions/Playing/Progress）。
func (e *EmbyService) RecordProgress(ctx context.Context, userID, mediaID string, positionTicks, runtimeTicks int64) error {
	return e.RecordProgressForMediaSource(ctx, userID, mediaID, "", positionTicks, runtimeTicks)
}

// RecordProgressForMediaSource records progress against the logical item while
// validating cloud playback against the physical version the client selected.
// Emby clients keep ItemId stable across versions and send the played version
// separately as MediaSourceId; persisting the source ID would fragment Resume.
func (e *EmbyService) RecordProgressForMediaSource(
	ctx context.Context,
	userID string,
	mediaID string,
	mediaSourceID string,
	positionTicks int64,
	runtimeTicks int64,
) error {
	resolvedMediaID := mediaID
	if strings.TrimSpace(mediaSourceID) != "" {
		var err error
		resolvedMediaID, err = e.ResolveMediaSourceID(ctx, mediaID, userID, mediaSourceID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(resolvedMediaID) == "" {
			return ErrEmbyMediaSourceUnavailable
		}
	}
	if e.playback != nil {
		if err := e.playback.ValidateProgressWrite(ctx, userID, resolvedMediaID); err != nil {
			return err
		}
	}
	pos := positionTicks / 10_000
	dur := runtimeTicks / 10_000
	if dur <= 0 {
		// runtimeTicks 缺失时使用实际播放版本的 DurationSec。
		if m, _ := e.repo.Media.FindByID(ctx, resolvedMediaID); m != nil {
			dur = int64(m.DurationSec) * 1000
		}
	}
	completed := dur > 0 && pos >= dur*9/10
	if err := e.repo.History.Upsert(ctx, &model.PlaybackHistory{
		UserID:     userID,
		MediaID:    mediaID,
		PositionMs: pos,
		DurationMs: dur,
		WatchedAt:  time.Now(),
		Completed:  completed,
	}); err != nil {
		return err
	}
	// 标准行为：被移出继续观看的条目再次观看时自动恢复。
	return e.repo.MediaPlaybackPreference.ClearHiddenFromResume(ctx, userID, mediaID)
}

// SetHiddenFromResume 按 Emby Hide 查询参数更新该用户的“移出继续观看”状态。
// Hide=false 必须撤销隐藏，不能和 Hide=true 一样写成隐藏。
func (e *EmbyService) SetHiddenFromResume(ctx context.Context, userID, mediaID string, hidden bool) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(mediaID) == "" {
		return errors.New("missing user or media")
	}
	if !hidden {
		return e.repo.MediaPlaybackPreference.ClearHiddenFromResume(ctx, userID, mediaID)
	}
	return e.repo.MediaPlaybackPreference.SetHiddenFromResume(ctx, userID, mediaID, true)
}

// MediaUserData returns the small Emby user-state payload used by action
// routes. It deliberately avoids constructing a complete Item (artwork,
// people, media sources and library hierarchy are irrelevant to the reply).
func (e *EmbyService) MediaUserData(ctx context.Context, userID, mediaID string) (map[string]any, bool, error) {
	var media model.Media
	mediaQuery := e.repo.DB.WithContext(ctx).Model(&model.Media{})
	mediaQuery = e.applyUserMediaVisibility(ctx, mediaQuery, userID)
	if err := mediaQuery.
		Select("id", "duration_sec").
		Where("id = ?", mediaID).
		First(&media).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}

	favorite := false
	positionMs := int64(0)
	watchedAt := time.Time{}
	if strings.TrimSpace(userID) != "" {
		var favoriteCount int64
		if err := e.repo.DB.WithContext(ctx).Model(&model.Favorite{}).
			Where("user_id = ? AND media_id = ?", userID, mediaID).
			Count(&favoriteCount).Error; err != nil {
			return nil, false, err
		}
		favorite = favoriteCount > 0

		var history model.PlaybackHistory
		err := e.repo.DB.WithContext(ctx).
			Select("position_ms", "watched_at").
			Where("user_id = ? AND media_id = ?", userID, mediaID).
			Order("watched_at DESC, updated_at DESC, id DESC").
			First(&history).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, err
		}
		if err == nil {
			positionMs = history.PositionMs
			watchedAt = history.WatchedAt
		}
	}

	return embyUserDataPayload(favorite, positionMs, int64(media.DurationSec)*1000, watchedAt), true, nil
}

func embyUserDataPayload(favorite bool, positionMs, durationMs int64, watchedAt time.Time) map[string]any {
	played := positionMs > 0 && durationMs > 0 && positionMs >= durationMs*9/10
	percentage := 0.0
	if durationMs > 0 {
		percentage = float64(positionMs) / float64(durationMs) * 100
	}
	userData := map[string]any{
		"PlaybackPositionTicks": positionMs * 10_000,
		"PlayCount":             0,
		"IsFavorite":            favorite,
		"Played":                played,
		"PlayedPercentage":      percentage,
	}
	if !watchedAt.IsZero() {
		userData["LastPlayedDate"] = watchedAt.UTC().Format(time.RFC3339Nano)
	}
	return userData
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func intToStr(v int) string {
	if v == 0 {
		return ""
	}
	return strconv.Itoa(v)
}
