package service

import (
	"strings"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/service/cloud"
)

func TestCloudSubtitleTracksRequireCurrentMediaInSharedDirectory(t *testing.T) {
	entries := []cloud.FileEntry{
		{ID: "/shows/Show.S01E01.zh.srt", Name: "Show.S01E01.zh.srt"},
		{ID: "/shows/Show.S01E02.zh.srt", Name: "Show.S01E02.zh.srt"},
		{ID: "/shows/Show.S01E010.zh.srt", Name: "Show.S01E010.zh.srt"},
		{ID: "/shows/notes.srt", Name: "notes.srt"},
	}

	tracks := cloudSubtitleTracks("openlist", entries, "Show.S01E01", true)
	if len(tracks) != 1 || !strings.Contains(tracks[0].Path, "Show.S01E01.zh.srt") {
		t.Fatalf("tracks=%#v, want only S01E01", tracks)
	}
}

func TestCloudSubtitleTracksAllowGenericNamesForSingleMediaDirectory(t *testing.T) {
	entries := []cloud.FileEntry{
		{ID: "/movies/Subtitles/Chinese.srt", Name: "Chinese.srt"},
		{ID: "/movies/Subtitles/English.ass", Name: "English.ass"},
	}

	tracks := cloudSubtitleTracks("openlist", entries, "Movie", false)
	if len(tracks) != 2 {
		t.Fatalf("tracks=%#v, want both generic subtitles", tracks)
	}
}

func TestCloudVideoFileCount(t *testing.T) {
	entries := []cloud.FileEntry{
		{Name: "Movie.mkv"},
		{Name: "Featurette.mp4"},
		{Name: "Movie.zh.srt"},
		{Name: "Subtitles", IsDir: true},
	}
	if got := cloudVideoFileCount(entries); got != 2 {
		t.Fatalf("cloudVideoFileCount=%d, want 2", got)
	}
}
