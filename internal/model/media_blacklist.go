package model

// MediaBlacklist is an account's hidden movie or whole series. TargetID uses
// the existing Emby projection; MediaID identifies its MSG management card.
type MediaBlacklist struct {
	Base
	UserID    string `gorm:"size:36;not null;uniqueIndex:uniq_blacklist_target,priority:1" json:"user_id"`
	Scope     string `gorm:"size:16;not null;uniqueIndex:uniq_blacklist_target,priority:2" json:"scope"`
	TargetID  string `gorm:"size:128;not null;uniqueIndex:uniq_blacklist_target,priority:3" json:"target_id"`
	LibraryID string `gorm:"size:36;not null;uniqueIndex:uniq_blacklist_target,priority:4" json:"library_id"`
	MediaID   string `gorm:"size:36;not null" json:"media_id"`
}
