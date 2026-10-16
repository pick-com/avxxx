import {api, ApiError, tokens} from './api';
import {t, uiLang} from './i18n';

// Community API (PRD 5.5, 19; internal/api/community.go): danmaku, ratings,
// short reviews and reports. Reads are public (a token adds the caller's own
// danmaku / my_score / liked_by_me); posting needs a user token, except
// ratings, which visitors cast per device with X-Device-ID.

const enc = encodeURIComponent;

export type DanmakuMode = 'scroll' | 'top' | 'bottom';

/** store.Danmaku. spoiler: folded as a spoiler by a moderator. */
export type Danmaku = {
  id: number;
  t_ms: number;
  body: string;
  color: string;
  mode: DanmakuMode;
  lang: string;
  spoiler: boolean;
  mine: boolean;
  created_at: string;
};

export type DanmakuPage = {version_key: string; segment_ms: number; from_ms: number; to_ms: number; items: Danmaku[]};

/** GET /api/releases/{slug}/danmaku?from_ms=&to_ms=&source_id= (whole 60 s segments, at most 5). */
export const getDanmaku = (slug: string, fromMs: number, toMs: number, sourceId?: number, signal?: AbortSignal) => {
  const q = new URLSearchParams({from_ms: String(fromMs), to_ms: String(toMs)});
  if (sourceId) q.set('source_id', String(sourceId));
  return api<DanmakuPage>(`releases/${enc(slug)}/danmaku?${q}`, {signal}).then(d => ({...d, items: d?.items ?? []}));
};

export type NewDanmaku = {t_ms: number; body: string; color?: string; mode?: DanmakuMode; lang?: string; source_id?: number};

/** POST /api/releases/{slug}/danmaku (201). 403 = muted, 429 = too many posts. */
export const postDanmaku = (slug: string, d: NewDanmaku) => api<Danmaku>(`releases/${enc(slug)}/danmaku`, {method: 'POST', body: d});

// ---- Ratings ----

/** store.RatingSummary: score is on a 10-point scale, null under 50 votes. my_score is 1-5. */
export type RatingSummary = {
  score: number | null;
  count: number;
  insufficient: boolean;
  distribution: number[] | null;
  my_score: number | null;
};

const DEVICE_KEY = 'rating_device_id';
let memoryDevice: string | null = null;

/**
 * X-Device-ID for visitors' votes (community.ValidDeviceID: [A-Za-z0-9_-]{8,128}).
 * Only sent on rating calls without a user token; the server stores a hash.
 */
export function ratingDeviceId(): string {
  try {
    let id = window.localStorage.getItem(DEVICE_KEY);
    if (!id || !/^[A-Za-z0-9_-]{8,128}$/.test(id)) {
      id = newDeviceId();
      window.localStorage.setItem(DEVICE_KEY, id);
    }
    return id;
  } catch {
    return (memoryDevice ??= newDeviceId());
  }
}

function newDeviceId(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
}

const deviceHeaders = (): Record<string, string> | undefined => (tokens.user() ? undefined : {'X-Device-ID': ratingDeviceId()});

export const getRating = (slug: string, signal?: AbortSignal) =>
  api<RatingSummary>(`releases/${enc(slug)}/rating`, {signal, headers: deviceHeaders()});

/** PUT /api/releases/{slug}/rating {score: 1-5}. */
export const putRating = (slug: string, score: number) =>
  api<RatingSummary>(`releases/${enc(slug)}/rating`, {method: 'PUT', body: {score}, headers: deviceHeaders()});

// ---- Reviews ----

export type ReviewSort = 'helpful' | 'newest' | 'finished';
export type WatchBadge = 'finished' | 'partial' | 'none';

export type Review = {
  id: number;
  body: string;
  spoiler: boolean;
  likes: number;
  liked_by_me: boolean;
  mine: boolean;
  pending: boolean;
  author: {id: number; display_name: string};
  watch_ratio: number;
  watch_badge: WatchBadge | string;
  watch_percent: number;
  created_at: string;
  updated_at: string;
};

export type ReviewPage = {items: Review[]; nextCursor: string | null};

export const getReviews = (slug: string, sort: ReviewSort, limit: number, cursor?: string | null, signal?: AbortSignal) => {
  const q = new URLSearchParams({sort, limit: String(limit)});
  if (cursor) q.set('cursor', cursor);
  return api<{items: Review[] | null; next_cursor?: string | null}>(`releases/${enc(slug)}/reviews?${q}`, {signal}).then(
    (d): ReviewPage => ({items: d?.items ?? [], nextCursor: d?.next_cursor || null}),
  );
};

export const postReview = (slug: string, body: string, spoiler: boolean) =>
  api<Review>(`releases/${enc(slug)}/reviews`, {method: 'POST', body: {body, spoiler}});
export const deleteReview = (id: number) => api<{deleted: boolean}>(`reviews/${id}`, {method: 'DELETE'});
export const likeReview = (id: number, like: boolean) =>
  api<{liked: boolean; likes: number}>(`reviews/${id}/like`, {method: like ? 'PUT' : 'DELETE'});

// ---- Reports ----

/** community.ReportReasons offered for user content (danmaku, reviews). */
export const CONTENT_REPORT_REASONS = ['spoiler', 'spam', 'abuse', 'illegal', 'underage', 'privacy', 'other'] as const;
export type ReportReason = (typeof CONTENT_REPORT_REASONS)[number];

export const reportDanmaku = (id: number, reason: ReportReason, note?: string) =>
  api<{id: number; status: string; created: boolean}>(`danmaku/${id}/report`, {method: 'POST', body: {reason, note}});
export const reportReview = (id: number, reason: ReportReason, note?: string) =>
  api<{id: number; status: string; created: boolean}>(`reviews/${id}/report`, {method: 'POST', body: {reason, note}});

// ---- Errors ----

/** User-facing message for a failed post (danmaku, review, like, report). */
export function postErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 401) return t('community.errLogin');
    if (error.status === 403) {
      const until = /muted until (\S+)/.exec(error.detail ?? '')?.[1];
      const date = until ? new Date(until) : null;
      return date && !Number.isNaN(date.getTime())
        ? t('community.errMutedUntil', {date: date.toLocaleString(uiLang(), {dateStyle: 'medium', timeStyle: 'short'})})
        : t('community.errMuted');
    }
    if (error.status === 429) return t('community.errTooFast');
    if (error.status === 409) return t('community.errConflict');
    if (error.status === 0) return t('community.errNetwork');
    if (error.status === 400) return t('community.errInvalid');
  }
  return t('community.errGeneric');
}
