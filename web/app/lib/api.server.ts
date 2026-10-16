// Server-side API access for route loaders (SSR). Never bundled for the
// browser (.server module): loaders call the Go API directly over the
// internal network instead of going through the public /api path.
//
// API_INTERNAL_URL  base URL of the Go API (default http://127.0.0.1:8080;
//                   compose: http://api:8080)
// API_TIMEOUT_MS    per-request timeout (default 4000)
// PUBLIC_BASE_URL   public origin for canonical / hreflang / sitemap URLs
//                   (e.g. https://reel.example); defaults to the request's origin

import {ApiError, localizeBody, type LangCode} from './api';
import {cleanStrings, type Messages} from './i18n';
import {DEFAULT_LANG} from './langs';

const apiBase = () => (process.env.API_INTERNAL_URL || 'http://127.0.0.1:8080').replace(/\/+$/, '');
const timeoutMs = () => {
  const n = Number(process.env.API_TIMEOUT_MS);
  return Number.isFinite(n) && n > 0 ? n : 4000;
};

const LOCALIZED_PATH = /^(releases|performers|catalog\/search|recs|trending)(\/|\?|$)/;

/**
 * GET /api/{path} from the server. `lang` adds ?lang= (content language) and
 * applies the same display_* mapping as the browser client. Merged slugs are
 * followed (the API answers 301); compare the returned slug to detect them.
 * Errors are ApiError with the upstream status (0 for network / timeout).
 */
export async function serverApi<T>(path: string, opts: {lang?: LangCode; signal?: AbortSignal} = {}): Promise<T> {
  let clean = path.replace(/^\/+/, '').replace(/^api\//, '');
  if (opts.lang && LOCALIZED_PATH.test(clean) && !/[?&]lang=/.test(clean)) clean += `${clean.includes('?') ? '&' : '?'}lang=${encodeURIComponent(opts.lang)}`;
  const signals = [AbortSignal.timeout(timeoutMs())];
  if (opts.signal) signals.push(opts.signal);
  let response: Response;
  try {
    response = await fetch(`${apiBase()}/api/${clean}`, {
      headers: {Accept: 'application/json', 'User-Agent': 'avxxx-web-ssr'},
      signal: AbortSignal.any(signals),
    });
  } catch (cause) {
    throw new ApiError(`api unreachable: ${cause instanceof Error ? cause.message : String(cause)}`, 0);
  }
  const text = await response.text();
  if (!response.ok) throw new ApiError(`api ${response.status}`, response.status, text.slice(0, 200));
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    throw new ApiError('api returned invalid JSON', 502);
  }
  return (LOCALIZED_PATH.test(clean) ? localizeBody(data) : data) as T;
}

/** 404 / 410 from the API: the entity is missing (or not public). */
export const isGone = (e: unknown) => e instanceof ApiError && (e.status === 404 || e.status === 410);

// ---- UI dictionaries (GET /api/i18n/ui/{lang}), cached in memory ----

const DICT_TTL_MS = 5 * 60 * 1000;
const dicts = new Map<LangCode, {at: number; strings: Messages; pending?: Promise<Messages>}>();

export async function serverUiDict(lang: LangCode): Promise<Messages> {
  if (lang === DEFAULT_LANG) return {};
  const hit = dicts.get(lang);
  const now = Date.now();
  if (hit && now - hit.at < DICT_TTL_MS) return hit.strings;
  if (hit?.pending) return hit.strings;
  const pending = serverApi<unknown>(`i18n/ui/${encodeURIComponent(lang)}`)
    .then(cleanStrings, e => {
      // 404: no reviewed strings for this language yet; zh-CN fallback.
      if (e instanceof ApiError && e.status === 404) return {} as Messages;
      throw e;
    })
    .then(
      strings => {
        dicts.set(lang, {at: Date.now(), strings});
        return strings;
      },
      () => {
        // Keep serving the stale copy (or nothing) and retry in a minute.
        const prev = dicts.get(lang);
        dicts.set(lang, {at: Date.now() - DICT_TTL_MS + 60_000, strings: prev?.strings ?? {}});
        return prev?.strings ?? {};
      },
    );
  dicts.set(lang, {at: hit?.at ?? 0, strings: hit?.strings ?? {}, pending});
  // First load waits; later refreshes serve the stale copy meanwhile.
  return hit ? hit.strings : pending;
}

/** Public origin for absolute URLs (canonical, hreflang, sitemaps). */
export function siteOrigin(request: Request): string {
  const configured = (process.env.PUBLIC_BASE_URL || '').trim().replace(/\/+$/, '');
  if (configured) return configured;
  return new URL(request.url).origin;
}
