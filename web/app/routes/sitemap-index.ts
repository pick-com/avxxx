import type {LoaderFunctionArgs} from 'react-router';
import {siteOrigin} from '../lib/api.server';
import {SITEMAP_HEADERS, sitemapIndex} from '../lib/sitemap.server';

// GET /sitemap.xml: index of the per-type, per-language files (lib/sitemap.server.ts).
export async function loader({request}: LoaderFunctionArgs) {
  try {
    return new Response(await sitemapIndex(siteOrigin(request)), {headers: SITEMAP_HEADERS});
  } catch (e) {
    console.error('sitemap index', e);
    return new Response('sitemap temporarily unavailable\n', {status: 503, headers: {'Content-Type': 'text/plain', 'Retry-After': '300', 'Cache-Control': 'no-store'}});
  }
}
