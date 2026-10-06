package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

var ErrLibraryCoverSelection = errors.New("invalid library cover selection")

type LibraryCoverItem struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	PosterURL      string    `json:"poster_url"`
	UpdatedAt      time.Time `json:"updated_at"`
	SelectionError string    `json:"selection_error,omitempty"`
}

// Only persist media references; artwork is resolved from the existing media
// and series metadata every time, including the user's chosen order.
func (e *EmbyService) LibraryCoverSelection(ctx context.Context, libraryID string, ids []string) ([]EmbyFolderCoverArtwork, []LibraryCoverItem, error) {
	if len(ids) > 4 {
		return nil, nil, fmt.Errorf("%w: 最多选择 4 部作品", ErrLibraryCoverSelection)
	}
	lib, err := e.repo.Library.FindByID(ctx, libraryID)
	if err != nil {
		return nil, nil, err
	}
	if lib == nil {
		return nil, nil, fmt.Errorf("%w: 媒体库不存在", ErrLibraryCoverSelection)
	}
	libraryIDs, err := MergedLibraryIDsForLibrary(ctx, e.repo, lib.ID)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]model.Media, 0, len(ids))
	seenIDs, seenWorks, seenURLs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, id := range ids {
		if id == "" || seenIDs[id] {
			return nil, nil, fmt.Errorf("%w: 作品 ID 为空或重复", ErrLibraryCoverSelection)
		}
		seenIDs[id] = true
		var m model.Media
		result := e.repo.DB.WithContext(ctx).Where("id = ? AND library_id IN ?", id, libraryIDs).Limit(1).Find(&m)
		if result.Error != nil {
			return nil, nil, result.Error
		}
		if result.RowsAffected != 1 {
			return nil, nil, fmt.Errorf("%w: 所选作品已删除或不属于该媒体库", ErrLibraryCoverSelection)
		}
		rows = append(rows, m)
	}
	artworks, items, err := e.libraryCoverItems(ctx, lib.Type, rows)
	if err != nil {
		return nil, nil, err
	}
	for i, art := range artworks {
		if items[i].SelectionError != "" {
			return nil, nil, fmt.Errorf("%w: %s", ErrLibraryCoverSelection, items[i].SelectionError)
		}
		if seenWorks[art.MediaID] || seenURLs[art.URL] {
			return nil, nil, fmt.Errorf("%w: 请选择不同作品及不同海报", ErrLibraryCoverSelection)
		}
		seenWorks[art.MediaID], seenURLs[art.URL] = true, true
	}
	return artworks, items, nil
}

// Candidates reuse the selection resolver, so a series card shows the exact
// canonical poster used by the renderer rather than an episode still.
func (e *EmbyService) LibraryCoverCandidates(ctx context.Context, libraryType string, rows []model.Media) ([]LibraryCoverItem, error) {
	_, items, err := e.libraryCoverItems(ctx, libraryType, rows)
	return items, err
}

func (e *EmbyService) libraryCoverItems(ctx context.Context, libraryType string, rows []model.Media) ([]EmbyFolderCoverArtwork, []LibraryCoverItem, error) {
	seriesByMedia := make(map[string]*embySeriesGroup)
	if embyLibraryTypeIsEpisodic(libraryType) && len(rows) > 0 {
		groups, err := e.seriesGroupsFromMedia(ctx, rows)
		if err != nil {
			return nil, nil, err
		}
		for i := range groups {
			for _, episode := range groups[i].Episodes {
				seriesByMedia[episode.ID] = &groups[i]
			}
		}
	}
	artworks := make([]EmbyFolderCoverArtwork, 0, len(rows))
	items := make([]LibraryCoverItem, 0, len(rows))
	for _, m := range rows {
		item := LibraryCoverItem{ID: m.ID, Title: m.Title, UpdatedAt: m.UpdatedAt}
		if m.DisplayTitle != "" {
			item.Title = m.DisplayTitle
		}
		art, ok := folderCoverArtworkForMedia(&m, "Primary")
		if embyLibraryTypeIsEpisodic(libraryType) {
			group, found := seriesByMedia[m.ID]
			if !found {
				return nil, nil, fmt.Errorf("resolve library cover candidate %q: series group missing", m.ID)
			}
			art, ok = folderCoverArtworkForSeries(group, "Primary")
			item.Title = group.Name
			if group.ArtworkUpdatedAt.After(item.UpdatedAt) {
				item.UpdatedAt = group.ArtworkUpdatedAt
			}
		}
		if ok {
			item.PosterURL = art.URL
		} else {
			item.SelectionError = fmt.Sprintf("「%s」没有作品海报，请先补全海报", item.Title)
		}
		artworks = append(artworks, art)
		items = append(items, item)
	}
	return artworks, items, nil
}

func (e *EmbyService) SaveLibraryCoverSelection(ctx context.Context, libraryID string, ids []string) error {
	if _, _, err := e.LibraryCoverSelection(ctx, libraryID, ids); err != nil {
		return err
	}
	if ids == nil {
		ids = []string{}
	}
	body, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	result := e.repo.DB.WithContext(ctx).Model(&model.Library{}).Where("id = ?", libraryID).Update("cover_media_ids", string(body))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: 媒体库不存在", ErrLibraryCoverSelection)
	}
	return nil
}
