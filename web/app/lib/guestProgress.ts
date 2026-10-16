// Watch history for signed-out viewers, kept in this browser only
// (localStorage): reopening a release resumes where it stopped, and 播放记录
// lists these entries. Signed-in viewers use the account's watch history
// (/api/me/watch-history) instead. Functional storage, never sent anywhere.

const KEY = 'reel.guestProgress.v1';
/** Most recent releases kept; older ones drop off. */
export const GUEST_PROGRESS_MAX = 100;
/** Entries not watched for this long are dropped (the 90-day event retention). */
export const GUEST_PROGRESS_TTL_MS = 90 * 24 * 60 * 60 * 1000;

/** p: position s, d: duration s, t: saved at (ms), title: shown in 播放记录. */
export type GuestEntry = {p: number; d: number; t: number; title?: string};
type Store = Record<string, GuestEntry>;

function read(): Store {
  try {
    const raw = window.localStorage.getItem(KEY);
    const parsed = raw ? JSON.parse(raw) : null;
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? prune(parsed as Store) : {};
  } catch {
    return {};
  }
}

/** Keeps the `max` most recently saved entries younger than the TTL. */
export function prune(store: Store, max = GUEST_PROGRESS_MAX, now = Date.now()): Store {
  const entries = Object.entries(store).filter(([, e]) => e && Number.isFinite(e.p) && Number.isFinite(e.t) && now - e.t < GUEST_PROGRESS_TTL_MS);
  if (entries.length <= max) return Object.fromEntries(entries);
  entries.sort((a, b) => b[1].t - a[1].t);
  return Object.fromEntries(entries.slice(0, max));
}

/**
 * Resume point for a stored entry: none under 5 s, and none once the viewer
 * reached the end (≥ 90 % or within 10 s), like the server's 看完 rule.
 */
export function resumePoint(e: GuestEntry | undefined): number {
  if (!e || !(e.p > 5)) return 0;
  if (e.d > 0 && (e.p >= e.d - 10 || e.p >= 0.9 * e.d)) return 0;
  return Math.floor(e.p);
}

export function guestResume(slug: string): number {
  if (typeof window === 'undefined') return 0;
  return resumePoint(read()[slug]);
}

function write(store: Store) {
  try {
    window.localStorage.setItem(KEY, JSON.stringify(prune(store)));
  } catch {
    /* storage full or blocked: guest history is best effort */
  }
}

export function saveGuestProgress(slug: string, position: number, duration: number, title = '') {
  if (typeof window === 'undefined' || !slug) return;
  const store = read();
  store[slug] = {p: Math.floor(position), d: Math.floor(duration || 0), t: Date.now(), title: title || store[slug]?.title || slug};
  write(store);
}

/** Guest 播放记录 in the shape of the account history, newest first. */
export function guestHistory(): {slug: string; title: string; progress_seconds: number; duration_seconds: number; watched_at: string}[] {
  if (typeof window === 'undefined') return [];
  return Object.entries(read())
    .filter(([, e]) => e && Number.isFinite(e.p) && Number.isFinite(e.t))
    .sort((a, b) => b[1].t - a[1].t)
    .map(([slug, e]) => ({slug, title: e.title || slug, progress_seconds: e.p, duration_seconds: e.d || 0, watched_at: new Date(e.t).toISOString()}));
}

export function deleteGuestEntry(slug: string) {
  if (typeof window === 'undefined') return;
  const store = read();
  delete store[slug];
  write(store);
}

/** Clears this browser's guest history; returns how many entries it had. */
export function clearGuestHistory(): number {
  if (typeof window === 'undefined') return 0;
  const n = Object.keys(read()).length;
  try {
    window.localStorage.removeItem(KEY);
  } catch {
    /* blocked storage */
  }
  return n;
}
