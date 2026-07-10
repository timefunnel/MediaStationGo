package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

const embyFolderCoverTagVersion = "timefunnel-folder-cover-v1"

type EmbyFolderCoverArtwork struct {
	MediaID string
	URL     string
}

func (e *EmbyService) FolderCoverArtwork(ctx context.Context, id, imageType string, limit int) ([]EmbyFolderCoverArtwork, error) {
	if e == nil || e.repo == nil || e.repo.Library == nil || e.repo.DB == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 8 {
		limit = 4
	}
	lib, err := e.repo.Library.FindByID(ctx, id)
	if err != nil || lib == nil {
		return nil, err
	}
	libraryIDs := e.mergedLibraryIDs(ctx, lib.ID)
	if len(libraryIDs) == 0 {
		libraryIDs = []string{lib.ID}
	}
	var rows []model.Media
	if err := e.repo.DB.WithContext(ctx).Model(&model.Media{}).
		Where("library_id IN ? AND deleted_at IS NULL", libraryIDs).
		Where("(poster_url <> '' OR backdrop_url <> '')").
		Order("updated_at DESC, created_at DESC, id DESC").
		Limit(limit * 4).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]EmbyFolderCoverArtwork, 0, limit)
	seen := map[string]struct{}{}
	for i := range rows {
		raw := folderCoverArtworkURL(&rows[i], imageType)
		if raw == "" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		out = append(out, EmbyFolderCoverArtwork{MediaID: rows[i].ID, URL: raw})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (e *EmbyService) FolderCoverTag(ctx context.Context, id, imageType string) string {
	artworks, err := e.FolderCoverArtwork(ctx, id, imageType, 4)
	if err != nil || len(artworks) == 0 {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(embyFolderCoverTagVersion))
	h.Write([]byte{0})
	h.Write([]byte(strings.ToLower(strings.TrimSpace(id))))
	h.Write([]byte{0})
	h.Write([]byte(strings.ToLower(strings.TrimSpace(imageType))))
	for _, artwork := range artworks {
		h.Write([]byte{0})
		h.Write([]byte(artwork.MediaID))
		h.Write([]byte{0})
		h.Write([]byte(artwork.URL))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func folderCoverArtworkURL(m *model.Media, imageType string) string {
	if m == nil {
		return ""
	}
	poster := strings.TrimSpace(m.PosterURL)
	backdrop := strings.TrimSpace(m.BackdropURL)
	switch strings.ToLower(strings.TrimSpace(imageType)) {
	case "backdrop", "art", "thumb":
		return firstNonEmpty(backdrop, poster)
	default:
		return firstNonEmpty(poster, backdrop)
	}
}
