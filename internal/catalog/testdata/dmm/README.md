# DMM/FANZA Affiliate API v3 fixtures (doc-derived)

These files are **not** recorded API responses. No live call was made (no
credentials existed when they were written). They are hand-built from the
field tables and example values of the official API reference:

- ItemList: https://affiliate.dmm.com/api/v3/itemlist.html
- ActressSearch: https://affiliate.dmm.com/api/v3/actresssearch.html

The official pages redirect non-Japanese IPs to an area-error page, so the
tables were read through the English reproduction that cites them,
`docs/products.md` and `docs/actresses.md` of py-dmmjp 0.0.12
(https://pypi.org/project/py-dmmjp/). Re-check against the official pages from
a Japanese IP, or a first real response, before trusting edge cases.

What is doc-derived and what is synthetic:

- `itemlist_page1.json`, item `15dss00145`: every value is the example value
  from the ItemList response table (content_id, product_id, title, volume,
  URL, affiliateURL, imageURL, sample images, sampleMovieURL, price,
  delivery, `date` in the documented `2012/8/3 10:00` form, genre, series,
  maker, actress, label).
- Item `sone00156`: values from the documented sample response (maker,
  actress with `ruby`, director with `ruby`, label, `maker_product`) moved to
  the digital/videoa floor; title is a placeholder.
- `itemlist_page2.json`, item `118abp00123`: synthetic values in documented
  fields only, to exercise content IDs with a numeric label prefix.
- `actresssearch.json`: the first actress is the ActressSearch table example;
  the second uses the documented fields with empty/null values.
- Envelope counts (`result_count`, `total_count`, `first_position`) are set
  for the paging tests. `status` is a number in ItemList and a string in
  ActressSearch, as the documented samples show both.

`{{API_ID}}` and `{{AFFILIATE_ID}}` are filled in by the test server with the
values the request carried, because the API echoes credentials in
`request.parameters` and embeds the affiliate ID in `affiliateURL`,
`sampleMovieURL` and `listURL`. The redaction tests rely on that.

Not covered (undocumented in the pages read): the error response body for
non-200 statuses, rate-limit responses, and whether `sort=date` is
descending. See the adapter report for the open questions.
