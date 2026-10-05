package repository

import (
	"strings"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMediaSearchLIKEExcludesOverviewOnlyMatches(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Media{}); err != nil {
		t.Fatal(err)
	}
	repos := New(db)
	rows := []model.Media{
		{Title: "江南", Path: "/media/target.mkv"},
		{Title: "唐伯虎点秋香", Path: "/media/other.mkv", Overview: "江南四大才子"},
	}
	for i := range rows {
		if err := repos.Media.Upsert(t.Context(), &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := repos.Media.SearchFilteredPage(t.Context(), "江南", 0, 20, MediaQueryFilter{IncludeNSFW: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != rows[0].ID {
		t.Fatalf("total=%d items=%#v; want only title match", total, items)
	}
}

func TestMediaSearchTermSQLPreservesAliasesAndLiteralMatches(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Media{}); err != nil {
		t.Fatal(err)
	}
	repos := New(db)
	rows := []model.Media{
		{Base: model.Base{ID: "chinese"}, Title: "江南", Path: "/media/chinese.mkv"},
		{Base: model.Base{ID: "actor"}, Title: "演员作品", Actors: "江南", Path: "/media/actor.mkv"},
		{Base: model.Base{ID: "code"}, Title: "MIZD-534", Path: "/media/code.mkv"},
		{Base: model.Base{ID: "kana"}, Title: "ナ・ミ", Path: "/media/kana.mkv"},
		{Base: model.Base{ID: "unknown-han"}, Title: "\U00030000・\U00030001", Path: "/media/unknown.mkv"},
		{Base: model.Base{ID: "literal"}, Title: "ABC%_\\123", Path: "/media/literal.mkv"},
		{Base: model.Base{ID: "wildcard-impostor"}, Title: "ABCxxx123", Path: "/media/impostor.mkv"},
	}
	for i := range rows {
		if err := repos.Media.Upsert(t.Context(), &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		query string
		want  string
	}{
		{"江南", "actor,chinese"},
		{"jiangnan", "actor,chinese"},
		{"jn", "actor,chinese"},
		{"mizd534", "code"},
		{"534", "code"},
		{"ナミ", "kana"},
		{"\U00030000\U00030001", "unknown-han"},
		{"abc%_\\123", "literal"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			condition, args := MediaSearchTermSQL(tc.query, "media")
			var got []string
			if err := db.Model(&model.Media{}).Where(condition, args...).Order("id").Pluck("id", &got).Error; err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != tc.want {
				t.Fatalf("ids=%v; want %s", got, tc.want)
			}
		})
	}
}
