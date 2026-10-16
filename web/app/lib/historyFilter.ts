// 播放记录 search for signed-out viewers: the guest history (lib/guestProgress)
// lives in this browser, so /history filters it here, the way the API's
// GET /api/me/watch-history?q= filters an account's (store.WatchHistory):
// title, performer and product code. Pure (node --test).

export type Searchable = {slug: string; title: string; performers?: string[]; codes?: string[]};

/** NFKC, lower case, whitespace collapsed (search.NormalizeQuery). */
export const normalizeHistoryQuery = (q: string) => q.normalize('NFKC').toLowerCase().split(/\s+/).filter(Boolean).join(' ').slice(0, 100);

/** Only [a-z0-9]: "ABC-123" and "abc123" compare equal. */
const compact = (s: string) => s.normalize('NFKC').toLowerCase().replace(/[^a-z0-9]/g, '');

/**
 * SQL code_match_key (migration 033): letters + number (+ letters) codes as
 * PREFIX-<number without leading zeros>[-SUFFIX], so "abc00123" = "ABC-123".
 */
export function codeMatchKey(s: string): string {
  const raw = compact(s);
  const m = /^([0-9]*[a-z]+)([0-9]+)([a-z]*)$/.exec(raw);
  if (!m) return raw.toUpperCase();
  return `${m[1]}-${m[2].replace(/^0+(?=.)/, '')}${m[3] ? `-${m[3]}` : ''}`.toUpperCase();
}

/** Whether a history row matches a normalized query (an empty one matches all). */
export function matchesHistory(item: Searchable, q: string): boolean {
  if (!q) return true;
  const text = [item.title, item.slug, ...(item.performers ?? [])];
  if (text.some(s => normalizeHistoryQuery(s).includes(q))) return true;
  // Product codes: the release's codes, and the slug (usually the code).
  if (!/[0-9]/.test(q)) return false;
  const key = codeMatchKey(q);
  const cq = compact(q);
  return [...(item.codes ?? []), item.slug].some(code => codeMatchKey(code) === key || (cq.length >= 3 && compact(code).startsWith(cq)));
}

export const filterHistory = <T extends Searchable>(items: T[], q: string): T[] => {
  const nq = normalizeHistoryQuery(q);
  return nq ? items.filter(i => matchesHistory(i, nq)) : items;
};
