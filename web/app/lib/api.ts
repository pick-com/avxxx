// Typed client for the Go API (cmd/api/main.go). All paths are relative to /api.
//
// Token rule (tech plan 2.6): the admin token is sent only to `admin/*`, the
// user token only to `me/*`, `auth/me` and play-config (for `resume`). Every
// other endpoint is public and gets no Authorization header. Tokens stay in
// localStorage until cookie sessions land (tech plan 3.6).
//
// Several endpoints exist only on newer backends (catalog-filters, play-config,
// identity-merge). Callers feature-detect from the response shape and degrade
// to the older behaviour instead of showing an error.

const USER_TOKEN_KEY = 'user_token';
const ADMIN_TOKEN_KEY = 'admin_token';

function readStorage(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStorage(key: string, value: string | null) {
  try {
    if (value) window.localStorage.setItem(key, value);
    else window.localStorage.removeItem(key);
  } catch {
    /* storage unavailable (private mode); keep going without persistence */
  }
}

export const tokens = {
  user: () => readStorage(USER_TOKEN_KEY),
  admin: () => readStorage(ADMIN_TOKEN_KEY),
  setUser: (value: string | null) => writeStorage(USER_TOKEN_KEY, value),
  setAdmin: (value: string | null) => writeStorage(ADMIN_TOKEN_KEY, value),
};

export class ApiError extends Error {
  status: number;
  /** Raw server message (`{"error":"..."}` or a plain-text body); shown on admin screens only. */
  detail?: string;
  constructor(message: string, status: number, detail?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.detail = detail;
  }
}

// play-config is public, but a user token adds `resume` (tech plan 3.3).
const PLAY_CONFIG_PATH = /^releases\/[^/?]+\/play-config(\?|$)/;

function tokenFor(path: string): string | null {
  if (path.startsWith('admin/')) return tokens.admin();
  if (path.startsWith('me/') || path === 'auth/me' || PLAY_CONFIG_PATH.test(path)) return tokens.user();
  return null;
}

function fallbackMessage(status: number): string {
  if (status === 401) return '需要登录后才能继续';
  if (status === 404) return '内容不存在或已下架';
  if (status === 409) return '请求冲突，请刷新后重试';
  if (status === 501) return '该片源尚未配置';
  if (status >= 500) return '服务暂时不可用，请稍后重试';
  return `请求失败（${status}）`;
}

// The API answers errors in three shapes: `{"error":"request failed"}` from the
// catalog handlers, `{"error":{"code","message"}}` from internal/media, and a
// plain-text body from the admin middleware. Only the media shape carries a
// useful message; the others fall back to a status-based Chinese message.
function errorMessage(data: unknown, status: number): string {
  if (data && typeof data === 'object' && 'error' in data) {
    const err = (data as {error: unknown}).error;
    if (err && typeof err === 'object' && 'message' in err && typeof (err as {message: unknown}).message === 'string') {
      return (err as {message: string}).message;
    }
  }
  return fallbackMessage(status);
}

function errorDetail(data: unknown): string | undefined {
  if (data && typeof data === 'object' && 'error' in data) {
    const err = (data as {error: unknown}).error;
    if (typeof err === 'string') return err;
    if (err && typeof err === 'object' && 'message' in err) return String((err as {message: unknown}).message);
  }
  return typeof data === 'string' && data ? data : undefined;
}

type RequestOptions = {
  method?: 'GET' | 'POST' | 'PUT';
  body?: unknown;
  signal?: AbortSignal;
  keepalive?: boolean;
};

export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const clean = path.replace(/^\/+/, '').replace(/^api\//, '');
  const headers = new Headers({Accept: 'application/json'});
  const token = tokenFor(clean);
  if (token) headers.set('Authorization', `Bearer ${token}`);
  if (options.body !== undefined) headers.set('Content-Type', 'application/json');
  let response: Response;
  try {
    response = await fetch(`/api/${clean}`, {
      method: options.method ?? 'GET',
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: options.signal,
      keepalive: options.keepalive,
    });
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause;
    throw new ApiError('网络连接失败，请检查网络后重试', 0);
  }
  const text = await response.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = response.ok ? null : text.trim().slice(0, 300);
    }
  }
  if (!response.ok) throw new ApiError(errorMessage(data, response.status), response.status, errorDetail(data));
  return data as T;
}

export function isAbort(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError';
}

/** 404 / 405 / 501: the route is not deployed on this backend yet. */
export function isMissingEndpoint(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 404 || error.status === 405 || error.status === 501);
}

// ---- Shapes (internal/model, internal/store) ----

export type Release = {
  id: number;
  slug: string;
  canonical_title: string;
  title_ja?: string;
  release_date?: string;
  runtime_minutes?: number;
  maker_name?: string;
  label_name?: string;
  series_name?: string;
  status: string;
};

export type TagRef = {slug: string; name: string};
export type ReleaseIdentifier = {type: string; value: string};
/** GET /api/releases/{slug}. tags / director_name / identifiers come only from the catalog-filters backend. */
export type ReleaseDetail = Release & {
  director_name?: string | null;
  tags?: TagRef[] | null;
  identifiers?: ReleaseIdentifier[] | null;
};

export type Performer = {
  id: number;
  slug: string;
  canonical_name: string;
  canonical_name_ja?: string;
  birth_date?: string;
  height_cm?: number;
  bust_cm?: number;
  waist_cm?: number;
  hip_cm?: number;
  status: string;
};

/** GET /api/performers item. works_count / latest_release_date come only from the catalog-filters backend. */
export type PerformerListItem = Performer & {works_count?: number; latest_release_date?: string | null};

// GET /api/performers/{slug} is row_to_json with text fallbacks from source
// records, so several numeric fields can arrive as strings.
export type PerformerDetail = {
  id: number;
  slug: string;
  canonical_name: string;
  canonical_name_ja?: string | null;
  birth_date?: string | null;
  birth_place?: string | null;
  debut_date?: string | null;
  debut_age?: number | null;
  blood_type?: string | null;
  height_cm?: string | number | null;
  bust_cm?: number | null;
  waist_cm?: number | null;
  hip_cm?: number | null;
  cup_size?: string | null;
  activity_start?: string | null;
  activity_end?: string | null;
  biography?: string | null;
  status: string;
};

export type Tag = {slug: string; name: string; count: number};
export type Maker = {name: string; count: number};
/** The optional fields come only from the catalog-filters backend (null when unknown). */
export type SearchHit = {
  type: 'performer' | 'release';
  id: number;
  slug: string;
  name: string;
  release_date?: string | null;
  maker_name?: string | null;
  runtime_minutes?: number | null;
  canonical_name_ja?: string | null;
  birth_date?: string | null;
};
export type WatchItem = {slug: string; title: string; progress_seconds: number; duration_seconds: number; watched_at: string};
export type FavoriteItem = {slug: string; title: string; created_at: string};
export type Playback = {source: string; source_key: string; source_url: string; detail_path: string; resolve_path: string};
export type Stream = {url: string; protocol: string; quality?: string};
export type User = {id: number; email: string};
export type AuthResult = {token: string; user: User};

export type Conflict = {
  id: number;
  entity_type: string;
  entity_id: number;
  field_name: string;
  current_value: unknown;
  candidates: unknown;
  severity: string;
  created_at: string;
};

export type IdentityCandidate = {
  id: number;
  entity_type: string;
  left_id: number;
  right_id: number;
  score: number;
  evidence: unknown;
  status: string;
};

type Items<T> = {items: T[] | null};
const items = <T,>(data: Items<T>) => data.items ?? [];
const enc = encodeURIComponent;

/**
 * One page of a cursor list. `paged` is false when the backend predates
 * cursor pagination (no next_cursor key in the body): it then ignored filters
 * and sort, and there is no next page.
 */
export type Page<T> = {items: T[]; nextCursor: string | null; paged: boolean};
type PageBody<T> = Items<T> & {next_cursor?: string | null};
const toPage = <T,>(data: PageBody<T> | null): Page<T> => ({
  items: data?.items ?? [],
  nextCursor: typeof data?.next_cursor === 'string' && data.next_cursor ? data.next_cursor : null,
  paged: !!data && typeof data === 'object' && 'next_cursor' in data,
});

function query(params: Record<string, string | number | null | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== null && v !== undefined && v !== '' && v !== 0) q.set(k, String(v));
  }
  const text = q.toString();
  return text ? `?${text}` : '';
}

export type ReleaseSort = 'newest' | 'oldest' | 'runtime';
export type ReleaseFilter = {
  q?: string;
  tag?: string;
  maker?: string;
  performer?: string;
  year?: number;
  min_runtime?: number;
  max_runtime?: number;
  sort?: ReleaseSort;
};
export type PerformerSort = 'name' | 'works' | 'newest_work';

/** True when the filter asks the server for more than the default newest-first list. */
export const needsFilterSupport = (f: ReleaseFilter) =>
  !!(f.tag || f.maker || f.performer || f.year || f.min_runtime || f.max_runtime || (f.sort && f.sort !== 'newest'));

// ---- Catalog (public) ----

export const listReleases = (limit: number, signal?: AbortSignal) =>
  api<Items<Release>>(`releases?limit=${limit}`, {signal}).then(items);
export const listPerformers = (limit: number, signal?: AbortSignal) =>
  api<Items<PerformerListItem>>(`performers?limit=${limit}`, {signal}).then(items);
export const listTags = (limit: number, signal?: AbortSignal) =>
  api<Items<Tag>>(`tags?limit=${limit}`, {signal}).then(items);
export const listMakers = (limit: number, signal?: AbortSignal) =>
  api<Items<Maker>>(`makers?limit=${limit}`, {signal}).then(items);
export const getRelease = (slug: string, signal?: AbortSignal) => api<ReleaseDetail>(`releases/${enc(slug)}`, {signal});
export const getReleasePerformers = (slug: string, signal?: AbortSignal) =>
  api<Items<Performer>>(`releases/${enc(slug)}/performers`, {signal}).then(items);
export const getPerformer = (slug: string, signal?: AbortSignal) => api<PerformerDetail>(`performers/${enc(slug)}`, {signal});
export const getPerformerReleases = (slug: string, limit: number, signal?: AbortSignal) =>
  api<Items<Release>>(`performers/${enc(slug)}/releases?limit=${limit}`, {signal}).then(items);
export const catalogSearch = (q: string, limit: number, signal?: AbortSignal) =>
  api<Items<SearchHit>>(`catalog/search?q=${enc(q)}&limit=${limit}`, {signal}).then(items);

/** GET /api/releases with filters, sort and cursor (catalog-filters backend). */
export const listReleasePage = (filter: ReleaseFilter, limit: number, cursor?: string | null, signal?: AbortSignal) =>
  api<PageBody<Release>>(
    `releases${query({...filter, sort: filter.sort && filter.sort !== 'newest' ? filter.sort : undefined, limit, cursor})}`,
    {signal},
  ).then(toPage);

/** GET /api/performers with sort and cursor (catalog-filters backend). */
export const listPerformerPage = (sort: PerformerSort, limit: number, cursor?: string | null, q?: string, signal?: AbortSignal) =>
  api<PageBody<PerformerListItem>>(`performers${query({q, sort: sort === 'name' ? undefined : sort, limit, cursor})}`, {signal}).then(toPage);

// ---- Playback ----

export const getPlayback = (slug: string, signal?: AbortSignal) =>
  api<Playback>(`releases/${enc(slug)}/playback?source=javtiful`, {signal});

export function resolvePlayback(resolvePath: string, signal?: AbortSignal) {
  // Only follow resolve paths the API hands out; never POST to arbitrary URLs.
  if (!/^\/api\/resolve\/[a-z0-9_-]+\//i.test(resolvePath)) {
    return Promise.reject(new ApiError('播放地址无效', 400));
  }
  return api<{streams: Stream[] | null}>(resolvePath, {method: 'POST', signal}).then(data => data.streams ?? []);
}

export type SourceKind = 'official_embed' | 'official_preview' | 'external_link' | 'hls_owned';
export type VersionKind = 'main' | 'high_quality' | 'dub' | 'alt';
export type PlaySource = {
  id: number;
  kind: SourceKind | string;
  provider: string;
  label: string;
  url: string | null;
  max_height: number | null;
  has_hdr: boolean;
  duration_seconds: number | null;
  audio_langs: string[] | null;
  subtitle_langs: string[] | null;
  version_kind: VersionKind | string;
  is_alt_version: boolean;
  priority: number;
};
export type PlayConfig = {
  release: {slug: string; title: string; duration_seconds: number | null};
  sources: PlaySource[];
  resume: {source_id: number; position_seconds: number} | null;
};

/**
 * GET /api/releases/{slug}/play-config (tech plan 3.3). Resolves to null when
 * the endpoint is not deployed (404) or fails for any reason, so the player
 * falls back to the playback?source=javtiful flow without showing an error.
 */
export const getPlayConfig = (slug: string, signal?: AbortSignal): Promise<PlayConfig | null> =>
  api<PlayConfig>(`releases/${enc(slug)}/play-config`, {signal}).then(
    data => (data && typeof data === 'object' && Array.isArray(data.sources) ? data : null),
    error => {
      if (isAbort(error)) throw error;
      return null;
    },
  );

// ---- Account ----

export const login = (email: string, password: string) =>
  api<AuthResult>('auth/login', {method: 'POST', body: {email, password}});
export const register = (email: string, password: string, displayName: string) =>
  api<AuthResult>('auth/register', {method: 'POST', body: {email, password, display_name: displayName}});
export const getMe = (signal?: AbortSignal) => api<User>('auth/me', {signal});
export const getWatchHistory = (signal?: AbortSignal) => api<Items<WatchItem>>('me/watch-history', {signal}).then(items);
export const saveWatchProgress = (slug: string, progressSeconds: number, durationSeconds: number, keepalive = false) =>
  api<{saved: boolean}>('me/watch-history', {
    method: 'POST',
    keepalive,
    body: {slug, progress_seconds: Math.floor(progressSeconds), duration_seconds: Math.floor(durationSeconds || 0)},
  });
export const getFavorites = (signal?: AbortSignal) => api<Items<FavoriteItem>>('me/favorites', {signal}).then(items);
export const setFavorite = (slug: string, enabled: boolean) =>
  api<{saved: boolean}>('me/favorites', {method: 'POST', body: {slug, enabled}});

// ---- Admin (admin token only) ----

// These endpoints return bare arrays rather than {items}; accept both.
const listOf = <T,>(data: T[] | Items<T> | null): T[] => (Array.isArray(data) ? data : data?.items ?? []);

export type EntityType = 'performer' | 'release';

export const adminConflicts = (limit: number) => api<Conflict[] | Items<Conflict>>(`admin/conflicts?limit=${limit}`).then(listOf);
export const adminIdentityCandidates = (limit: number, entityType?: EntityType) =>
  api<IdentityCandidate[] | Items<IdentityCandidate>>(`admin/identity-candidates${query({limit, entity_type: entityType})}`).then(listOf);
export const adminResolveConflict = (id: number, value: unknown, note: string) =>
  api<{ok: boolean}>(`admin/conflicts/${id}/resolve`, {method: 'POST', body: {value, note}});

// Identity review (identity-merge backend). `actor` is a self-declared label
// recorded in review_events (admin auth is a shared token).
export type GenerateStats = {
  entities?: number;
  compared?: number;
  generated?: number;
  inserted?: number;
  skipped_existing?: number;
  skipped_not_duplicate?: number;
  below_threshold?: number;
  oversized_blocks?: number;
};
export type MergeResult = {
  review_event_id: number;
  entity_type: string;
  target_id: number;
  source_id: number;
  target_slug: string;
  source_slug: string;
  moved?: Record<string, number> | null;
  deduplicated?: Record<string, number> | null;
  added_aliases?: unknown[] | null;
};
export type DecisionResult = {review_event_id: number; candidate_id: number; status: string};
export type UndoResult = {
  review_event_id: number;
  reverted_event_id: number;
  entity_type: string;
  restored_id: number;
  restored_slug: string;
  target_id: number;
};
export type ReviewBody = {actor?: string; reason?: string};

export const adminGenerateCandidates = (entityType?: EntityType, body: ReviewBody = {}) =>
  api<Record<string, GenerateStats> | null>('admin/identity-candidates/generate', {method: 'POST', body: {...body, entity_type: entityType}});
export const adminMergeCandidate = (id: number, targetId: number, body: ReviewBody = {}) =>
  api<MergeResult>(`admin/identity-candidates/${id}/merge`, {method: 'POST', body: {...body, target_id: targetId}});
export const adminNotDuplicate = (id: number, body: ReviewBody = {}) =>
  api<DecisionResult>(`admin/identity-candidates/${id}/not-duplicate`, {method: 'POST', body});
export const adminUndoMerge = (eventId: number, body: ReviewBody = {}) =>
  api<UndoResult>(`admin/merges/${eventId}/undo`, {method: 'POST', body});
