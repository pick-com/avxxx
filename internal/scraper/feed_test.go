package scraper

import (
	"avxxx/internal/model"
	"strings"
	"testing"
)

func TestFeeds(t *testing.T) {
	cases := []struct{ body, url string }{{`<rss><channel><item><title>One</title><link>/one</link><description>&lt;b&gt;summary&lt;/b&gt;</description></item><item><title>Unsafe</title><link>javascript:alert(1)</link></item></channel></rss>`, "https://example.com/one"}, {`<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>Two</title><link rel="self" href="/feed/2"/><link rel="alternate" href="/two"/><summary>Text</summary></entry></feed>`, "https://example.com/two"}}
	for _, c := range cases {
		v, e := Parse(strings.NewReader(c.body), model.Source{ID: 1, URL: "https://example.com/feed"})
		if e != nil || len(v) != 1 || v[0].URL != c.url {
			t.Fatalf("got %#v, %v", v, e)
		}
	}
}
func TestRejectNonFeed(t *testing.T) {
	_, e := Parse(strings.NewReader(`<html></html>`), model.Source{})
	if e == nil {
		t.Fatal("expected unsupported feed error")
	}
}
