// The visitor's country for SSR loaders (region-targeted home slots and
// announcements; internal/api/region.go). SSR calls the API over the
// internal network, so the CDN's country header never reaches it: loaders
// pass it on as ?region=. Browser calls reach the API through the reverse
// proxy with the header intact and the API reads it there.
//
//   CF-IPCountry   set by Cloudflare ("XX" unknown and "T1" Tor are ignored)
//   GEOIP_HEADER   optional: the name of a country header a trusted reverse
//                  proxy / CDN sets and overwrites (e.g. X-Country-Code); the
//                  API reads the same variable
//
// Without either, the region is unknown and region features stay inactive.
// There is no GeoIP database lookup.

const valid = (v: string | null | undefined): string => {
  const code = (v ?? '').trim().toUpperCase();
  return /^[A-Z]{2}$/.test(code) && code !== 'XX' && code !== 'T1' ? code : '';
};

/** ISO 3166-1 alpha-2 country of the request, or '' when unknown. */
export function visitorRegion(request: Request): string {
  const cf = valid(request.headers.get('cf-ipcountry'));
  if (cf) return cf;
  const name = (process.env.GEOIP_HEADER ?? '').trim();
  return name ? valid(request.headers.get(name)) : '';
}

/** Adds region= to the query when the visitor's country is known. */
export function withRegion(query: URLSearchParams, request: Request): URLSearchParams {
  const region = visitorRegion(request);
  if (region) query.set('region', region);
  return query;
}
