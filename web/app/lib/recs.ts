// Recommendations V1 client (backend internal/api/recs.go; PRD 4.1, 4.2,
// 6.1, 6.2, 10, 18):
//
//   GET /api/recs?slot=home|related|similar_performers|performers|tonight
//   GET /api/trending?period=today|week|month|year
//   GET /api/trending/performers?period=
//
// The routes are optional: a backend without them answers 404 (not merged)
// or 503 (recs store not configured). Either marks recommendations as
// unavailable for this page load, and every caller hides its section and
// keeps the pre-recs shelves instead of showing an error.

import {useEffect, useSyncExternalStore} from 'react';
import {api, ApiError, isMissingEndpoint, query, toPage, type Page} from './api';
import {t, uiLang} from './i18n';
import type {MessageKey} from './messages.zh-CN';

// ---- Types (mirror internal/api/recs.go) ----

export type RecReason = {code: string; text: string};

export type RecPerformer = {id: number; slug: string; name: string; display_name?: string};

/** recItem: one recommended release. */
export type RecItem = {
  id: number;
  slug: string;
  title: string;
  display_title?: string;
  runtime_minutes?: number;
  release_date?: string;
  maker_name?: string;
  series_name?: string;
  playable: boolean;
  rating: number | null;
  performer: RecPerformer | null;
  score: number;
  reason: RecReason;
  channels?: string[];
};

export type Movement = 'up' | 'down' | 'same' | 'new';

export type TrendingItem = {
  rank: number;
  prev_rank: number | null;
  score: number;
  plays: number;
  completion: number;
  movement: Movement;
  delta: number;
  release?: RecItem;
};

export type HeatItem = {
  rank: number;
  prev_rank: number | null;
  score: number;
  plays: number;
  new_follows: number;
  movement: Movement;
  delta: number;
  performer: RecPerformer;
};

/** A genre chip above the board: one of the most frequent tags among its releases. */
export type TrendingTag = {slug: string; name: string; count: number};

export type TrendingPage = Page<TrendingItem> & {period: string; computedAt: string | null; performers: HeatItem[]; tags: TrendingTag[]};

export type PerformerRecItem = {
  id: number;
  slug: string;
  name: string;
  display_name?: string;
  score: number;
  match_percent: number;
  evidence?: {co_star?: number; co_watch?: number; shared_tags?: string[]; shared_makers?: string[]};
  reason: RecReason;
};

export type TonightDuration = 'any' | '30' | '60' | '120' | 'series';
export type TonightResult = {item: RecItem | null; alternates: RecItem[]; duration: string; exclude: number[]};

export type Period = 'today' | 'week' | 'month' | 'year';
export const PERIODS: {value: Period; label: MessageKey}[] = [
  {value: 'today', label: 'trending.today'},
  {value: 'week', label: 'trending.week'},
  {value: 'month', label: 'trending.month'},
  {value: 'year', label: 'trending.year'},
];

// ---- Availability ----

type Availability = 'unknown' | 'yes' | 'no';
let availability: Availability = 'unknown';
const listeners = new Set<() => void>();

function setAvailability(next: Availability) {
  if (availability === next || availability === 'no') return;
  availability = next;
  listeners.forEach(l => l());
}

/** 404 / 405 / 501: route not deployed; 503: recs store not configured. */
export function recsMissing(error: unknown): boolean {
  return isMissingEndpoint(error) || (error instanceof ApiError && error.status === 503);
}

async function recsApi<T>(path: string, signal?: AbortSignal): Promise<T> {
  try {
    const data = await api<T>(path, {signal});
    setAvailability('yes');
    return data;
  } catch (error) {
    if (recsMissing(error)) setAvailability('no');
    throw error;
  }
}

let probe: Promise<unknown> | null = null;

/**
 * 'unknown' until the first recs / trending response, then 'yes' or 'no'.
 * With probe=true an unknown state is settled with one small request.
 */
export function useRecsAvailable(withProbe = false): Availability {
  const value = useSyncExternalStore(
    cb => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    () => availability,
    () => 'unknown' as Availability,
  );
  useEffect(() => {
    if (!withProbe || availability !== 'unknown' || probe) return;
    probe = recsApi(`trending${query({period: 'week', limit: 1})}`).catch(() => undefined);
  }, [withProbe]);
  return value;
}

// ---- Requests ----

export const getRecs = (
  slot: 'home' | 'related',
  opts: {release?: string; limit?: number; cursor?: string | null} = {},
  signal?: AbortSignal,
): Promise<Page<RecItem>> =>
  recsApi<{items: RecItem[] | null; next_cursor?: string | null}>(
    `recs${query({slot, release: opts.release, limit: opts.limit, cursor: opts.cursor})}`,
    signal,
  ).then(toPage);

export const getPerformerRecs = (
  slot: 'similar_performers' | 'performers',
  opts: {performer?: string; limit?: number; cursor?: string | null} = {},
  signal?: AbortSignal,
): Promise<Page<PerformerRecItem>> =>
  recsApi<{items: PerformerRecItem[] | null; next_cursor?: string | null}>(
    `recs${query({slot, performer: opts.performer, limit: opts.limit, cursor: opts.cursor})}`,
    signal,
  ).then(toPage);

/** 想要 / 和谁看: soft preferences on the backend (internal/recs prefs.go), never filters. */
export type TonightMood = 'any' | 'light' | 'thrill' | 'touching' | 'brainy';
export type TonightCompany = 'self' | 'friends' | 'family';

export const getTonight = (
  opts: {duration?: TonightDuration; mood?: TonightMood; company?: TonightCompany; exclude?: number[]},
  signal?: AbortSignal,
): Promise<TonightResult> =>
  recsApi<TonightResult>(
    `recs${query({
      slot: 'tonight',
      duration: opts.duration && opts.duration !== 'any' ? opts.duration : undefined,
      mood: opts.mood && opts.mood !== 'any' ? opts.mood : undefined,
      company: opts.company && opts.company !== 'self' ? opts.company : undefined,
      exclude: opts.exclude?.slice(-50).join(','),
    })}`,
    signal,
  ).then(r => ({...r, alternates: r.alternates ?? [], exclude: r.exclude ?? []}));

type TrendingBody = {
  items: TrendingItem[] | null;
  next_cursor?: string | null;
  period: string;
  computed_at?: string | null;
  performers?: HeatItem[] | null;
  tags?: TrendingTag[] | null;
};

/** getTrending: tag keeps the board's releases with that tag (genre chips; ranks stay board-wide). */
export const getTrending = (period: Period, opts: {limit?: number; cursor?: string | null; tag?: string} = {}, signal?: AbortSignal): Promise<TrendingPage> =>
  recsApi<TrendingBody>(`trending${query({period, tag: opts.tag, limit: opts.limit, cursor: opts.cursor})}`, signal).then(body => ({
    ...toPage(body),
    period: body.period,
    computedAt: body.computed_at ?? null,
    performers: body.performers ?? [],
    tags: body.tags ?? [],
  }));

export const getPerformerHeat = (period: Period, opts: {limit?: number; cursor?: string | null} = {}, signal?: AbortSignal): Promise<Page<HeatItem>> =>
  recsApi<{items: HeatItem[] | null; next_cursor?: string | null}>(`trending/performers${query({period, limit: opts.limit, cursor: opts.cursor})}`, signal).then(
    toPage,
  );

// ---- Display helpers ----

export const recTitle = (it: {title: string; display_title?: string}) => it.display_title || it.title;
export const performerName = (p: {name: string; display_name?: string} | null | undefined) => (p ? p.display_name || p.name : '');

/** Reason codes with a translatable template (internal/recs ReasonFor, performerRecs). */
const REASON_KEYS: Record<string, MessageKey> = {
  because_watched: 'reason.becauseWatched',
  cowatch: 'reason.cowatch',
  same_series: 'reason.sameSeries',
  same_maker: 'reason.sameMaker',
  similar_tags: 'reason.similarTags',
  same_performer: 'reason.samePerformer',
  followed_new: 'reason.followedNew',
  interests: 'reason.interests',
  trending: 'reason.trending',
  new: 'reason.new',
  hot_performer: 'reason.hotPerformer',
  similar_performer: 'reason.similarPerformer',
  similar_to_followed: 'reason.similarToFollowed',
  similar_to_watched: 'reason.similarToWatched',
};

/**
 * The one-line 推荐理由 (PRD 10 可解释). The backend writes zh-CN text with
 * the localized seed titles and names; other UI languages get the code's
 * template with what the item itself carries (series, maker, performer).
 */
export function reasonText(reason: RecReason | undefined, item?: Partial<RecItem>): string {
  if (!reason) return '';
  const key = REASON_KEYS[reason.code];
  if (uiLang() === 'zh-CN' || !key) return reason.text || (key ? t(key, {}) : '');
  const name = performerName(item?.performer ?? null);
  const vars: Record<string, string> = {name, series: item?.series_name ?? '', maker: item?.maker_name ?? ''};
  // Templates that need a value the item does not carry keep the server text.
  if ((reason.code === 'same_performer' || reason.code === 'followed_new') && !name) return reason.text;
  if (reason.code === 'because_watched' || reason.code === 'similar_tags' || reason.code === 'trending') return reason.text;
  return t(key, vars);
}

/**
 * Related-list tabs (Video 「相关推荐 / 同演员 / 同系列」). An item belongs to a
 * tab when its reason or any recall channel says so (it can be in both).
 */
export type RelatedTab = 'all' | 'performer' | 'series';
export function inRelatedTab(item: RecItem, tab: RelatedTab): boolean {
  if (tab === 'all') return true;
  if (tab === 'performer') return item.reason.code === 'same_performer' || !!item.channels?.includes('same_performer');
  return item.reason.code === 'same_series' || !!item.channels?.includes('series');
}

export function movementLabel(m: Movement, delta: number): string {
  if (m === 'new') return t('trending.new');
  if (m === 'up') return `▲ ${delta}`;
  if (m === 'down') return `▼ ${Math.abs(delta)}`;
  return '—';
}
