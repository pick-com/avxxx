// Sitemaps (PRD 13 SEO): /sitemap.xml is an index of per-type, per-language
// files /sitemaps/{type}-{lang}-{n}.xml. Every URL lists its hreflang
// alternates (xhtml:link) in all launch languages plus x-default.
//
// Types and sources (Go API, internal/api/sitemap.go, cursor paged):
//   releases    GET /api/sitemap/releases    published, unmerged, playable
//   performers  GET /api/sitemap/performers  active, with a published release
//   tags        GET /api/sitemap/tags        tags of published releases (/discover?tag=)
//   series      GET /api/series?sort=name    series with a published release (/series/{slug});
//               lastmod is the series' latest release date
//   playlists   GET /api/sitemap/playlists  public 收藏夹 with a published release (/playlists/{share_token});
//               never link-only or private ones
//   pages       static: home, discover, categories, actors, series list, legal documents
// A backend without the sitemap routes (404) falls back to the public lists
// (GET /api/releases, /api/performers, /api/tags) so the files still exist.
//
// SITEMAP_TTL_SECONDS  how long the lists are cached in memory (default 3600)
// SITEMAP_CHUNK        URLs per file (default 5000; protocol max 50 000)

import {ApiError, type LangCode} from './api';
import {serverApi} from './api.server';
import {DEFAULT_LANG, LANGS, localizePath} from './langs';
import {LEGAL_DOCS} from './legal';

export type SitemapType = 'pages' | 'releases' | 'performers' | 'tags' | 'series' | 'playlists';
export const SITEMAP_TYPES: SitemapType[] = ['pages', 'releases', 'performers', 'tags', 'series', 'playlists'];

export type Entry = {path: string; lastmod?: string};

const ttlMs = () => {
  const n = Number(process.env.SITEMAP_TTL_SECONDS);
  return (Number.isFinite(n) && n >= 0 ? n : 3600) * 1000;
};
export const chunkSize = () => {
  const n = Number(process.env.SITEMAP_CHUNK);
  return Number.isInteger(n) && n > 0 && n <= 50000 ? n : 5000;
};

type Page<T> = {items: T[] | null; next_cursor?: string | null};
type SitemapRow = {slug: string; updated_at?: string | null};
const MAX_PAGES = 1000; // guard against a cursor loop

async function walk<T>(path: (cursor: string | null) => string, signal?: AbortSignal): Promise<T[]> {
  const out: T[] = [];
  let cursor: string | null = null;
  for (let i = 0; i < MAX_PAGES; i++) {
    const page: Page<T> = await serverApi<Page<T>>(path(cursor), {signal});
    out.push(...(page.items ?? []));
    cursor = page.next_cursor || null;
    if (!cursor) break;
  }
  return out;
}

const q = (cursor: string | null) => (cursor ? `&cursor=${encodeURIComponent(cursor)}` : '');
const day = (v?: string | null) => (v && /^\d{4}-\d{2}-\d{2}/.test(v) ? v.slice(0, 10) : undefined);
const missingRoute = (e: unknown) => e instanceof ApiError && (e.status === 404 || e.status === 405);

async function load(type: SitemapType, signal?: AbortSignal): Promise<Entry[]> {
  switch (type) {
    case 'pages':
      return [
        {path: '/'},
        {path: '/discover'},
        {path: '/discover?tab=categories'},
        {path: '/discover?tab=actors'},
        {path: '/series'},
        ...LEGAL_DOCS.map(d => ({path: `/legal/${encodeURIComponent(d.slug)}`})),
      ];
    case 'releases':
      try {
        const rows = await walk<SitemapRow>(c => `sitemap/releases?limit=5000${q(c)}`, signal);
        return rows.map(r => ({path: `/releases/${encodeURIComponent(r.slug)}`, lastmod: day(r.updated_at)}));
      } catch (e) {
        if (!missingRoute(e)) throw e;
        const rows = await walk<{slug: string; release_date?: string}>(c => `releases?limit=100${q(c)}`, signal);
        return rows.map(r => ({path: `/releases/${encodeURIComponent(r.slug)}`}));
      }
    case 'performers':
      try {
        const rows = await walk<SitemapRow>(c => `sitemap/performers?limit=5000${q(c)}`, signal);
        return rows.map(r => ({path: `/performers/${encodeURIComponent(r.slug)}`, lastmod: day(r.updated_at)}));
      } catch (e) {
        if (!missingRoute(e)) throw e;
        const rows = await walk<{slug: string}>(c => `performers?limit=100${q(c)}`, signal);
        return rows.map(r => ({path: `/performers/${encodeURIComponent(r.slug)}`}));
      }
    case 'tags':
      try {
        const rows = await walk<SitemapRow>(c => `sitemap/tags?limit=5000${q(c)}`, signal);
        return rows.map(r => ({path: `/discover?tag=${encodeURIComponent(r.slug)}`, lastmod: day(r.updated_at)}));
      } catch (e) {
        if (!missingRoute(e)) throw e;
        const rows = await serverApi<Page<{slug: string}>>('tags?limit=1000', {signal});
        return (rows.items ?? []).map(r => ({path: `/discover?tag=${encodeURIComponent(r.slug)}`}));
      }
    case 'series':
      try {
        // The public list (name order is stable for the cursor walk); 100 per page is its maximum.
        const rows = await walk<{slug: string; latest_release_date?: string | null}>(c => `series?sort=name&limit=100${q(c)}`, signal);
        return rows.map(r => ({path: `/series/${encodeURIComponent(r.slug)}`, lastmod: day(r.latest_release_date)}));
      } catch (e) {
        // A backend without the series routes has no series pages either.
        if (missingRoute(e)) return [];
        throw e;
      }
    case 'playlists':
      try {
        const rows = await walk<SitemapRow>(c => `sitemap/playlists?limit=5000${q(c)}`, signal);
        return rows.map(r => ({path: `/playlists/${encodeURIComponent(r.slug)}`, lastmod: day(r.updated_at)}));
      } catch (e) {
        // No fallback: only this route knows which playlists are public.
        if (missingRoute(e)) return [];
        throw e;
      }
  }
}

const cache = new Map<SitemapType, {at: number; entries: Entry[]; pending?: Promise<Entry[]>}>();

/** The type's URLs (unprefixed paths), cached for SITEMAP_TTL_SECONDS. */
export async function sitemapEntries(type: SitemapType): Promise<Entry[]> {
  const hit = cache.get(type);
  if (hit && Date.now() - hit.at < ttlMs()) return hit.entries;
  if (hit?.pending) return hit.pending;
  const pending = load(type).then(
    entries => {
      cache.set(type, {at: Date.now(), entries});
      return entries;
    },
    e => {
      // Serve the previous list if there is one; otherwise fail the request.
      if (hit) {
        cache.set(type, {at: Date.now() - ttlMs() + 60_000, entries: hit.entries});
        return hit.entries;
      }
      cache.delete(type);
      throw e;
    },
  );
  cache.set(type, {at: hit?.at ?? 0, entries: hit?.entries ?? [], pending});
  return pending;
}

// ---- XML ----

const xml = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&apos;');

export const fileName = (type: SitemapType, lang: LangCode, n: number) => `${type}-${lang}-${n}.xml`;

export function parseFileName(name: string): {type: SitemapType; lang: LangCode; n: number} | null {
  const m = /^([a-z]+)-([A-Za-z-]+)-(\d+)\.xml$/.exec(name);
  if (!m) return null;
  const type = m[1] as SitemapType;
  const lang = m[2] as LangCode;
  const n = Number(m[3]);
  if (!SITEMAP_TYPES.includes(type) || !LANGS.includes(lang) || !Number.isInteger(n) || n < 1) return null;
  return {type, lang, n};
}

const maxDay = (entries: Entry[]) => entries.reduce<string | undefined>((m, e) => (e.lastmod && (!m || e.lastmod > m) ? e.lastmod : m), undefined);

export async function sitemapIndex(origin: string): Promise<string> {
  const lists = await Promise.all(SITEMAP_TYPES.map(async type => [type, await sitemapEntries(type)] as const));
  const size = chunkSize();
  const parts: string[] = [];
  for (const [type, entries] of lists) {
    for (let i = 0; i * size < entries.length; i++) {
      const lastmod = maxDay(entries.slice(i * size, (i + 1) * size));
      for (const lang of LANGS) {
        parts.push(
          `<sitemap><loc>${xml(`${origin}/sitemaps/${fileName(type, lang, i + 1)}`)}</loc>${lastmod ? `<lastmod>${lastmod}</lastmod>` : ''}</sitemap>`,
        );
      }
    }
  }
  return `<?xml version="1.0" encoding="UTF-8"?>\n<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${parts.join('\n')}\n</sitemapindex>\n`;
}

/** One file, or null when it does not exist (past the last chunk). */
export async function sitemapFile(origin: string, type: SitemapType, lang: LangCode, n: number): Promise<string | null> {
  const entries = await sitemapEntries(type);
  const size = chunkSize();
  const slice = entries.slice((n - 1) * size, n * size);
  if (slice.length === 0 && !(n === 1 && type === 'pages')) return null;
  const urls = slice.map(e => {
    const alternates = [
      ...LANGS.map(l => `<xhtml:link rel="alternate" hreflang="${l}" href="${xml(origin + localizePath(e.path, l))}"/>`),
      `<xhtml:link rel="alternate" hreflang="x-default" href="${xml(origin + localizePath(e.path, DEFAULT_LANG))}"/>`,
    ].join('');
    return `<url><loc>${xml(origin + localizePath(e.path, lang))}</loc>${e.lastmod ? `<lastmod>${e.lastmod}</lastmod>` : ''}${alternates}</url>`;
  });
  return `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">\n${urls.join('\n')}\n</urlset>\n`;
}

export const SITEMAP_HEADERS = {
  'Content-Type': 'application/xml; charset=utf-8',
  'Cache-Control': 'public, max-age=3600',
  'X-Robots-Tag': 'noindex',
};
