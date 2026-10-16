# ThePornDB API fixtures (doc-derived)

These files are **not** recorded API responses. No live call was made (no
API token existed when they were written). They are hand-built from the
official OpenAPI document that the API documentation page renders:

- Documentation (Swagger UI): https://api.theporndb.net/docs
- OpenAPI 3.0 document: https://api.theporndb.net/specs?openapi.json
  (`info.version` v3.24.757, read 2026-10-16)

Endpoints and schemas used: `GET /jav` (List/Search Jav), `GET /jav/{identifier}`
(Get Jav), `GET /performers`, `GET /performers/{identifier}`; response
schemas `SceneResource`, `PerformerResource`, `SiteResource`,
`DirectorResource`, `TagResource`, `MediaResource` and `Meta`. List responses
are `{"data": [...], "meta": Meta}`, single resources `{"data": {...}}`.

What is doc-derived and what is synthetic:

- `jav_page1.json`, first scene (`d284e2a0-…`): every value is the example
  value of the `SceneResource` schema (title, slug, external_id, date, url,
  image fields, background / posters, sku `SKU0001`, created, last_updated),
  with the `SiteResource`, `PerformerResource`, `TagResource` and
  `DirectorResource` examples embedded. The schema's example `description`
  is left out on purpose (the adapter does not store synopses). The example
  performer's `extras.gender` is `"Male"` in the schema, so the adapter
  drops it from the credits, exactly as it would for a real male performer.
- `jav_page1.json` second scene, `jav_page2.json` and `jav_scene.json`:
  synthetic values in documented fields only, shaped like JAV (`sku`
  `SONE-156`, `external_id` `sone00156` / `118abp00123`, `type` `JAV`, one
  female and one male performer). Which field carries the JAV product code is
  not documented; these fixtures assume nothing beyond "a code-shaped sku or
  external_id", which is all the adapter relies on.
- `performer.json`: the `PerformerResource` example verbatim (all `extras`
  examples, aliases, posters `MediaResource` example).
- `performers_page1.json`: the same example plus a synthetic performer with
  a partial birthday (`birthday_type` `YEAR`) and a unit-less height.
- `meta`: the `Meta` schema fields (`total`, `per_page`, `current_page`,
  `last`) with values set for the paging tests.

Mapped (see `internal/catalog/theporndb.go`): scene `id` (UUID, source key),
`_id`, `slug`, `title`, `type`, `sku` / `external_id` (DVD code only when one
has the letters-digits shape), `date` -> `release_date`, `site.name` ->
`maker` (site ids kept as `theporndb_site_*`, never as a DMM maker id),
first director, non-male `performers` with their UUIDs, image URLs as
evidence (`image` -> `cover_url`), `url` -> `studio_url`. Performer `id`,
`name`, `aliases`, `disambiguation`, `extras.birthday` -> `birth_date` (only a
full date with no `birthday_type`), `birthplace`, `birthplace_code`,
`nationality`, `height` -> `height_cm` (only `NNNcm`), career years, images.

Deliberately unmapped (documented but ambiguous or out of scope):
`duration` (no unit documented; kept as `theporndb_duration`), `measurements`,
`cupsize`, `waist`, `hips`, `weight` (no unit system; kept as `*_raw`),
`description` / `bio` (synopsis text), `tags` (English tag vocabulary would
mix into release tags), `trailer`, `hashes`, `markers`, `links`, `rating`,
`ethnicity`, `astrology`, `tattoos`, `piercings`, `fake_boobs`.

Not covered (undocumented): rate-limit responses and headers (the spec lists
only 200 / 403 "Unauthorized" / 404 "Not Found"), whether `meta.last` is the
last page number (it is read that way), and whether `orderBy=recently_released`
is newest first.
