// Recent searches live in localStorage for everyone for now. Tech plan 3.13
// moves signed-in users to `user_search_history`.
// TODO(tech plan 3.13): sync to user_search_history when logged in.

const KEY = 'recent_searches';
const MAX = 10;

export function readRecentSearches(): string[] {
  try {
    const parsed: unknown = JSON.parse(window.localStorage.getItem(KEY) || '[]');
    return Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === 'string').slice(0, MAX) : [];
  } catch {
    return [];
  }
}

export function rememberSearch(query: string) {
  const q = query.trim();
  if (!q) return;
  const next = [q, ...readRecentSearches().filter(x => x !== q)].slice(0, MAX);
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
