package acceptance

import (
	"avxxx/internal/catalog"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Canaries planted in source payloads. They are stored in raw_data and
// source_field_values (fields the public evidence does not report) and must
// never appear in a public API response.
const (
	rawCanary     = "RAWCANARY-acceptance-7f3a"
	dmmAPIID      = "ACCEPTANCE-APIID-5e1b"
	dmmAffiliate  = "acceptance-990"
	overrideNote  = "OVERRIDE-REASON-acceptance"
	overrideActor = "OVERRIDE-ACTOR-acceptance"
)

// variant selects the fixture rendering: plain, or "noisy" — the same data
// with reordered listings and lists, different casing and extra whitespace
// (what a site redesign or a sloppy edit produces). Re-ingesting the noisy
// variant must not create any new entity.
type variant struct{ noisy bool }

func (v variant) pick(plain, noisy string) string {
	if v.noisy {
		return noisy
	}
	return plain
}

func (v variant) order(xs []string) []string {
	out := slices.Clone(xs)
	if v.noisy {
		slices.Reverse(out)
	}
	return out
}

func htmlPage(title, body string) string {
	return "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>" + title + "</title></head><body>" +
		"<header class=\"site-header\"><h1 class=\"site-title\"><a href=\"/\">Fixture Site</a></h1></header><main><article>" +
		body + "</article></main></body></html>"
}

func links(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		fmt.Fprintf(&b, "<div class=\"card\"><a href=\"%s\">%s</a></div>\n", pairs[i], pairs[i+1])
	}
	return b.String()
}

func anchors(hrefs, texts []string) string {
	var b strings.Builder
	for i := range texts {
		if hrefs != nil {
			fmt.Fprintf(&b, "<a href=\"%s\">%s</a> ", hrefs[i], texts[i])
		} else {
			fmt.Fprintf(&b, "<a>%s</a> ", texts[i])
		}
	}
	return b.String()
}

// pairs flattens [][2]string{{href,text}} in v's order.
func ordered(v variant, items ...[2]string) []string {
	if v.noisy {
		items = slices.Clone(items)
		slices.Reverse(items)
	}
	var out []string
	for _, it := range items {
		out = append(out, it[0], it[1])
	}
	return out
}

// ---- JAV Database (WordPress archive /movies/, /movies/page/N/)

func loadJAVDatabase(s *site, v variant) {
	s.set("/movies/", htmlPage("Movies Archive - JAV Database",
		links(ordered(v, [2]string{"/movies/juq-743/", "JUQ-743"}, [2]string{"/movies/abc-001/", "ABC-001"},
			[2]string{"/idols/yurine-tsukino/", "Yurine Tsukino"}, [2]string{"/idols/hanako-yamada/", "Hanako Yamada"})...)+
			`<nav class="pagination"><a class="next page-numbers" href="/movies/page/2/">Next</a></nav>`))
	s.set("/movies/page/2/", htmlPage("Movies Archive - Page 2 - JAV Database",
		links(ordered(v, [2]string{"/movies/abc-002/", "ABC-002"}, [2]string{"/idols/jane-doe/", "Jane Doe"})...)))
	release := func(heading, code, cid, date, runtime, studio, director string, genres, idols, idolKeys []string) string {
		g := v.order(genres)
		return htmlPage(heading+" - JAV Database", `<div class="entry-header"><h1 class="entry-title">`+heading+`</h1></div>`+
			`<p>DVD ID: `+code+`</p><p>Content ID: `+cid+`</p><p>Release Date: `+date+`</p><p>Runtime: `+runtime+` min.</p>`+
			`<p>Studio: <a href="/studios/x/">`+studio+`</a></p><p>Director: <a href="/directors/x/">`+director+`</a></p>`+
			`<p>Genre(s): `+anchors(nil, g)+`</p><p>Idol(s)/Actress(es): `+anchors(idolKeys, idols)+`</p>`)
	}
	s.set("/movies/juq-743/", release(v.pick("JUQ-743 Summer Story", "JUQ-743   Summer Story"), v.pick("JUQ-743", " juq-743 "), "juq00743",
		"2024-05-24", "150", v.pick("MADONNA", " Madonna "), "Director X", []string{v.pick("Mature", "MATURE"), v.pick("Drama", " drama ")},
		[]string{v.pick("Yurine Tsukino", "YURINE  TSUKINO")}, []string{"/idols/yurine-tsukino/"}))
	s.set("/movies/abc-001/", release("ABC-001 Example Title", v.pick("ABC-001", "abc-001"), "", "2024-06-01", "120", "Example Maker", "Director Y",
		[]string{"Amateur"}, v.order([]string{v.pick("Jane Doe", "jane doe"), "Hanako Yamada"}), v.order([]string{"/idols/jane-doe/", "/idols/hanako-yamada/"})))
	s.set("/movies/abc-002/", release("ABC-002 Second Story", "ABC-002", "", "2024-07-01", "110", "Example Maker", "Director Y",
		[]string{"Drama"}, []string{"Jane Doe"}, []string{"/idols/jane-doe/"}))
	idol := func(heading, jp, dob, height, cup, measurements string) string {
		return htmlPage(heading+" - JAV Japanese Porn Actress & Idol Database", `<div class="entry-header"><h1 class="entry-title">`+heading+` - JAV Profile</h1></div>`+
			`<p>JP: `+jp+`</p><p>DOB: `+dob+`</p><p>Cup: `+cup+`</p><p>Height: `+height+` cm</p><p>Measurements: `+measurements+`</p>`)
	}
	s.set("/idols/yurine-tsukino/", idol(v.pick("Yurine Tsukino", "  Yurine   Tsukino "), "月野ゆりね", "1990-01-01", "160", "E", "B88-W58-H86 "+rawCanary))
	s.set("/idols/hanako-yamada/", idol("Hanako Yamada", "山田花子", "1999-01-02", "160", "D", "B80-W58-H84"))
	s.set("/idols/jane-doe/", idol(v.pick("Jane Doe", "JANE DOE"), "ジェーン", "1995-01-01", "158", "C", "B82-W57-H83"))
}

// ---- XCITY (/idol/ list with 次へ pagination, /idol/detail/N/, /title/detail/N/)

func loadXCITY(s *site, v variant) {
	s.set("/idol/", htmlPage("女優一覧 | XCITY",
		links(ordered(v, [2]string{"/idol/detail/1001/", v.pick("山田花子", "山田　花子")}, [2]string{"/idol/detail/1002/", "佐藤美咲"})...)+
			`<div class="pager"><a href="/idol/?page=2">次へ</a></div>`))
	s.set("/idol/?page=2", htmlPage("女優一覧 2 | XCITY", links("/title/detail/5001/", "Summer Story XC")))
	idol := func(name, dob, place, height string) string {
		return htmlPage(name+" | XCITY", `<div class="entry-header"><h1 class="entry-title">`+name+`</h1></div>`+
			`<dl><dt>生年月日</dt><dd>`+dob+`</dd><dt>出身地</dt><dd>`+place+`</dd><dt>身長</dt><dd>`+height+`cm</dd></dl>`)
	}
	s.set("/idol/detail/1001/", idol(v.pick("山田花子", "山田　花子 "), "1999-01-02", "東京都", "160"))
	s.set("/idol/detail/1002/", idol("佐藤美咲", "2000-03-04", "大阪府", "155"))
	s.set("/title/detail/5001/", htmlPage("Summer Story XC | XCITY", `<div class="entry-header"><h1 class="entry-title">`+v.pick("Summer Story XC", "SUMMER  STORY  XC")+`</h1></div>`+
		`<dl><dt>発売日</dt><dd>2024-07-15</dd><dt>メーカー</dt><dd>ムーディーズ</dd><dt>レーベル</dt><dd>MOODYZ DIVA</dd></dl>`+
		anchors(v.order([]string{"/genre/10/", "/genre/11/"}), v.order([]string{v.pick("Drama", "DRAMA"), "Romance"}))+
		`<p>出演: <a href="/idol/detail/1001/">`+v.pick("山田花子", "山田 花子")+`</a></p>`))
}

// ---- Jav.Guru (WordPress home /, /page/N/, /jav/<id>/<slug>/, /actress/<slug>/)

func loadJavGuru(s *site, v variant) {
	s.set("/", htmlPage("Jav Guru",
		links(ordered(v, [2]string{"/jav/12345/abc-001-example-title/", "ABC-001 Example Title"}, [2]string{"/actress/jane-doe/", "Jane Doe"})...)+
			`<div class="wp-pagenavi"><a class="nextpostslink" rel="next" href="/page/2/">»</a></div>`))
	s.set("/page/2/", htmlPage("Jav Guru - Page 2", links("/jav/12346/abc-003-third-story/", "ABC-003 Third Story")))
	release := func(heading, code, date, maker string, genres []string, actress [][2]string) string {
		var a strings.Builder
		for _, x := range actress {
			fmt.Fprintf(&a, `<a href="%s">%s</a> `, x[0], x[1])
		}
		g := v.order(genres)
		gh := make([]string, len(g))
		for i := range g {
			gh[i] = "/genre/" + catalog.Slug(g[i]) + "/"
		}
		return htmlPage(heading+" - Jav Guru", `<div class="entry-header"><h1 class="entry-title">`+heading+`</h1></div>`+
			`<ul><li>Code: `+code+`</li><li>Release Date: `+date+`</li><li>Maker: `+maker+`</li></ul>`+
			`<p>Tags: `+anchors(gh, g)+`</p><p>Actress: `+a.String()+`</p>`)
	}
	s.set("/jav/12345/abc-001-example-title/", release("ABC-001 Example Title", v.pick("ABC-001", "abc-001"), "2024-06-01", "Example Maker",
		[]string{"Amateur", v.pick("Drama", "drama")}, [][2]string{{"/actress/jane-doe/", v.pick("Jane Doe", "Jane  Doe")}}))
	s.set("/jav/12346/abc-003-third-story/", release("ABC-003 Third Story", "ABC-003", "2024-08-01", "Example Maker",
		[]string{"Drama"}, [][2]string{{"/actress/mary-roe/", "Mary Roe"}}))
	s.set("/actress/jane-doe/", htmlPage("Jane Doe - Jav Guru", `<div class="entry-header"><h1 class="entry-title">`+v.pick("Jane Doe", "jane doe")+`</h1></div>`+
		`<ul><li>Japanese Name: ジェーン</li><li>Cup: C</li><li>Height: 158 cm</li><li>Birthday: 1995-01-01</li></ul>`))
}

// ---- DMM / FANZA Affiliate API (ItemList and ActressSearch JSON)

const (
	dmmItemList      = "/affiliate/v3/ItemList?floor=videoa&hits=100&output=json&service=digital&site=FANZA&sort=date"
	dmmItemListPage2 = "/affiliate/v3/ItemList?floor=videoa&hits=100&offset=3&output=json&service=digital&site=FANZA&sort=date"
	dmmActresses     = "/affiliate/v3/ActressSearch?hits=100&output=json&sort=-id"
)

func dmmJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func dmmEnvelope(first, count, total int, key string, list []map[string]any) string {
	return dmmJSON(map[string]any{
		// Real responses echo the request parameters, credentials included.
		"request": map[string]any{"parameters": map[string]any{"api_id": dmmAPIID, "affiliate_id": dmmAffiliate}},
		"result":  map[string]any{"status": 200, "result_count": count, "total_count": total, "first_position": first, key: list},
	})
}

func dmmItem(v variant, cid, product, title, date string, maker [2]string, actress [][3]string, genres []string) map[string]any {
	acts := []map[string]any{}
	for _, a := range actress {
		acts = append(acts, map[string]any{"id": a[0], "name": a[1], "ruby": a[2]})
	}
	gs := []map[string]any{}
	for i, g := range v.order(genres) {
		gs = append(gs, map[string]any{"id": 100 + i, "name": g})
	}
	return map[string]any{
		"service_code": "digital", "floor_code": "videoa", "content_id": cid, "product_id": cid, "maker_product": product,
		"title": title, "volume": "150", "date": date,
		"URL":          "https://video.dmm.co.jp/av/content/?id=" + cid,
		"affiliateURL": "https://al.dmm.co.jp/?lurl=https%3A%2F%2Fvideo.dmm.co.jp%2Fav%2Fcontent%2F%3Fid%3D" + cid + "&af_id=" + dmmAffiliate + "&ch=api",
		"iteminfo": map[string]any{
			"maker": []map[string]any{{"id": maker[0], "name": maker[1]}}, "actress": acts, "genre": gs,
		},
	}
}

func loadDMM(s *site, v variant) {
	items := []map[string]any{
		dmmItem(v, "juq00743", v.pick("JUQ-743", "juq-743"), v.pick("サマーストーリー", " サマーストーリー "), "2024-05-24 10:00:00", [2]string{"1234", "マドンナ"},
			[][3]string{{"1044099", "月野ゆりね", "つきのゆりね"}}, []string{"ドラマ", "Mature"}),
		dmmItem(v, "abc00001", "ABC-001", "ABC-001 Example Title (FANZA)", "2024-06-01 10:00:00", [2]string{"999", "Example Maker"},
			[][3]string{{"2001", "ジェーン・ドウ", "じぇーんどう"}}, []string{"Amateur"}),
	}
	if v.noisy {
		slices.Reverse(items)
	}
	s.set(dmmItemList, dmmEnvelope(1, 2, 3, "items", items))
	s.set(dmmItemListPage2, dmmEnvelope(3, 1, 3, "items", []map[string]any{
		dmmItem(v, "118abp00123", "ABP-123", "Fixture item C", "2012-07-20 10:00:00", [2]string{"40136", "プレステージ"}, nil, nil),
	}))
	actresses := []map[string]any{
		{"id": "1044099", "name": "月野ゆりね", "ruby": "つきのゆりね", "height": "160", "birthday": "1990-01-01", "cup": "E", "hobby": rawCanary,
			"listURL": map[string]any{"digital": "https://al.fanza.co.jp/?lurl=x&af_id=" + dmmAffiliate + "&ch=api"}},
		{"id": "2001", "name": "ジェーン・ドウ", "ruby": "じぇーんどう", "height": "158"},
	}
	if v.noisy {
		slices.Reverse(actresses)
	}
	s.set(dmmActresses, dmmEnvelope(1, 2, 2, "actress", actresses))
}

// newDMMSite serves the DMM API fixtures, requires signed requests and
// registers a DMM adapter whose credentials go only to this test host.
func (e *env) newDMMSite(t *testing.T) *site {
	s := newSite(t)
	creds := catalog.DMMCredentials{APIID: dmmAPIID, AffiliateID: dmmAffiliate}
	s.creds = &creds
	host := strings.TrimPrefix(s.srv.URL, "http://")
	if err := e.registry.Register(catalog.NewDMMAdapterWith(creds, host)); err != nil {
		t.Fatal(err)
	}
	return s
}

// ---- RSS

func loadRSS(s *site, v variant) {
	items := []string{
		fmt.Sprintf(`<item><title>%s</title><link>%s</link><description>First</description></item>`, v.pick("Feed One", "  Feed One "), s.url("/posts/1")),
		fmt.Sprintf(`<item><title>Feed Two</title><link>%s</link><description>Second</description></item>`, s.url("/posts/2")),
		fmt.Sprintf(`<item><title>Feed Three</title><link>%s</link><description>Third</description></item>`, s.url("/posts/3")),
	}
	if v.noisy {
		slices.Reverse(items)
	}
	s.set("/feed.xml", `<?xml version="1.0"?><rss version="2.0"><channel><title>Fixture Feed</title>`+strings.Join(items, "")+`</channel></rss>`)
}

// ---- A crawled catalog: every adapter, two passes.

type catalogSites struct {
	jdb, xc, guru, dmm, rss                  *site
	jdbID, xcID, guruID, dmmID, actID, rssID int64
}

func (c *catalogSites) load(v variant) {
	loadJAVDatabase(c.jdb, v)
	loadXCITY(c.xc, v)
	loadJavGuru(c.guru, v)
	loadDMM(c.dmm, v)
	loadRSS(c.rss, v)
}

func (c *catalogSites) catalogSources() []int64 {
	return []int64{c.jdbID, c.xcID, c.guruID, c.dmmID, c.actID}
}

// newCatalog starts every fixture site and registers its source.
func (e *env) newCatalog(t *testing.T) *catalogSites {
	c := &catalogSites{jdb: newSite(t), xc: newSite(t), guru: newSite(t), dmm: e.newDMMSite(t), rss: newSite(t)}
	c.load(variant{})
	c.jdbID = e.addSource("javdatabase-fixture", "javdatabase", c.jdb.url("/movies/"), "structured_database")
	c.xcID = e.addSource("xcity-fixture", "xcity", c.xc.url("/idol/"), "official_maker")
	c.guruID = e.addSource("javguru-fixture", "javguru", c.guru.url("/"), "aggregator")
	c.dmmID = e.addSource("dmm-itemlist-fixture", catalog.DMMAdapterName, c.dmm.url(dmmItemList), "official_catalog")
	c.actID = e.addSource("dmm-actress-fixture", catalog.DMMAdapterName, c.dmm.url(dmmActresses), "official_catalog")
	c.rssID = e.int64(`INSERT INTO sources(name,url,adapter) VALUES('rss-fixture',$1,'rss') RETURNING id`, c.rss.url("/feed.xml"))
	return c
}

// crawlAll runs phase for every source and requires each run to succeed.
func (e *env) crawlAll(c *catalogSites, phase catalog.Phase) {
	e.t.Helper()
	for _, id := range append(c.catalogSources(), c.rssID) {
		if id == c.rssID && phase != catalog.PhaseIncremental {
			continue // feeds have one phase
		}
		e.mustSucceed(e.run(id, phase), fmt.Sprintf("%s of source %d", phase, id))
	}
}
