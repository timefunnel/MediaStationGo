package service

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func embyFavoritesFixture(t *testing.T) (*EmbyService, string) {
	t.Helper()
	e := newTestEmbyService(t)
	e.SetRuntimeCache(NewRuntimeCacheService(&config.Config{}, zap.NewNop()))
	for _, lib := range []model.Library{
		{Base: model.Base{ID: "movies"}, Name: "Movies", Path: "/test/movies", Type: "movie", Enabled: true},
		{Base: model.Base{ID: "tv"}, Name: "TV", Path: "/test/tv", Type: "tv", Enabled: true},
		{Base: model.Base{ID: "hidden"}, Name: "Hidden", Path: "/test/hidden", Type: "tv", Enabled: true},
	} {
		if err := e.repo.Library.Create(t.Context(), &lib); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"viewer", "other"} {
		user := model.User{Base: model.Base{ID: id}, Username: id, Role: "user", IsActive: true, HideAdult: true, AllowedLibraryIDs: []string{"movies", "tv"}}
		if err := e.repo.User.Create(t.Context(), &user); err != nil {
			t.Fatal(err)
		}
	}
	// Both persisted and virtual series use their existing public identities.
	for _, series := range []model.Series{
		{Base: model.Base{ID: "persisted-series"}, LibraryID: "tv", Title: "B Show"},
		{Base: model.Base{ID: "hidden-series"}, LibraryID: "hidden", Title: "B Show"},
		{Base: model.Base{ID: "nsfw-series"}, LibraryID: "tv", Title: "NSFW Show"},
	} {
		if err := e.repo.DB.Create(&series).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows := []model.Media{{Base: model.Base{ID: "movie"}, LibraryID: "movies", Title: "A Movie", Path: "/test/movies/a.mkv"}}
	for _, show := range []struct{ prefix, library, series, title string }{
		{"persisted", "tv", "persisted-series", "B Show"},
		{"virtual", "tv", "", "C Show"},
		{"hidden", "hidden", "hidden-series", "B Show"},
	} {
		for i := 1; i <= 3; i++ {
			rows = append(rows, model.Media{Base: model.Base{ID: fmt.Sprintf("%s-%d", show.prefix, i)}, LibraryID: show.library, SeriesID: show.series, Title: show.title,
				Path: fmt.Sprintf("/test/%s/%s/Season 01/%d.mkv", show.library, show.title, i), SeasonNum: 1, EpisodeNum: i})
		}
	}
	rows = append(rows, model.Media{Base: model.Base{ID: "nsfw-episode"}, LibraryID: "tv", SeriesID: "nsfw-series", Title: "NSFW Show", NSFW: true,
		Path: "/test/tv/NSFW Show/Season 01/1.mkv", SeasonNum: 1, EpisodeNum: 1})
	if err := e.repo.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := e.InitializeBrowseKeys(t.Context()); err != nil {
		t.Fatal(err)
	}
	return e, e.seriesIDForMedia(&rows[4])
}

func favoriteParams(user string) ItemsParams {
	return ItemsParams{UserID: user, Recursive: true, Filters: []string{"IsFavorite"}, IncludeItemTypes: []string{"Movie", "Series", "Video", "MusicVideo"}, SortBy: "SortName", SortOrder: "Ascending", Limit: 1, OmitMediaSources: true}
}

func TestEmbySeriesFavoriteRoundTrip(t *testing.T) {
	e, virtual := embyFavoritesFixture(t)
	for _, id := range []string{"persisted-series", virtual} {
		for _, favorite := range []bool{true, true, false, false, true} {
			if err := e.SetFavorite(t.Context(), "viewer", id, favorite); err != nil {
				t.Fatalf("SetFavorite(%s, %t): %v", id, favorite, err)
			}
			item, err := e.Item(t.Context(), id, "viewer")
			if err != nil || item == nil || item["UserData"].(map[string]any)["IsFavorite"] != favorite {
				t.Fatalf("favorite readback for %s=%t: item=%v err=%v", id, favorite, item, err)
			}
			other, err := e.Item(t.Context(), id, "other")
			if err != nil || other["UserData"].(map[string]any)["IsFavorite"] != false {
				t.Fatalf("other user's favorite leaked: %v %v", other, err)
			}
		}
	}
	var episodes int64
	if err := e.repo.DB.Model(&model.Favorite{}).Where("media_id IN ?", []string{"persisted-1", "persisted-2", "persisted-3", "virtual-1", "virtual-2", "virtual-3"}).Count(&episodes).Error; err != nil || episodes != 0 {
		t.Fatalf("series favorite propagated to episodes: count=%d err=%v", episodes, err)
	}
}

func TestEmbyFavoriteListMixedPaginationAndVisibility(t *testing.T) {
	e, virtual := embyFavoritesFixture(t)
	for _, id := range []string{"movie", "persisted-series", virtual, "hidden-series", "persisted-1", "nsfw-series"} {
		if err := e.repo.DB.Create(&model.Favorite{UserID: "viewer", MediaID: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// A single-episode favorite is independent of its whole-series favorite.
	for i, id := range []string{"movie", "persisted-series", virtual, ""} {
		p := favoriteParams("viewer")
		p.StartIndex = i
		page, err := e.Items(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
		items := page["Items"].([]map[string]any)
		if fmt.Sprint(page["TotalRecordCount"]) != "3" || page["StartIndex"] != i {
			t.Fatalf("page %d envelope: %v", i, page)
		}
		if id == "" {
			if len(items) != 0 {
				t.Fatalf("end page: %v", page)
			}
			continue
		}
		if len(items) != 1 || items[0]["Id"] != id || items[0]["UserData"].(map[string]any)["IsFavorite"] != true {
			t.Fatalf("page %d: %v", i, page)
		}
		if i > 0 && items[0]["RecursiveItemCount"] != 3 {
			t.Fatalf("favorite series lost episodes: %v", items[0])
		}
	}
	for _, p := range []ItemsParams{
		favoriteParams("other"), favoriteParams(""),
		{UserID: "viewer", ParentID: "hidden", Filters: []string{"IsFavorite"}, IncludeItemTypes: []string{"Series"}},
	} {
		page, err := e.Items(t.Context(), p)
		if err != nil || fmt.Sprint(page["TotalRecordCount"]) != "0" {
			t.Fatalf("favorite visibility: %v %v", page, err)
		}
	}
	for _, p := range []ItemsParams{
		{UserID: "viewer", ParentID: "tv", Filters: []string{"IsFavorite"}, IncludeItemTypes: []string{"Series"}},
		{UserID: "viewer", Filters: []string{"IsFavorite"}, IncludeItemTypes: []string{"Series"}},
	} {
		page, err := e.Items(t.Context(), p)
		if err != nil || fmt.Sprint(page["TotalRecordCount"]) != "2" {
			t.Fatalf("series-only favorites: %v %v", page, err)
		}
	}
	p := favoriteParams("viewer")
	p.SearchTerm = "C Show"
	page, err := e.Items(t.Context(), p)
	if err != nil || fmt.Sprint(page["TotalRecordCount"]) != "1" || page["Items"].([]map[string]any)[0]["Id"] != virtual {
		t.Fatalf("favorite search: %v %v", page, err)
	}
	// Favoriting just one episode must not select its whole series.
	if err := e.repo.DB.Where("user_id = ? AND media_id = ?", "viewer", "persisted-series").Delete(&model.Favorite{}).Error; err != nil {
		t.Fatal(err)
	}
	e.SetRuntimeCache(nil)
	page, err = e.Items(t.Context(), favoriteParams("viewer"))
	if err != nil || fmt.Sprint(page["TotalRecordCount"]) != "2" {
		t.Fatalf("episode favorite selected whole series: %v %v", page, err)
	}
}

func TestEmbyFavoriteWriteRefreshesWarmPages(t *testing.T) {
	e, _ := embyFavoritesFixture(t)
	for _, favorite := range []bool{true, false, true} {
		// Warm every read path before the write.
		for _, p := range []ItemsParams{favoriteParams("viewer"), {UserID: "viewer", ParentID: "tv", Limit: 10}, {UserID: "viewer", ParentID: "movies", Limit: 10}} {
			if _, err := e.Items(t.Context(), p); err != nil {
				t.Fatal(err)
			}
		}
		for _, lib := range []string{"tv", "movies"} {
			if _, err := e.LatestItems(t.Context(), "viewer", lib, 10); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{"persisted-series", "movie"} {
			if err := e.SetFavorite(t.Context(), "viewer", id, favorite); err != nil {
				t.Fatal(err)
			}
		}
		for _, lib := range []string{"tv", "movies"} {
			p := ItemsParams{UserID: "viewer", ParentID: lib, Limit: 10}
			page, err := e.Items(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			latest, err := e.LatestItems(t.Context(), "viewer", lib, 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, items := range [][]map[string]any{page["Items"].([]map[string]any), latest} {
				for _, item := range items {
					if item["Id"] == "persisted-series" || item["Id"] == "movie" {
						if item["UserData"].(map[string]any)["IsFavorite"] != favorite {
							t.Fatalf("warm %s page stale after favorite=%t: %v", lib, favorite, item)
						}
					}
				}
			}
		}
		page, err := e.Items(t.Context(), favoriteParams("viewer"))
		want := "0"
		if favorite {
			want = "2"
		}
		if err != nil || fmt.Sprint(page["TotalRecordCount"]) != want {
			t.Fatalf("warm favorite list stale: %v %v", page, err)
		}
	}
}

func TestEmbyFavoriteFailuresDoNotSucceed(t *testing.T) {
	e, _ := embyFavoritesFixture(t)
	for _, id := range []string{"missing", "hidden-series", "hidden-1", "nsfw-series", "nsfw-episode"} {
		if err := e.SetFavorite(t.Context(), "viewer", id, true); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("inaccessible %s favorite error=%v", id, err)
		}
	}
	if err := e.repo.DB.Migrator().DropTable(&model.Favorite{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Item(t.Context(), "persisted-series", "viewer"); err == nil {
		t.Fatal("series favorite read failure was hidden")
	}
	if _, err := e.Items(t.Context(), ItemsParams{UserID: "viewer", ParentID: "tv"}); err == nil {
		t.Fatal("series list favorite read failure was hidden")
	}
}

func TestEmbyFavoriteWriteFailurePreservesState(t *testing.T) {
	e, _ := embyFavoritesFixture(t)
	p := favoriteParams("viewer")
	before := e.embyItemsCacheKey(t.Context(), "items", p)
	if err := e.repo.DB.Callback().Create().Before("gorm:create").Register("favorite-write-error", func(db *gorm.DB) {
		if db.Statement.Table == "favorites" {
			db.AddError(errors.New("favorite write failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetFavorite(t.Context(), "viewer", "persisted-series", true); err == nil {
		t.Fatal("favorite write error was hidden")
	}
	if after := e.embyItemsCacheKey(t.Context(), "items", p); after != before {
		t.Fatal("failed favorite write invalidated state")
	}
	item, err := e.Item(t.Context(), "persisted-series", "viewer")
	if err != nil || item["UserData"].(map[string]any)["IsFavorite"] != false {
		t.Fatalf("failed favorite write changed state: %v %v", item, err)
	}
}

func TestEmbyFavoriteOldCacheFillCannotRestoreStalePage(t *testing.T) {
	e, _ := embyFavoritesFixture(t)
	params := ItemsParams{UserID: "viewer", ParentID: "tv", IncludeItemTypes: []string{"Series"}, Limit: 10}
	otherKey := e.embySeriesCacheKey(t.Context(), ItemsParams{UserID: "other", ParentID: "tv"})
	read := make(chan struct{})
	release := make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	var paused atomic.Bool
	if err := e.repo.DB.Callback().Query().After("gorm:query").Register("pause-old-favorites-read", func(db *gorm.DB) {
		if db.Statement.Table == "favorites" && paused.CompareAndSwap(false, true) {
			close(read)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		page map[string]any
		err  error
	}
	done := make(chan result, 1)
	go func() {
		page, err := e.Items(t.Context(), params)
		done <- result{page, err}
	}()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("old list did not reach favorites read")
	}
	if err := e.SetFavorite(t.Context(), "viewer", "persisted-series", true); err != nil {
		t.Fatal(err)
	}
	unblock.Do(func() { close(release) })
	select {
	case old := <-done:
		if old.err != nil {
			t.Fatal(old.err)
		}
		for _, item := range old.page["Items"].([]map[string]any) {
			if item["UserData"].(map[string]any)["IsFavorite"] != false {
				t.Fatal("test did not capture pre-write state")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old list did not finish")
	}
	page, err := e.Items(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page["Items"].([]map[string]any) {
		if item["Id"] == "persisted-series" {
			found = true
			if item["UserData"].(map[string]any)["IsFavorite"] != true {
				t.Fatal("late cache fill restored stale favorite state")
			}
		}
	}
	if !found {
		t.Fatal("favorite series missing after late cache fill")
	}
	if got := e.embySeriesCacheKey(t.Context(), ItemsParams{UserID: "other", ParentID: "tv"}); got != otherKey {
		t.Fatal("favorite write invalidated another user's cache")
	}
}

func TestEmbyFavoriteMovieLibraryAggregateCards(t *testing.T) {
	e, _ := embyFavoritesFixture(t)
	series := model.Series{Base: model.Base{ID: "mixed-series"}, LibraryID: "movies", Title: "D Show"}
	if err := e.repo.DB.Create(&series).Error; err != nil {
		t.Fatal(err)
	}
	rows := []model.Media{
		{Base: model.Base{ID: "mixed-1"}, LibraryID: "movies", SeriesID: series.ID, Title: series.Title, Path: "/test/movies/D Show/Season 01/1.mkv", SeasonNum: 1, EpisodeNum: 1},
		{Base: model.Base{ID: "mixed-2"}, LibraryID: "movies", SeriesID: series.ID, Title: series.Title, Path: "/test/movies/D Show/Season 01/2.mkv", SeasonNum: 1, EpisodeNum: 2},
		{Base: model.Base{ID: "part-1"}, LibraryID: "movies", Title: "E Work", Path: "/test/movies/E Work/1.mkv", PartGroupKey: "work", PartGroupTitle: "E Work", PartIndex: 1},
		{Base: model.Base{ID: "part-2"}, LibraryID: "movies", Title: "E Work", Path: "/test/movies/E Work/2.mkv", PartGroupKey: "work", PartGroupTitle: "E Work", PartIndex: 2},
	}
	if err := e.repo.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := e.InitializeBrowseKeys(t.Context()); err != nil {
		t.Fatal(err)
	}
	ids := []string{"movie", series.ID, multipartSeriesID("movies", "work")}
	for _, favorite := range []bool{true, false} {
		p := favoriteParams("viewer")
		p.ParentID = "movies"
		// The mixed movie-library branch also holds a hot favorite page.
		if _, err := e.Items(t.Context(), p); err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if err := e.SetFavorite(t.Context(), "viewer", id, favorite); err != nil {
				t.Fatal(err)
			}
			item, err := e.Item(t.Context(), id, "viewer")
			if err != nil || item["UserData"].(map[string]any)["IsFavorite"] != favorite {
				t.Fatalf("aggregate readback: %v %v", item, err)
			}
		}
		for start, id := range ids {
			p.StartIndex = start
			page, err := e.Items(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			items := page["Items"].([]map[string]any)
			if !favorite {
				if fmt.Sprint(page["TotalRecordCount"]) != "0" || len(items) != 0 {
					t.Fatalf("cancelled movie-library favorites: %v", page)
				}
				continue
			}
			if fmt.Sprint(page["TotalRecordCount"]) != "3" || len(items) != 1 || items[0]["Id"] != id || items[0]["UserData"].(map[string]any)["IsFavorite"] != true {
				t.Fatalf("movie-library favorite page %d: %v", start, page)
			}
			if start > 0 && items[0]["RecursiveItemCount"] != 2 {
				t.Fatalf("incomplete aggregate: %v", items[0])
			}
		}
	}
}
