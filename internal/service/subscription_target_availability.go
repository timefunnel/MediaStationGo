package service

import (
	"context"
	"strings"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func SubscriptionTargetLocalAvailability(ctx context.Context, repo *repository.Container, sub *model.Subscription) (LocalAvailability, error) {
	out := newSubscriptionSeasonAvailability(sub)
	if repo == nil || repo.DB == nil || sub == nil || strings.TrimSpace(sub.LibraryID) == "" || strings.TrimSpace(sub.LibraryRootID) == "" {
		return out, nil
	}
	queries := subscriptionTitleMatchQueries(sub)
	if len(queries) == 0 {
		return out, nil
	}
	var rows []model.Media
	if err := repo.DB.WithContext(ctx).
		Where("library_id = ? AND library_root_id = ?", sub.LibraryID, sub.LibraryRootID).
		Order("season_num ASC, episode_num ASC, created_at DESC").
		Limit(10000).Find(&rows).Error; err != nil {
		return out, err
	}
	targetSeason := subscriptionSeasonNumber(sub)
	for _, row := range rows {
		text := strings.TrimSpace(strings.Join([]string{row.Title, row.OriginalName, row.EpisodeTitle, row.Path}, " "))
		if !availabilityTitleMatchesAny(text, queries) {
			continue
		}
		out.LocalMediaCount++
		out.InLibrary = true
		season := row.SeasonNum
		if season <= 0 {
			season = 1
		}
		if row.EpisodeNum <= 0 {
			if targetSeason == 1 && (row.SeasonNum <= 1) {
				out.HasSeriesPack = true
			}
			continue
		}
		if season != targetSeason {
			continue
		}
		out.ExistingEpisodeKeys[episodeKey(targetSeason, row.EpisodeNum)] = struct{}{}
	}
	return finalizeSubscriptionSeasonAvailability(sub, out), nil
}

func newSubscriptionSeasonAvailability(sub *model.Subscription) LocalAvailability {
	total := 0
	if sub != nil {
		total = sub.TotalEpisodes
	}
	return LocalAvailability{
		TotalEpisodes:       total,
		ExistingEpisodeKeys: map[string]struct{}{},
		MissingEpisodeKeys:  map[string]struct{}{},
	}
}

func finalizeSubscriptionSeasonAvailability(sub *model.Subscription, out LocalAvailability) LocalAvailability {
	season := subscriptionSeasonNumber(sub)
	out.DownloadedEpisodes = len(out.ExistingEpisodeKeys)
	out.MissingEpisodes = missingEpisodesForSeason(out.ExistingEpisodeKeys, out.TotalEpisodes, season)
	out.MissingEpisodeKeys = map[string]struct{}{}
	for _, episode := range out.MissingEpisodes {
		out.MissingEpisodeKeys[episodeKey(season, episode)] = struct{}{}
	}
	return out
}

func mergeLocalAvailabilityForSeason(season int, values ...LocalAvailability) LocalAvailability {
	if season <= 0 {
		season = 1
	}
	out := LocalAvailability{
		ExistingEpisodeKeys: map[string]struct{}{},
		MissingEpisodeKeys:  map[string]struct{}{},
	}
	for _, value := range values {
		if value.TotalEpisodes > out.TotalEpisodes {
			out.TotalEpisodes = value.TotalEpisodes
		}
		out.LocalMediaCount += value.LocalMediaCount
		out.InLibrary = out.InLibrary || value.InLibrary
		out.HasSeriesPack = out.HasSeriesPack || value.HasSeriesPack
		for key := range value.ExistingEpisodeKeys {
			out.ExistingEpisodeKeys[key] = struct{}{}
		}
	}
	out.DownloadedEpisodes = len(out.ExistingEpisodeKeys)
	out.MissingEpisodes = missingEpisodesForSeason(out.ExistingEpisodeKeys, out.TotalEpisodes, season)
	for _, episode := range out.MissingEpisodes {
		out.MissingEpisodeKeys[episodeKey(season, episode)] = struct{}{}
	}
	return out
}

func missingEpisodesForSeason(existing map[string]struct{}, total, season int) []int {
	if total <= 0 {
		return nil
	}
	if season <= 0 {
		season = 1
	}
	missing := make([]int, 0, total)
	for episode := 1; episode <= total; episode++ {
		if _, ok := existing[episodeKey(season, episode)]; ok {
			continue
		}
		missing = append(missing, episode)
	}
	return missing
}
