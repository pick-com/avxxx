// URL <-> ReleaseFilter for the filtered release lists (/discover, /search).
// Query keys mirror GET /api/releases: tag, maker, performer, year,
// min_runtime, max_runtime, sort, role, and the playback-source facets
// min_height (清晰度), subtitle (字幕) and audio_lang (语言).

import type {ReleaseFilter, ReleaseSort} from './api';
import {t} from './i18n';

export type DurationBucket = {key: string; label: string; min?: number; max?: number};

// Runtime facets (NavCategories 按时长), in minutes. Bounds are inclusive on
// the server, so adjacent buckets share their edge minute. Labels are getters
// so they follow the current UI language.
export const DURATION_BUCKETS: DurationBucket[] = [
  {key: 'lt60', get label() { return t('filter.durMax', {n: 60}); }, max: 60},
  {key: '60-120', get label() { return t('filter.durRange', {min: 60, max: 120}); }, min: 60, max: 120},
  {key: '120-180', get label() { return t('filter.durRange', {min: 120, max: 180}); }, min: 120, max: 180},
  {key: 'gt180', get label() { return t('filter.durMin', {n: 180}); }, min: 180},
];

export const RELEASE_SORTS: {value: ReleaseSort; label: string}[] = [
  {value: 'newest', get label() { return t('filter.sortNewest'); }},
  {value: 'oldest', get label() { return t('filter.sortOldest'); }},
  {value: 'runtime', get label() { return t('filter.sortRuntime'); }},
];

/** The years offered by the year facet: this year back through the last 15. */
export function yearOptions(now = new Date()): number[] {
  const current = now.getFullYear();
  return Array.from({length: 16}, (_, i) => current - i);
}

/** 清晰度 buckets (GET /api/catalog/facets heights), in lines. */
export const HEIGHT_BUCKETS = [720, 1080, 2160] as const;

export const heightLabel = (h: number) => (h >= 2160 ? '4K' : `${h}p`);

const LANG_CODE = /^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$/;

const int = (value: string | null, min: number, max: number): number | undefined => {
  if (!value || !/^\d+$/.test(value)) return undefined;
  const n = Number(value);
  return n >= min && n <= max ? n : undefined;
};

export function parseReleaseFilter(params: URLSearchParams): ReleaseFilter {
  const sort = params.get('sort');
  const min = int(params.get('min_runtime'), 1, 10000);
  let max = int(params.get('max_runtime'), 1, 10000);
  if (min !== undefined && max !== undefined && min > max) max = undefined;
  return {
    tag: params.get('tag')?.trim() || undefined,
    maker: params.get('maker')?.trim() || undefined,
    performer: params.get('performer')?.trim() || undefined,
    year: int(params.get('year'), 1900, 9999),
    min_runtime: min,
    max_runtime: max,
    sort: sort === 'newest' || sort === 'oldest' || sort === 'runtime' ? sort : undefined,
    ...facetFilter(params),
  };
}

/** The playback-source facets and credit role from the URL (omitted when absent or malformed). */
function facetFilter(params: URLSearchParams): Pick<ReleaseFilter, 'role' | 'min_height' | 'subtitle' | 'audio_lang'> {
  const out: Pick<ReleaseFilter, 'role' | 'min_height' | 'subtitle' | 'audio_lang'> = {};
  const role = params.get('role');
  if ((role === 'lead' || role === 'guest') && params.get('performer')) out.role = role;
  const height = int(params.get('min_height'), 1, 4320);
  if (height) out.min_height = height;
  const subtitle = params.get('subtitle')?.trim();
  if (subtitle && (subtitle === 'any' || LANG_CODE.test(subtitle))) out.subtitle = subtitle;
  const audio = params.get('audio_lang')?.trim();
  if (audio && LANG_CODE.test(audio)) out.audio_lang = audio;
  return out;
}

/** Writes filter keys onto a copy of params; undefined removes a key. */
export function withFilter(params: URLSearchParams, patch: Partial<Record<keyof ReleaseFilter, string | number | undefined>>): URLSearchParams {
  const next = new URLSearchParams(params);
  for (const [k, v] of Object.entries(patch)) {
    if (v === undefined || v === '' || v === 0) next.delete(k);
    else next.set(k, String(v));
  }
  return next;
}

export function bucketFor(filter: ReleaseFilter): DurationBucket | undefined {
  return DURATION_BUCKETS.find(b => b.min === filter.min_runtime && b.max === filter.max_runtime);
}

export function durationLabel(filter: ReleaseFilter): string {
  const bucket = bucketFor(filter);
  if (bucket) return bucket.label;
  if (filter.min_runtime && filter.max_runtime) return t('filter.durRange', {min: filter.min_runtime, max: filter.max_runtime});
  if (filter.min_runtime) return t('filter.durMin', {n: filter.min_runtime});
  if (filter.max_runtime) return t('filter.durMax', {n: filter.max_runtime});
  return '';
}

/** Link to the filtered list for one facet value. */
export function discoverHref(patch: Partial<Record<keyof ReleaseFilter, string | number | undefined>>): string {
  const params = withFilter(new URLSearchParams(), patch);
  const text = params.toString();
  return text ? `/discover?${text}` : '/discover';
}
