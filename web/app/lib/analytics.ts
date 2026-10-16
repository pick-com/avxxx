// Analytics event client (PRD 14; backend internal/events + POST /api/events).
//
// Every event carries the PRD 14.1 common fields at the top level and its
// event-specific props under `props`, exactly as internal/events/registry.go
// (schema_ver 1) defines them. EventProps below mirrors that registry for the
// events this client sends; add an event here only after it exists there —
// unknown names or bad props are quarantined server-side, never stored.
//
// Delivery (PRD 14.5): events queue in memory and in localStorage (bounded),
// and go out in batches of 20 or every 30 s. A failed batch (offline, 5xx,
// 429) stays queued and is retried; event_id dedupe makes re-sends safe. On
// pagehide / visibilitychange→hidden everything left goes out with
// fetch(keepalive) so play_end survives closing the tab. fetch keepalive is
// used instead of sendBeacon because the request carries the bearer token
// (the server attaches user_id from it, and only with analytics consent).
//
// Privacy (PRD 14.5): `consent` is consentField(). device_id exists only
// while analytics consent is given and no GPC signal is present; without it
// the stored id is deleted and events are anonymous (the server strips
// user_id / device_id as well). The token is only sent with that consent.
//
// A 404 / 405 from POST /api/events means the endpoint is not deployed:
// sending stops for this page load and the bounded queue waits for the next.
//
// Debug (dev builds only): open any page with ?analytics_debug=1 to log every
// event and batch, with the server's accepted / duplicates / quarantined
// counts, to the console. ?analytics_debug=0 turns it off.

import {tokens} from './api';
import {consentField, detectGpc} from './consent';

declare const __APP_VERSION__: string;

const SCHEMA_VER = 1;
const BATCH_SIZE = 20;
const FLUSH_MS = 30_000;
const MAX_QUEUE = 300; // bounded local queue: oldest events are dropped first
const MAX_PER_REQUEST = 50; // server limit (maxEventBatch)
const MAX_KEEPALIVE_BYTES = 60_000; // browsers cap in-flight keepalive bodies at 64 KB
const QUEUE_KEY = 'analytics_queue_v1';
const DEVICE_KEY = 'analytics_device_id';
const SESSION_KEY = 'analytics_session_id';
const DEBUG_KEY = 'analytics_debug';

// ---- Registry mirror (internal/events/registry.go, schema_ver 1) ----

export type PlayEntryKind = 'home_row' | 'search' | 'actor' | 'recommend' | 'history' | 'favorites' | 'playlist' | 'autoplay' | 'deeplink' | 'other';
export type SeekMethod = 'drag' | 'double_tap' | 'chapter' | 'peak' | 'keyboard';
export type ChangeMode = 'manual' | 'auto';
export type PlayEndReason = 'completed' | 'closed' | 'autoplay_next' | 'error';
export type ConsentCategory = 'necessary' | 'analytics' | 'personalized_ads';

export type EventProps = {
  play_request: {release_id: number; source_id: number; entry: PlayEntryKind; reason?: string};
  first_frame: {source_id: number; quality: string; elapsed_ms: number; network: string};
  heartbeat: {position_ms: number; rate: number; quality: string; fullscreen: boolean; pip: boolean; foreground: boolean; buckets?: string};
  rebuffer: {source_id: number; duration_ms: number; position_ms: number};
  seek: {from_ms: number; to_ms: number; method: SeekMethod};
  pause: {position_ms: number};
  resume: {position_ms: number; paused_ms: number};
  quality_change: {from: string; to: string; mode: ChangeMode};
  rate_change: {from: number; to: number; mode: ChangeMode};
  source_switch: {from_source_id: number; to_source_id: number; mode: ChangeMode; reason: string};
  play_end: {reason: PlayEndReason; watched_seconds: number; buckets?: string; error_code?: string};
  progress: {release_id: number; milestone: 25 | 50 | 75 | 100};
  autoplay_next: {from_release_id: number; to_release_id: number; cancelled: boolean; countdown_left_s: number};
  actor_card_open: {performer_id: number; source: 'hover' | 'half_sheet' | 'search'};
  follow_actor: {performer_id: number; source: string};
  search: {query: string; result_count: number; is_retry: boolean; clicked_rank?: number};
  subtitle_change: {lang: string; from?: string};
  audio_change: {lang: string; from?: string};
  theme_change: {theme: 'system' | 'dark' | 'light'};
  consent_change: {categories: ConsentCategory[]; gpc?: boolean};
  // 14.4 弹幕 / 高能波峰
  peak_click: {curve: 'danmaku' | 'playback'; position_ms: number; top3: boolean; watched_30s: boolean};
  danmaku_toggle: {enabled: boolean};
  danmaku_send: {release_id: number; position_ms: number; length: number; color: string};
  danmaku_report: {danmaku_id: number; reason: string};
  danmaku_setting: {setting: string};
  // 14.4 评分与短评 (score on the 10-point scale)
  rating_submit: {release_id: number; score: number; watched: boolean};
  review_post: {release_id: number; spoiler: boolean; watched: boolean};
  review_like: {review_id: number};
};
export type EventName = keyof EventProps;

/** Events that belong to one playback and must carry play_id (Schema.PlayScoped). */
const PLAY_SCOPED = new Set<EventName>([
  'play_request',
  'first_frame',
  'heartbeat',
  'rebuffer',
  'seek',
  'pause',
  'resume',
  'quality_change',
  'rate_change',
  'source_switch',
  'play_end',
  'progress',
  'peak_click',
]);

/** PRD 14.1 wire format of one event. */
export type WireEvent = {
  event_id: string;
  event: EventName;
  schema_ver: number;
  ts_client: number;
  device_id?: string;
  session_id: string;
  play_id?: string;
  platform: string;
  app_ver: string;
  ui_lang?: string;
  theme?: string;
  page?: string;
  ref_page?: string;
  slot?: string;
  consent: string[];
  exp: string[];
  props: Record<string, unknown>;
};

// ---- Ids ----

function uuid(): string {
  try {
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  } catch {
    /* insecure context */
  }
  const bytes = new Uint8Array(16);
  if (typeof crypto !== 'undefined' && crypto.getRandomValues) crypto.getRandomValues(bytes);
  else for (let i = 0; i < 16; i++) bytes[i] = Math.floor(Math.random() * 256);
  return Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
}

export const newPlayId = uuid;

function sessionId(): string {
  try {
    let id = window.sessionStorage.getItem(SESSION_KEY);
    if (!id) {
      id = uuid();
      window.sessionStorage.setItem(SESSION_KEY, id);
    }
    return id;
  } catch {
    return (memorySession ??= uuid());
  }
}
let memorySession: string | undefined;

function analyticsAllowed(consent: string[]): boolean {
  return consent.includes('analytics') && !detectGpc();
}

/** device_id only with analytics consent and no GPC; otherwise the stored id is removed. */
function deviceId(allowed: boolean): string | undefined {
  try {
    if (!allowed) {
      window.localStorage.removeItem(DEVICE_KEY);
      return undefined;
    }
    let id = window.localStorage.getItem(DEVICE_KEY);
    if (!id) {
      id = uuid();
      window.localStorage.setItem(DEVICE_KEY, id);
    }
    return id;
  } catch {
    return undefined;
  }
}

// ---- Context: page / ref_page / ui_lang / theme ----

const context: {page?: string; refPage?: string; slot?: string; uiLang?: string} = {};

/** Called by the route tracker on every navigation. */
export function setPage(page: string, slot?: string) {
  context.refPage = context.page;
  context.page = page;
  context.slot = slot;
}

export const currentPage = () => ({page: context.page, refPage: context.refPage});

export function setUiLang(lang: string) {
  context.uiLang = lang;
}

function themePref(): string {
  try {
    const t = window.localStorage.getItem('theme');
    return t === 'dark' || t === 'light' ? t : 'system';
  } catch {
    return 'system';
  }
}

/** Route name for `page` / `ref_page` (PRD 14.1). */
export function pageName(pathname: string): string {
  if (pathname === '/' || pathname === '') return 'home';
  const first = pathname.split('/')[1] ?? '';
  switch (first) {
    case 'releases':
      return 'watch';
    case 'performers':
      return 'actor';
    case 'legal':
      return 'legal';
    case 'admin':
      return 'admin';
    default:
      return first || 'home';
  }
}

// ---- Queue ----

let queue: WireEvent[] = [];
let loaded = false;
let inflight = false;
let disabled = false;
let timer: number | undefined;
let installed = false;

function load() {
  if (loaded) return;
  loaded = true;
  try {
    const raw = window.localStorage.getItem(QUEUE_KEY);
    const stored = raw ? (JSON.parse(raw) as WireEvent[]) : [];
    if (Array.isArray(stored)) queue = stored.filter(e => e && typeof e.event_id === 'string').concat(queue);
  } catch {
    /* corrupt or unavailable: start empty */
  }
  trim();
}

function trim() {
  if (queue.length > MAX_QUEUE) queue = queue.slice(queue.length - MAX_QUEUE);
}

function persist() {
  try {
    if (queue.length === 0) window.localStorage.removeItem(QUEUE_KEY);
    else window.localStorage.setItem(QUEUE_KEY, JSON.stringify(queue));
  } catch {
    /* quota or private mode: the in-memory queue still sends */
  }
}

function debugOn(): boolean {
  if (!import.meta.env.DEV) return false;
  try {
    const param = new URLSearchParams(window.location.search).get('analytics_debug');
    if (param === '1') window.sessionStorage.setItem(DEBUG_KEY, '1');
    if (param === '0') window.sessionStorage.removeItem(DEBUG_KEY);
    return window.sessionStorage.getItem(DEBUG_KEY) === '1';
  } catch {
    return false;
  }
}

type TrackOptions = {playId?: string; slot?: string};

/** Queue one event. Never throws; analytics must not break the page. */
export function track<E extends EventName>(event: E, props: EventProps[E], options: TrackOptions = {}) {
  if (typeof window === 'undefined') return;
  try {
    install();
    if (PLAY_SCOPED.has(event) && !options.playId) return; // would be quarantined
    const consent = consentField();
    const allowed = analyticsAllowed(consent);
    const clean: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(props)) if (v !== undefined && v !== null) clean[k] = v;
    const e: WireEvent = {
      event_id: uuid(),
      event,
      schema_ver: SCHEMA_VER,
      ts_client: Date.now(),
      session_id: sessionId(),
      platform: 'web',
      app_ver: typeof __APP_VERSION__ === 'string' ? __APP_VERSION__ : '0.0.0',
      consent,
      exp: [],
      props: clean,
    };
    const device = deviceId(allowed);
    if (device) e.device_id = device;
    if (options.playId) e.play_id = options.playId;
    if (context.uiLang) e.ui_lang = context.uiLang;
    e.theme = themePref();
    if (context.page) e.page = context.page;
    if (context.refPage) e.ref_page = context.refPage;
    const slot = options.slot ?? context.slot;
    if (slot) e.slot = slot;
    if (debugOn()) console.debug('[analytics] queue', event, e);
    queue.push(e);
    trim();
    persist();
    if (queue.length >= BATCH_SIZE) void flush();
  } catch {
    /* ignore */
  }
}

type IngestResult = {accepted: number; duplicates: number; quarantined: number};

function headers(): Headers {
  const h = new Headers({'Content-Type': 'application/json', Accept: 'application/json'});
  const token = tokens.user();
  if (token && analyticsAllowed(consentField())) h.set('Authorization', `Bearer ${token}`);
  return h;
}

function remove(ids: Set<string>) {
  queue = queue.filter(e => !ids.has(e.event_id));
  persist();
}

async function send(batch: WireEvent[], keepalive: boolean): Promise<void> {
  const debug = debugOn();
  let response: Response;
  try {
    response = await fetch('/api/events', {method: 'POST', headers: headers(), body: JSON.stringify(batch), keepalive});
  } catch (cause) {
    if (debug) console.debug('[analytics] batch failed (kept for retry)', cause);
    return; // offline: keep queued
  }
  if (response.status === 404 || response.status === 405) {
    disabled = true; // endpoint not deployed on this backend
    if (debug) console.debug('[analytics] /api/events missing; sending paused for this page load');
    return;
  }
  if (response.status >= 500 || response.status === 429) {
    if (debug) console.debug('[analytics] batch failed with', response.status, '(kept for retry)');
    return;
  }
  // 2xx: stored / deduped / quarantined. Other 4xx: malformed batch; drop it
  // rather than retrying forever.
  remove(new Set(batch.map(e => e.event_id)));
  if (debug) {
    let result: IngestResult | null = null;
    try {
      result = (await response.json()) as IngestResult;
    } catch {
      /* no body */
    }
    console.groupCollapsed(`[analytics] batch of ${batch.length} → ${response.status}`, result ?? '');
    console.table(batch.map(e => ({event: e.event, play_id: e.play_id ?? '', page: e.page ?? '', props: JSON.stringify(e.props)})));
    console.groupEnd();
  }
}

/** Send queued events now. `unload` sends everything with keepalive. */
export async function flush(unload = false): Promise<void> {
  if (typeof window === 'undefined' || disabled) return;
  load();
  if (queue.length === 0) return;
  if (unload) {
    // Everything left, in server-sized chunks within the keepalive budget.
    let budget = MAX_KEEPALIVE_BYTES;
    for (let i = 0; i < queue.length; i += MAX_PER_REQUEST) {
      const chunk = queue.slice(i, i + MAX_PER_REQUEST);
      const size = JSON.stringify(chunk).length;
      if (size > budget) break;
      budget -= size;
      void send(chunk, true);
    }
    return;
  }
  if (inflight) return;
  inflight = true;
  try {
    while (queue.length > 0 && !disabled) {
      const before = queue.length;
      const batch = queue.slice(0, BATCH_SIZE);
      await send(batch, false);
      if (queue.length >= before) break; // not acknowledged: retry on the next tick
    }
  } finally {
    inflight = false;
  }
}

const unloadHandlers = new Set<() => void>();

/** Run fn on pagehide before the final flush (e.g. emit play_end). */
export function onUnload(fn: () => void): () => void {
  unloadHandlers.add(fn);
  return () => unloadHandlers.delete(fn);
}

function install() {
  if (installed || typeof window === 'undefined') return;
  installed = true;
  load();
  timer = window.setInterval(() => void flush(), FLUSH_MS);
  window.addEventListener('pagehide', () => {
    for (const fn of Array.from(unloadHandlers)) {
      try {
        fn();
      } catch {
        /* ignore */
      }
    }
    void flush(true);
  });
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') void flush(true);
  });
  window.addEventListener('online', () => void flush());
}

/** Start the flush timer and send anything left from an earlier visit. */
export function startAnalytics() {
  install();
  void flush();
}

/** For tests / teardown. */
export function stopAnalytics() {
  window.clearInterval(timer);
}

// ---- Helpers shared by instrumented components ----

/** navigator.connection effective type for first_frame `network`. */
export function networkType(): string {
  const c = (navigator as Navigator & {connection?: {type?: string; effectiveType?: string}}).connection;
  if (!c) return 'unknown';
  if (c.type && c.type !== 'unknown') return c.effectiveType ? `${c.type}:${c.effectiveType}` : c.type;
  return c.effectiveType || 'unknown';
}

/** play_request `entry` from where the viewer came from. */
export function entryFrom(refPage: string | undefined, opts: {playlist?: boolean; autoplay?: boolean} = {}): PlayEntryKind {
  if (opts.autoplay) return 'autoplay';
  if (opts.playlist) return 'playlist';
  switch (refPage) {
    case undefined:
      return 'deeplink';
    case 'home':
      return 'home_row';
    case 'search':
      return 'search';
    case 'actor':
      return 'actor';
    case 'watch':
      return 'recommend';
    case 'history':
      return 'history';
    case 'favorites':
      return 'favorites';
    case 'later':
      return 'playlist';
    default:
      return 'other';
  }
}
