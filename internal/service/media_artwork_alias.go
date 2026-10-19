package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var ErrCloudArtworkTargetGone = errors.New("migrated cloud artwork target is unavailable")

// CloudArtworkSourceHash uses the decoded provider/ref, independently of the
// public route, URL escaping, and requested image dimensions.
func CloudArtworkSourceHash(provider, ref string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(provider) + ":" + strings.TrimSpace(ref)))
	return hex.EncodeToString(sum[:])
}

// ResolveCloudArtworkAlias returns found=true for a known migration even when
// its current target is missing. Callers must not resolve the old cloud image
// after a migration lookup or target error.
func (s *MediaService) ResolveCloudArtworkAlias(ctx context.Context, provider, ref string) (raw string, found bool, err error) {
	if s == nil || s.repo == nil || s.repo.Media == nil {
		return "", false, errors.New("media service unavailable")
	}
	alias, err := s.repo.Media.FindCloudArtworkAlias(ctx, CloudArtworkSourceHash(provider, ref))
	if err != nil {
		return "", false, fmt.Errorf("find migrated cloud artwork: %w", err)
	}
	if alias == nil {
		return "", false, nil
	}
	media, err := s.repo.Media.FindByID(ctx, alias.MediaID)
	if err != nil {
		return "", true, fmt.Errorf("read migrated artwork media: %w", err)
	}
	if media == nil {
		return "", true, ErrCloudArtworkTargetGone
	}
	switch alias.ImageField {
	case "poster_url":
		raw = strings.TrimSpace(media.PosterURL)
	case "backdrop_url":
		raw = strings.TrimSpace(media.BackdropURL)
	default:
		return "", true, errors.New("invalid migrated artwork image field")
	}
	if raw == "" {
		return "", true, ErrCloudArtworkTargetGone
	}
	if _, _, cloud := ParseCloudArtworkURL(raw); cloud {
		return "", true, errors.New("migrated artwork target still points to cloud storage")
	}
	return raw, true, nil
}
