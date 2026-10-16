// Typed client for the Go API (cmd/api/main.go). All paths are relative to /api.
//
// Token rule (tech plan 2.6): the user token is sent only to `me/*`,
// `auth/me`, play-config (for `resume`) and the community routes (danmaku,
// rating, reviews, reports: posting needs it, reads add the caller's own
// state). Every other endpoint is public and gets no Authorization header.
//
// Back office (tech plan 3.6, internal/api/admin_auth.go): admins sign in at
// /admin/login; the session is an HttpOnly cookie scoped to /api/admin that
// page scripts never see. Every `admin/*` and `admin-auth/*` request carries
// X-Admin-CSRF: 1 (the API refuses the cookie without it). The ADMIN_TOKEN
// break-glass credential, when entered, lives in sessionStorage only (never
// localStorage) and is sent as a bearer with its label in X-Admin-User.
//
// Several endpoints exist only on newer backends (catalog-filters, play-config,
// identity-merge). Callers feature-detect from the response shape and degrade
// to the older behaviour instead of showing an error.

import {t} from './i18n';

const USER_TOKEN_KEY = 'user_token';
// Break-glass ADMIN_TOKEN and its label (recorded as token:<label>).
const ADMIN_TOKEN_KEY = 'admin_token';
const ADMIN_ACTOR_KEY = 'admin_actor';

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

function readSession(key: string): string | null {
  try {
    return window.sessionStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeSession(key: string, value: string | null) {
  try {
    if (value) window.sessionStorage.setItem(key, value);
    else window.sessionStorage.removeItem(key);
  } catch {
    /* storage unavailable */
  }
}

/** Drops the pre-account admin token and 操作人 from localStorage (long-lived admin secrets must not persist). */
function dropLegacyAdminStorage() {
  try {
    window.localStorage.removeItem(ADMIN_TOKEN_KEY);
    window.localStorage.removeItem(ADMIN_ACTOR_KEY);
  } catch {
    /* ignore */
  }
}

export const tokens = {
  user: () => readStorage(USER_TOKEN_KEY),
  setUser: (value: string | null) => writeStorage(USER_TOKEN_KEY, value),
  /** Break-glass ADMIN_TOKEN for this tab (sessionStorage), or null. */
  admin: () => {
    dropLegacyAdminStorage();
    return readSession(ADMIN_TOKEN_KEY);
  },
  setAdmin: (value: string | null) => writeSession(ADMIN_TOKEN_KEY, value),
  /** Break-glass label, recorded as token:<label>. */
  adminActor: (): string => readSession(ADMIN_ACTOR_KEY) ?? '',
  setAdminActor: (value: string) => writeSession(ADMIN_ACTOR_KEY, value.trim() || null),
};

/** HTTP header values must be ISO-8859-1; the break-glass label is printable ASCII without spaces. */
export const validAdminActor = (v: string) => /^[\x21-\x7e]{0,64}$/.test(v.trim());

/** Headers for an admin request: the CSRF header, plus the break-glass bearer and label when set. */
export function adminRequestHeaders(headers: Headers = new Headers()): Headers {
  headers.set('X-Admin-CSRF', '1');
  const glass = tokens.admin();
  if (glass) {
    headers.set('Authorization', `Bearer ${glass}`);
    const label = tokens.adminActor();
    if (label && validAdminActor(label)) headers.set('X-Admin-User', label.trim());
  }
  return headers;
}

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
// Recommendations (internal/api/recs.go): public, but a user token adds the
// viewer's history, follows and watched filter.
const RECS_PATH = /^recs(\?|$)/;
// Upcoming (internal/api/catalog.go): public, but ?followed=1 needs the user
// and a token adds each item's `reserved` state.
const UPCOMING_PATH = /^upcoming(\?|$)/;
// Community routes (internal/api/community.go) and 赞 / 踩 / comments
// (internal/api/interactions.go): a signed-in voter is identified by the
// token only (no X-Device-ID), and posting or liking a comment needs it.
const COMMUNITY_PATH =
  /^(releases\/[^/?]+\/(danmaku|rating|reviews|reaction|comments)|danmaku\/\d+\/report|reviews\/\d+(\/(like|report))?|comments\/\d+(\/(like|replies|report))?|reports)(\?|$)/;

function tokenFor(path: string): string | null {
  if (path.startsWith('me/') || path === 'auth/me' || PLAY_CONFIG_PATH.test(path) || COMMUNITY_PATH.test(path) || RECS_PATH.test(path) || UPCOMING_PATH.test(path)) return tokens.user();
  return null;
}

function fallbackMessage(status: number): string {
  if (status === 401) return t('api.needLogin');
  if (status === 404) return t('api.notFound');
  if (status === 409) return t('api.conflict');
  if (status === 501) return t('api.sourceMissing');
  if (status >= 500) return t('api.unavailable');
  return t('api.failed', {status});
}

// The API answers errors in three shapes: `{"error":"request failed"}` from the
// catalog handlers, `{"error":{"code","message"}}` from internal/media, and a
// plain-text body from the admin middleware. Only the media shape carries a
// useful message; the others fall back to a status-based message (t()).
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

// ---- Content language (PRD 8, i18n backend) ----
//
// Catalog reads carry ?lang=<content language>. A backend with the i18n
// routes answers with display_<field> (+ display_<field>_lang) next to the
// original fields; older backends ignore the parameter. localizeBody() puts
// the display value in the field the UI already reads (canonical_title,
// canonical_name, name, synopsis, biography, tag name) and keeps the original
// as original_<field>, so every screen prefers the translation when present.

let contentLang: string | null = null;
export function setContentLang(lang: string | null) {
  contentLang = lang;
}

const LOCALIZED_PATH = /^(releases|performers|series|catalog\/search|search\/suggest|recs|trending)(\/|\?|$)/;
const UNLOCALIZED_PATH = /\/(play-config|playback|danmaku|rating|reviews)(\?|$)/;
const isLocalizedPath = (path: string) => LOCALIZED_PATH.test(path) && !UNLOCALIZED_PATH.test(path);

function withLang(path: string): string {
  if (!contentLang || !isLocalizedPath(path) || /[?&]lang=/.test(path)) return path;
  return `${path}${path.includes('?') ? '&' : '?'}lang=${encodeURIComponent(contentLang)}`;
}

const DISPLAY_TARGETS: Record<string, string[]> = {
  display_title: ['canonical_title', 'title'],
  display_name: ['canonical_name', 'name'],
  display_synopsis: ['synopsis'],
  display_biography: ['biography'],
};

function localizeObject(o: Record<string, unknown>): Record<string, unknown> {
  let out = o;
  for (const [display, targets] of Object.entries(DISPLAY_TARGETS)) {
    const v = o[display];
    if (typeof v !== 'string' || !v) continue;
    const target = targets.find(t => t in o) ?? targets[0];
    if (o[target] === v) continue;
    if (out === o) out = {...o};
    out[`original_${target}`] = o[target];
    out[target] = v;
  }
  if (Array.isArray(o.tags)) {
    if (out === o) out = {...o};
    out.tags = o.tags.map(t => (t && typeof t === 'object' ? localizeObject(t as Record<string, unknown>) : t));
  }
  return out;
}

export function localizeBody(data: unknown): unknown {
  if (Array.isArray(data)) return data.map(localizeBody);
  if (!data || typeof data !== 'object') return data;
  const o = data as Record<string, unknown>;
  if (Array.isArray(o.items)) return {...o, items: o.items.map(localizeBody)};
  return localizeObject(o);
}

type RequestOptions = {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  body?: unknown;
  signal?: AbortSignal;
  keepalive?: boolean;
  headers?: Record<string, string>;
};

export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const clean = withLang(path.replace(/^\/+/, '').replace(/^api\//, ''));
  const headers = new Headers({...options.headers, Accept: 'application/json'});
  const token = tokenFor(clean);
  if (token) headers.set('Authorization', `Bearer ${token}`);
  if (clean.startsWith('admin/')) adminRequestHeaders(headers);
  else if (clean.startsWith('admin-auth/')) headers.set('X-Admin-CSRF', '1');
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
    throw new ApiError(t('api.network'), 0);
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
  return (isLocalizedPath(clean) ? localizeBody(data) : data) as T;
}

export function isAbort(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError';
}

/** 404 / 405 / 501: the route is not deployed on this backend yet. */
export function isMissingEndpoint(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 404 || error.status === 405 || error.status === 501);
}

// ---- Shapes (internal/model, internal/store) ----

/**
 * A stored catalog image (internal/images): url is the display size (720w
 * when stored), width/height its pixels, srcset every variant narrowest
 * first. url is relative (/api/images/...) in the API's default proxy mode,
 * absolute with a CDN base or presigned URLs.
 */
export type ApiImage = {
  url: string;
  width: number;
  height: number;
  srcset?: {url: string; width: number; height: number}[] | null;
};

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
  /** Stored cover; absent when none is stored or it was taken down. */
  cover?: ApiImage | null;
};

export type TagRef = {slug: string; name: string};
export type ReleaseIdentifier = {type: string; value: string};
/** GET /api/releases/{slug}. tags / director_name / identifiers come only from the catalog-filters backend. */
export type ReleaseDetail = Release & {
  synopsis?: string | null;
  director_name?: string | null;
  tags?: TagRef[] | null;
  identifiers?: ReleaseIdentifier[] | null;
  /** The release's series (migrations/027_series.sql); display_name with ?lang=. */
  series?: {id: number; slug: string; name: string; display_name?: string} | null;
  /** When an upcoming release goes live (status 'upcoming'; absent: date not fixed). */
  scheduled_at?: string | null;
  /** 其他版本: published releases linked as versions of the same work (migration 047). */
  other_versions?: ReleaseVersionRef[] | null;
};

/** One entry of ReleaseDetail.other_versions (store.VersionRef). */
export type ReleaseVersionRef = {
  id: number;
  slug: string;
  title: string;
  release_date?: string | null;
  runtime_minutes?: number | null;
  edition_type: string;
  censorship_type?: string | null;
  code?: string;
  status: string;
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
  /** Stored portrait; absent when none is stored or it was removed. */
  portrait?: ApiImage | null;
  /** Release cast only (GET /api/releases/{slug}/performers): 主演 vs 客串, the source's role text and credited name. */
  lead?: boolean;
  role?: string;
  credited_name?: string;
};

/** GET /api/performers item. works_count / latest_release_date come only from the catalog-filters backend. */
export type PerformerListItem = Performer & {
  works_count?: number;
  latest_release_date?: string | null;
  /** From the hourly performer stats (0 before its first run). heat is the 30-day heat x 1000. */
  followers?: number;
  heat?: number;
  total_plays?: number;
  first_work_date?: string | null;
};

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
  /** True when the birth date is hidden (birth_date is then null). */
  birth_date_hidden?: boolean | null;
  status: string;
  portrait?: ApiImage | null;
  /** 别名 (stage, birth, romanized, former ...). */
  aliases?: PerformerAlias[] | null;
  /** Reviewed name translations (多语言名). */
  names?: {lang: string; name: string}[] | null;
  stats?: PerformerStats | null;
};

export type PerformerAlias = {name: string; language?: string; type: string};
/**
 * Actor page stats. works / lead / upcoming / followers are live; total_plays,
 * heat (30-day heat x 1000) and heat_rank come from the hourly job
 * (computed_at null before its first run).
 */
export type PerformerStats = {
  works_count: number;
  lead_count: number;
  upcoming_count: number;
  followers: number;
  total_plays: number;
  heat: number;
  heat_rank: number | null;
  computed_at: string | null;
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
export const toPage = <T,>(data: PageBody<T> | null): Page<T> => ({
  items: data?.items ?? [],
  nextCursor: typeof data?.next_cursor === 'string' && data.next_cursor ? data.next_cursor : null,
  paged: !!data && typeof data === 'object' && 'next_cursor' in data,
});

export function query(params: Record<string, string | number | null | undefined>): string {
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
  /** With performer: 主演 (lead) or 客串 (guest) credits only. */
  role?: CreditRole;
  /** Playback-source facets: 清晰度 (min lines), 字幕 ('any' or a language), 语言 (audio). */
  min_height?: number;
  subtitle?: string;
  audio_lang?: string;
};
export type CreditRole = 'lead' | 'guest';
export type PerformerSort = 'name' | 'works' | 'newest_work' | 'heat' | 'followers' | 'rising';

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

/** NavActors filters (GET /api/performers): era is a decade's first year. */
export type PerformerFilter = {era?: number; tag?: string; audio_lang?: string};

/** GET /api/performers with sort, NavActors filters and cursor. */
export const listPerformersFiltered = (sort: PerformerSort, filter: PerformerFilter, limit: number, cursor?: string | null, signal?: AbortSignal) =>
  api<PageBody<PerformerListItem>>(`performers${query({...filter, sort: sort === 'name' ? undefined : sort, limit, cursor})}`, {signal}).then(toPage);

/** One 即将上线 row (GET /api/upcoming). date null = 日期待定; reserved only when signed in. */
export type UpcomingItem = Release & {
  display_title?: string;
  scheduled_at?: string | null;
  date: string | null;
  reservable: boolean;
  reserved?: boolean;
  performers: {id: number; slug: string; name: string}[];
};

/** GET /api/upcoming: soonest first; followed needs a signed-in user. */
export const listUpcoming = (opts: {performer?: string; followed?: boolean; limit?: number}, cursor?: string | null, signal?: AbortSignal) =>
  api<PageBody<UpcomingItem>>(
    `upcoming${query({performer: opts.performer, followed: opts.followed ? 1 : undefined, limit: opts.limit, cursor})}`,
    {signal},
  ).then(toPage);

export type FacetCount = {value: string; count: number};
/** GET /api/catalog/facets: playback-source facet values that have data. */
export type CatalogFacets = {audio_langs: FacetCount[]; subtitle_langs: FacetCount[]; heights: FacetCount[]; subtitled: number};

export const getCatalogFacets = (signal?: AbortSignal) => api<CatalogFacets>('catalog/facets', {signal});

// Signed-in recent searches (最近搜索; anonymous ones stay in localStorage).
export const getSearchHistory = (signal?: AbortSignal) => api<Items<string>>('me/search-history', {signal}).then(items);
export const recordSearchHistory = (q: string) => api<{saved: boolean}>('me/search-history', {method: 'POST', body: {q}});
export const clearSearchHistory = () => api<{deleted: number}>('me/search-history', {method: 'DELETE'});

// ---- Playback ----

export const getPlayback = (slug: string, signal?: AbortSignal) =>
  api<Playback>(`releases/${enc(slug)}/playback?source=javtiful`, {signal});

export function resolvePlayback(resolvePath: string, signal?: AbortSignal) {
  // Only follow resolve paths the API hands out; never POST to arbitrary URLs.
  if (!/^\/api\/resolve\/[a-z0-9_-]+\//i.test(resolvePath)) {
    return Promise.reject(new ApiError(t('api.badPlayUrl'), 400));
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
  release: {id?: number; slug: string; title: string; duration_seconds: number | null};
  sources: PlaySource[];
  resume: {source_id: number; position_seconds: number} | null;
  /** enabled, segment_ms, alt_curves (peak curves of alternate versions keyed by source id). */
  danmaku?: {enabled?: boolean; segment_ms?: number; alt_curves?: Record<string, number[] | null> | null} | null;
  /** 高能波峰 of the main timeline (PRD 5.5.1): 200 values 0-255, null under 50 valid danmaku. */
  danmaku_curve?: number[] | null;
  /**
   * 播放波峰 (PRD 14.3, replay-curves backend): replay rate of the main
   * timeline, 200 values 0-255; null under 1000 valid plays. Shown when
   * danmaku is off or has no curve («最多重看»).
   */
  replay_curve?: number[] | null;
  /** Curve to show with danmaku on: 70 % danmaku + 30 % replay, or whichever exists. */
  peak_curve?: number[] | null;
  /** Chapters of the main timeline (tech plan 3.3: start_ms, title); empty until release_assets land. */
  chapters?: {start_ms?: number; start_seconds?: number; title?: string}[] | null;
  /** Sources exist but none is licensed for ?region= (PRD 15 地区限制). */
  region_blocked?: boolean;
};

/**
 * GET /api/releases/{slug}/play-config (tech plan 3.3); `quality` is the
 * preferred max height (settings.quality_pref, capped on cellular). Resolves to null when
 * the endpoint is not deployed (404) or fails for any reason, so the player
 * falls back to the playback?source=javtiful flow without showing an error.
 */
export const getPlayConfig = (slug: string, signal?: AbortSignal, quality?: number): Promise<PlayConfig | null> =>
  api<PlayConfig>(`releases/${enc(slug)}/play-config${query({quality})}`, {signal}).then(
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
/** later_removed: the save reached 90 % and the server dropped the release from 稍后观看. */
export const saveWatchProgress = (slug: string, progressSeconds: number, durationSeconds: number, keepalive = false) =>
  api<{saved: boolean; later_removed?: boolean}>('me/watch-history', {
    method: 'POST',
    keepalive,
    body: {slug, progress_seconds: Math.floor(progressSeconds), duration_seconds: Math.floor(durationSeconds || 0)},
  });
export const getFavorites = (signal?: AbortSignal) => api<Items<FavoriteItem>>('me/favorites', {signal}).then(items);
export const setFavorite = (slug: string, enabled: boolean) =>
  api<{saved: boolean}>('me/favorites', {method: 'POST', body: {slug, enabled}});

// ---- Playlists (收藏夹; internal/api/playlists.go, migration 036) ----
// me/* routes are owner-only; playlists/{token} is the public read for
// public and link-only playlists (no token sent). The default playlist backs
// the legacy me/favorites routes above.

export type PlaylistVisibility = 'private' | 'public' | 'link';
export const PLAYLIST_VISIBILITIES: PlaylistVisibility[] = ['private', 'link', 'public'];

export type Playlist = {
  id: number;
  name: string;
  description: string;
  visibility: PlaylistVisibility;
  /** Owner responses only. */
  share_token?: string;
  is_default: boolean;
  item_count: number;
  cover_slugs: string[];
  /** Only with ?release=: whether that release is in the playlist. */
  contains?: boolean;
  /** Public responses only. */
  owner_name?: string;
  created_at: string;
  updated_at: string;
};
export type PlaylistItem = {slug: string; title: string; title_ja: string; runtime_minutes: number; added_at: string};
export type PlaylistDetail = {playlist: Playlist; items: PlaylistItem[]};
export type PlaylistInput = {name?: string; description?: string; visibility?: PlaylistVisibility};

export const getPlaylists = (release?: string, signal?: AbortSignal) =>
  api<Items<Playlist>>(`me/playlists${query({release})}`, {signal}).then(items);
export const getPlaylist = (id: number, signal?: AbortSignal) => api<PlaylistDetail>(`me/playlists/${id}`, {signal});
export const createPlaylist = (body: PlaylistInput & {name: string}) => api<Playlist>('me/playlists', {method: 'POST', body});
export const updatePlaylist = (id: number, body: PlaylistInput) => api<Playlist>(`me/playlists/${id}`, {method: 'PATCH', body});
export const deletePlaylist = (id: number) => api<{deleted: boolean}>(`me/playlists/${id}`, {method: 'DELETE'});
export const rotatePlaylistToken = (id: number) => api<Playlist>(`me/playlists/${id}/share-token`, {method: 'POST'});
export const addToPlaylist = (id: number, slug: string) => api<{saved: boolean}>(`me/playlists/${id}/items/${enc(slug)}`, {method: 'PUT'});
export const removeFromPlaylist = (id: number, slug: string) =>
  api<{deleted: boolean}>(`me/playlists/${id}/items/${enc(slug)}`, {method: 'DELETE'});
export const reorderPlaylist = (id: number, slugs: string[]) => api<{saved: boolean}>(`me/playlists/${id}/reorder`, {method: 'POST', body: {slugs}});
export const moveLaterToPlaylist = (slug: string, playlistId: number) =>
  api<{moved: boolean}>(`me/watch-later/${enc(slug)}/move`, {method: 'POST', body: {playlist_id: playlistId}});
/** Unprefixed path of the public page for a share token. */
export const playlistSharePath = (token: string) => `/playlists/${encodeURIComponent(token)}`;

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

// ---- Social: follows, reservations, watch later, settings, notifications ----
// (internal/api/social.go, internal/api/notifications.go; PRD 7, 17, 20.)
// Every route needs the user token (me/*). Callers treat 404 / 405 as "not
// deployed on this backend" via isMissingEndpoint() and hide the feature.

export type NotifyMode = 'all' | 'lead_only' | 'off';
export const NOTIFY_MODES: NotifyMode[] = ['all', 'lead_only', 'off'];

/** GET /api/me/follows item. new_count: works gone live since the following feed was last opened. */
export type FollowItem = {
  slug: string;
  name: string;
  name_ja: string;
  notify_mode: NotifyMode;
  created_at: string;
  last_seen_at: string | null;
  new_count: number;
};

/** GET /api/me/following/feed item. Upcoming items are not reachable via GET /api/releases/{slug}. */
export type FeedItem = {
  slug: string;
  title: string;
  title_ja: string;
  release_date: string | null;
  scheduled_at: string | null;
  status: 'published' | 'upcoming' | string;
  reserved: boolean;
  followed_performers: string[] | null;
};

export type ReservationItem = {
  slug: string;
  title: string;
  title_ja: string;
  status: 'published' | 'upcoming' | string;
  release_date: string | null;
  scheduled_at: string | null;
  reserved_at: string;
};

export type WatchLaterItem = {
  slug: string;
  title: string;
  title_ja: string;
  runtime_minutes: number;
  position: number;
  added_at: string;
};

export type NotifyKind = 'new_work' | 'reservation' | 'comment_reply' | 'favorite_update';
export type NotifyChannel = 'in_app' | 'push' | 'email';
export type QualityPref = 'auto' | '2160p' | '1080p' | '720p' | '480p' | '360p';
export type LangCode = 'zh-CN' | 'zh-TW' | 'en' | 'ja' | 'ko' | 'vi' | 'es' | 'th';

/** store.UserSettings. */
export type UserSettings = {
  ui_lang: LangCode;
  content_lang: LangCode;
  subtitle_langs: LangCode[];
  quality_pref: QualityPref;
  data_saver: boolean;
  autoplay: boolean;
  remember_rate: boolean;
  history_paused: boolean;
  /** 看完自动移出 (migration 036); absent on older backends. */
  later_auto_remove?: boolean;
  theme: 'system' | 'dark' | 'light';
  timezone: string;
  notify_prefs: Record<NotifyKind, Record<NotifyChannel, boolean>>;
  quiet_hours: {start: string; end: string} | null;
  /** Migration 037; absent on older backends. */
  notify_daily_cap?: number;
  reservation_timing?: 'go_live' | 'day_before' | 'both';
  personalize_recs?: boolean;
  /** 弹幕个人屏蔽词 (migration 045); absent on older backends. */
  danmaku_block_words?: string[];
  /** 默认片源 (migration 047): provider play-config orders first; '' = automatic. Absent on older backends. */
  preferred_provider?: string;
};

/** A provider a viewer can pick as 默认片源 (GET /api/playback/providers). */
export type PlaybackProvider = {provider: string; releases: number};
export const getPlaybackProviders = (signal?: AbortSignal) =>
  api<{items: PlaybackProvider[] | null}>('playback/providers', {signal}).then(d => d.items ?? []);

/** One release inside a (possibly merged, one-per-day) notification. */
export type NotificationRelease = {
  release_id?: number;
  slug: string;
  title: string;
  reason: 'follow' | 'reservation' | string;
  performers: {slug: string; name: string}[] | null;
};

export type NotificationItem = {
  id: number;
  kind: NotifyKind | string;
  payload: {items?: NotificationRelease[] | null; count?: number} | null;
  read: boolean;
  read_at: string | null;
  created_at: string;
  updated_at: string;
};

export const getFollows = (signal?: AbortSignal) => api<Items<FollowItem>>('me/follows', {signal}).then(items);
export const followPerformer = (slug: string, mode: NotifyMode) =>
  api<{performer_slug: string; notify_mode: NotifyMode}>(`me/follows/${enc(slug)}`, {method: 'PUT', body: {notify_mode: mode}});
export const unfollowPerformer = (slug: string) => api<{deleted: boolean}>(`me/follows/${enc(slug)}`, {method: 'DELETE'});
export const getFollowingFeed = (limit: number, cursor?: string | null, signal?: AbortSignal) =>
  api<PageBody<FeedItem>>(`me/following/feed${query({limit, cursor})}`, {signal}).then(toPage);
/** The following feed narrowed to one followed performer (?performer=; 按演员筛选). */
export const getFollowingFeedFor = (performer: string, limit: number, cursor?: string | null, signal?: AbortSignal) =>
  api<PageBody<FeedItem>>(`me/following/feed${query({performer, limit, cursor})}`, {signal}).then(toPage);

export const getReservations = (signal?: AbortSignal) => api<Items<ReservationItem>>('me/reservations', {signal}).then(items);
export const reserveRelease = (slug: string) => api<{reserved: boolean}>(`me/reservations/${enc(slug)}`, {method: 'PUT'});
export const unreserveRelease = (slug: string) => api<{deleted: boolean}>(`me/reservations/${enc(slug)}`, {method: 'DELETE'});

export const getWatchLater = (signal?: AbortSignal) => api<Items<WatchLaterItem>>('me/watch-later', {signal}).then(items);
export const addWatchLater = (slug: string) => api<{saved: boolean}>(`me/watch-later/${enc(slug)}`, {method: 'PUT'});
export const removeWatchLater = (slug: string) => api<{deleted: boolean}>(`me/watch-later/${enc(slug)}`, {method: 'DELETE'});
export const reorderWatchLater = (slugs: string[]) => api<{saved: boolean}>('me/watch-later/reorder', {method: 'POST', body: {slugs}});

export const getSettings = (signal?: AbortSignal) => api<UserSettings>('me/settings', {signal});
export const patchSettings = (patch: Partial<UserSettings> | Record<string, unknown>) =>
  api<UserSettings>('me/settings', {method: 'PATCH', body: patch});

export const deleteWatchEntry = (slug: string) => api<{deleted: boolean}>(`me/watch-history/${enc(slug)}`, {method: 'DELETE'});
export const clearWatchHistory = () => api<{deleted: number}>('me/watch-history', {method: 'DELETE'});

export const getNotifications = (limit: number, cursor?: string | null, signal?: AbortSignal) =>
  api<PageBody<NotificationItem>>(`me/notifications${query({limit, cursor})}`, {signal}).then(toPage);
export const getUnreadCount = (signal?: AbortSignal) =>
  api<{unread: number}>('me/notifications/unread-count', {signal}).then(d => d?.unread ?? 0);
export const markNotificationsRead = (ids: number[] | 'all') =>
  api<{updated: number}>('me/notifications/read', {method: 'POST', body: ids === 'all' ? {all: true} : {ids}});

// ---- 观看统计 (WatchStats, PRD 20; internal/api/watchstats.go) ----
// Owner-only; built from user_watch_daily, which cmd/audit refreshes hourly.

export type WatchStatsRange = '7d' | '30d' | '365d';
export const WATCH_STATS_RANGES: WatchStatsRange[] = ['7d', '30d', '365d'];

export type WatchStats = {
  range: WatchStatsRange;
  /** First and last local date of the range (YYYY-MM-DD). */
  from: string;
  to: string;
  /** IANA zone the days are counted in (user_settings.timezone, else UTC). */
  tz: string;
  totals: {seconds: number; plays: number; titles: number; finished: number; completion_rate: number; active_days: number};
  /** Watch seconds by weekday (0 = Monday … 6 = Sunday) and hour (0–23). */
  heatmap: number[][];
  streak: {current: number; longest: number};
  daily: {day: string; seconds: number; finished: number}[];
  performers: {slug: string; name: string; name_ja?: string; seconds: number; portrait?: ApiImage | null}[];
  genres: {slug: string; name: string; seconds: number}[];
  /** False until the first statistics row exists (empty state). */
  has_data: boolean;
  updated_at: string | null;
};

export const getWatchStats = (range: WatchStatsRange, signal?: AbortSignal) =>
  api<WatchStats>(`me/watch-stats${query({range})}`, {signal});
