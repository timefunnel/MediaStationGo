package service

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

func (e *EmbyService) mediaSourcesForItem(ctx context.Context, m *model.Media, asEmbedded, directOnly bool) []map[string]any {
	siblings := e.mediaVersionSiblings(ctx, m)
	if len(siblings) == 0 {
		return []map[string]any{e.mediaSource(ctx, m, asEmbedded, directOnly)}
	}
	sources := make([]map[string]any, 0, len(siblings))
	for i := range siblings {
		media := siblings[i]
		sources = append(sources, e.mediaSource(ctx, &media, asEmbedded, directOnly))
	}
	return sources
}

func (e *EmbyService) mediaVersionSiblings(ctx context.Context, m *model.Media) []model.Media {
	if e == nil || e.repo == nil || e.repo.DB == nil || m == nil || strings.TrimSpace(m.ID) == "" {
		return nil
	}
	libraryIDs := e.mergedLibraryIDs(ctx, m.LibraryID)
	if len(libraryIDs) == 0 {
		libraryIDs = []string{m.LibraryID}
	}
	q := e.repo.DB.WithContext(ctx).Model(&model.Media{}).
		Where("library_id IN ?", libraryIDs).
		Where("season_num = ? AND episode_num = ?", m.SeasonNum, m.EpisodeNum)
	if m.TMDbID > 0 {
		q = q.Where("tm_db_id = ?", m.TMDbID)
	} else if m.BangumiID > 0 {
		q = q.Where("bangumi_id = ?", m.BangumiID)
	} else {
		title := strings.TrimSpace(m.Title)
		if title == "" {
			title = strings.TrimSpace(m.OriginalName)
		}
		if title == "" {
			return []model.Media{*m}
		}
		q = q.Where("LOWER(title) = ?", strings.ToLower(title))
		if m.Year > 0 {
			q = q.Where("year = ?", m.Year)
		}
	}
	var rows []model.Media
	if err := q.Find(&rows).Error; err != nil || len(rows) == 0 {
		return []model.Media{*m}
	}
	rows = e.collapseExactPathRows(rows)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].ID == m.ID {
			return true
		}
		if rows[j].ID == m.ID {
			return false
		}
		return preferMediaVersion(rows[i], rows[j])
	})
	return rows
}

func (e *EmbyService) collapseExactPathRows(rows []model.Media) []model.Media {
	if len(rows) < 2 {
		return rows
	}
	out := rows[:0]
	seen := map[string]struct{}{}
	for _, row := range rows {
		path := strings.TrimSpace(row.Path)
		if path != "" {
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
		}
		out = append(out, row)
	}
	return out
}

func (e *EmbyService) mediaVersionKey(ctx context.Context, m *model.Media) string {
	if e == nil || m == nil {
		return ""
	}
	ids := e.mergedLibraryIDs(ctx, m.LibraryID)
	sort.Strings(ids)
	libraryGroup := strings.Join(ids, ",")
	if libraryGroup == "" {
		libraryGroup = strings.TrimSpace(m.LibraryID)
	}
	if m.TMDbID > 0 {
		return fmt.Sprintf("%s|tmdb:%d|s:%d|e:%d", libraryGroup, m.TMDbID, m.SeasonNum, m.EpisodeNum)
	}
	if m.BangumiID > 0 {
		return fmt.Sprintf("%s|bangumi:%d|s:%d|e:%d", libraryGroup, m.BangumiID, m.SeasonNum, m.EpisodeNum)
	}
	title := strings.ToLower(strings.TrimSpace(m.Title))
	if title == "" {
		title = strings.ToLower(strings.TrimSpace(m.OriginalName))
	}
	if title == "" {
		return ""
	}
	return fmt.Sprintf("%s|title:%s|y:%d|s:%d|e:%d", libraryGroup, title, m.Year, m.SeasonNum, m.EpisodeNum)
}

func (e *EmbyService) attachExternalSubtitleStreams(ctx context.Context, m *model.Media, src map[string]any) {
	if e == nil || e.subtitle == nil || m == nil || src == nil || strings.TrimSpace(m.ID) == "" {
		return
	}
	tracks, err := e.subtitle.Discover(ctx, m.ID)
	if err != nil || len(tracks) == 0 {
		return
	}
	streams, ok := src["MediaStreams"].([]map[string]any)
	if !ok {
		return
	}
	nextIndex := nextMediaStreamIndex(streams)
	firstSubtitleIndex := -1
	for trackIndex, track := range tracks {
		streamIndex := nextIndex + trackIndex
		if firstSubtitleIndex < 0 {
			firstSubtitleIndex = streamIndex
		}
		streams = append(streams, embyExternalSubtitleStream(m.ID, streamIndex, trackIndex, track))
	}
	src["MediaStreams"] = streams
	if firstSubtitleIndex >= 0 {
		src["DefaultSubtitleStreamIndex"] = firstSubtitleIndex
	}
}

func nextMediaStreamIndex(streams []map[string]any) int {
	next := 0
	for _, stream := range streams {
		index, ok := stream["Index"].(int)
		if ok && index >= next {
			next = index + 1
		}
	}
	return next
}

func embyExternalSubtitleStream(mediaID string, streamIndex, trackIndex int, track SubtitleTrack) map[string]any {
	label := strings.TrimSpace(firstNonEmpty(track.Label, track.Lang, "Subtitle"))
	lang := strings.TrimSpace(firstNonEmpty(track.Lang, "und"))
	localizedDefault := ""
	ext := subtitleDeliveryExtension(track)
	codec := subtitleCodecForExtension(ext)
	if trackIndex == 0 {
		localizedDefault = "Default"
	}
	return map[string]any{
		"Index":                  streamIndex,
		"Type":                   "Subtitle",
		"Codec":                  codec,
		"Language":               lang,
		"DisplayTitle":           embySubtitleDisplayTitle(label, subtitleCodecDisplayLabel(codec)),
		"Title":                  label,
		"IsExternal":             true,
		"IsExternalUrl":          false,
		"IsInterlaced":           false,
		"IsForced":               false,
		"IsDefault":              trackIndex == 0,
		"IsTextSubtitleStream":   true,
		"SupportsExternalStream": true,
		"DeliveryMethod":         "External",
		"DeliveryUrl":            embySubtitleDeliveryURL(mediaID, streamIndex, trackIndex, ext),
		"Path":                   track.Path,
		"Protocol":               "File",
		"LocalizedDefault":       localizedDefault,
		"LocalizedForced":        "",
		"LocalizedExternal":      "External",
	}
}

func subtitleDeliveryExtension(track SubtitleTrack) string {
	if ext := subtitleExtensionFromCodec(track.Codec); ext != "" {
		return ext
	}
	if ext := subtitleExtensionFromPath(track.Path); ext != "" {
		return ext
	}
	return ".vtt"
}

func subtitleExtensionFromCodec(codec string) string {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "webvtt", "vtt":
		return ".vtt"
	case "srt", "ass", "ssa":
		return "." + strings.ToLower(strings.TrimSpace(codec))
	default:
		return ""
	}
}

func subtitleExtensionFromPath(path string) string {
	if parsed, err := url.Parse(strings.TrimSpace(path)); err == nil {
		if name := strings.TrimSpace(parsed.Query().Get("name")); name != "" {
			if ext, ok := normalizeSubtitleExtension(filepath.Ext(name)); ok {
				return ext
			}
		}
	}
	trimmed := strings.TrimSpace(path)
	if idx := strings.IndexAny(trimmed, "?#"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	if ext, ok := normalizeSubtitleExtension(filepath.Ext(trimmed)); ok {
		return ext
	}
	return ""
}

func subtitleCodecForExtension(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".vtt":
		return "webvtt"
	case ".srt":
		return "srt"
	case ".ass":
		return "ass"
	case ".ssa":
		return "ssa"
	default:
		return "webvtt"
	}
}

func subtitleCodecDisplayLabel(codec string) string {
	if strings.EqualFold(strings.TrimSpace(codec), "webvtt") {
		return "WEBVTT"
	}
	return strings.ToUpper(strings.TrimSpace(codec))
}

func embySubtitleDisplayTitle(label, codecLabel string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Subtitle"
	}
	codecLabel = strings.TrimSpace(codecLabel)
	if codecLabel == "" {
		codecLabel = "WEBVTT"
	}
	upperLabel := strings.ToUpper(label)
	if strings.Contains(strings.ToLower(label), "external") || strings.Contains(upperLabel, codecLabel) {
		return label
	}
	return label + " - " + codecLabel + " - External"
}

func embySubtitleDeliveryURL(mediaID string, streamIndex, trackIndex int, ext string) string {
	if _, ok := SubtitleContentType(ext); !ok {
		ext = ".vtt"
	}
	q := url.Values{}
	q.Set("mp_track", fmt.Sprintf("%d", trackIndex))
	mediaID = url.PathEscape(strings.TrimSpace(mediaID))
	return fmt.Sprintf("/emby/Videos/%s/%s/Subtitles/%d/Stream%s?%s", mediaID, mediaID, streamIndex, ext, q.Encode())
}

func preferMediaVersion(candidate, current model.Media) bool {
	candidateCloud := strings.TrimSpace(candidate.STRMURL) != "" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(candidate.Path)), "cloud://")
	currentCloud := strings.TrimSpace(current.STRMURL) != "" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(current.Path)), "cloud://")
	if candidateCloud != currentCloud {
		return !candidateCloud
	}
	if candidate.Width != current.Width {
		return candidate.Width > current.Width
	}
	if candidate.SizeBytes != current.SizeBytes {
		return candidate.SizeBytes > current.SizeBytes
	}
	return candidate.CreatedAt.After(current.CreatedAt)
}

func embySTRMStreamURL(mediaID string) string {
	return "/api/stream/" + url.PathEscape(strings.TrimSpace(mediaID))
}

func embyDirectStreamURL(mediaID, container string) string {
	mediaID = strings.TrimSpace(mediaID)
	container = strings.Trim(strings.ToLower(container), ". ")
	if container == "" || container == "strm" {
		return "/Videos/" + mediaID + "/stream"
	}
	return "/Videos/" + mediaID + "/stream." + container
}

func (e *EmbyService) mediaStreams(m *model.Media) []map[string]any {
	streams := []map[string]any{}
	if m.VideoCodec != "" || m.Width > 0 {
		streams = append(streams, map[string]any{
			"Codec":        m.VideoCodec,
			"Type":         "Video",
			"Index":        0,
			"Width":        m.Width,
			"Height":       m.Height,
			"AspectRatio":  "",
			"IsDefault":    true,
			"IsForced":     false,
			"IsExternal":   false,
			"DisplayTitle": fmt.Sprintf("%dx%d %s", m.Width, m.Height, m.VideoCodec),
		})
	}
	if m.AudioCodec != "" {
		streams = append(streams, map[string]any{
			"Codec":      m.AudioCodec,
			"Type":       "Audio",
			"Index":      1,
			"IsDefault":  true,
			"IsForced":   false,
			"IsExternal": false,
		})
	}
	if len(streams) == 0 {
		streams = append(streams, map[string]any{
			"Codec":        "unknown",
			"Type":         "Video",
			"Index":        0,
			"IsDefault":    true,
			"IsForced":     false,
			"IsExternal":   false,
			"DisplayTitle": "Video",
		})
	}
	return streams
}
