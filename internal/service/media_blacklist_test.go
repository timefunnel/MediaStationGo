package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"gorm.io/gorm"
)

func TestMediaBlacklistWholeSeriesAndAccountIsolation(t *testing.T) {
	e, virtual := embyFavoritesFixture(t)
	for _, id := range []string{"persisted-2", "virtual-2", "movie"} {
		for _, blocked := range []bool{true, true, false, false, true} {
			if err := e.SetMediaBlacklisted(t.Context(), "viewer", id, blocked); err != nil {
				t.Fatal(err)
			}
			state, err := e.MediaBlacklisted(t.Context(), "viewer", id)
			if err != nil || state != blocked {
				t.Fatalf("state=%v blocked=%v err=%v", state, blocked, err)
			}
		}
	}
	for _, id := range []string{"movie", "persisted-series", virtual, "persisted-1", "persisted-3", "virtual-1", seasonID("persisted-series", 1), seasonID(virtual, 1)} {
		if item, err := e.Item(t.Context(), id, "viewer"); err != nil || item != nil {
			t.Fatalf("blocked detail %s leaked: %v %v", id, item, err)
		}
		if item, err := e.Item(t.Context(), id, "other"); err != nil || item == nil {
			t.Fatalf("other account lost %s: %v %v", id, item, err)
		}
	}
	for _, id := range []string{"movie", "persisted-3", "persisted-series", virtual} {
		if item, err := e.PlaybackInfo(t.Context(), id, "viewer"); err != nil || item != nil {
			t.Fatalf("blocked playback %s leaked: %v %v", id, item, err)
		}
	}
	// New seasons inherit the existing series restriction without copying flags
	// to episodes or enumerating them during a blacklist mutation.
	row := model.Media{Base: model.Base{ID: "new-season"}, LibraryID: "tv", SeriesID: "persisted-series", Title: "B Show", Path: "/test/tv/B Show/Season 02/1.mkv", SeasonNum: 2, EpisodeNum: 1}
	if err := e.repo.Media.Upsert(t.Context(), &row); err != nil {
		t.Fatal(err)
	}
	if item, err := e.Item(t.Context(), row.ID, "viewer"); err != nil || item != nil {
		t.Fatalf("new season leaked: %v %v", item, err)
	}
	items, err := e.ListMediaBlacklist(t.Context(), "viewer")
	if err != nil || len(items) != 3 {
		t.Fatalf("blacklist=%v err=%v", items, err)
	}
	for _, item := range items {
		if item.Media == nil {
			t.Fatal("MSG management card disappeared")
		}
		if err := e.RemoveBlacklistItem(t.Context(), "other", item.ID); err != nil {
			t.Fatal(err)
		}
	}
	if items, err := e.ListMediaBlacklist(t.Context(), "viewer"); err != nil || len(items) != 3 {
		t.Fatalf("another account removed entries: %v %v", items, err)
	}
	if err := e.SetMediaBlacklisted(t.Context(), "viewer", "persisted-1", false); err != nil {
		t.Fatal(err)
	}
	if item, err := e.Item(t.Context(), "new-season", "viewer"); err != nil || item == nil {
		t.Fatalf("unblock through another episode failed: %v %v", item, err)
	}
}

func TestMediaBlacklistSQLPaginationVersionsAndWarmCaches(t *testing.T) {
	e, _ := embyFavoritesFixture(t)
	for _, row := range []model.Media{
		{Base: model.Base{ID: "movie-version"}, LibraryID: "movies", Title: "A Movie", Path: "/test/movies/a-4k.mkv", Width: 3840},
		{Base: model.Base{ID: "movie-b"}, LibraryID: "movies", Title: "B Movie", Path: "/test/movies/b.mkv"},
		{Base: model.Base{ID: "movie-c"}, LibraryID: "movies", Title: "C Movie", Path: "/test/movies/c.mkv"},
	} {
		if err := e.repo.Media.Upsert(t.Context(), &row); err != nil {
			t.Fatal(err)
		}
	}
	p := ItemsParams{UserID: "viewer", ParentID: "movies", IncludeItemTypes: []string{"Movie"}, SortBy: "SortName", SortOrder: "Ascending", Limit: 1, OmitMediaSources: true}
	for _, user := range []string{"viewer", "other"} {
		p.UserID = user
		if _, err := e.Items(t.Context(), p); err != nil {
			t.Fatal(err)
		}
		if _, err := e.LatestItems(t.Context(), user, "movies", 20); err != nil {
			t.Fatal(err)
		}
	}
	p.UserID = "viewer"
	otherKey := e.embyItemsCacheKey(t.Context(), "items", ItemsParams{UserID: "other"})
	for _, blocked := range []bool{true, false, true} {
		if err := e.SetMediaBlacklisted(t.Context(), "viewer", "movie", blocked); err != nil {
			t.Fatal(err)
		}
		for index := 0; index <= 2; index++ {
			p.StartIndex = index
			page, err := e.Items(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			wantTotal := 3
			wantID := []string{"movie-version", "movie-b", "movie-c"}[index]
			wantLen := 1
			if blocked {
				wantTotal = 2
				wantID = []string{"movie-b", "movie-c", ""}[index]
				if index == 2 {
					wantLen = 0
				}
			}
			items := page["Items"].([]map[string]any)
			if fmt.Sprint(page["TotalRecordCount"]) != fmt.Sprint(wantTotal) || len(items) != wantLen || wantLen > 0 && items[0]["Id"] != wantID {
				t.Fatalf("blocked=%v page=%d: %v", blocked, index, page)
			}
		}
		latest, err := e.LatestItems(t.Context(), "viewer", "movies", 20)
		if err != nil || len(latest) != map[bool]int{true: 2, false: 3}[blocked] {
			t.Fatalf("warm latest=%v err=%v", latest, err)
		}
	}
	if got := e.embyItemsCacheKey(t.Context(), "items", ItemsParams{UserID: "other"}); got != otherKey {
		t.Fatal("invalidated another account's cache")
	}
	if blocked, err := e.MediaBlacklisted(t.Context(), "viewer", "movie-version"); err != nil || !blocked {
		t.Fatalf("version status=%v %v", blocked, err)
	}
	if item, err := e.Item(t.Context(), "movie-version", "viewer"); err != nil || item != nil {
		t.Fatalf("version detail leaked: %v %v", item, err)
	}
}

func TestMediaBlacklistEmbyDiscoverySurfaces(t *testing.T) {
	e, virtual := embyFavoritesFixture(t)
	for _, id := range []string{"movie", "persisted-series"} {
		if err := e.SetFavorite(t.Context(), "viewer", id, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"movie", "persisted-1", "virtual-1"} {
		if err := e.repo.DB.Create(&model.PlaybackHistory{UserID: "viewer", MediaID: id, PositionMs: 1000, DurationMs: 100000, WatchedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
		if err := e.SetMediaBlacklisted(t.Context(), "viewer", id, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []ItemsParams{
		{UserID: "viewer", Filters: []string{"IsFavorite"}, IncludeItemTypes: []string{"Movie", "Series"}, Recursive: true},
		{UserID: "viewer", ParentID: "tv", IncludeItemTypes: []string{"Series"}},
		{UserID: "viewer", SearchTerm: "Show", Recursive: true},
		{UserID: "viewer", ShowID: "persisted-series", ParentID: "persisted-series", IncludeItemTypes: []string{"Episode"}},
		{UserID: "viewer", ParentID: "persisted-series", IncludeItemTypes: []string{"Season"}},
		{UserID: "viewer", ParentID: virtual, IncludeItemTypes: []string{"Season"}},
		{UserID: "viewer", IDs: []string{"movie", "persisted-series", "persisted-1", virtual}},
	} {
		page, err := e.Items(t.Context(), p)
		if err != nil || fmt.Sprint(page["TotalRecordCount"]) != "0" {
			t.Fatalf("filtered surface %+v: %v %v", p, page, err)
		}
	}
	if latest, err := e.LatestItems(t.Context(), "viewer", "tv", 20); err != nil || len(latest) != 0 {
		t.Fatalf("series latest=%v %v", latest, err)
	}
	if page, err := e.ResumeItems(t.Context(), "viewer"); err != nil || fmt.Sprint(page["TotalRecordCount"]) != "0" {
		t.Fatalf("resume=%v %v", page, err)
	}
	if page, err := e.NextUpItems(t.Context(), NextUpParams{UserID: "viewer", SeriesIDs: []string{"persisted-series", virtual}}); err != nil || fmt.Sprint(page["TotalRecordCount"]) != "0" {
		t.Fatalf("next up=%v %v", page, err)
	}
	if counts, err := e.ItemCounts(t.Context(), "viewer"); err != nil || fmt.Sprint(counts["ItemCount"]) != "0" {
		t.Fatalf("counts=%v %v", counts, err)
	}
	var favorites int64
	if err := e.repo.DB.Model(&model.Favorite{}).Where("user_id = ?", "viewer").Count(&favorites).Error; err != nil || favorites != 2 {
		t.Fatalf("blacklisting changed saved favorites: count=%d err=%v", favorites, err)
	}
}

func TestMediaBlacklistErrorsAndIndexedSQL(t *testing.T) {
	e, _ := embyFavoritesFixture(t)
	for _, id := range []string{"missing", "hidden-1", "nsfw-episode"} {
		if err := e.SetMediaBlacklisted(t.Context(), "viewer", id, true); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("inaccessible %s: %v", id, err)
		}
	}
	q := e.applyMediaBlacklist(e.repo.DB.Model(&model.Media{}), "viewer").Select("media.id")
	stmt := q.Session(&gorm.Session{DryRun: true}).Find(&[]model.Media{}).Statement
	var plan []struct{ Detail string }
	if err := e.repo.DB.Raw("EXPLAIN QUERY PLAN "+stmt.SQL.String(), stmt.Vars...).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	indexed := 0
	for _, step := range plan {
		if strings.Contains(step.Detail, "SEARCH b USING INDEX uniq_blacklist_target") {
			indexed++
		}
	}
	if indexed != 2 {
		t.Fatalf("blacklist anti-joins did not use the composite index: %+v", plan)
	}
	if err := e.repo.DB.Callback().Create().Before("gorm:create").Register("fail-blacklist", func(db *gorm.DB) {
		if db.Statement.Table == "media_blacklists" {
			db.AddError(errors.New("blacklist unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	before := e.userBlacklistCacheRevision(t.Context(), "viewer")
	if err := e.SetMediaBlacklisted(t.Context(), "viewer", "movie", true); err == nil {
		t.Fatal("failed write reported success")
	}
	if got := e.userBlacklistCacheRevision(t.Context(), "viewer"); got != before {
		t.Fatal("failed write invalidated cache")
	}
	if err := e.repo.DB.Migrator().DropTable(&model.MediaBlacklist{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Items(t.Context(), ItemsParams{UserID: "viewer", ParentID: "movies"}); err == nil {
		t.Fatal("blacklist lookup failure was ignored")
	}
}

func TestMediaBlacklistRetainsEmptySeriesRestrictionAndCanRemoveMissingCard(t *testing.T) {
	e, _ := embyFavoritesFixture(t)
	if err := e.SetMediaBlacklisted(t.Context(), "viewer", "persisted-1", true); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.DB.Where("series_id = ?", "persisted-series").Delete(&model.Media{}).Error; err != nil {
		t.Fatal(err)
	}
	if item, err := e.Item(t.Context(), "persisted-series", "viewer"); err != nil || item != nil {
		t.Fatalf("empty series bypassed blacklist: %v %v", item, err)
	}
	items, err := e.ListMediaBlacklist(t.Context(), "viewer")
	if err != nil || len(items) != 1 || items[0].Media != nil {
		t.Fatalf("missing-card entry=%v %v", items, err)
	}
	if err := e.RemoveBlacklistItem(t.Context(), "viewer", items[0].ID); err != nil {
		t.Fatal(err)
	}
	if item, err := e.Item(t.Context(), "persisted-series", "viewer"); err != nil || item == nil {
		t.Fatalf("could not unblock empty series: %v %v", item, err)
	}
}
