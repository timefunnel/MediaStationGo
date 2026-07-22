package model

// UserMediaPlaybackPreference stores one user's subtitle and audio selections
// for one playable media item. No row means the user has not configured this
// item yet; SubtitleEnabled=false therefore remains an explicit user choice.
type UserMediaPlaybackPreference struct {
	Base
	UserID           string `gorm:"index;size:36;not null;uniqueIndex:uniq_user_media_playback_preference" json:"user_id"`
	MediaID          string `gorm:"index;size:128;not null;uniqueIndex:uniq_user_media_playback_preference" json:"media_id"`
	SubtitleEnabled  bool   `gorm:"not null" json:"subtitle_enabled"`
	SubtitleTrackKey string `gorm:"size:512;not null;default:''" json:"subtitle_track_key,omitempty"`
	AudioTrackKey    string `gorm:"size:512;not null;default:''" json:"audio_track_key,omitempty"`
}
