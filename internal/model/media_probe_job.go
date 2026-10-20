package model

import "time"

// MediaProbeJob stores queue state only. Technical metadata remains in Media.
type MediaProbeJob struct {
	MediaID     string     `gorm:"primaryKey;size:36" json:"media_id"`
	Fingerprint string     `gorm:"size:64;not null" json:"-"`
	Status      string     `gorm:"index;size:24;not null" json:"status"`
	Attempts    int        `json:"attempts"`
	Error       string     `gorm:"type:text" json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}
