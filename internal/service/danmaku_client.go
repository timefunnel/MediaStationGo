package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrDanmakuConflict = errors.New("danmaku association changed on another device")

type ClientDanmakuAssociation struct {
	Source        string  `json:"source"`
	EpisodeURL    string  `json:"episode_url"`
	AnimeTitle    string  `json:"anime_title"`
	EpisodeTitle  string  `json:"episode_title"`
	OffsetSeconds float64 `json:"offset_seconds"`
	Revision      string  `json:"revision"`
}

type ClientDanmakuContext struct {
	MediaID     string                    `json:"media_id"`
	Target      DanmakuTarget             `json:"target"`
	Association *ClientDanmakuAssociation `json:"association"`
	Revision    string                    `json:"revision"`
}

type ClientDanmakuUpdate struct {
	ClientDanmakuAssociation
	ExpectedRevision string `json:"expected_revision"`
}

var sourceEpisodePatterns = map[string]*regexp.Regexp{
	"tencent": regexp.MustCompile(`^/x/cover/[a-z0-9]+/[a-z0-9]+\.html$`),
	"iqiyi":   regexp.MustCompile(`^/v_[a-z0-9]+\.html$`),
	"youku":   regexp.MustCompile(`^/v_show/id_[A-Za-z0-9=_-]+\.html$`),
}

func validateClientDanmaku(in ClientDanmakuUpdate) error {
	hosts := map[string]string{"tencent": "v.qq.com", "iqiyi": "www.iqiyi.com", "youku": "v.youku.com"}
	pattern, ok := sourceEpisodePatterns[in.Source]
	u, err := url.Parse(in.EpisodeURL)
	if !ok || err != nil || strings.ContainsAny(in.EpisodeURL, "?#") || len(in.EpisodeURL) > 2048 || u.Scheme != "https" || u.Host != hosts[in.Source] ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || !pattern.MatchString(u.Path) {
		return fmt.Errorf("%w: invalid source episode URL", ErrDanmakuInvalidInput)
	}
	if len(in.AnimeTitle) > 255 || len(in.EpisodeTitle) > 255 {
		return fmt.Errorf("%w: danmaku title is too long", ErrDanmakuInvalidInput)
	}
	return validateDanmakuOffset(in.OffsetSeconds)
}

func (s *DanmakuService) ClientContext(ctx context.Context, mediaID, userID string) (ClientDanmakuContext, error) {
	if mediaID == "" || userID == "" {
		return ClientDanmakuContext{}, ErrDanmakuInvalidInput
	}
	var media model.Media
	if err := s.repos.DB.WithContext(ctx).First(&media, "id = ?", mediaID).Error; err != nil {
		return ClientDanmakuContext{}, err
	}
	row, err := s.repos.MediaPlaybackPreference.FindByUserAndMedia(ctx, userID, mediaID)
	if err != nil {
		return ClientDanmakuContext{}, err
	}
	out := ClientDanmakuContext{MediaID: mediaID, Target: danmakuTargetFromMedia(media), Revision: "0"}
	if row == nil {
		return out, nil
	}
	out.Revision = strconv.FormatUint(row.DanmakuRevision, 10)
	if row.DanmakuEpisodeURL != "" {
		out.Association = &ClientDanmakuAssociation{Source: row.DanmakuSource, EpisodeURL: row.DanmakuEpisodeURL,
			AnimeTitle: row.DanmakuAnimeTitle, EpisodeTitle: row.DanmakuEpisodeTitle,
			OffsetSeconds: row.DanmakuOffsetSeconds, Revision: out.Revision}
	}
	return out, nil
}

// SaveClientAssociation only stores the selected reference; it performs no network requests.
// Compare-and-swap keeps an automatic match from overwriting another device's correction.
func (s *DanmakuService) SaveClientAssociation(ctx context.Context, mediaID, userID string, in ClientDanmakuUpdate) (ClientDanmakuContext, error) {
	if mediaID == "" || userID == "" {
		return ClientDanmakuContext{}, ErrDanmakuInvalidInput
	}
	if err := validateClientDanmaku(in); err != nil {
		return ClientDanmakuContext{}, err
	}
	expected, err := strconv.ParseUint(in.ExpectedRevision, 10, 64)
	// Stored in a signed database bigint; reserve room for the increment.
	if err != nil || expected >= 1<<63-1 {
		return ClientDanmakuContext{}, ErrDanmakuInvalidInput
	}
	unlock := s.lockAutoMatch(userID + ":" + mediaID)
	defer unlock()
	err = s.repos.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var media model.Media
		if err := tx.First(&media, "id = ?", mediaID).Error; err != nil {
			return err
		}
		var row model.UserMediaPlaybackPreference
		err := tx.Where("user_id = ? AND media_id = ?", userID, mediaID).First(&row).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		newRow := errors.Is(err, gorm.ErrRecordNotFound)
		if newRow {
			if expected != 0 {
				return ErrDanmakuConflict
			}
			row.MediaID, row.UserID, row.SubtitleEnabled = mediaID, userID, true
		} else {
			if expected != row.DanmakuRevision {
				return ErrDanmakuConflict
			}
		}
		row.DanmakuSource, row.DanmakuEpisodeURL = in.Source, in.EpisodeURL
		row.DanmakuAnimeTitle, row.DanmakuEpisodeTitle = in.AnimeTitle, in.EpisodeTitle
		row.DanmakuOffsetSeconds, row.DanmakuRevision = in.OffsetSeconds, expected+1
		var result *gorm.DB
		if newRow {
			result = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		} else {
			result = tx.Model(&model.UserMediaPlaybackPreference{}).
				Where("user_id = ? AND media_id = ? AND danmaku_revision = ?", userID, mediaID, expected).
				Updates(map[string]any{"danmaku_source": in.Source, "danmaku_episode_url": in.EpisodeURL,
					"danmaku_anime_title": in.AnimeTitle, "danmaku_episode_title": in.EpisodeTitle,
					"danmaku_offset_seconds": in.OffsetSeconds, "danmaku_revision": expected + 1})
		}
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrDanmakuConflict
		}
		return nil
	})
	if err != nil {
		return ClientDanmakuContext{}, err
	}
	return s.ClientContext(ctx, mediaID, userID)
}
