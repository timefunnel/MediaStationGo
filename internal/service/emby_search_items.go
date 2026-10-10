package service

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

// The SQL page already contains one row per public search result. Only load
// card metadata for its series keys, using the same projections as library
// browsing; never materialize an episode inventory to collapse a search page.
func (e *EmbyService) payloadsForSearchRows(ctx context.Context, rows []model.Media, p ItemsParams) ([]map[string]any, error) {
	seriesIDs := []string{}
	movies := make([]model.Media, 0, len(rows))
	seriesRow := func(row model.Media) bool {
		return row.SeasonNum > 0 || row.EpisodeNum > 0 || strings.TrimSpace(row.PartGroupKey) != ""
	}
	for _, row := range rows {
		if seriesRow(row) {
			seriesIDs = append(seriesIDs, row.EmbyListKey)
		} else {
			movies = append(movies, row)
		}
	}
	movieItems, err := e.payloadsForMediaRows(ctx, movies, p.UserID, !p.OmitMediaSources, false)
	if err != nil {
		return nil, err
	}
	if len(movieItems) != len(movies) {
		return nil, fmt.Errorf("Emby search movie payload count mismatch: got %d, want %d", len(movieItems), len(movies))
	}
	bySeries := make(map[string]map[string]any, len(seriesIDs))
	if len(seriesIDs) > 0 {
		// Search chooses a series when any of its visible episodes matches.
		// Its card counts and metadata describe the complete visible series.
		scope := e.repo.DB.WithContext(ctx).Model(&model.Media{}).Where("emby_list_key IN ?", seriesIDs)
		scope = e.applyUserMediaVisibility(ctx, scope, p.UserID)
		var keys []embySeriesPageKey
		if err := scope.Session(&gorm.Session{}).Select("emby_list_key AS group_key, COUNT(*) AS episode_count").Group("emby_list_key").Scan(&keys).Error; err != nil {
			return nil, err
		}
		items, err := e.seriesCardsSQL(ctx, scope, keys, "emby_list_key", embySeriesAnchorOrder, false, p.UserID)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			id, ok := item["Id"].(string)
			if !ok {
				return nil, fmt.Errorf("Emby search series card has no public identity")
			}
			bySeries[id] = item
		}
	}
	items := make([]map[string]any, 0, len(rows))
	movieIndex := 0
	for _, row := range rows {
		if seriesRow(row) {
			item, ok := bySeries[row.EmbyListKey]
			if !ok {
				return nil, fmt.Errorf("Emby search series %q changed membership during pagination", row.EmbyListKey)
			}
			items = append(items, item)
		} else {
			items = append(items, movieItems[movieIndex])
			movieIndex++
		}
	}
	return items, nil
}
