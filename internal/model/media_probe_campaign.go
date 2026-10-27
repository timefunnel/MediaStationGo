package model

// Encrypted orchestration checkpoint; Media remains the metadata authority.
type MediaProbeCampaign struct {
	Base
	Payload string `gorm:"type:text;not null" json:"-"`
}
