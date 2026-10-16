package catalog

import (
	"context"
	"testing"
)

// Label parsing regressions found by the acceptance suite (internal/acceptance).

func TestJAVDatabaseEmptyLabelHasNoValue(t *testing.T) {
	body := `<html><body><div class="entry-header"><h1 class="entry-title">ABC-001 Example</h1></div>` +
		`<p>DVD ID: ABC-001</p><p>Content ID: </p><p>Release Date: 2024-06-01</p><p>Runtime: 120 min.</p></body></html>`
	r, err := JAVDatabaseAdapter{}.Parse(context.Background(), Document{URL: "https://www.javdatabase.com/movies/abc-001/", FinalURL: "https://www.javdatabase.com/movies/abc-001/", Body: []byte(body)}, Release)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Fields["content_id"]; got != "" {
		t.Errorf("content_id = %q, want empty (the label had no value)", got)
	}
	if got := r.Fields["release_date"]; got != "2024-06-01" {
		t.Errorf("release_date = %q", got)
	}
}

func TestJavGuruLabelValues(t *testing.T) {
	body := `<html><body><div class="entry-header"><h1 class="entry-title">ABC-003 Third Story</h1></div>` +
		`<ul><li>Code: ABC-003</li><li>Release Date: 2024-08-01</li><li>Maker: Example Maker</li></ul></body></html>`
	r, err := JavGuruAdapter{}.Parse(context.Background(), Document{URL: "https://jav.guru/jav/12346/abc-003/", FinalURL: "https://jav.guru/jav/12346/abc-003/", Body: []byte(body)}, Release)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"dvd_code": "ABC-003", "release_date": "2024-08-01", "maker": "Example Maker"} {
		if got := r.Fields[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestXCITYDefinitionListLabels(t *testing.T) {
	body := `<html><body><div class="entry-header"><h1 class="entry-title">Summer Story XC</h1></div>` +
		`<dl><dt>発売日</dt><dd>2024-07-15</dd><dt>メーカー</dt><dd>ムーディーズ</dd><dt>レーベル</dt><dd>MOODYZ DIVA</dd></dl>` +
		`<a href="/genre/10/">Drama</a><p>出演: <a href="/idol/detail/1001/">山田花子</a></p></body></html>`
	r, err := XCITYAdapter{}.Parse(context.Background(), Document{URL: "https://xxx.xcity.jp/title/detail/5001/", FinalURL: "https://xxx.xcity.jp/title/detail/5001/", Body: []byte(body)}, Release)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"release_date": "2024-07-15", "maker": "ムーディーズ", "label": "MOODYZ DIVA"} {
		if got := r.Fields[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}
