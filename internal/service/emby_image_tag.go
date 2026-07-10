package service

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"time"
)

func embyImageTag(itemID, kind, raw string, updatedAt time.Time) string {
	itemID = strings.TrimSpace(itemID)
	raw = strings.TrimSpace(raw)
	if itemID == "" || raw == "" {
		return ""
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	stamp := ""
	if !updatedAt.IsZero() {
		stamp = updatedAt.UTC().Format(time.RFC3339Nano)
	}
	sum := sha1.Sum([]byte(kind + "\x00" + raw + "\x00" + stamp))
	encoded := hex.EncodeToString(sum[:])
	return itemID + "-" + encoded[:12]
}
