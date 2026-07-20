package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

const AdultLibraryIDsSettingKey = "adult.library_ids"

const noLibraryAccessID = "__no_library_access__"

// AdultContentEnabled reads the global Adult / NSFW switch.
func AdultContentEnabled(ctx context.Context, repo *repository.Container) bool {
	if repo == nil || repo.Setting == nil {
		return true
	}
	value, err := repo.Setting.Get(ctx, "adult.enabled")
	if err != nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled", "启用", "开启":
		return true
	case "0", "false", "no", "off", "disabled", "禁用", "关闭":
		return false
	default:
		return true
	}
}

// UserHidesAdult reports whether either the user's own preference or an
// administrator-enforced restriction overrides all playback profiles.
func UserHidesAdult(ctx context.Context, repo *repository.Container, userID string) bool {
	if strings.TrimSpace(userID) == "" || repo == nil || repo.User == nil {
		return false
	}
	user, err := repo.User.FindByID(ctx, userID)
	if err != nil {
		// Adult visibility is a security boundary. A failed policy lookup must
		// not silently turn adult content back on for an authenticated user.
		return true
	}
	if user == nil {
		return false
	}
	return user.HideAdult || user.AdultContentBlocked
}

// UserDefaultMediaVisibility is the visibility policy used by clients that
// cannot pass a web play-profile token, notably Emby/Jellyfin-compatible apps.
func UserDefaultMediaVisibility(ctx context.Context, repo *repository.Container, userID string) MediaVisibility {
	adminAllowedLibraryIDs := UserAllowedLibraryIDs(ctx, repo, userID)
	visibility := MediaVisibility{
		IncludeNSFW:       AdultContentEnabled(ctx, repo),
		AllowedLibraryIDs: adminAllowedLibraryIDs,
	}
	if repo == nil {
		return visibility
	}
	if UserHidesAdult(ctx, repo, userID) {
		visibility.IncludeNSFW = false
	}
	visibility.HiddenLibraryIDs = hiddenAdultLibraryIDs(ctx, repo, visibility.IncludeNSFW)
	if userID == "" || repo.PlayProfile == nil {
		return visibility
	}
	rows, err := repo.PlayProfile.ListByUser(ctx, userID)
	if err != nil {
		return visibility
	}
	for _, row := range rows {
		if !row.IsDefault {
			continue
		}
		visibility.IncludeNSFW = visibility.IncludeNSFW && row.AllowAdult
		visibility.AllowedLibraryIDs = CombineAllowedLibraryIDs(
			ctx,
			repo,
			adminAllowedLibraryIDs,
			DecodeAllowedLibraryIDs(row.AllowedLibraryIDs),
		)
		visibility.HiddenLibraryIDs = hiddenAdultLibraryIDs(ctx, repo, visibility.IncludeNSFW)
		break
	}
	return visibility
}

// UserAllowedLibraryIDs returns the administrator-enforced library scope.
// Administrators are unrestricted. A normal user with no explicit assignment
// receives a deny-all sentinel so new or unassigned libraries never become
// visible implicitly.
func UserAllowedLibraryIDs(ctx context.Context, repo *repository.Container, userID string) []string {
	if strings.TrimSpace(userID) == "" || repo == nil || repo.User == nil {
		return nil
	}
	// Some isolated service tests intentionally construct repositories without
	// the users table. They have no authenticated user policy to enforce.
	if repo.DB != nil && !repo.DB.Migrator().HasTable(&model.User{}) {
		return nil
	}
	user, err := repo.User.FindByID(ctx, userID)
	if err != nil {
		return []string{noLibraryAccessID}
	}
	if user == nil || user.Role == "admin" {
		return nil
	}
	ids := NormalizeAllowedLibraryIDs(user.AllowedLibraryIDs)
	if len(ids) == 0 {
		return []string{noLibraryAccessID}
	}
	return ids
}

// NormalizeAllowedLibraryIDs trims and de-duplicates persisted/request IDs.
func NormalizeAllowedLibraryIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// CombineAllowedLibraryIDs applies a profile as a further restriction within
// the administrator-enforced scope. Empty means unrestricted on either side.
func CombineAllowedLibraryIDs(ctx context.Context, repo *repository.Container, adminIDs, profileIDs []string) []string {
	adminIDs = NormalizeAllowedLibraryIDs(expandMergedLibraryIDs(ctx, repo, adminIDs))
	profileIDs = NormalizeAllowedLibraryIDs(expandMergedLibraryIDs(ctx, repo, profileIDs))
	if len(adminIDs) == 0 {
		return profileIDs
	}
	if len(profileIDs) == 0 {
		return adminIDs
	}

	allowed := make(map[string]struct{}, len(adminIDs))
	for _, id := range adminIDs {
		allowed[id] = struct{}{}
	}
	intersection := make([]string, 0, len(profileIDs))
	for _, id := range profileIDs {
		if _, ok := allowed[id]; ok {
			intersection = append(intersection, id)
		}
	}
	if len(intersection) == 0 {
		return []string{noLibraryAccessID}
	}
	return intersection
}

// DecodeAllowedLibraryIDs normalises a PlayProfile allowed-library JSON string.
func DecodeAllowedLibraryIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	out := ids[:0]
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			out = append(out, strings.TrimSpace(id))
		}
	}
	return out
}

// LibraryVisibleForUser applies profile library limits and adult-directory
// hiding to a library card/folder.
func LibraryVisibleForUser(ctx context.Context, repo *repository.Container, lib model.Library, visibility MediaVisibility) bool {
	if !lib.Enabled {
		return false
	}
	if len(visibility.AllowedLibraryIDs) > 0 {
		found := false
		for _, id := range visibility.AllowedLibraryIDs {
			if id == lib.ID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if visibility.IncludeNSFW {
		return true
	}
	hiddenLibraryIDs := visibility.HiddenLibraryIDs
	configuredAdultLibraryIDs := AdultLibraryIDs(ctx, repo)
	hasConfiguredAdultLibraries := len(hiddenLibraryIDs) > 0 || len(configuredAdultLibraryIDs) > 0
	if len(hiddenLibraryIDs) == 0 {
		hiddenLibraryIDs = configuredAdultLibraryIDs
	}
	for _, id := range hiddenLibraryIDs {
		if id == lib.ID {
			return false
		}
	}
	if hasConfiguredAdultLibraries {
		return true
	}
	if LibraryLooksAdult(lib) {
		return false
	}
	return true
}

// LibraryLooksAdult catches adult-only roots even before all rows are scraped.
func LibraryLooksAdult(lib model.Library) bool {
	text := strings.ToLower(strings.TrimSpace(lib.Name + " " + lib.Path + " " + lib.Type))
	if text == "" {
		return false
	}
	for _, token := range []string{"成人", "限制级", "nsfw", "adult", "jav", "javdb", "javbus", "9kg", "里番", "番号"} {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}

func AdultLibraryIDs(ctx context.Context, repo *repository.Container) []string {
	if repo == nil || repo.Setting == nil {
		return nil
	}
	raw, err := repo.Setting.Get(ctx, AdultLibraryIDsSettingKey)
	if err != nil {
		return nil
	}
	return DecodeAllowedLibraryIDs(raw)
}

func hiddenAdultLibraryIDs(ctx context.Context, repo *repository.Container, includeNSFW bool) []string {
	if includeNSFW {
		return nil
	}
	return AdultLibraryIDs(ctx, repo)
}
