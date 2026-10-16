# Source evaluations

This page records evaluations of candidate metadata sources: what the source
is, its licensing and terms, what it would add, and the decision. Tiers and
gate statuses follow FINAL_DATA_DESIGN 2.4 and `catalog.Gate` (migration 024).
Evaluations use public documentation, READMEs and terms pages only. The
source itself is never called or scraped while it is being evaluated.

## javinfo (evaluated 2026-10-16)

**Verdict: reject.** It is not to be implemented as an adapter, cross-check
source or fallback, and it is not seeded as a `sources` row. Use for manual
verification is not recommended either (see below). Re-evaluate only if
javinfo publishes terms of service and a data license, and drops piracy
providers from the responses we would use.

### What it is

FINAL_DATA_DESIGN 2.4 / 8 lists "javinfo" in the `third_party_api` tier as
"统一查询 DMM/FANZA、JAV Database、JAVLibrary 等来源，适合批量查询和 fallback" (a
unified query over DMM/FANZA, JAV Database, JAVLibrary and others, suitable
for batch lookup and fallback). The name refers to the **javinfo** project:

- **Hosted API (current):** `https://javinfo.dev`, API host
  `https://api.javinfo.dev`, keys and billing at `https://app.javinfo.dev`,
  also listed on RapidAPI (`https://rapidapi.com/javinfo/api/javinfo`). It
  is a paid lookup by DVD code. `POST /movie` returns the best match,
  `POST /query` returns a list and `/idols` covers performers. Auth is an
  `x-javinfo-key` header. Billing is per call and prepaid with crypto. The
  rate limit is 10 requests/min, or 60/min after a top-up.
- **Open-source code:** GitHub org `https://github.com/javinfo`, with
  `mcp` (MIT), `cli` (MIT) and `legacy`. The original FastAPI scraper
  (`iamrony777/JavInfo-api`, now `javinfo/legacy`) is marked
  "[Deprecated] An API to scrape Japanese adult video data from various
  sites". Docker image: `https://hub.docker.com/r/iamrony777/javinfo-api`.

### Assessment

| Question | Finding |
| --- | --- |
| Official API / data license? | It has a commercial API, but **no data license**. The service resells data it scrapes from third-party sites in real time. The privacy page says API metadata comes from "public third-party providers" that javinfo normalizes. No page grants rights to the data, and javinfo cannot grant rights it does not hold over DMM, JavDB, JAVLibrary, MissAV or JAV Database data. |
| Terms / robots? | **No terms of service.** `https://javinfo.dev/terms` returns 404, and the home page and privacy page (`https://javinfo.dev/privacy`, last updated 2026-08-23) link none. The operator gives only `admin@javinfo.dev`, with no legal entity or jurisdiction. `https://javinfo.dev/robots.txt` allows `*` apart from `/cdn-cgi/` and `/flags/`, with `Content-Signal: search=yes, ai-input=yes, ai-train=yes`. That covers the marketing site, not the API, and says nothing about the rights to the data. |
| Adds data we lack? | **No first-party data.** The providers listed on javinfo.dev are `fanza`/`dmm` (we have the official DMM Affiliate API adapter), `javdatabase` (we have a direct adapter), `javdb` and `javlibrary` (on our own list pending terms/VPN review, so routing them through javinfo would bypass that review), and `missav`, `sextb` and `magneto`. The last three are piracy/streaming/torrent indexes: responses include HLS stream URLs and magnet/PikPak links. The only "new" content is from sources we must not ingest. Field values would also lose provenance: one record mixes several upstreams, so our per-source evidence, tier priority and conflict review would credit "javinfo" for what is really DMM or JavDB data. |
| Maintained? | Yes, actively. `https://javinfo.dev/releases` shows v0.5.2 (2026-07-28) through v0.6.4 (2026-09-16), and the `mcp` and `cli` repos were updated in September 2026. The legacy self-hostable scraper is deprecated. |

### Why not the other options

- **Implement (`third_party_api`, priority 50):** rejected. There is no terms
  of service or license to record in a terms review, so `terms_confirmed`
  could never honestly be set and the gate would keep it verify-only
  forever. It is paid per call with crypto top-ups. It would launder
  JavDB/JAVLibrary data past the reviews those sources still need, and its
  responses carry piracy links we would have to strip. ThePornDB already
  covers the `third_party_api` cross-check role with a documented API.
- **Manual-verify only:** not recommended. Admins can look up a code on the
  DMM / JAV Database pages we already ingest (with provenance). javinfo
  would add only unvetted upstreams, and recording it as a source would
  suggest it was reviewed.
- **Self-hosting the legacy scraper:** rejected. It is deprecated, and it
  would make us the scraper of the same upstreams, each of which needs its
  own adapter and review (that is the roadmap's per-source process).

If the design doc's description is revisited, replace javinfo in the
`third_party_api` tier examples with ThePornDB only.

### Sources

- https://javinfo.dev/ (service description, providers, pricing, rate limits)
- https://javinfo.dev/privacy (operator contact, data origin statement)
- https://javinfo.dev/terms (404, no terms page)
- https://javinfo.dev/robots.txt
- https://javinfo.dev/releases (release history)
- https://github.com/javinfo (organization, repository licenses and activity)
- https://github.com/iamrony777/JavInfo-api (redirects to the deprecated `javinfo/legacy`)
- https://hub.docker.com/r/iamrony777/javinfo-api
- https://rapidapi.com/javinfo/api/javinfo (listing linked from javinfo.dev; not read)
