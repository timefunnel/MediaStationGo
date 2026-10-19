package service

import "testing"

func TestCloudMetadataNeedsExternalEnrichTriggersOnMissingRating(t *testing.T) {
	// 回归：NFO 有 tmdb_id + 标题 + 海报 + 简介但评分缺失（tMM 嵌套 ratings
	// 读不出时 rating=0），也必须触发外部 enrich 以从 TMDB 补评分。
	meta := &LocalMetadata{
		Title:       "月球",
		TMDbID:      17431,
		PosterURL:   "https://example.com/poster.jpg",
		BackdropURL: "https://example.com/backdrop.jpg",
		Overview:    "月球上的孤独。",
		Rating:      0,
	}
	if !cloudMetadataNeedsExternalEnrich(meta) {
		t.Fatal("expected enrich to trigger when rating is missing, got false")
	}
}

func TestCloudMetadataNeedsExternalEnrichSkipsWhenRatingPresent(t *testing.T) {
	meta := &LocalMetadata{
		Title:       "月球",
		TMDbID:      17431,
		PosterURL:   "https://example.com/poster.jpg",
		BackdropURL: "https://example.com/backdrop.jpg",
		Overview:    "月球上的孤独。",
		Rating:      7.6,
	}
	if cloudMetadataNeedsExternalEnrich(meta) {
		t.Fatal("expected enrich to skip when all fields including rating are present")
	}
}

func TestCloudMetadataNeedsExternalEnrichSkipsWithoutExternalID(t *testing.T) {
	// 没有外部 ID 时，即便评分缺失也不应触发 enrich（无 ID 无法按 ID 查 TMDB）。
	meta := &LocalMetadata{
		Title:  "某部无 ID 电影",
		Rating: 0,
	}
	if cloudMetadataNeedsExternalEnrich(meta) {
		t.Fatal("expected no enrich without external ID")
	}
}

func TestCloudMetadataNeedsExternalEnrichForCloudArtwork(t *testing.T) {
	for _, field := range []string{"poster", "backdrop"} {
		t.Run(field, func(t *testing.T) {
			meta := &LocalMetadata{
				Title: "Complete metadata", TMDbID: 123, Overview: "Overview", Rating: 8,
				PosterURL: "https://images.example/poster.jpg", BackdropURL: "https://images.example/backdrop.jpg",
				HasNFO: true, HasArtwork: true,
			}
			if field == "poster" {
				meta.PosterURL = CloudArtworkURL("openlist", "/Movies/Example/poster.jpg")
			} else {
				meta.BackdropURL = CloudArtworkURL("openlist", "/Movies/Example/fanart.jpg")
			}
			if !cloudMetadataNeedsExternalEnrich(meta) {
				t.Fatal("cloud artwork should request external artwork even when NFO metadata is complete")
			}
			meta.TMDbID = 0
			if cloudMetadataNeedsExternalEnrich(meta) {
				t.Fatal("cloud artwork without an external ID should not trigger an external lookup")
			}
		})
	}
}

func TestCloudMetadataEnrichArtworkPriority(t *testing.T) {
	cloudPoster := CloudArtworkURL("openlist", "/Movies/Example/poster.jpg")
	cloudBackdrop := CloudArtworkURL("openlist", "/Movies/Example/fanart.jpg")
	const remotePoster = "https://images.example/poster.jpg"
	const remoteBackdrop = "https://images.example/backdrop.jpg"
	for _, source := range []struct {
		name     string
		pathHint bool
	}{{"nfo", false}, {"path_hint", true}} {
		for _, tc := range []struct {
			name, poster, backdrop, wantPoster, wantBackdrop string
		}{
			{"external images", remotePoster, remoteBackdrop, remotePoster, remoteBackdrop},
			{"cloud backdrop only", remotePoster, "", remotePoster, cloudBackdrop},
			{"cloud poster only", "", remoteBackdrop, cloudPoster, remoteBackdrop},
			{"no external images", "", "", cloudPoster, cloudBackdrop},
		} {
			t.Run(tc.name+"/"+source.name, func(t *testing.T) {
				meta := &LocalMetadata{Title: "Original", PosterURL: cloudPoster, BackdropURL: cloudBackdrop, HasArtwork: true, PathHint: source.pathHint}
				match := &Match{Title: "External", PosterURL: tc.poster, BackdropURL: tc.backdrop}
				scanner := &ScannerService{}
				got := scanner.mergeCloudMetadataFromExternalIDs(t.Context(), meta, match)
				if got.PosterURL != tc.wantPoster || got.BackdropURL != tc.wantBackdrop {
					t.Fatalf("artwork = %q / %q, want %q / %q", got.PosterURL, got.BackdropURL, tc.wantPoster, tc.wantBackdrop)
				}
				if meta.PosterURL != cloudPoster || meta.BackdropURL != cloudBackdrop || match.PosterURL != tc.poster || match.BackdropURL != tc.backdrop {
					t.Fatal("artwork selection mutated original metadata or the shared external match")
				}
			})
		}
	}
}
