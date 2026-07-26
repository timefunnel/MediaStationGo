package service

import (
	"context"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

// SearchMedia performs a simple LIKE search across titles.
func (s *MediaService) SearchMedia(ctx context.Context, query string, limit int) ([]model.Media, error) {
	return s.SearchMediaVisible(ctx, query, limit, MediaVisibility{IncludeNSFW: true})
}

func (s *MediaService) SearchMediaVisible(ctx context.Context, query string, limit int, visibility MediaVisibility) ([]model.Media, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > maxMediaSearchLimit {
		limit = maxMediaSearchLimit
	}
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	items, err := s.repo.Media.SearchFiltered(ctx, query, limit, repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
	})
	if err != nil {
		return nil, err
	}
	s.attachLibraryMetadata(ctx, items)
	return items, nil
}

func (s *MediaService) SearchMediaVisibleGrouped(ctx context.Context, query string, limit int, visibility MediaVisibility) ([]MediaItem, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > maxMediaSearchLimit {
		limit = maxMediaSearchLimit
	}
	items, err := s.SearchMediaVisible(ctx, query, maxMediaSearchLimit, visibility)
	if err != nil {
		return nil, err
	}
	return firstMediaItems(groupMediaVersions(items), limit), nil
}

func (s *MediaService) SearchMediaVisiblePage(ctx context.Context, query string, page, pageSize int, visibility MediaVisibility) ([]model.Media, int64, error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > maxMediaSearchPageSize {
		pageSize = maxMediaSearchPageSize
	}
	if page < 1 {
		page = 1
	}
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	items, total, err := s.repo.Media.SearchFilteredPage(ctx, query, (page-1)*pageSize, pageSize, repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
	})
	if err != nil {
		return nil, 0, err
	}
	s.attachLibraryMetadata(ctx, items)
	return items, total, nil
}

func (s *MediaService) SearchMediaVisiblePageGrouped(ctx context.Context, query string, page, pageSize int, visibility MediaVisibility) ([]MediaItem, int64, error) {
	page, pageSize = normalizeGroupedMediaPage(page, pageSize)
	items, err := s.SearchMediaVisible(ctx, query, maxMediaSearchLimit, visibility)
	if err != nil {
		return nil, 0, err
	}
	grouped := groupMediaVersions(items)
	return paginateMediaItems(grouped, page, pageSize), int64(len(grouped)), nil
}

func (s *MediaService) SearchMediaVisibleSeriesPage(ctx context.Context, query string, page, pageSize int, visibility MediaVisibility) ([]SeriesCard, int64, error) {
	page, pageSize = normalizeGroupedMediaPage(page, pageSize)
	items, err := s.SearchMediaVisible(ctx, query, maxMediaSearchLimit, visibility)
	if err != nil {
		return nil, 0, err
	}
	cards := groupMediaSearchCards(items)
	if len(cards) == 0 {
		return []SeriesCard{}, 0, nil
	}
	start := (page - 1) * pageSize
	if start >= len(cards) {
		return []SeriesCard{}, int64(len(cards)), nil
	}
	end := start + pageSize
	if end > len(cards) {
		end = len(cards)
	}
	return cards[start:end], int64(len(cards)), nil
}
