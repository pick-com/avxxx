# XSList fixtures (SYNTHETIC)

**Every file in this directory is synthetic.** No page of xslist.org was saved
to write them: on 2026-10-16 every request from our egress, `robots.txt`
included, got a Cloudflare managed challenge (HTTP 403, `cf-mitigated:
challenge`). The markup follows the site's documented URL scheme
(`/{lang}/model/{id}.html` profiles, `/en/ranking`, `/search?query=…&lg=en`)
and schema.org `Person` microdata, with a `Label: value` and a `dt`/`dd`
fallback. Names, codes and dates are fictional; each manifest row has
`"origin": "synthetic"`, so none counts toward the recorded-fixture target.

Before the source is enabled an operator saves real pages through the VPN
egress (docs/DEPLOYMENT.md 2.8), adds them with `"origin": "recorded"`, and
adjusts `internal/catalog/xslist.go` until `go run ./cmd/fixturecheck` passes.

| file | entity | what it covers |
| --- | --- | --- |
| `discovery/ranking_page1.html` | discovery | two profiles, nav-menu and off-site links (ignored), `rel=next` |
| `discovery/ranking_page2.html` | discovery | last page |
| `discovery/search_hanako.html` | discovery | a name search result (lookup entry point) |
| `performers/model_90001.html` | performer | microdata: name + Japanese name in the heading, aliases, birth date, birthplace, height, `B84(D) / W58 / H86`, blood type, activity, filmography codes |
| `performers/model_90002.html` | performer | no microdata: `dt`/`dd` list, `March 3, 1998`, `86-59-88`, cup, AV debut |
| `performers/model_no_name.html` | performer (expect skip) | only a logo heading |
