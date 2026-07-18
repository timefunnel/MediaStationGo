package service

import (
	"context"
	"strings"
	"sync"
)

type DiscoverCatalogSearchResult struct {
	Items  []ExternalMediaResult
	Errors map[string]error
}

type discoverCatalogSearchTask struct {
	key string
	run func() ([]ExternalMediaResult, error)
}

type discoverCatalogSearchTaskResult struct {
	key   string
	items []ExternalMediaResult
	err   error
}

// SearchDiscoverCatalog searches the configured catalog providers in
// parallel. Callers decide which providers the current user may access by
// passing nil for disabled providers.
func SearchDiscoverCatalog(
	ctx context.Context,
	query string,
	tmdb *TMDbProvider,
	douban *DoubanProvider,
	bangumi *BangumiProvider,
	adult *AdultProvider,
) DiscoverCatalogSearchResult {
	query = strings.TrimSpace(query)
	result := DiscoverCatalogSearchResult{Items: []ExternalMediaResult{}, Errors: map[string]error{}}
	if query == "" {
		return result
	}

	tasks := make([]discoverCatalogSearchTask, 0, 6)
	if tmdb != nil {
		tasks = append(tasks,
			discoverCatalogSearchTask{key: "tmdb_movie", run: func() ([]ExternalMediaResult, error) {
				matches, err := tmdb.SearchMovieCandidates(ctx, query, 0)
				return externalMediaResultsFromMatches("tmdb", "movie", matches, 12), err
			}},
			discoverCatalogSearchTask{key: "tmdb_tv", run: func() ([]ExternalMediaResult, error) {
				matches, err := tmdb.SearchTVCandidates(ctx, query, 0)
				return externalMediaResultsFromMatches("tmdb", "tv", matches, 12), err
			}},
		)
	}
	if douban != nil {
		tasks = append(tasks, discoverCatalogSearchTask{key: "douban", run: func() ([]ExternalMediaResult, error) {
			match, err := douban.SearchMatch(ctx, query)
			if err != nil || match == nil {
				return nil, err
			}
			mediaType := normalizeDoubanType(match.MediaType, "")
			return []ExternalMediaResult{externalMediaResultFromMatch("douban", mediaType, match)}, nil
		}})
	}
	if bangumi != nil {
		tasks = append(tasks, discoverCatalogSearchTask{key: "bangumi", run: func() ([]ExternalMediaResult, error) {
			match, err := bangumi.Search(ctx, query)
			if err != nil || match == nil {
				return nil, err
			}
			return []ExternalMediaResult{externalMediaResultFromMatch("bangumi", "anime", match)}, nil
		}})
	}
	if adult != nil {
		if normalizeAdultCode(query) == "" {
			tasks = append(tasks, discoverCatalogSearchTask{key: "javdb_performer", run: func() ([]ExternalMediaResult, error) {
				return adult.SearchPerformers(ctx, query)
			}})
		}
		tasks = append(tasks, discoverCatalogSearchTask{key: "javdb_adult", run: func() ([]ExternalMediaResult, error) {
			return adult.SearchMovies(ctx, query)
		}})
	}

	results := make(chan discoverCatalogSearchTaskResult, len(tasks))
	var wait sync.WaitGroup
	for _, task := range tasks {
		task := task
		wait.Add(1)
		go func() {
			defer wait.Done()
			items, err := task.run()
			results <- discoverCatalogSearchTaskResult{key: task.key, items: items, err: err}
		}()
	}
	wait.Wait()
	close(results)

	byKey := make(map[string]discoverCatalogSearchTaskResult, len(tasks))
	for taskResult := range results {
		byKey[taskResult.key] = taskResult
	}
	for _, task := range tasks {
		taskResult := byKey[task.key]
		if taskResult.err != nil {
			result.Errors[task.key] = taskResult.err
			continue
		}
		result.Items = append(result.Items, taskResult.items...)
	}
	result.Items = dedupeExternalMedia(result.Items)
	return result
}

func externalMediaResultsFromMatches(source, mediaType string, matches []*Match, limit int) []ExternalMediaResult {
	if limit <= 0 || limit > len(matches) {
		limit = len(matches)
	}
	items := make([]ExternalMediaResult, 0, limit)
	for _, match := range matches[:limit] {
		if match == nil || strings.TrimSpace(match.Title) == "" {
			continue
		}
		items = append(items, externalMediaResultFromMatch(source, mediaType, match))
	}
	return items
}
