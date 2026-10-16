import type {LoaderFunctionArgs} from 'react-router';
import {siteOrigin} from '../lib/api.server';
import {DEFAULT_LANG, LANGS} from '../lib/langs';

// GET /robots.txt (PRD 13 SEO). Public pages are crawlable in every language
// prefix; the back office, the API, personal pages and search result URLs
// are not. Pages that are crawlable but must stay out of the index (404s,
// filtered lists) carry <meta name="robots" content="noindex"> instead.
//
// Rules are spelled out per language prefix and anchored with `$`, so a slug
// such as /performers/mei-x or /releases/later-1 is never caught by them.
const PRIVATE = ['settings', 'me', 'history', 'favorites', 'later', 'reservations', 'notifications', 'following', 'login', 'welcome', 'tonight'];

export function loader({request}: LoaderFunctionArgs) {
  const prefixes = ['', ...LANGS.filter(l => l !== DEFAULT_LANG).map(l => `/${l}`)];
  const lines = ['User-agent: *', 'Allow: /', 'Disallow: /admin$', 'Disallow: /admin/', 'Disallow: /api/'];
  for (const prefix of prefixes) {
    for (const p of PRIVATE) lines.push(`Disallow: ${prefix}/${p}$`);
    lines.push(`Disallow: ${prefix}/settings/`, `Disallow: ${prefix}/search?`);
  }
  lines.push('', `Sitemap: ${siteOrigin(request)}/sitemap.xml`, '');
  return new Response(lines.join('\n'), {headers: {'Content-Type': 'text/plain; charset=utf-8', 'Cache-Control': 'public, max-age=3600'}});
}
