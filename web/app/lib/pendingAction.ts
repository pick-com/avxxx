// The guest action waiting for a sign-in (components/AuthPrompt.tsx).
//
// A guest who taps 收藏 / 稍后观看 / 关注 / 点赞, or sends a half-written
// 弹幕 or 评论, gets the sign-in sheet; the action is kept here so it can
// finish once they are signed in — right away for an in-page sign-in, or
// after the full-page Google redirect comes back (the callback lands on
// /login, which then returns to the page). sessionStorage: this tab only,
// one action at a time, and it expires after 10 minutes. Text is only ever
// put back into its input, never posted automatically.

const KEY = 'reel.pendingAction.v1';
export const PENDING_TTL_MS = 10 * 60 * 1000;
/** Drafts longer than this are cut (danmaku 50, comments 500 characters). */
export const PENDING_DRAFT_MAX = 2000;

export type PendingPayload = Record<string, string | number | boolean>;

export type PendingAction = {
  kind: string;
  /** What it applies to: a release or performer slug, `comment:<slug>`, … */
  target: string;
  payload?: PendingPayload;
  created_at: number;
};

type StorageLike = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;

function session(): StorageLike | null {
  try {
    return typeof window === 'undefined' ? null : window.sessionStorage;
  } catch {
    return null;
  }
}

/** Parses a stored action; null when malformed or older than the TTL (or from the future). */
export function parsePending(raw: string | null, now = Date.now()): PendingAction | null {
  if (!raw) return null;
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!value || typeof value !== 'object') return null;
  const p = value as Partial<PendingAction>;
  if (typeof p.kind !== 'string' || !p.kind || typeof p.target !== 'string' || typeof p.created_at !== 'number') return null;
  const age = now - p.created_at;
  if (!(age >= 0 && age < PENDING_TTL_MS)) return null;
  const payload = p.payload && typeof p.payload === 'object' && !Array.isArray(p.payload) ? p.payload : undefined;
  return {kind: p.kind, target: p.target, created_at: p.created_at, ...(payload ? {payload} : {})};
}

/** Bounds the payload: strings are cut to PENDING_DRAFT_MAX characters. */
export function boundPayload(payload: PendingPayload | undefined): PendingPayload | undefined {
  if (!payload) return undefined;
  const out: PendingPayload = {};
  for (const [k, v] of Object.entries(payload)) out[k] = typeof v === 'string' ? Array.from(v).slice(0, PENDING_DRAFT_MAX).join('') : v;
  return out;
}

export function savePending(action: Omit<PendingAction, 'created_at'>, now = Date.now(), store: StorageLike | null = session()) {
  if (!store) return;
  const value: PendingAction = {kind: action.kind, target: action.target, created_at: now};
  const payload = boundPayload(action.payload);
  if (payload) value.payload = payload;
  try {
    store.setItem(KEY, JSON.stringify(value));
  } catch {
    /* storage blocked: the action is simply not resumed */
  }
}

/** The stored action if still valid; an expired or malformed one is removed. */
export function peekPending(now = Date.now(), store: StorageLike | null = session()): PendingAction | null {
  if (!store) return null;
  let raw: string | null = null;
  try {
    raw = store.getItem(KEY);
  } catch {
    return null;
  }
  const p = parsePending(raw, now);
  if (!p && raw) clearPending(store);
  return p;
}

/** Removes and returns the stored action when it is `kind` for `target`; once only. */
export function takePending(kind: string, target: string, now = Date.now(), store: StorageLike | null = session()): PendingAction | null {
  const p = peekPending(now, store);
  if (!p || p.kind !== kind || p.target !== target) return null;
  clearPending(store);
  return p;
}

/**
 * A list refetched on sign-in (comments, 短评) is ready for a pending action
 * once the page it shows was loaded for the signed-in account: finishing the
 * action earlier races the refetch, whose older answer would overwrite the
 * like or reply state just set.
 */
export function listReadyForAccount(loadedAs: string | null, status: string, loading: boolean): boolean {
  return status === 'user' && loadedAs === 'user' && !loading;
}

export function clearPending(store: StorageLike | null = session()) {
  try {
    store?.removeItem(KEY);
  } catch {
    /* ignore */
  }
}

// A provider sign-in started from the sheet (auth_prompt_converted is sent
// when the page is back signed in). Kept apart from the action itself, which
// not every prompt has. Same 10-minute limit.
const OAUTH_KEY = 'reel.authPromptOAuth.v1';

export function markPromptOAuth(action: string, method: string, now = Date.now(), store: StorageLike | null = session()) {
  try {
    store?.setItem(OAUTH_KEY, JSON.stringify({action, method, at: now}));
  } catch {
    /* ignore */
  }
}

/** Removes and returns the marker when still fresh. */
export function takePromptOAuth(now = Date.now(), store: StorageLike | null = session()): {action: string; method: string} | null {
  if (!store) return null;
  let raw: string | null = null;
  try {
    raw = store.getItem(OAUTH_KEY);
    if (raw) store.removeItem(OAUTH_KEY);
  } catch {
    return null;
  }
  try {
    const v = raw ? JSON.parse(raw) : null;
    if (!v || typeof v.action !== 'string' || typeof v.method !== 'string' || typeof v.at !== 'number') return null;
    const age = now - v.at;
    return age >= 0 && age < PENDING_TTL_MS ? {action: v.action, method: v.method} : null;
  } catch {
    return null;
  }
}
