// Guest 播放记录 → account on sign-in. What this browser watched while signed
// out (lib/guestProgress.ts) is uploaded to the account through POST
// /api/me/watch-history with each entry's own watched_at, one at a time;
// the server keeps any newer progress the account already has
// (store.MergeWatch). Uploaded entries are then removed from the browser.
// Pure helpers here; components/AuthPrompt.tsx runs it.

import type {GuestEntry} from './guestProgress';

/** Uploaded per sign-in at most (the guest store keeps 100). */
export const GUEST_MERGE_MAX = 100;

export type MergeEntry = {slug: string; progress_seconds: number; duration_seconds: number; watched_at: string};

/**
 * The entries to upload from the guest store (already pruned to its TTL):
 * the `max` most recent with any progress, oldest first.
 */
export function selectGuestMerge(store: Record<string, GuestEntry>, now = Date.now(), max = GUEST_MERGE_MAX): MergeEntry[] {
  return Object.entries(store)
    .filter(([slug, e]) => !!slug && !!e && Number.isFinite(e.p) && Number.isFinite(e.t) && e.p >= 1)
    .sort((a, b) => b[1].t - a[1].t)
    .slice(0, max)
    .reverse()
    .map(([slug, e]) => ({
      slug,
      progress_seconds: Math.floor(e.p),
      duration_seconds: Math.max(0, Math.floor(e.d || 0)),
      watched_at: new Date(Math.min(e.t, now)).toISOString(),
    }));
}

/**
 * A failed upload that will never succeed (the release is gone or the entry
 * is rejected): drop it. A network error, 5xx, 401 or 429 stops the run and
 * keeps the rest for the next sign-in.
 */
export function isPermanentFailure(cause: unknown): boolean {
  const status = typeof cause === 'object' && cause !== null ? (cause as {status?: unknown}).status : undefined;
  return typeof status === 'number' && status >= 400 && status < 500 && status !== 401 && status !== 408 && status !== 429;
}

/**
 * Uploads `entries` in order with `save`; returns the slugs that are done
 * (saved, kept because the account had newer progress, or permanently
 * rejected) and whether every entry was handled.
 */
export async function uploadGuestHistory(entries: MergeEntry[], save: (e: MergeEntry) => Promise<unknown>): Promise<{done: string[]; complete: boolean}> {
  const done: string[] = [];
  for (const entry of entries) {
    try {
      await save(entry);
    } catch (cause) {
      if (!isPermanentFailure(cause)) return {done, complete: false};
    }
    done.push(entry.slug);
  }
  return {done, complete: true};
}
