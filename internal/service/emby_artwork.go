package service

import (
	"context"
	"strings"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

// ImageURL returns artwork for a media/series/season item id.
func (e *EmbyService) ImageURL(ctx context.Context, id, imageType string) (string, error) {
	raw, _, err := e.ImageSource(ctx, id, imageType)
	return raw, err
}

// ImageSource resolves collection folders before the expensive series lookup.
// The loaded library is reused by the cover selector within this request.
func (e *EmbyService) ImageSource(ctx context.Context, id, imageType string) (string, *model.Library, error) {
	pick := func(primary, backdrop string) string {
		switch strings.ToLower(imageType) {
		case "backdrop", "art":
			if backdrop != "" {
				return backdrop
			}
		}
		if primary != "" {
			return primary
		}
		return backdrop
	}
	if personName, ok := embyPersonName(id); ok {
		if strings.ToLower(strings.TrimSpace(imageType)) != "primary" {
			return "", nil, nil
		}
		snapshot, err := e.personMetadataSnapshot(ctx)
		if err != nil {
			return "", nil, err
		}
		return snapshot[normalizePersonNameKey(personName)].ImageURL, nil, nil
	}
	if strings.HasPrefix(id, embyVirtualSeasonPrefix) {
		if raw, ok := e.cachedArtworkURL(id, imageType); ok {
			return raw, nil, nil
		}
		if season, ok, err := e.findSeasonGroup(ctx, id, ""); err != nil {
			return "", nil, err
		} else if ok {
			return pick(season.Series.PosterURL, season.Series.BackdropURL), nil, nil
		}
		return "", nil, nil
	}
	if strings.HasPrefix(id, embyVirtualSeriesPrefix) {
		if raw, ok := e.cachedArtworkURL(id, imageType); ok {
			return raw, nil, nil
		}
		if series, ok, err := e.findSeriesGroup(ctx, id, ""); err != nil {
			return "", nil, err
		} else if ok {
			return pick(series.PosterURL, series.BackdropURL), nil, nil
		}
		return "", nil, nil
	}
	m, err := e.repo.Media.FindByID(ctx, id)
	if err == nil && m != nil {
		return pick(e.mediaPrimaryArtwork(ctx, m), e.mediaBackdropArtwork(ctx, m)), nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	lib, err := e.repo.Library.FindByID(ctx, id)
	if err != nil || lib != nil {
		return "", lib, err
	}
	if series, ok, err := e.findSeriesGroup(ctx, id, ""); err != nil {
		return "", nil, err
	} else if ok {
		return pick(series.PosterURL, series.BackdropURL), nil, nil
	}
	return "", nil, nil
}

func (e *EmbyService) mediaPrimaryArtwork(ctx context.Context, m *model.Media) string {
	if m == nil {
		return ""
	}
	return mediaPrimaryArtworkForEpisode(m, e.mediaShouldBeEpisode(ctx, m))
}

func mediaPrimaryArtworkForEpisode(m *model.Media, isEpisode bool) string {
	if m == nil {
		return ""
	}
	if isEpisode && strings.TrimSpace(m.BackdropURL) != "" {
		return m.BackdropURL
	}
	if strings.TrimSpace(m.PosterURL) != "" {
		return m.PosterURL
	}
	return m.GeneratedPosterURL
}

func (e *EmbyService) mediaBackdropArtwork(ctx context.Context, m *model.Media) string {
	if m == nil {
		return ""
	}
	if strings.TrimSpace(m.BackdropURL) != "" {
		return m.BackdropURL
	}
	return m.GeneratedBackdropURL
}
