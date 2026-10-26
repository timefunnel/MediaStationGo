package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

// ExportExchangeManifest reads MSG's database only. No cloud resolver is used.
func (s *MediaProbeQueueService) ExportExchangeManifest(ctx context.Context, libraryIDs []string) (*ProbeExchangeManifest, error) {
	var media []model.Media
	var roots []model.LibraryRoot
	var libraries []model.Library
	err := s.repo.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Where("path LIKE ?", "cloud://%")
		if len(libraryIDs) > 0 {
			q = q.Where("library_id IN ?", libraryIDs)
		}
		if err := q.Order("library_id, path, id").Find(&media).Error; err != nil {
			return err
		}
		if err := tx.Find(&libraries).Error; err != nil {
			return err
		}
		return tx.Find(&roots).Error
	})
	if err != nil {
		return nil, err
	}
	knownLibraries := map[string]bool{}
	for _, library := range libraries {
		knownLibraries[library.ID] = true
	}
	for _, id := range libraryIDs {
		if !knownLibraries[id] {
			return nil, errors.New("导出范围包含不存在的媒体库")
		}
	}
	m := &ProbeExchangeManifest{Version: ProbeExchangeVersion, ID: uuid.NewString(), CreatedAt: time.Now().UTC(), Items: []ProbeExchangeItem{}}
	for _, row := range media {
		rootID, relative, err := exchangeRelativeSource(&row, libraries, roots)
		if err != nil {
			return nil, fmt.Errorf("媒体 %s 无法导出: %w", row.ID, err)
		}
		m.Items = append(m.Items, ProbeExchangeItem{MediaID: row.ID, LibraryID: row.LibraryID, RootID: rootID,
			RelativePath: relative, SizeBytes: row.SizeBytes, Fingerprint: mediaProbeFingerprint(&row), Metadata: probeMetadataFromMedia(&row)})
	}
	m.Digest, err = probeExchangeDigest(m)
	if err != nil {
		return nil, err
	}
	if err := m.CheckDigest(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(data) > ProbeExchangeMaxBytes {
		return nil, errors.New("清单超过 64 MiB，请限定媒体库后导出")
	}
	record := model.MediaProbeExchange{Base: model.Base{ID: m.ID}, Kind: "manifest", ManifestID: m.ID, Digest: m.Digest, Payload: string(data)}
	if err := s.repo.DB.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, err
	}
	return m, nil
}

func exchangeSourcePath(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "cloud" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("云端来源路径格式无效")
	}
	return u.Host + "/" + strings.Trim(u.Path, "/"), nil
}

func exchangeRelativeSource(m *model.Media, libraries []model.Library, roots []model.LibraryRoot) (string, string, error) {
	file, err := exchangeSourcePath(m.Path)
	if err != nil {
		return "", "", err
	}
	type candidate struct{ id, value string }
	candidates := []candidate{}
	for _, root := range roots {
		if root.LibraryID == m.LibraryID && (m.LibraryRootID == "" || root.ID == m.LibraryRootID) {
			candidates = append(candidates, candidate{root.ID, root.Path})
		}
	}
	if m.LibraryRootID == "" {
		for _, library := range libraries {
			if library.ID == m.LibraryID {
				candidates = append(candidates, candidate{library.ID, library.Path})
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return len(candidates[i].value) > len(candidates[j].value) })
	for _, root := range candidates {
		base, err := exchangeSourcePath(root.value)
		if err != nil {
			continue // Non-cloud roots are not candidates for this cloud file.
		}
		base = strings.TrimRight(base, "/") + "/"
		if !strings.HasPrefix(file, base) {
			continue
		}
		relative := strings.TrimPrefix(file, base)
		if err := validateProbeRelativePath(relative); err != nil {
			return "", "", err
		}
		if m.RelativePath != "" && path.Clean(strings.ReplaceAll(m.RelativePath, "\\", "/")) != relative {
			return "", "", errors.New("记录中的相对路径与来源根不一致")
		}
		return root.id, relative, nil
	}
	return "", "", errors.New("找不到包含此文件的云端库根")
}

func (s *MediaProbeQueueService) PreviewExchange(ctx context.Context, bundle *ProbeExchangeBundle) (*ProbeExchangePreview, error) {
	return s.previewExchange(ctx, s.repo.DB, bundle, false)
}

func (s *MediaProbeQueueService) previewExchange(ctx context.Context, db *gorm.DB, b *ProbeExchangeBundle, lock bool) (*ProbeExchangePreview, error) {
	if b == nil || b.Version != ProbeExchangeVersion || !probeDecimalID(b.AccountID) || b.AccountID == "0" || len(b.Results) == 0 || len(b.Results) > 100 || b.BatchID == "" || strings.TrimSpace(b.Provenance) == "" || !probeSHA256RE.MatchString(b.PlanDigest) {
		return nil, errors.New("结果包版本、批次、来源证明或执行计划无效")
	}
	bundleDigest, err := probeExchangeDigest(b)
	if err != nil {
		return nil, err
	}
	var record model.MediaProbeExchange
	q := db.WithContext(ctx)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.First(&record, "id = ? AND kind = ?", b.ManifestID, "manifest").Error; err != nil {
		return nil, fmt.Errorf("找不到服务器保存的原任务清单: %w", err)
	}
	var manifest ProbeExchangeManifest
	if err := json.Unmarshal([]byte(record.Payload), &manifest); err != nil {
		return nil, err
	}
	if err := manifest.CheckDigest(); err != nil {
		return nil, err
	}
	if b.ManifestDigest != manifest.Digest {
		return nil, errors.New("结果包与服务器原任务清单不一致")
	}
	preview := &ProbeExchangePreview{Counts: map[string]int{}, Changes: []ProbeExchangeChange{}}
	var applied int64
	if err := db.WithContext(ctx).Model(&model.MediaProbeExchange{}).Where("kind = ? AND digest = ?", "import", bundleDigest).Count(&applied).Error; err != nil {
		return nil, err
	}
	if applied > 0 {
		preview.AlreadyApplied = true
		preview.Counts["already_applied"] = len(b.Results)
		preview.Digest = bundleDigest
		return preview, nil
	}
	items := map[string]ProbeExchangeItem{}
	for _, item := range manifest.Items {
		items[item.MediaID] = item
	}
	seen := map[string]bool{}
	for _, result := range b.Results {
		original, found := items[result.MediaID]
		if !found || seen[result.MediaID] || result.Fingerprint != original.Fingerprint || result.SizeBytes != original.SizeBytes {
			return nil, errors.New("结果包存在重复媒体或不匹配的原文件身份")
		}
		seen[result.MediaID] = true
	}
	ordered := append([]ProbeExchangeResult(nil), b.Results...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].MediaID < ordered[j].MediaID })
	for _, result := range ordered {
		change := ProbeExchangeChange{MediaID: result.MediaID, Status: result.Status, RelativePath: items[result.MediaID].RelativePath}
		switch result.Status {
		case "completed", "partial":
			if !probeDecimalID(result.FileID) || result.FileID == "0" || !probeSHA1RE.MatchString(result.SHA1) || (result.Source != "existing" && result.Source != "shared" && result.Source != "ffprobe") {
				return nil, errors.New("有效结果缺少 B 文件身份或可信技术信息来源")
			}
			var current model.Media
			query := db.WithContext(ctx)
			if lock {
				query = query.Clauses(clause.Locking{Strength: "UPDATE"})
			}
			if err := query.First(&current, "id = ?", result.MediaID).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, err
				}
				change.Status, change.Error = "conflict", "媒体已删除"
			} else if mediaProbeFingerprint(&current) != result.Fingerprint {
				change.Status, change.Error = "conflict", "来源文件已变化"
			} else {
				merged, err := mergeExchangeProbe(probeMetadataFromMedia(&current), result.Metadata)
				if err != nil {
					change.Status, change.Error = "conflict", err.Error()
				} else {
					estimated := result.EstimatedBitRate || (merged.BitRate == 0 && merged.DurationSec > 0)
					merged.BitRate = effectiveMediaBitRate(merged.BitRate, current.SizeBytes, merged.DurationSec)
					change.UnknownFields = probeExchangeUnknownFields(merged)
					before, after := probeResultUpdates(probeMetadataFromMedia(&current)), probeResultUpdates(merged)
					delete(before, "media_probe_version")
					after["media_probe_version"] = current.MediaProbeVersion
					before["media_probe_version"] = current.MediaProbeVersion
					if probeExchangeBasicComplete(merged) {
						after["media_probe_version"] = mediaProbeMetadataVersion
						change.Status = "completed"
					} else {
						change.Status = "partial"
					}
					change.Before, change.After = map[string]any{}, map[string]any{}
					for key, value := range after {
						if fmt.Sprint(before[key]) != fmt.Sprint(value) {
							previous, exists := before[key]
							if !exists {
								previous = reflect.Zero(reflect.TypeOf(value)).Interface()
							}
							change.Before[key], change.After[key] = previous, value
						}
					}
					if len(change.After) == 0 {
						change.Status = "unchanged"
					}
					if _, found := change.After["bit_rate"]; found && estimated {
						change.EstimatedFields = []string{"bit_rate"}
					}
				}
			}
		case "pending", "failed", "unsupported", "mapping_conflict":
			change.Error = safeMediaProbeQueueError(errors.New(result.Error))
		default:
			return nil, errors.New("结果包包含无效状态")
		}
		preview.Counts[change.Status]++
		preview.Changes = append(preview.Changes, change)
	}
	preview.Digest, err = probeExchangeDigest(struct {
		Bundle  string
		Changes []ProbeExchangeChange
	}{bundleDigest, preview.Changes})
	return preview, err
}

func (s *MediaProbeQueueService) ApplyExchange(ctx context.Context, bundle *ProbeExchangeBundle, digest string, confirmed bool) (*ProbeExchangePreview, error) {
	if !confirmed || digest == "" || bundle == nil {
		return nil, errors.New("请先预览并明确确认结果包导入范围")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil, errors.New("请先暂停主库文件信息队列")
	}
	reserved := []string{}
	defer func() {
		for _, id := range reserved {
			s.stream.finishPlaybackCloudProbe(id, 0)
		}
	}()
	if s.stream != nil {
		for _, result := range bundle.Results {
			if !s.stream.reserveExplicitMediaProbe(result.MediaID) {
				return nil, errors.New("当前有同一媒体正在探测，请等待后重新预览")
			}
			reserved = append(reserved, result.MediaID)
		}
	}
	var preview *ProbeExchangePreview
	err := s.repo.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		preview, err = s.previewExchange(ctx, tx, bundle, true)
		if err != nil {
			return err
		}
		if preview.AlreadyApplied {
			return nil
		}
		if preview.Digest != digest {
			return errors.New("预览后字段或来源已变化，请重新预览")
		}
		for _, change := range preview.Changes {
			if len(change.After) == 0 {
				continue
			}
			var current model.Media
			if err := tx.First(&current, "id = ?", change.MediaID).Error; err != nil {
				return err
			}
			var merged *ProbeResult
			for _, result := range bundle.Results {
				if result.MediaID == change.MediaID {
					merged, err = mergeExchangeProbe(probeMetadataFromMedia(&current), result.Metadata)
					break
				}
			}
			if err != nil || merged == nil {
				return errors.New("导入计划缺少媒体结果")
			}
			version := current.MediaProbeVersion
			if value, found := change.After["media_probe_version"]; found {
				version = value.(int)
			}
			if err := persistMediaProbeResult(ctx, repository.New(tx), nil, nil, s.log, &current, merged); err != nil {
				return err
			}
			if err := tx.Model(&current).Update("media_probe_version", version).Error; err != nil {
				return err
			}
			finishedAt := time.Now().UTC()
			job := model.MediaProbeJob{MediaID: current.ID, Fingerprint: mediaProbeFingerprint(&current), Status: change.Status, FinishedAt: &finishedAt}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "media_id"}}, DoUpdates: clause.AssignmentColumns([]string{"status", "finished_at", "error", "updated_at"}), Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "media_probe_jobs.fingerprint = excluded.fingerprint AND media_probe_jobs.status <> 'running'"}}}}).Create(&job).Error; err != nil {
				return err
			}
		}
		data, err := json.Marshal(struct {
			Preview *ProbeExchangePreview `json:"preview"`
			Bundle  *ProbeExchangeBundle  `json:"bundle"`
		}{preview, bundle})
		if err != nil {
			return err
		}
		bundleDigest, err := probeExchangeDigest(bundle)
		if err != nil {
			return err
		}
		return tx.Create(&model.MediaProbeExchange{Kind: "import", ManifestID: bundle.ManifestID, Digest: bundleDigest, Payload: string(data)}).Error
	})
	if err == nil && s.stream != nil && s.stream.cache != nil && !preview.AlreadyApplied {
		s.stream.cache.DeletePrefix(context.WithoutCancel(ctx), "media:")
	}
	if err == nil && !preview.AlreadyApplied {
		s.plan = nil
	}
	return preview, err
}
