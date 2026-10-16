import {api, ApiError, tokens} from './api';
import {ratingDeviceId, type ReportReason} from './community';

// 社交互动 API (PRD 5.4; internal/api/interactions.go): 赞 / 踩 on releases
// and comments with one level of replies. Votes work like ratings: a user
// token, or the visitor's device id (X-Device-ID). Comments need a user token
// to post, like or report; COMMENTS_ENABLED=false makes every comment route
// 404 (commentsDisabled).

const enc = encodeURIComponent;

// ---- 赞 / 踩 ----

/** store.ReactionSummary: only the 赞 count is public; my_vote is 1 / -1 / 0. */
export type ReactionSummary = {likes: number; my_vote: -1 | 0 | 1};

const deviceHeaders = (): Record<string, string> | undefined => (tokens.user() ? undefined : {'X-Device-ID': ratingDeviceId()});

export const getReaction = (slug: string, signal?: AbortSignal) =>
  api<ReactionSummary>(`releases/${enc(slug)}/reaction`, {signal, headers: deviceHeaders()});

/** PUT {value: 1 | -1}; 0 undoes the vote (DELETE). */
export const setReaction = (slug: string, value: -1 | 0 | 1) =>
  value === 0
    ? api<ReactionSummary>(`releases/${enc(slug)}/reaction`, {method: 'DELETE', headers: deviceHeaders()})
    : api<ReactionSummary>(`releases/${enc(slug)}/reaction`, {method: 'PUT', body: {value}, headers: deviceHeaders()});

// ---- Comments ----

export type CommentSort = 'top' | 'newest';

/** store.Comment. A deleted comment with live replies is a placeholder (deleted, no body, no author). */
export type Comment = {
  id: number;
  parent_id: number | null;
  body: string;
  spoiler: boolean;
  likes: number;
  liked_by_me: boolean;
  mine: boolean;
  pending: boolean;
  deleted: boolean;
  author: {id: number; display_name: string};
  reply_count: number;
  replies?: Comment[] | null;
  created_at: string;
  /** Last author edit (the 已编辑 marker); null when never edited. */
  edited_at?: string | null;
  /** Own comments: the end of the 10-minute edit window. */
  editable_until?: string | null;
};

export type CommentPage = {items: Comment[]; nextCursor: string | null};

const toPage = (d: {items: Comment[] | null; next_cursor?: string | null} | null): CommentPage => ({
  items: d?.items ?? [],
  nextCursor: d?.next_cursor || null,
});

export const COMMENT_MAX = 500;

export const getComments = (slug: string, sort: CommentSort, limit: number, cursor?: string | null, signal?: AbortSignal) => {
  const q = new URLSearchParams({sort, limit: String(limit)});
  if (cursor) q.set('cursor', cursor);
  return api<{items: Comment[] | null; next_cursor?: string | null}>(`releases/${enc(slug)}/comments?${q}`, {signal}).then(toPage);
};

export const getReplies = (id: number, limit: number, cursor?: string | null, signal?: AbortSignal) => {
  const q = new URLSearchParams({limit: String(limit)});
  if (cursor) q.set('cursor', cursor);
  return api<{items: Comment[] | null; next_cursor?: string | null}>(`comments/${id}/replies?${q}`, {signal}).then(toPage);
};

export const postComment = (slug: string, body: string, spoiler: boolean, parentId?: number) =>
  api<Comment>(`releases/${enc(slug)}/comments`, {method: 'POST', body: {body, spoiler, parent_id: parentId || undefined}});
/** PATCH /api/comments/{id}: the author's edit within 10 minutes of posting (409 after). */
export const editComment = (id: number, body: string, spoiler: boolean) =>
  api<Comment>(`comments/${id}`, {method: 'PATCH', body: {body, spoiler}});
export const deleteComment = (id: number) => api<{deleted: boolean}>(`comments/${id}`, {method: 'DELETE'});
export const likeComment = (id: number, like: boolean) =>
  api<{liked: boolean; likes: number}>(`comments/${id}/like`, {method: like ? 'PUT' : 'DELETE'});
export const reportComment = (id: number, reason: ReportReason, note?: string) =>
  api<{id: number; status: string; created: boolean}>(`comments/${id}/report`, {method: 'POST', body: {reason, note}});

/** The comment routes answer 404 "comments are not enabled" while COMMENTS_ENABLED=false. */
export const commentsDisabled = (error: unknown) => error instanceof ApiError && error.status === 404;

// ---- Share ----

/** The release's canonical link (no queue / list params), optionally starting at `t` seconds. */
export function shareUrl(href: string, t?: number): string {
  const u = new URL(href);
  const out = new URL(u.origin + u.pathname);
  if (t && t > 0) out.searchParams.set('t', String(Math.floor(t)));
  return out.toString();
}

/** `?t=` on a release URL: seconds (also 1h2m3s / 2m30s), or undefined. */
export function parseStartTime(raw: string | null | undefined): number | undefined {
  if (!raw) return undefined;
  const s = raw.trim();
  if (/^\d+$/.test(s)) {
    const n = Number(s);
    return n > 0 && n < 24 * 3600 ? n : undefined;
  }
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(s);
  if (!m || !s) return undefined;
  const n = Number(m[1] ?? 0) * 3600 + Number(m[2] ?? 0) * 60 + Number(m[3] ?? 0);
  return n > 0 && n < 24 * 3600 ? n : undefined;
}
