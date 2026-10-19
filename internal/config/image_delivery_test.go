package config

import (
	"strings"
	"testing"
)

func TestImageDeliveryRejectsUnknownMode(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("MEDIASTATION_CACHE_REMOTE_IMAGE_DELIVERY", "automatic")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "cache.remote_image_delivery must be proxy or direct") {
		t.Fatalf("unknown delivery mode should fail explicitly, got %v", err)
	}
}
