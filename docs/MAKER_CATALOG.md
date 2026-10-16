# Official maker catalog adapter (`maker:<slug>`)

Studio sites (S1, MOODYZ, IDEA POCKET, Prestige, …) are the `official_maker`
tier of FINAL_DATA_DESIGN 2.4: default priority 20, so they beat structured
databases (30, JAV Database / XCITY) and aggregators (40) and lose only to
the official catalog (10, DMM). One configuration-driven adapter
(`internal/catalog/maker.go`) serves every studio. A studio is a **profile**,
not new code.

## How it works

| Piece | Where |
| --- | --- |
| Adapter, profile validation, code and date normalization | `internal/catalog/maker.go` |
| Built-in profiles (`s1`, `ideapocket`) | `internal/catalog/maker_profiles.json` (embedded) |
| Extra or overriding profiles | JSON file named by `MAKER_PROFILES_FILE` (same format; same slug replaces the built-in profile) |
| Seeded sources (disabled, not reviewed) | `migrations/034_maker_sources.sql` |
| Synthetic fixtures + manifest | `internal/catalog/testdata/maker-<slug>/` |

- **Registration:** every profile registers as adapter `maker:<slug>` in
  `catalog.Adapters()`, which covers cmd/scraper, cmd/fixturecheck and the
  acceptance suite. Each studio is its own `sources` row (`adapter =
  'maker:<slug>'`, `source_kind = 'official_maker'`). That gives it its own
  robots.txt check, contact User-Agent (`CATALOG_CONTACT`), request spacing
  and daily budget (`source_crawl_policies`; the seed is 5 s and 300
  requests a day), checkpoints and license review.
- **Phases:** baseline, incremental and verify are the generic `catalog.Crawl`
  phases (`internal/crawlrun`). The listing (`listing_url`) must be sorted
  **newest first**. Incremental runs read from the head and stop at the
  previous run's head records (the watermark). Pagination follows
  `next_selector`, then `rel=next` and common markup. A next link is used
  only if it is on the same host and matches `listing_pattern`.
- **Maker scope:** each release carries `maker` (the profile's maker name),
  `maker_scope` (`maker:<Slug(maker)>`, the #82 rules) and `maker_profile`.
  A profile whose maker name has no ASCII slug is rejected, so the scope is
  never `maker-name:<hash>` or bare `maker:`. Use the studio name the other
  sources use (JAV Database "Studio:"). Their DVD-code identifiers then fall
  in the same scope, and no second identifier row is added.
- **Own titles only:** `code_pattern` (regexp on the normalized code, e.g.
  `^(SONE|SSIS)-\d{3,}$`) limits the source to the studio's own titles.
  Partner or guest titles listed on the studio site are dropped at discovery
  (when `code_from_key` is set) or skipped at parse (`ErrSkipRecord`). The
  maker tier therefore never overrides another studio's data.
- **Codes:** `NormalizeMakerCode` takes the code from the 品番 cell, or from
  the detail key when `code_from_key` is set. NFKC is applied, numeric label
  prefixes are dropped and digits are de-padded (`sone00156`, `ＳＯＮＥ１５６`
  and `SONE-156` all become `SONE-156`). The release slug is therefore the
  one JAV Database / DMM already created, so re-crawls never duplicate a
  release.
- **Dates:** `date_layouts` (Go layouts, tried after NFKC, also on the date
  found inside longer text). An unparsable date is dropped and is never
  stored raw.
- **Failure modes:** a release page without a title fails the run
  (`ErrMakerNoTitle`), because the selectors no longer fit and the run
  should fail loudly. A page with no code or a foreign code is skipped. An
  empty listing page fails the run (generic `empty_listing`).

## Profile fields

```jsonc
{
  "slug": "s1",                         // adapter maker:s1, fixtures testdata/maker-s1
  "maker": "S1 NO.1 STYLE",             // stored maker name; scope maker:s1-no1-style
  "base_url": "https://s1s1s1.com",
  "listing_url": "https://s1s1s1.com/works/date",       // seeded sources.url, newest first
  "listing_pattern": "^/works/date(\\?page=\\d+)?$",    // path?query of listing pages
  "detail_pattern": "^/works/detail/([A-Za-z0-9_-]+)/?$", // group 1 = record key
  "performer_pattern": "^/actress/detail/(\\d+)/?$",     // optional
  "item_selector": ".p-worksList .item a[href]",          // listing entries only (not nav)
  "next_selector": ".p-pagination a[rel='next']",
  "code_pattern": "^(SONE|SSIS|...)-\\d{3,}$",
  "code_from_key": true,
  "date_layouts": ["2006年1月2日", "2006/1/2"],
  "release":   { "title": "...", "code": "...", "release_date": "...", "runtime": "...", "label": "...",
                 "series": "...", "director": "...", "performers": "...", "genres": "...", "cover": "img@data-src" },
  "performer": { "name": "...", "name_romaji": "...", "name_kana": "...", "birth_date": "...", "height": "...",
                 "measurements": "...", "cup": "...", "birth_place": "..." }
}
```

Selectors are CSS (goquery/cascadia, including `:has()`, `:containsOwn("…")`
and `+`). Add an `@attr` suffix to read an attribute instead of the text.
Label/value tables are usually best written as
`.table .item:has(.th:containsOwn("発売日")) .td`. Matches inside site chrome
(header, nav, footer, sidebar) are ignored. Unknown JSON keys are rejected,
so a misspelt selector name fails at startup instead of being ignored.

## Shipped profiles

| Profile | Maker (scope) | Seeded source | Status |
| --- | --- | --- | --- |
| `maker:s1` | S1 NO.1 STYLE (`maker:s1-no1-style`) | `https://s1s1s1.com/works/date` | disabled, `review_required`, `terms_reviewed_at` NULL, `terms_url` NULL |
| `maker:ideapocket` | IDEA POCKET (`maker:idea-pocket`) | `https://ideapocket.com/works/date` | disabled, `review_required`, `terms_reviewed_at` NULL, `terms_url` NULL |

The URL shapes, selectors and code prefixes of both profiles are **unverified
assumptions**. They were written against synthetic fixtures; no studio page
was fetched while building this. `review_required` is the gate's "terms not
reviewed yet" status. Until a review is recorded, only manual verify jobs run,
and they write nothing.

## Enabling a studio (operator checklist)

1. **Read the terms and robots.txt by hand** (browser, not the crawler).
   Find the studio's terms of use / site policy page. Confirm automated
   metadata access is not forbidden. Note any attribution or rate
   requirements, and check `https://<host>/robots.txt` for the listing and
   detail paths. If the terms forbid automated access, set `denied` and stop.
2. **Save real pages** from a normal browser session: the first two listing
   pages, at least 5 release detail pages (include an older title, one with
   several performers and one partner/guest title if the site lists any) and
   2 actress pages. Use "Save page as → HTML only". Put them under
   `internal/catalog/testdata/maker-<slug>/` and add manifest rows with the
   page URL, `"origin": "recorded"` and `"captured_at"`. Strip cookies,
   tracking query strings and anything personal. Do not commit cover
   images.
3. **Adjust the profile** in `maker_profiles.json` (or a
   `MAKER_PROFILES_FILE` override for a quick trial) until
   `go run ./cmd/fixturecheck` parses every recorded page with no failures:
   - check `listing_pattern`, `detail_pattern` and `performer_pattern`
     against the saved URLs;
   - set `code_pattern` to the studio's real prefixes;
   - set `maker` to the name JAV Database / XCITY use for the studio.

   Then run `go test ./internal/catalog/`.
4. **Dry run one page** with the real site (robots.txt is honoured, one
   request, nothing written):
   `CATALOG_CONTACT=ops@example.org go run ./cmd/scraper -dry-run -adapter maker:<slug>`
   (add `-url` to probe another listing page). Check the records and
   `next_page`.
5. **Verify job** from the admin source page (phase verify, ≤3 pages).
   Review the parse report: required-field and normalization percentages,
   and skipped pages.
6. **Record the terms review**: `PUT /api/admin/sources/{id}/license` with
   `license_status = terms_confirmed` (or `licensed` with a written
   agreement), `terms_url = <the studio's terms page>`, `reviewed = true` and
   a note. This sets `terms_reviewed_at`/`terms_reviewed_by`. If the terms
   require credit, put the credit text in `sources.attribution_html`.
7. **Tune the crawl policy** if the terms ask for slower access (seed: 5 s,
   300 requests/day, daily discovery). Then enable the source, request one
   baseline, and let the scheduler run incrementals.
8. **Watch** the first baseline: source health, `field_conflicts` for
   titles/dates (the maker now wins them), and no new performers or releases
   for titles JAV Database already had (the acceptance scenario
   `TestMakerRecrawlNoDuplicatesIntegration` checks this with fixtures).

## Adding another studio

Add a profile: a JSON object in `maker_profiles.json` (or the override file).
Add synthetic or, better, recorded fixtures under `testdata/maker-<slug>/`
with a manifest. Add a `sources` row in a new migration, seeded `enabled =
false`, `license_status = 'review_required'`, `terms_reviewed_at` NULL, plus
a `source_crawl_policies` row. Then follow the checklist above. No Go code is
needed unless the site's code or date format is unusual.
