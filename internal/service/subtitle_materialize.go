package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

const (
	materializedSubtitleScheme         = "materialized-subtitle"
	maxMaterializedCloudSubtitleBytes  = 8 << 20
	maxMaterializedCloudSubtitleTracks = 32
)

type CloudSubtitleMaterializeResult struct {
	Status     string `json:"status"`
	MediaID    string `json:"media_id"`
	Provider   string `json:"provider,omitempty"`
	Discovered int    `json:"discovered"`
	Cached     int    `json:"cached"`
	Removed    int    `json:"removed"`
	Reason     string `json:"reason,omitempty"`
	Error      string `json:"error,omitempty"`
}

func cloudSubtitleMaterializedDirFromEnv() string {
	return strings.TrimSpace(os.Getenv("MEDIASTATION_CLOUD_SUBTITLE_CACHE_DIR"))
}

func (s *SubtitleService) SetMaterializedCacheDir(dir string) {
	if s != nil {
		s.materializedCacheDir = strings.TrimSpace(dir)
	}
}

func (s *SubtitleService) EnsureCloudSubtitles(ctx context.Context, mediaID string) (CloudSubtitleMaterializeResult, error) {
	return s.materializeCloudSubtitles(ctx, mediaID, false)
}

func (s *SubtitleService) RefreshCloudSubtitles(ctx context.Context, mediaID string) (CloudSubtitleMaterializeResult, error) {
	return s.materializeCloudSubtitles(ctx, mediaID, true)
}

func (s *SubtitleService) materializeCloudSubtitles(ctx context.Context, mediaID string, force bool) (CloudSubtitleMaterializeResult, error) {
	result := CloudSubtitleMaterializeResult{Status: "skipped", MediaID: strings.TrimSpace(mediaID)}
	if s == nil || s.repo == nil || s.repo.Media == nil {
		return failedCloudSubtitleMaterializeResult(result, errors.New("subtitle service unavailable"))
	}
	if !safeSubtitleMediaID(result.MediaID) {
		return failedCloudSubtitleMaterializeResult(result, errors.New("invalid media id"))
	}
	if strings.TrimSpace(s.materializedCacheDir) == "" {
		result.Reason = "cache_disabled"
		return result, nil
	}
	media, err := s.repo.Media.FindByID(ctx, result.MediaID)
	if err != nil {
		return failedCloudSubtitleMaterializeResult(result, err)
	}
	if media == nil {
		return failedCloudSubtitleMaterializeResult(result, errors.New("media not found"))
	}
	provider, _, cloudMedia := cloudSubtitleMediaRef(*media)
	if !cloudMedia {
		result.Reason = "not_cloud_media"
		return result, nil
	}
	result.Provider = provider
	if !force {
		if discovered, cached, current := s.currentMaterializedCloudSubtitles(*media, provider); current {
			result.Cached = cached
			result.Discovered = discovered
			result.Reason = "cache_current"
			return result, nil
		}
	}
	if s.storage == nil {
		return failedCloudSubtitleMaterializeResult(result, errors.New("cloud storage service unavailable"))
	}

	s.materializeMu.Lock()
	defer s.materializeMu.Unlock()
	if !force {
		if discovered, cached, current := s.currentMaterializedCloudSubtitles(*media, provider); current {
			result.Cached = cached
			result.Discovered = discovered
			result.Reason = "cache_current"
			return result, nil
		}
	}

	tracks, err := loadCloudSubtitles(ctx, s, *media)
	if err != nil {
		return failedCloudSubtitleMaterializeResult(result, err)
	}
	tracks = uniqueCloudSubtitleTracks(tracks)
	result.Discovered = len(tracks)
	if len(tracks) > maxMaterializedCloudSubtitleTracks {
		return failedCloudSubtitleMaterializeResult(result, fmt.Errorf("cloud subtitle count %d exceeds limit %d", len(tracks), maxMaterializedCloudSubtitleTracks))
	}

	root, err := filepath.Abs(s.materializedCacheDir)
	if err != nil {
		return failedCloudSubtitleMaterializeResult(result, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return failedCloudSubtitleMaterializeResult(result, err)
	}
	stageDir, err := os.MkdirTemp(root, "."+media.ID+"-stage-")
	if err != nil {
		return failedCloudSubtitleMaterializeResult(result, err)
	}
	defer os.RemoveAll(stageDir)

	index := localSubtitleIndex{
		MediaID:   media.ID,
		Source:    "cloud",
		Provider:  provider,
		MediaPath: media.Path,
		CachedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Tracks:    make([]localSubtitleIndexTrack, 0, len(tracks)),
	}
	for _, track := range tracks {
		typ, ref, name, ok := parseCloudSubtitlePath(track.Path)
		if !ok || typ != provider {
			return failedCloudSubtitleMaterializeResult(result, fmt.Errorf("invalid cloud subtitle path %q", track.Path))
		}
		ext, ok := normalizeSubtitleExtension(filepath.Ext(firstNonEmpty(name, ref)))
		if !ok {
			return failedCloudSubtitleMaterializeResult(result, fmt.Errorf("unsupported cloud subtitle format %q", name))
		}
		body, err := s.storage.CloudReadText(ctx, typ, ref, maxMaterializedCloudSubtitleBytes)
		if err != nil {
			return failedCloudSubtitleMaterializeResult(result, fmt.Errorf("cache cloud subtitle %q: %w", name, err))
		}
		filename := materializedCloudSubtitleFilename(track.Path, ext)
		if err := os.WriteFile(filepath.Join(stageDir, filename), []byte(body), 0o644); err != nil {
			return failedCloudSubtitleMaterializeResult(result, err)
		}
		index.Tracks = append(index.Tracks, localSubtitleIndexTrack{
			MediaID:    media.ID,
			Filename:   filename,
			Lang:       track.Lang,
			Label:      track.Label,
			SourcePath: track.Path,
		})
	}
	indexBody, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return failedCloudSubtitleMaterializeResult(result, err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "tracks.json"), append(indexBody, '\n'), 0o644); err != nil {
		return failedCloudSubtitleMaterializeResult(result, err)
	}
	removed, err := replaceMaterializedSubtitleDir(root, media.ID, stageDir)
	if err != nil {
		return failedCloudSubtitleMaterializeResult(result, err)
	}
	result.Status = "success"
	result.Cached = len(index.Tracks)
	result.Removed = removed
	return result, nil
}

func (s *SubtitleService) currentMaterializedCloudSubtitles(media model.Media, provider string) (discovered, cached int, current bool) {
	tracks, exists, index := discoverCachedSubtitles(s.materializedCacheDir, media.ID, materializedSubtitleScheme)
	if !exists || index == nil || index.MediaID != media.ID || index.Source != "cloud" || !strings.EqualFold(index.Provider, provider) || index.MediaPath != media.Path {
		return 0, 0, false
	}
	if len(tracks) != len(index.Tracks) {
		return len(index.Tracks), len(tracks), false
	}
	return len(index.Tracks), len(tracks), true
}

func failedCloudSubtitleMaterializeResult(result CloudSubtitleMaterializeResult, err error) (CloudSubtitleMaterializeResult, error) {
	result.Status = "failed"
	if err != nil {
		result.Error = err.Error()
	}
	return result, err
}

func uniqueCloudSubtitleTracks(tracks []SubtitleTrack) []SubtitleTrack {
	out := make([]SubtitleTrack, 0, len(tracks))
	seen := map[string]struct{}{}
	for _, track := range tracks {
		key := strings.TrimSpace(track.Path)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, track)
	}
	return out
}

func materializedCloudSubtitleFilename(sourcePath, ext string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sourcePath)))
	return "cloud-" + hex.EncodeToString(sum[:8]) + ext
}

func replaceMaterializedSubtitleDir(root, mediaID, stageDir string) (int, error) {
	targetDir, ok := localSubtitleMediaDir(root, mediaID)
	if !ok {
		return 0, errors.New("materialized subtitle cache path invalid")
	}
	removed := countCachedSubtitleFiles(targetDir)
	backupDir := targetDir + ".old-" + fmt.Sprint(time.Now().UnixNano())
	hadTarget := false
	if _, err := os.Stat(targetDir); err == nil {
		hadTarget = true
		if err := os.Rename(targetDir, backupDir); err != nil {
			return 0, err
		}
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	if err := os.Rename(stageDir, targetDir); err != nil {
		if hadTarget {
			if rollbackErr := os.Rename(backupDir, targetDir); rollbackErr != nil {
				return 0, fmt.Errorf("install materialized subtitle cache: %w; rollback failed: %v", err, rollbackErr)
			}
		}
		return 0, err
	}
	if hadTarget {
		if err := os.RemoveAll(backupDir); err != nil {
			return 0, fmt.Errorf("remove replaced subtitle cache %q: %w", backupDir, err)
		}
	}
	return removed, nil
}

func countCachedSubtitleFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := normalizeSubtitleExtension(filepath.Ext(entry.Name())); ok {
			count++
		}
	}
	return count
}

func discoverMaterializedCloudSubtitles(s *SubtitleService, mediaID string) ([]SubtitleTrack, bool) {
	if s == nil {
		return []SubtitleTrack{}, false
	}
	tracks, exists, _ := discoverCachedSubtitles(s.materializedCacheDir, mediaID, materializedSubtitleScheme)
	return tracks, exists
}

func materializedSubtitleURI(mediaID, filename string) string {
	return cachedSubtitleURI(materializedSubtitleScheme, mediaID, filename)
}

func parseMaterializedSubtitlePath(raw string) (mediaID, filename string, ok bool) {
	u, err := parseCachedSubtitlePath(raw, materializedSubtitleScheme)
	if err != nil {
		return "", "", false
	}
	return u.mediaID, u.filename, true
}

func serveMaterializedCloudSubtitle(s *SubtitleService, mediaID, localMediaID, filename, format string, w io.Writer) error {
	if s == nil {
		return errors.New("subtitle service unavailable")
	}
	return serveCachedSubtitleFromRoot(s.materializedCacheDir, mediaID, localMediaID, filename, format, w)
}

type cachedSubtitlePath struct {
	mediaID  string
	filename string
}

func parseCachedSubtitlePath(raw, scheme string) (cachedSubtitlePath, error) {
	mediaID, filename, ok := parseSubtitleURI(raw, scheme)
	if !ok {
		return cachedSubtitlePath{}, errors.New("invalid cached subtitle path")
	}
	return cachedSubtitlePath{mediaID: mediaID, filename: filename}, nil
}
