package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func TestNormalizeAdultCode(t *testing.T) {
	cases := map[string]string{
		"SSIS001.mp4":          "SSIS-001",
		"fc2-ppv-1234567.mkv":  "FC2-PPV-1234567",
		"heyzo_1234.mp4":       "HEYZO-1234",
		"120118_001-carib.mp4": "120118-001",
		"movie.1080p.x264.mkv": "",
	}
	for in, want := range cases {
		if got := normalizeAdultCode(in); got != want {
			t.Fatalf("normalizeAdultCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseAdultDetailHTML(t *testing.T) {
	html := `<html>
<h2 class="title"><strong>SSIS-001 测试标题</strong></h2>
<img class="video-cover" src="/covers/ssis001.jpg">
<a class="sample-box" href="/samples/1.jpg"></a>
<a href="/actors/censored">有碼</a>
<a href="/actors/male">男性演员</a><strong class="symbol male">♂</strong>
<a href="/actors/abc">七沢みあ</a><strong class="symbol female">♀</strong>
<div class="panel-block"><strong>評分:</strong><span class="value">4.7分, 由9人評價</span></div>
<div class="panel-block"><strong>日期:</strong><span class="value">2024-05-01</span></div>
<div class="panel-block"><strong>時長:</strong><span class="value">125 分鍾</span></div>
<div class="panel-block"><strong>片商:</strong><span class="value"><a href="/makers/test">测试片商</a></span></div>
<div class="panel-block"><strong>類別:</strong><span class="value"><a>單體作品</a>, <a>美少女電影</a></span></div>
</html>`
	got := parseAdultDetailHTML(html, "SSIS-001", "javdb", "https://javdb.com/v/abc")
	if got == nil {
		t.Fatal("parseAdultDetailHTML returned nil")
	}
	if got.Title != "测试标题" || got.OriginalName != "SSIS-001" || !got.NSFW {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	if got.PosterURL != "https://javdb.com/covers/ssis001.jpg" || got.BackdropURL != "https://javdb.com/samples/1.jpg" {
		t.Fatalf("unexpected artwork: %+v", got)
	}
	if got.Year != 2024 {
		t.Fatalf("year = %d, want 2024", got.Year)
	}
	if got.ReleaseDate != "2024-05-01" || got.DurationMinutes != 125 || got.Maker != "测试片商" || got.Rating != 4.7 {
		t.Fatalf("detail fields = release %q duration %d maker %q rating %.1f", got.ReleaseDate, got.DurationMinutes, got.Maker, got.Rating)
	}
	if len(got.Genres) != 4 || got.Genres[2] != "單體作品" || got.Genres[3] != "美少女電影" {
		t.Fatalf("genres = %#v", got.Genres)
	}
	if len(got.Actors) != 1 || got.Actors[0] != "七沢みあ" {
		t.Fatalf("actors = %#v", got.Actors)
	}
	if len(got.People) != 1 || got.People[0].Source != "javdb" || got.People[0].SourceID != "abc" {
		t.Fatalf("people = %#v", got.People)
	}
}

func TestAdultProviderDiscoverMovieDetailUsesRequestedJavDBItem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v/QNRVYG" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`<html>
			<h2 class="title"><strong>MIZD-534 测试详情标题</strong></h2>
			<div class="panel-block"><strong>日期:</strong><span class="value">2026-08-04</span></div>
			<div class="panel-block"><strong>時長:</strong><span class="value">240 分鍾</span></div>
			<div class="panel-block"><strong>片商:</strong><span class="value">MOODYZ</span></div>
			<a href="/actors/QV0p9">石川澪</a><strong class="symbol female">♀</strong>
		</html>`))
	}))
	defer server.Close()
	withAdultDefaultBases(t, []string{server.URL})

	provider := NewAdultProvider(zap.NewNop(), nil)
	item, err := provider.DiscoverMovieDetail(context.Background(), "javdb", "QNRVYG", "MIZD-534")
	if err != nil {
		t.Fatal(err)
	}
	if item.ProviderID != "QNRVYG" || item.ProviderURL != server.URL+"/v/QNRVYG" {
		t.Fatalf("provider reference = %q %q", item.ProviderID, item.ProviderURL)
	}
	if item.ReleaseDate != "2026-08-04" || item.DurationMinutes != 240 || item.Maker != "MOODYZ" {
		t.Fatalf("detail item = %+v", item)
	}
	if len(item.People) != 1 || item.People[0].Name != "石川澪" || item.People[0].SourceID != "QV0p9" {
		t.Fatalf("people = %#v", item.People)
	}
}

func TestParseAdultDetailHTMLReadsJSONLDActors(t *testing.T) {
	body := `<html>
<h3>ABF-001 Test</h3>
<script type="application/ld+json">{"@type":"Movie","actor":[{"name":"Actor A"},{"name":"Actor B"}]}</script>
</html>`

	got := parseAdultDetailHTML(body, "ABF-001", "javbus", "https://example.com/ABF-001")
	if got == nil {
		t.Fatal("parseAdultDetailHTML returned nil")
	}
	if len(got.Actors) != 2 || got.Actors[0] != "Actor A" || got.Actors[1] != "Actor B" {
		t.Fatalf("actors = %#v", got.Actors)
	}
}

func TestParseAdultDetailHTMLDerivesDMMPoster(t *testing.T) {
	html := `<html>
	<h3>NACR-833 测试标题</h3>
	<a class="sample-box" href="https://pics.dmm.co.jp/digital/video/h_237nacr00833/h_237nacr00833jp-1.jpg"></a>
	</html>`

	got := parseAdultDetailHTML(html, "NACR-833", "javbus", "https://www.javbus.com/NACR-833")
	if got == nil {
		t.Fatal("parseAdultDetailHTML returned nil")
	}
	if got.PosterURL != "https://pics.dmm.co.jp/digital/video/h_237nacr00833/h_237nacr00833pl.jpg" {
		t.Fatalf("PosterURL = %q", got.PosterURL)
	}
}

func TestParseAdultDetailHTMLClassifiesMGStageArtwork(t *testing.T) {
	html := `<html>
	<h3>ABF-246 测试标题</h3>
	<a class="bigImage" href="https://www.javbus.com/pics/cover/bima_b.jpg"></a>
	<a class="sample-box" href="https://image.mgstage.com/images/prestige/abf/246/cap_e_0_abf-246.jpg"></a>
	</html>`

	got := parseAdultDetailHTML(html, "ABF-246", "javbus", "https://www.javbus.com/ABF-246")
	if got == nil {
		t.Fatal("parseAdultDetailHTML returned nil")
	}
	if got.PosterURL != "https://image.mgstage.com/images/prestige/abf/246/cap_e_0_abf-246.jpg" {
		t.Fatalf("PosterURL = %q", got.PosterURL)
	}
	if got.BackdropURL != "https://image.mgstage.com/images/prestige/abf/246/pb_e_abf-246.jpg" {
		t.Fatalf("BackdropURL = %q", got.BackdropURL)
	}
}

func withAdultDefaultBases(t *testing.T, bases []string) {
	t.Helper()
	original := defaultAdultBases
	defaultAdultBases = append([]string(nil), bases...)
	t.Cleanup(func() {
		defaultAdultBases = original
	})
}

func TestAdultProviderUsesConfiguredMultipleSources(t *testing.T) {
	withAdultDefaultBases(t, nil)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary failure", http.StatusInternalServerError)
	}))
	defer bad.Close()

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<a class="box" href="/v/ssis001"><strong>SSIS-001 多源入口</strong></a>`))
		case "/v/ssis001":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<h2 class="title"><strong>SSIS-001 多源命中标题</strong></h2><img class="video-cover" src="/cover.jpg">`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer good.Close()

	db := newServiceTestDB(t, &model.APIConfig{})
	apiConfig := NewAPIConfigService(zap.NewNop(), repository.New(db), NewCryptoService("", zap.NewNop()))
	baseURL := bad.URL + "\n" + good.URL
	if _, err := apiConfig.Update(context.Background(), "adult", APIConfigPatch{BaseURL: &baseURL}); err != nil {
		t.Fatal(err)
	}

	provider := NewAdultProvider(zap.NewNop(), apiConfig)
	match, err := provider.Search(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatal(err)
	}
	if match == nil || match.Title != "多源命中标题" || match.OriginalName != "SSIS-001" || !match.NSFW {
		t.Fatalf("multi-source adult match = %+v", match)
	}
}

func TestAdultProviderSearchAllReturnsMultipleSourceMatches(t *testing.T) {
	withAdultDefaultBases(t, nil)

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<a class="box" href="/v/adn093"><strong>ADN-093 第一源</strong></a>`))
		case "/v/adn093":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<h2 class="title"><strong>ADN-093 第一源标题</strong></h2><img class="video-cover" src="/cover1.jpg">`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<a class="box" href="/v/adn093"><strong>ADN-093 第二源</strong></a>`))
		case "/v/adn093":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<h2 class="title"><strong>ADN-093 第二源标题</strong></h2><img class="video-cover" src="/cover2.jpg">`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer second.Close()

	db := newServiceTestDB(t, &model.APIConfig{})
	apiConfig := NewAPIConfigService(zap.NewNop(), repository.New(db), NewCryptoService("", zap.NewNop()))
	baseURL := first.URL + "\n" + second.URL
	if _, err := apiConfig.Update(context.Background(), "adult", APIConfigPatch{BaseURL: &baseURL}); err != nil {
		t.Fatal(err)
	}

	provider := NewAdultProvider(zap.NewNop(), apiConfig)
	matches, err := provider.SearchAll(context.Background(), "ADN-093")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches len = %d, want 2: %+v", len(matches), matches)
	}
	if matches[0].Title != "第一源标题" || matches[1].Title != "第二源标题" {
		t.Fatalf("unexpected multi-source titles: %+v", matches)
	}
}

func TestAdultSourceKindRecognizesAdultSources(t *testing.T) {
	cases := map[string]string{
		"https://javdb.com":       "javdb",
		"https://onejav.com":      "onejav",
		"https://javbus.sbs":      "javbus",
		"https://www.javbus.com":  "javbus",
		"https://www.cdnbus.cyou": "javbus",
		"https://www.javsee.cyou": "javbus",
		"https://www.busjav.cyou": "javbus",
		"www.cdnbus.cyou":         "javbus",
		"https://example.invalid": "javdb",
	}
	for in, want := range cases {
		if got := adultSourceKind(in); got != want {
			t.Fatalf("adultSourceKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAdultProviderDefaultBases(t *testing.T) {
	provider := &AdultProvider{}
	got := provider.resolveBases(context.Background())
	want := []string{
		"https://javdb.com",
		"https://onejav.com",
		"https://javbus.sbs",
		"https://www.javbus.com",
		"https://www.cdnbus.cyou",
		"https://www.javsee.cyou",
		"https://www.busjav.cyou",
	}
	if len(got) != len(want) {
		t.Fatalf("resolveBases len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resolveBases[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAdultConfiguredBasesSkipsMissAV(t *testing.T) {
	got := adultConfiguredBases("https://missav.live, javdb.com; https://www.javbus.com")
	want := []string{"https://javdb.com", "https://www.javbus.com"}
	if len(got) != len(want) {
		t.Fatalf("adultConfiguredBases len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("adultConfiguredBases[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseOneJavDetailHTML(t *testing.T) {
	html := `<html><title>FC2PPV4661145 - OneJAV.com - Free JAV Torrents</title><img class="image" src="/fc2.jpg"></html>`
	got := parseOneJavDetailHTML(html, "FC2-PPV-4661145", "https://onejav.com/torrent/fc2ppv4661145")
	if got == nil {
		t.Fatal("parseOneJavDetailHTML returned nil")
	}
	if got.Title != "FC2-PPV-4661145" || got.OriginalName != "FC2-PPV-4661145" || got.PosterURL != "https://onejav.com/fc2.jpg" {
		t.Fatalf("unexpected onejav match: %+v", got)
	}
}
