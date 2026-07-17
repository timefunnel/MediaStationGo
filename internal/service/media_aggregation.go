package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

const (
	MediaAggregationActionGroup  = "group"
	MediaAggregationActionDetach = "detach"
)

type MediaAggregationRequest struct {
	Action   string   `json:"action"`
	Title    string   `json:"title,omitempty"`
	MediaIDs []string `json:"media_ids"`
}

type MediaAggregationResult struct {
	Action   string `json:"action"`
	Updated  int    `json:"updated"`
	GroupKey string `json:"group_key,omitempty"`
}

// UpdateMediaAggregation manages the explicit parent/child presentation used
// by multipart works. Version relationships are intentionally left untouched.
func (s *MediaService) UpdateMediaAggregation(ctx context.Context, libraryID string, req MediaAggregationRequest) (*MediaAggregationResult, error) {
	if s == nil || s.repo == nil || s.repo.DB == nil || s.repo.Library == nil {
		return nil, errors.New("media service unavailable")
	}
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return nil, errors.New("library id required")
	}
	lib, err := s.repo.Library.FindByID(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	if lib == nil {
		return nil, gorm.ErrRecordNotFound
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	ids, err := normalizeAggregationMediaIDs(req.MediaIDs)
	if err != nil {
		return nil, err
	}
	if action != MediaAggregationActionGroup && action != MediaAggregationActionDetach {
		return nil, errors.New("action 必须为 group 或 detach")
	}
	if action == MediaAggregationActionGroup && len(ids) < 2 {
		return nil, errors.New("聚合至少需要两个作品")
	}
	if action == MediaAggregationActionDetach && len(ids) < 1 {
		return nil, errors.New("解除聚合至少需要一个作品")
	}
	title := strings.Join(strings.Fields(strings.TrimSpace(req.Title)), " ")
	if action == MediaAggregationActionGroup && (title == "" || len([]rune(title)) > 255) {
		return nil, errors.New("聚合标题不能为空且不能超过 255 个字符")
	}

	var rows []model.Media
	if err := s.repo.DB.WithContext(ctx).
		Where("library_id = ? AND id IN ? AND deleted_at IS NULL", libraryID, ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != len(ids) {
		return nil, errors.New("部分媒体不存在或不属于当前媒体库")
	}
	byID := make(map[string]model.Media, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	ordered := make([]model.Media, 0, len(ids))
	for _, id := range ids {
		ordered = append(ordered, byID[id])
	}

	if action == MediaAggregationActionGroup {
		if err := s.validateManualAggregationRows(ctx, libraryID, ordered); err != nil {
			return nil, err
		}
	}

	affectedKeys := make(map[string]struct{})
	for _, row := range ordered {
		if key := strings.TrimSpace(row.PartGroupKey); key != "" {
			affectedKeys[key] = struct{}{}
		}
	}
	groupKey := ""
	if action == MediaAggregationActionGroup {
		groupKey = strings.TrimSpace(ordered[0].PartGroupKey)
		if groupKey == "" {
			groupKey = uuid.NewString()
		}
	}

	err = s.repo.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		switch action {
		case MediaAggregationActionGroup:
			for index, row := range ordered {
				if err := tx.Model(&model.Media{}).Where("id = ?", row.ID).Updates(map[string]any{
					"part_group_key":   groupKey,
					"part_group_title": title,
					"part_index":       index + 1,
				}).Error; err != nil {
					return err
				}
			}
		case MediaAggregationActionDetach:
			if err := tx.Model(&model.Media{}).Where("id IN ?", ids).Updates(map[string]any{
				"part_group_key":   "",
				"part_group_title": "",
				"part_index":       0,
			}).Error; err != nil {
				return err
			}
		}
		for key := range affectedKeys {
			if action == MediaAggregationActionGroup && key == groupKey {
				continue
			}
			if err := normalizeRemainingPartGroup(tx, libraryID, key); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.invalidateMediaCache(ctx)
	return &MediaAggregationResult{Action: action, Updated: len(ids), GroupKey: groupKey}, nil
}

func normalizeAggregationMediaIDs(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id == "" {
			return nil, errors.New("media_ids 不能包含空值")
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("media_ids 包含重复项: %s", id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func (s *MediaService) validateManualAggregationRows(ctx context.Context, libraryID string, selected []model.Media) error {
	selectedByID := make(map[string]struct{}, len(selected))
	selectedPartCounts := make(map[string]int)
	for _, row := range selected {
		selectedByID[row.ID] = struct{}{}
		if strings.TrimSpace(row.VersionGroupKey) != "" {
			return fmt.Errorf("多版本作品不能手动聚合: %s", row.Title)
		}
		if key := strings.TrimSpace(row.PartGroupKey); key != "" {
			selectedPartCounts[key]++
		}
	}

	for key, selectedCount := range selectedPartCounts {
		var total int64
		if err := s.repo.DB.WithContext(ctx).Model(&model.Media{}).
			Where("library_id = ? AND part_group_key = ? AND deleted_at IS NULL", libraryID, key).
			Count(&total).Error; err != nil {
			return err
		}
		if int64(selectedCount) != total {
			return fmt.Errorf("请完整选择现有聚合作品后再调整: %s", key)
		}
	}

	var libraryRows []model.Media
	if err := s.repo.DB.WithContext(ctx).
		Where("library_id = ? AND deleted_at IS NULL", libraryID).
		Find(&libraryRows).Error; err != nil {
		return err
	}
	versionCounts := make(map[string]int)
	versionKeyByID := make(map[string]string)
	for _, row := range libraryRows {
		if strings.TrimSpace(row.PartGroupKey) != "" {
			continue
		}
		key := mediaVersionGroupKey(row)
		if key == "" {
			continue
		}
		versionCounts[key]++
		versionKeyByID[row.ID] = key
	}
	for id := range selectedByID {
		if key := versionKeyByID[id]; key != "" && versionCounts[key] > 1 {
			return fmt.Errorf("多版本作品不能手动聚合: %s", byMediaIDTitle(selected, id))
		}
	}
	return nil
}

func byMediaIDTitle(rows []model.Media, id string) string {
	for _, row := range rows {
		if row.ID == id {
			return row.Title
		}
	}
	return id
}

func normalizeRemainingPartGroup(tx *gorm.DB, libraryID, groupKey string) error {
	groupKey = strings.TrimSpace(groupKey)
	if groupKey == "" {
		return nil
	}
	var rows []model.Media
	if err := tx.Where("library_id = ? AND part_group_key = ? AND deleted_at IS NULL", libraryID, groupKey).
		Order("part_index ASC, path ASC").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) < 2 {
		if len(rows) == 0 {
			return nil
		}
		return tx.Model(&model.Media{}).Where("id = ?", rows[0].ID).Updates(map[string]any{
			"part_group_key": "", "part_group_title": "", "part_index": 0,
		}).Error
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].PartIndex != rows[j].PartIndex {
			return rows[i].PartIndex < rows[j].PartIndex
		}
		return rows[i].Path < rows[j].Path
	})
	for index, row := range rows {
		if row.PartIndex == index+1 {
			continue
		}
		if err := tx.Model(&model.Media{}).Where("id = ?", row.ID).Update("part_index", index+1).Error; err != nil {
			return err
		}
	}
	return nil
}
