// URL <-> ReleaseFilter for the filtered release lists (/discover, /search).
// Query keys mirror GET /api/releases: tag, maker, performer, year,
// min_runtime, max_runtime, sort.

import type {ReleaseFilter, ReleaseSort} from './api';

export type DurationBucket = {key: string; label: string; min?: number; max?: number};

// Runtime facets (NavCategories 按时长), in minutes. Bounds are inclusive on
// the server, so adjacent buckets share their edge minute.
export const DURATION_BUCKETS: DurationBucket[] = [
  {key: 'lt60', label: '60 分钟内', max: 60},
  {key: '60-120', label: '60–120 分钟', min: 60, max: 120},
  {key: '120-180', label: '120–180 分钟', min: 120, max: 180},
  {key: 'gt180', label: '180 分钟以上', min: 180},
];

export const RELEASE_SORTS: {value: ReleaseSort; label: string}[] = [
  {value: 'newest', label: '最新'},
  {value: 'oldest', label: '最早'},
  {value: 'runtime', label: '时长'},
];

/** The years offered by the year facet: this year back through the last 15. */
export function yearOptions(now = new Date()): number[] {
  const current = now.getFullYear();
  return Array.from({length: 16}, (_, i) => current - i);
}

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
  };
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
  if (filter.min_runtime && filter.max_runtime) return `${filter.min_runtime}–${filter.max_runtime} 分钟`;
  if (filter.min_runtime) return `${filter.min_runtime} 分钟以上`;
  if (filter.max_runtime) return `${filter.max_runtime} 分钟内`;
  return '';
}

/** Link to the filtered list for one facet value. */
export function discoverHref(patch: Partial<Record<keyof ReleaseFilter, string | number | undefined>>): string {
  const params = withFilter(new URLSearchParams(), patch);
  const text = params.toString();
  return text ? `/discover?${text}` : '/discover';
}
