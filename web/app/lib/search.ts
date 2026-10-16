// Search query understanding (PRD 4.5; tech plan 3.4, 3.13; backend
// internal/api/search.go). Three public routes:
//
//   GET /api/catalog/search?q=   typed results (&type=performer|release) + the
//                                envelope: counts (&counts=1), effective_query
//                                (synonym or modifier-stripped retry), rescued,
//                                redirect {target_url, reason}, exact_match
//                                {type, id, slug} for a single product-code hit;
//                                every item carries `match`.
//   GET /api/search/hot          大家都在搜 / 热搜词 (24 h board).
//   GET /api/search/suggest?q=   typed suggestions while typing
//                                (release | performer | upcoming | tag | query).
//
// Older backends answer catalog/search with only {items} and 404 the other
// two: callers get a plain envelope and `null` (feature absent), never an error.

import {api, isAbort, isMissingEndpoint, type SearchHit} from './api';

const enc = encodeURIComponent;

/** How an item matched (store.matchLabel): name/title are plain matches. */
export type MatchKind = 'code' | 'name' | 'alias' | 'title' | 'translation' | 'fuzzy';

export type SearchItem = SearchHit & {match?: MatchKind | string};

export type SearchRedirect = {target_url: string; reason?: string};
export type ExactMatch = {type: 'release' | 'performer' | string; id: number; slug: string};

export type SearchResponse = {
  items: SearchItem[];
  /** Normalized input (newer backends). */
  query?: string;
  effective_query?: string;
  rescued?: boolean;
  redirect?: SearchRedirect;
  exact_match?: ExactMatch;
  /** Per-type match counts (counts=1; each capped at 1000). */
  counts?: {performer: number; release: number};
};

/** Search results 类型 filter: only performers or only releases. */
export type SearchType = 'performer' | 'release';

export function searchCatalog(q: string, limit: number, signal?: AbortSignal, opts: {type?: SearchType; counts?: boolean} = {}): Promise<SearchResponse> {
  const extra = `${opts.type ? `&type=${opts.type}` : ''}${opts.counts ? '&counts=1' : ''}`;
  return api<SearchResponse | null>(`catalog/search?q=${enc(q)}&limit=${limit}${extra}`, {signal}).then(d => ({
    ...(d ?? {}),
    items: d?.items ?? [],
  }));
}

export type SuggestType = 'release' | 'performer' | 'upcoming' | 'tag' | 'query';
export type Suggestion = {
  type: SuggestType | string;
  name: string;
  id?: number;
  slug?: string;
  release_date?: string | null;
  scheduled_date?: string | null;
};

/** Suggestions for q, or null when the backend has no suggest route. */
export function getSuggestions(q: string, limit: number, signal?: AbortSignal): Promise<Suggestion[] | null> {
  return api<{items: Suggestion[] | null}>(`search/suggest?q=${enc(q)}&limit=${limit}`, {signal}).then(
    d => d?.items ?? [],
    error => {
      if (isMissingEndpoint(error)) return null;
      throw error;
    },
  );
}

export type HotSearch = {rank: number; q: string; searches: number};
export type HotBoard = {period: string; items: HotSearch[]; computed_at: string | null};

/** The hot-search board, or null when the route is not deployed or fails. */
export function getHotSearches(limit: number, signal?: AbortSignal): Promise<HotBoard | null> {
  return api<HotBoard>(`search/hot?limit=${limit}`, {signal}).then(
    d => (d && Array.isArray(d.items) ? d : {period: '', items: [], computed_at: null}),
    error => {
      if (isAbort(error)) throw error;
      return null;
    },
  );
}

/** Link for a suggestion; null for types without a page (handled as a query). */
export function suggestionPath(s: Suggestion): string {
  switch (s.type) {
    case 'release':
    case 'upcoming':
      if (s.slug) return `/releases/${enc(s.slug)}`;
      break;
    case 'performer':
      if (s.slug) return `/performers/${enc(s.slug)}`;
      break;
    case 'tag':
      if (s.slug) return `/discover?tag=${enc(s.slug)}`;
      break;
  }
  return `/search?q=${enc(s.name)}`;
}

/** Where a catalog search should send the viewer instead of showing results. */
export function searchJump(res: SearchResponse): {to: string; external: boolean} | null {
  const target = res.redirect?.target_url?.trim();
  if (target) {
    if (target.startsWith('/') && !target.startsWith('//')) return {to: target, external: false};
    if (/^https:\/\//i.test(target)) return {to: target, external: true};
  }
  const exact = res.exact_match;
  if (exact?.slug) {
    if (exact.type === 'release') return {to: `/releases/${enc(exact.slug)}`, external: false};
    if (exact.type === 'performer') return {to: `/performers/${enc(exact.slug)}`, external: false};
  }
  return null;
}
