package service

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

func embySearchSeriesFixture(t *testing.T) *EmbyService {
	t.Helper()
	e := newTestEmbyService(t)
	libraries := []model.Library{
		{Base: model.Base{ID: "search-anime"}, Name: "动画", Path: "/media/anime", Type: "anime", Enabled: true},
		{Base: model.Base{ID: "search-movies"}, Name: "电影", Path: "/media/movies", Type: "movie", Enabled: true},
	}
	for i := range libraries {
		if err := e.repo.Library.Create(t.Context(), &libraries[i]); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	rows := make([]model.Media, 0, 247)
	for episode := 1; episode <= 243; episode++ {
		row := model.Media{Base: model.Base{ID: fmt.Sprintf("star-episode-%03d", episode), CreatedAt: stamp}, LibraryID: libraries[0].ID,
			Title: "吞噬星空", Path: fmt.Sprintf("/media/anime/吞噬星空/Season 1/E%03d.mkv", episode), SeasonNum: 1, EpisodeNum: episode,
			TMDbID: 1000, Genres: "动画", PosterURL: "https://img.example/star.jpg"}
		if episode == 243 {
			row.CreatedAt = stamp.Add(time.Hour)
			row.Actors = "专属演员"
		}
		rows = append(rows, row)
	}
	for index := 0; index < 3; index++ {
		rows = append(rows, model.Media{Base: model.Base{ID: fmt.Sprintf("star-movie-%d", index), CreatedAt: stamp},
			LibraryID: libraries[1].ID, Title: fmt.Sprintf("吞噬星空电影 %d", index), TMDbID: 2000 + index, Path: fmt.Sprintf("/media/movies/star-%d.mkv", index)})
	}
	version := rows[len(rows)-1]
	version.ID, version.Path, version.Width = "star-movie-version", "/media/movies/star-version.mkv", 3840
	rows = append(rows, version)
	if err := e.repo.DB.CreateInBatches(&rows, 30).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := e.InitializeBrowseKeys(t.Context()); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEmbyGlobalSearchGroups243EpisodesAndHonorsTypes(t *testing.T) {
	e := embySearchSeriesFixture(t)
	for _, tc := range []struct {
		name                     string
		types                    []string
		movies, series, episodes int
	}{
		{name: "default", movies: 3, series: 1},
		{name: "SenPlayer", types: []string{"Movie", "Series", "Video", "Person"}, movies: 3, series: 1},
		{name: "movies", types: []string{"Movie"}, movies: 3},
		{name: "series", types: []string{"Series"}, series: 1},
		{name: "episodes", types: []string{"Episode"}, episodes: 243},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := e.Items(t.Context(), ItemsParams{SearchTerm: "吞噬星空", IncludeItemTypes: tc.types, Recursive: true, Limit: 500, OmitMediaSources: true})
			if err != nil {
				t.Fatal(err)
			}
			want := tc.movies + tc.series + tc.episodes
			if embyTotalRecordCount(t, out) != want {
				t.Fatalf("total=%v, want %d", out["TotalRecordCount"], want)
			}
			counts := map[string]int{}
			for _, item := range out["Items"].([]map[string]any) {
				counts[item["Type"].(string)]++
				if item["Type"] == "Series" && (item["Name"] != "吞噬星空" || item["RecursiveItemCount"] != 243 || item["ChildCount"] != 1) {
					t.Fatalf("incorrect series card: %#v", item)
				}
			}
			if counts["Movie"] != tc.movies || counts["Series"] != tc.series || counts["Episode"] != tc.episodes {
				t.Fatalf("types=%v", counts)
			}
		})
	}
}

func TestEmbyGlobalSearchPaginatesMoviesAndSeriesTogether(t *testing.T) {
	e := embySearchSeriesFixture(t)
	for _, sortBy := range []string{"DateCreated", "SortName", ""} {
		for _, direction := range []string{"Ascending", "Descending"} {
			params := ItemsParams{SearchTerm: "吞噬星空", Recursive: true, Limit: 2, SortBy: sortBy, SortOrder: direction, OmitMediaSources: true}
			seen := map[string]bool{}
			for _, offset := range []int{0, 2, 4, 20} {
				params.StartIndex = offset
				out, err := e.Items(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				if embyTotalRecordCount(t, out) != 4 {
					t.Fatalf("%s/%s offset %d total=%v", sortBy, direction, offset, out["TotalRecordCount"])
				}
				items := out["Items"].([]map[string]any)
				wantLength := 0
				if offset < 4 {
					wantLength = 2
				}
				if len(items) != wantLength {
					t.Fatalf("offset %d items=%d", offset, len(items))
				}
				for _, item := range items {
					id := item["Id"].(string)
					if seen[id] || item["Type"] == "Episode" {
						t.Fatalf("duplicate or ungrouped result: %#v", item)
					}
					seen[id] = true
				}
				again, err := e.Items(t.Context(), params)
				if err != nil || !reflect.DeepEqual(embyItemIDs(t, out), embyItemIDs(t, again)) {
					t.Fatalf("page order changed: %v", err)
				}
			}
			if len(seen) != 4 {
				t.Fatalf("missing public cards: %v", seen)
			}
		}
	}
}

func TestEmbyGlobalSearchSeriesCardOpensExistingHierarchy(t *testing.T) {
	e := embySearchSeriesFixture(t)
	// Only one episode matches this actor; card metadata still describes all 243.
	out, err := e.Items(t.Context(), ItemsParams{SearchTerm: "专属演员", Limit: 10, OmitMediaSources: true})
	if err != nil {
		t.Fatal(err)
	}
	items := out["Items"].([]map[string]any)
	if len(items) != 1 || items[0]["Type"] != "Series" || items[0]["RecursiveItemCount"] != 243 {
		t.Fatalf("partial match=%#v", out)
	}
	id := items[0]["Id"].(string)
	detail, err := e.Item(t.Context(), id, "")
	if err != nil || detail["Type"] != "Series" {
		t.Fatalf("detail=%v err=%v", detail, err)
	}
	seasons, err := e.Items(t.Context(), ItemsParams{ParentID: id, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	seasonItems := seasons["Items"].([]map[string]any)
	if len(seasonItems) != 1 || seasonItems[0]["Type"] != "Season" {
		t.Fatalf("seasons=%v", seasons)
	}
	episodes, err := e.Items(t.Context(), ItemsParams{ShowID: id, ParentID: seasonItems[0]["Id"].(string), IncludeItemTypes: []string{"Episode"}, Recursive: true, Limit: 2, OmitMediaSources: true})
	if err != nil || embyTotalRecordCount(t, episodes) != 243 || len(episodes["Items"].([]map[string]any)) != 2 {
		t.Fatalf("episodes=%v err=%v", episodes, err)
	}
}
