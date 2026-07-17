package service

import "testing"

func TestParseJavDBMovieList(t *testing.T) {
	body := `<div class="movie-list">
<div class="item"><a href="/v/1Axvvw" class="box" title="Sample title">
<div class="cover"><img src="https://img.example/MVSD-696.jpg"></div>
<div class="video-title"><strong>MVSD-696</strong> Sample title</div>
<div class="score"><span class="value">4.73分, 由181人评价</span></div>
<div class="meta">2026-07-21</div></a></div></div>`
	items := parseJavDBMovieList(body, "https://javdb.example")
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	item := items[0]
	if item.Source != "javdb" || item.MediaType != "adult" || item.OriginalName != "MVSD-696" {
		t.Fatalf("unexpected identity: %#v", item)
	}
	if item.Title != "Sample title" || item.PosterURL != "https://img.example/MVSD-696.jpg" {
		t.Fatalf("unexpected display data: %#v", item)
	}
	if item.ReleaseDate != "2026-07-21" || item.Year != 2026 || item.Rating != 4.73 {
		t.Fatalf("unexpected release metadata: %#v", item)
	}
	if item.ProviderID != "1Axvvw" || item.ProviderURL != "https://javdb.example/v/1Axvvw" {
		t.Fatalf("unexpected provider link: %#v", item)
	}
}

func TestParseJavBusMovieList(t *testing.T) {
	body := `<div class="item"><a class="movie-box" href="https://www.javbus.example/ABF-363">
<div class="photo-frame"><img src="/pics/thumb/abc.jpg" title="JavBus sample title"></div>
<div class="photo-info"><span>JavBus sample title<br><date>ABF-363</date> / <date>2026-07-18</date></span></div>
</a></div>`
	items := parseJavBusMovieList(body, "https://www.javbus.example")
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	item := items[0]
	if item.Source != "javbus" || item.OriginalName != "ABF-363" || item.Title != "JavBus sample title" {
		t.Fatalf("unexpected item: %#v", item)
	}
	if item.PosterURL != "https://www.javbus.example/pics/thumb/abc.jpg" || item.ReleaseDate != "2026-07-18" {
		t.Fatalf("unexpected artwork/date: %#v", item)
	}
}

func TestParseJavDBPerformerList(t *testing.T) {
	body := `<div id="actors" class="actors">
<div class="box actor-box"><a href="/actors/BzpA" title="Actor Alias">
<figure class="image"><img class="avatar" src="https://img.example/BzpA.jpg"></figure>
<strong>Actor Name</strong></a></div>
<a href="/actors/censored">Category</a></div>`
	items := parseJavDBPerformerList(body, "https://javdb.example")
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	item := items[0]
	if item.MediaType != "person" || item.Title != "Actor Name" || item.ProviderID != "BzpA" {
		t.Fatalf("unexpected performer: %#v", item)
	}
	if item.PosterURL != "https://img.example/BzpA.jpg" || item.ProviderURL != "https://javdb.example/actors/BzpA" {
		t.Fatalf("unexpected performer links: %#v", item)
	}
	if len(item.People) != 1 || item.People[0].SourceID != "BzpA" {
		t.Fatalf("unexpected performer metadata: %#v", item.People)
	}
}

func TestNormalizeAdultPerformerImageURL(t *testing.T) {
	if got, ok := NormalizeAdultPerformerImageURL("https://c0.jdbstatic.com/avatars/a.jpg"); !ok || got == "" {
		t.Fatalf("expected jdbstatic image, got %q ok=%v", got, ok)
	}
	if _, ok := NormalizeAdultPerformerImageURL("http://127.0.0.1/private"); ok {
		t.Fatal("local or non-HTTPS image URL must be rejected")
	}
	if _, ok := NormalizeAdultPerformerImageURL("https://example.com/avatar.jpg"); ok {
		t.Fatal("untrusted image host must be rejected")
	}
}
