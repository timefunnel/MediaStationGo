package model

// UserDiscoverPreference stores one user's selected Discover modules.
// A persisted empty slice is different from no row: it means the user
// intentionally disabled every module.
type UserDiscoverPreference struct {
	Base
	UserID           string   `gorm:"uniqueIndex;size:36;not null" json:"user_id"`
	SelectedSections []string `gorm:"serializer:json;type:text;not null;default:'[]'" json:"selected_sections"`
}
