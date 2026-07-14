package service

import (
	"context"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"gorm.io/gorm"
)

// ListLibraries returns every library configured on the server.
func (s *MediaService) ListLibraries(ctx context.Context) ([]model.Library, error) {
	return s.repo.Library.List(ctx)
}

func (s *MediaService) UpdateLibraryEnabled(ctx context.Context, id string, enabled bool) (*model.Library, error) {
	lib, err := s.repo.Library.FindByID(ctx, id)
	if err != nil || lib == nil {
		return lib, err
	}
	if lib.Enabled == enabled {
		return lib, nil
	}
	if err := s.repo.Library.UpdateEnabled(ctx, id, enabled); err != nil {
		return nil, err
	}
	s.invalidateMediaCache(ctx)
	return s.repo.Library.FindByID(ctx, id)
}

// DeleteLibrary removes a library and its media rows. The on-disk files are
// left untouched.
func (s *MediaService) DeleteLibrary(ctx context.Context, id string) error {
	lib, err := s.repo.Library.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if lib != nil {
		if _, ok := ParseCloudLibraryMount(lib.Path); ok {
			err := s.repo.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if err := tx.Unscoped().Where("library_id = ?", id).Delete(&model.Media{}).Error; err != nil {
					return err
				}
				if err := hardDeleteLibraryRoots(ctx, tx, id); err != nil {
					return err
				}
				return tx.Unscoped().Where("id = ?", id).Delete(&model.Library{}).Error
			})
			if err == nil {
				s.invalidateMediaCache(ctx)
			}
			return err
		}
	}
	err = s.repo.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("library_id = ?", id).Delete(&model.Media{}).Error; err != nil {
			return err
		}
		if err := hardDeleteLibraryRoots(ctx, tx, id); err != nil {
			return err
		}
		return tx.Delete(&model.Library{}, "id = ?", id).Error
	})
	if err == nil {
		s.invalidateMediaCache(ctx)
	}
	return err
}

func hardDeleteLibraryRoots(ctx context.Context, tx *gorm.DB, libraryID string) error {
	if tx == nil || !tx.Migrator().HasTable(&model.LibraryRoot{}) {
		return nil
	}
	return tx.WithContext(ctx).Unscoped().Where("library_id = ?", libraryID).Delete(&model.LibraryRoot{}).Error
}
