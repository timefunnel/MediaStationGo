package model

// MediaProbeExchange stores immutable export snapshots and import audit records.
// Technical metadata is still served exclusively from Media.
type MediaProbeExchange struct {
	Base
	Kind       string `gorm:"size:16;not null;index"`
	ManifestID string `gorm:"size:36;not null;index"`
	Digest     string `gorm:"size:64;not null;uniqueIndex"`
	Payload    string `gorm:"type:text;not null" json:"-"`
}
