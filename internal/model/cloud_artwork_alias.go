package model

import "time"

// CloudArtworkAlias preserves a migrated cloud image URL without duplicating
// its replacement URL. The current image remains owned by the media record.
type CloudArtworkAlias struct {
	SourceHash string    `gorm:"primaryKey;type:varchar(64)" json:"source_hash"`
	MediaID    string    `gorm:"type:varchar(36);not null;index" json:"media_id"`
	ImageField string    `gorm:"type:varchar(16);not null;check:cloud_artwork_alias_image_field,image_field IN ('poster_url','backdrop_url')" json:"image_field"`
	CreatedAt  time.Time `json:"created_at"`
}
