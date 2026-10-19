package repository

import (
	"context"
	"errors"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"gorm.io/gorm"
)

func (r *MediaRepository) FindCloudArtworkAlias(ctx context.Context, sourceHash string) (*model.CloudArtworkAlias, error) {
	var alias model.CloudArtworkAlias
	err := r.db.WithContext(ctx).Where("source_hash = ?", sourceHash).Take(&alias).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &alias, nil
}
