// 「登录后观看记录可跨设备同步」: a dismissible bar for signed-out viewers
// (components/SyncReminder.tsx), shown below the player from their 3rd
// watched video or when a release resumes from guest history, at most once
// per calendar day. The day it was last shown is kept in localStorage.

const KEY = 'reel.syncReminder.v1';
/** Distinct videos watched (guest history + the open one) before the bar shows. */
export const SYNC_REMINDER_AFTER = 3;

/** Local calendar day, e.g. 2026-10-16. */
export function dayKey(ms: number): string {
  const d = new Date(ms);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

/**
 * Whether to show the bar: not yet today, and either this is at least the
 * 3rd distinct video the guest watches or the video resumes from guest history.
 */
export function shouldRemind({lastShown, now, watched, current, resumed}: {
  /** dayKey of the last showing, or null. */
  lastShown: string | null;
  now: number;
  /** Slugs in guest history. */
  watched: string[];
  /** The release being watched. */
  current: string;
  resumed: boolean;
}): boolean {
  if (lastShown === dayKey(now)) return false;
  if (resumed) return true;
  const distinct = new Set(watched);
  if (current) distinct.add(current);
  return distinct.size >= SYNC_REMINDER_AFTER;
}

export function readLastShown(): string | null {
  try {
    return window.localStorage.getItem(KEY);
  } catch {
    return null;
  }
}

export function markShown(now = Date.now()) {
  try {
    window.localStorage.setItem(KEY, dayKey(now));
  } catch {
    /* storage blocked: the bar may show again */
  }
}
