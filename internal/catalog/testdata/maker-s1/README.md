# Maker catalog fixtures (SYNTHETIC)

**Every file in this directory is synthetic.** No page of the studio's site was
fetched or saved to write them: the markup is hand-written to follow a
plausible official-studio layout (works listing with `?page=N` pagination,
work detail page with a 女優 / 発売日 / シリーズ / レーベル / 監督 / ジャンル /
品番 / 収録時間 table, actress profile with 誕生日 / 身長 / スリーサイズ / 出身地).
Titles, performer names, product numbers and images are fictional. Each HTML
file starts with a `SYNTHETIC FIXTURE` comment and every manifest row has
`"origin": "synthetic"`, so none of them counts toward the 20 performer /
100 release recorded-fixture target of `cmd/fixturecheck`.

The selectors of the built-in profile (`internal/catalog/maker_profiles.json`)
were written against these files only. They are a starting point, not a
statement about the real site: before the source is enabled an operator saves
real pages, adds them here with `"origin": "recorded"`, and adjusts the
profile until `go run ./cmd/fixturecheck` parses them (see
`docs/MAKER_CATALOG.md`).

| file | entity | what it covers |
| --- | --- | --- |
| `listing_page1.html` | discovery | own titles (key -> code `SONE-902`, `SONE-901`), a foreign title (`ABP-123`, dropped by `code_pattern`), an actress link, site-chrome links outside `item_selector`, `rel=next` |
| `listing_page2.html` | discovery | last page (no next link) |
| `release_sone00901.html` | release | full table, Japanese date, absolute cover URL |
| `release_sone00900.html` | release | code printed as `sone00900` (normalized to `SONE-900`), `YYYY/M/D` date, full-width runtime, relative cover URL, empty series/director |
| `release_abp00123_foreign.html` | release (expect skip) | a partner title on the studio site: not this maker's code, so `ErrSkipRecord` |
| `actress_40001.html` | performer | kanji name + romaji, cup size taken from スリーサイズ |
| `actress_no_name.html` | performer (expect skip) | no name block |

Generated once by hand-run script; edit the files directly.
