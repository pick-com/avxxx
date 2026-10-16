import type {LoaderFunctionArgs} from 'react-router';
import {siteOrigin} from '../lib/api.server';
import {parseFileName, SITEMAP_HEADERS, sitemapFile} from '../lib/sitemap.server';

// GET /sitemaps/{type}-{lang}-{n}.xml (lib/sitemap.server.ts).
export async function loader({request, params}: LoaderFunctionArgs) {
  const file = parseFileName(params.file ?? '');
  const notFound = () => new Response('not found\n', {status: 404, headers: {'Content-Type': 'text/plain'}});
  if (!file) return notFound();
  try {
    const body = await sitemapFile(siteOrigin(request), file.type, file.lang, file.n);
    return body === null ? notFound() : new Response(body, {headers: SITEMAP_HEADERS});
  } catch (e) {
    console.error('sitemap file', params.file, e);
    return new Response('sitemap temporarily unavailable\n', {status: 503, headers: {'Content-Type': 'text/plain', 'Retry-After': '300', 'Cache-Control': 'no-store'}});
  }
}
