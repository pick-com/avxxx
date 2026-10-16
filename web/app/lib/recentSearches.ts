// Recent searches (最近搜索). Signed-out visitors keep them in localStorage;
// signed-in users also store them server-side (tech plan 3.13
// `user_search_history`, GET|POST|DELETE /api/me/search-history), so they
// follow the account across devices. A backend without the route falls back
// to the local list.

import {clearSearchHistory, getSearchHistory, recordSearchHistory} from './api';

const KEY = 'recent_searches';
const MAX = 10;

/** The dedupe key of a search: width-folded (NFKC), lower case, single spaces. */
export const searchKey = (q: string) => q.normalize('NFKC').toLowerCase().trim().replace(/\s+/g, ' ');

export function readRecentSearches(): string[] {
  try {
    const parsed: unknown = JSON.parse(window.localStorage.getItem(KEY) || '[]');
    return Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === 'string').slice(0, MAX) : [];
  } catch {
    return [];
  }
}

export function rememberSearch(query: string) {
  const q = query.trim().replace(/\s+/g, ' ');
  if (!q) return;
  // As typed, deduplicated case-insensitively like the server (search.NormalizeQuery).
  const key = searchKey(q);
  const next = [q, ...readRecentSearches().filter(x => searchKey(x) !== key)].slice(0, MAX);
  try {
    window.localStorage.setItem(KEY, JSON.stringify(next));
  } catch {
    /* not persisted */
  }
}

export function clearRecentSearches() {
  try {
    window.localStorage.removeItem(KEY);
  } catch {
    /* ignore */
  }
}

/**
 * Records q (when given) and returns the recent list: the account's history
 * when signed in (local list on any server failure), else the local list.
 */
export async function syncRecentSearches(signedIn: boolean, q?: string, signal?: AbortSignal): Promise<string[]> {
  if (q) rememberSearch(q);
  if (!signedIn) return readRecentSearches();
  try {
    if (q) await recordSearchHistory(q.trim());
    const list = await getSearchHistory(signal);
    return list.slice(0, MAX);
  } catch {
    return readRecentSearches();
  }
}

/** 清除: the local list and, when signed in, the account's history. */
export async function clearAllRecentSearches(signedIn: boolean): Promise<void> {
  clearRecentSearches();
  if (signedIn) await clearSearchHistory().catch(() => undefined);
}
