package acceptance

import (
	"avxxx/internal/catalog"
	"fmt"
	"strings"
	"testing"
)

// ---- Official maker catalog (maker:<slug>, S1-style markup, synthetic)

const makerSlug = "madonna-acceptance"

func makerWork(title, code, date string, cast [][2]string) string {
	var act strings.Builder
	for _, c := range cast {
		fmt.Fprintf(&act, `<a href="/actress/detail/%s" class="c-tag">%s</a> `, c[0], c[1])
	}
	return htmlPage(title+" | Official", `<div class="p-workPage"><h2 class="p-workPage__title">`+title+`</h2><div class="p-workPage__table">`+
		`<div class="item"><div class="th">女優</div><div class="td">`+act.String()+`</div></div>`+
		`<div class="item"><div class="th">発売日</div><div class="td">`+date+`</div></div>`+
		`<div class="item"><div class="th">品番</div><div class="td">`+code+`</div></div>`+
		`<div class="item"><div class="th">収録時間</div><div class="td">150分</div></div>`+
		`<div class="item"><div class="th">ジャンル</div><div class="td"><a href="/works/list/genre/1">ドラマ</a></div></div></div></div>`)
}

func loadMaker(s *site) {
	s.set("/works/date", htmlPage("作品一覧 | Official", `<section class="p-worksList">`+
		`<div class="item"><a href="/works/detail/juq00800">JUQ-800</a></div>`+
		`<div class="item"><a href="/works/detail/juq00743">JUQ-743</a></div>`+
		`<div class="item"><a href="/works/detail/abc00001">ABC-001 (partner title)</a></div>`+
		`</section><div class="p-pagination"><a rel="next" href="/works/date?page=2">次へ</a></div>`))
	s.set("/works/date?page=2", htmlPage("作品一覧 2 | Official", `<section class="p-worksList">`+
		`<div class="item"><a href="/works/detail/juq00700">JUQ-700</a></div></section>`))
	yurine := [][2]string{{"1044099", "月野ゆりね"}}
	s.set("/works/detail/juq00800", makerWork("公式 新作タイトル", "JUQ-800", "2024年8月2日", yurine))
	s.set("/works/detail/juq00743", makerWork("サマーストーリー 公式タイトル", "juq00743", "2024年5月24日", yurine))
	s.set("/works/detail/juq00700", makerWork("公式 旧作タイトル", "JUQ-700", "2024/3/1", yurine))
	// Listed but not the maker's: never fetched (code_pattern), and if it
	// were, Parse would skip it.
	s.set("/works/detail/abc00001", makerWork("Partner", "ABC-001", "2024/6/1", nil))
}

func (e *env) newMakerSource(t *testing.T, s *site) int64 {
	t.Helper()
	var p catalog.MakerProfile
	for _, b := range catalog.BuiltinMakerProfiles() {
		if b.Slug == "s1" {
			p = b
		}
	}
	p.Slug, p.Maker, p.CodePattern = makerSlug, "Madonna", `^JUQ-\d{3,}$`
	p.BaseURL, p.ListingURL = s.url(""), s.url("/works/date")
	a, err := catalog.NewMakerAdapter(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.registry.Register(a); err != nil {
		t.Fatal(err)
	}
	return e.addSource("maker-fixture", a.Name(), p.ListingURL, catalog.MakerSourceKind)
}

// TestMakerRecrawlNoDuplicatesIntegration: releases and performers first
// ingested from JAV Database are not duplicated when the official maker
// catalog crawls the same titles (one release per DVD code; Japanese credits
// resolve to the existing performer), the maker's values win the canonical
// fields, and repeated maker baseline / incremental runs add nothing.
func TestMakerRecrawlNoDuplicatesIntegration(t *testing.T) {
	e := newEnv(t)
	jdb, mk := newSite(t), newSite(t)
	loadJAVDatabase(jdb, variant{})
	loadMaker(mk)
	jdbID := e.addSource("javdatabase-fixture", "javdatabase", jdb.url("/movies/"), "structured_database")
	makerID := e.newMakerSource(t, mk)

	e.mustSucceed(e.run(jdbID, catalog.PhaseBaseline), "javdatabase baseline")
	before := e.snap()

	o := e.mustSucceed(e.run(makerID, catalog.PhaseBaseline), "maker baseline")
	after := e.snap()
	if mk.hitCount("/works/detail/abc00001") != 0 {
		t.Error("partner title outside the maker's code pattern was fetched")
	}
	// Exactly the maker-only titles are new; nothing JAV Database had is duplicated.
	if got, want := after.count("releases")-before.count("releases"), 2; got != want {
		t.Errorf("maker baseline added %d releases, want %d (JUQ-800, JUQ-700): %s %v", got, want, diffCounts(before, after, []string{"releases"}), o)
	}
	if d := diffCounts(before, after, []string{"performers", "performer_aliases", "slug_redirects"}); d != "" {
		t.Errorf("maker credits created performers:%s", d)
	}
	for q, want := range map[string]int64{
		`SELECT count(*) FROM releases WHERE slug='juq-743'`:                                                                                    1,
		`SELECT count(*) FROM source_records sr JOIN releases r ON r.id=sr.release_id WHERE r.slug='juq-743'`:                                   2,
		`SELECT count(*) FROM release_identifiers i JOIN releases r ON r.id=i.release_id WHERE r.slug='juq-743' AND identifier_type='dvd_code'`: 1,
		`SELECT count(*) FROM release_performers rp JOIN releases r ON r.id=rp.release_id WHERE r.slug='juq-743'`:                               1,
	} {
		if got := e.int64(q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
	// official_maker (20) beats structured_database (30).
	if got := e.str(`SELECT canonical_title FROM releases WHERE slug='juq-743'`); got != "サマーストーリー 公式タイトル" {
		t.Errorf("juq-743 title = %q, want the maker's", got)
	}
	if got := e.str(`SELECT i.identifier_scope FROM release_identifiers i JOIN releases r ON r.id=i.release_id WHERE r.slug='juq-800'`); got != "maker:madonna" {
		t.Errorf("juq-800 scope = %s", got)
	}
	if got := e.str(`SELECT p.slug FROM release_performers rp JOIN releases r ON r.id=rp.release_id JOIN performers p ON p.id=rp.performer_id WHERE r.slug='juq-800'`); got != "yurine-tsukino" {
		t.Errorf("juq-800 credit -> %s, want yurine-tsukino", got)
	}

	steps := []struct {
		name string
		run  func()
	}{
		{"maker incremental", func() { e.mustSucceed(e.run(makerID, catalog.PhaseIncremental), "maker incremental") }},
		{"maker second baseline", func() { e.mustSucceed(e.run(makerID, catalog.PhaseBaseline), "maker baseline 2") }},
		{"javdatabase after the maker", func() { e.mustSucceed(e.run(jdbID, catalog.PhaseBaseline), "javdatabase baseline 2") }},
		{"maker incremental at the watermark", func() {
			o := e.mustSucceed(e.run(makerID, catalog.PhaseIncremental), "maker incremental 2")
			if o.Result["stop"] != string(catalog.StopWatermark) {
				t.Errorf("second incremental stop = %v, want watermark", o.Result["stop"])
			}
		}},
	}
	for _, st := range steps {
		st.run()
		now := e.snap()
		if d := diffCounts(after, now, identityTables); d != "" {
			t.Errorf("%s created or lost rows:%s", st.name, d)
		}
		if d := keptLastSeen(after, now); d != "" {
			t.Errorf("%s: source records lost last_seen_at:%s", st.name, d)
		}
	}
	if got := e.str(`SELECT canonical_title FROM releases WHERE slug='juq-743'`); got != "サマーストーリー 公式タイトル" {
		t.Errorf("after re-crawls juq-743 title = %q, want the maker's", got)
	}
}
