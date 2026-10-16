package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func htmlDoc(t *testing.T, name, url string) Document {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return Document{URL: url, FinalURL: url, Status: 200, ContentType: "text/html", Body: b}
}

// Regression: a javdatabase idol page whose first <h1> is the site logo
// saved "JAV Japanese Porn Actress & Idol Database" as a performer.
func TestJAVDatabasePerformerIgnoresSiteTitle(t *testing.T) {
	doc := htmlDoc(t, "javdatabase/performers/sena-hashimoto-sitetitle.html", "https://www.javdatabase.com/idols/sena-hashimoto/")
	r, err := JAVDatabaseAdapter{}.Parse(context.Background(), doc, Performer)
	if err != nil {
		t.Fatal(err)
	}
	if r.Fields["name"] != "Sena Hashimoto" {
		t.Fatalf("name = %q, want Sena Hashimoto", r.Fields["name"])
	}
	if r.Fields["birth_date"] != "2006-05-10" || r.Fields["name_ja"] != "橋本セナ" {
		t.Errorf("fields: %#v", r.Fields)
	}
	if r.Fields[FieldPageTitle] != "Sena Hashimoto - JAV Japanese Porn Actress & Idol Database" {
		t.Errorf("page_title = %q", r.Fields[FieldPageTitle])
	}
}

func TestJAVDatabasePerformerLogoOnlyIsSkipped(t *testing.T) {
	doc := htmlDoc(t, "javdatabase/performers/logo-only.html", "https://www.javdatabase.com/idols/whoever/")
	_, err := JAVDatabaseAdapter{}.Parse(context.Background(), doc, Performer)
	if !errors.Is(err, ErrSkipRecord) {
		t.Fatalf("err = %v, want ErrSkipRecord", err)
	}
}

func TestJavGuruReleaseCastIgnoresNavigation(t *testing.T) {
	doc := htmlDoc(t, "javguru/releases/abc-001-nav.html", "https://jav.guru/12345/abc-001-example/")
	doc.FinalURL = "https://jav.guru/jav/12345/abc-001-example/"
	r, err := JavGuruAdapter{}.Parse(context.Background(), doc, Release)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := r.Fields["performers"].([]string)
	if want := []string{"Jane Doe", "Mary Roe"}; !slices.Equal(got, want) {
		t.Fatalf("performers = %q, want %q", got, want)
	}
	if r.Fields["title"] != "ABC-001 Example Title" {
		t.Errorf("title = %q", r.Fields["title"])
	}
}

func TestJavGuruPerformerWithoutHeadingUsesTitleSubject(t *testing.T) {
	doc := Document{FinalURL: "https://jav.guru/actress/jane-doe/", Body: []byte(`<html><head><title>Jane Doe - Jav Guru</title></head><body><header><h1>Jav Guru</h1></header><p>Cup: C</p></body></html>`)}
	r, err := JavGuruAdapter{}.Parse(context.Background(), doc, Performer)
	if err != nil {
		t.Fatal(err)
	}
	if r.Fields["name"] != "Jane Doe" {
		t.Fatalf("name = %q", r.Fields["name"])
	}
	// A bare site <title> is never a name.
	doc.Body = []byte(`<html><head><title>JAV Japanese Porn Actress &amp; Idol Database</title></head><body></body></html>`)
	if _, err = (JavGuruAdapter{}).Parse(context.Background(), doc, Performer); !errors.Is(err, ErrSkipRecord) {
		t.Fatalf("err = %v, want ErrSkipRecord", err)
	}
}

func TestXCITYDetailKeyAndName(t *testing.T) {
	for raw, want := range map[string]string{
		"https://xxx.xcity.jp/idol/detail/1001/":    "1001",
		"https://xxx.xcity.jp/idol/detail/?id=2002": "2002",
		"https://xxx.xcity.jp/idol/1003/":           "1003",
		"https://xxx.xcity.jp/idol/detail/":         "",
		"https://xxx.xcity.jp/idol/?page=2":         "",
	} {
		if _, key := xcityClassify(raw); key != want {
			t.Errorf("xcityClassify(%s) key = %q, want %q", raw, key, want)
		}
	}
	recs, err := XCITYAdapter{}.Discover(context.Background(), htmlDoc(t, "xcity/discovery/idol_list.html", "https://xxx.xcity.jp/idol/"))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].SourceKey != "1001" || recs[1].SourceKey != "1002" {
		t.Fatalf("discover = %+v, want two idols keyed 1001 and 1002", recs)
	}
	r, err := XCITYAdapter{}.Parse(context.Background(), htmlDoc(t, "xcity/performers/idol-detail-1001.html", "https://xxx.xcity.jp/idol/detail/1001/"), Performer)
	if err != nil {
		t.Fatal(err)
	}
	if r.Fields["name"] != "山田花子" || r.SourceKey != "1001" {
		t.Fatalf("record = %+v", r)
	}
}

// A performer page with only the site title as heading is skipped, not
// saved and not fatal for the crawl.
func TestCrawlSkipsSiteTitlePerformer(t *testing.T) {
	s := newJAVDatabaseSite(t)
	s.setOverride("/idols/jane-doe/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<html><head><title>JAV Japanese Porn Actress &amp; Idol Database</title></head><body><header class="site-header"><h1>JAV Japanese Porn Actress &amp; Idol Database</h1></header></body></html>`)
	})
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	res, err := newCrawl(1, s, cps, sink, FetchPolicy{}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopComplete || res.Skipped != 1 || res.Records != 3 {
		t.Fatalf("result %+v", res)
	}
	if slices.Contains(sink.saved, "1:jane-doe") {
		t.Fatalf("site-title performer saved: %v", sink.saved)
	}
	if !slices.Contains(cps.fails, "1:catalog_detail:parse_skipped") {
		t.Fatalf("fails = %v", cps.fails)
	}
}

func TestBoilerplateName(t *testing.T) {
	cases := []struct {
		name   string
		titles []string
		want   string
	}{
		{"Sena Hashimoto", nil, ""},
		{"橋本セナ", nil, ""},
		{"Yui Hatano", []string{"Yui Hatano - JAV Database"}, ""},
		{"三上悠亜", []string{"三上悠亜 | XCITY"}, ""},
		{"Jane Doe-Smith", nil, ""},
		{"JAV Japanese Porn Actress & Idol Database", nil, NameBoilerplateWords},
		{"Idol Database", nil, NameBoilerplateWords},
		{"XCITY 公式サイト", nil, NameBoilerplateWords},
		{"女優一覧", nil, NameBoilerplateWords},
		{"Official Site", nil, NameBoilerplateWords},
		{"https://www.javdatabase.com/", nil, NameURLLike},
		{"javdatabase.com", nil, NameURLLike},
		{"An Extremely Long Name That Is Clearly Not A Person At All", nil, NameTooLong},
		{"Aa Bb Cc Dd Ee Ff", nil, NameTooManyWords},
		{"Some Site", []string{"Some Site"}, ""}, // a bare title may just be the name
		{"山田花子", []string{"山田花子"}, ""},
		{"Yui Hatano - Some Site", []string{"Yui Hatano - Some Site"}, NameMatchesPageTitle},
		{"Some Site", []string{"Yui Hatano | Some Site"}, NameMatchesSiteName},
		{"  ", nil, ""},
	}
	for _, c := range cases {
		if got := BoilerplateName(c.name, c.titles...); got != c.want {
			t.Errorf("BoilerplateName(%q, %q) = %q, want %q", c.name, c.titles, got, c.want)
		}
	}
}

func TestSameName(t *testing.T) {
	if !SameName("JAV  Database!", "jav database") || SameName("", "") || SameName("Yui", "Yuia") {
		t.Fatal("SameName")
	}
}
