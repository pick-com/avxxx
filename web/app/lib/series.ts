import {api, query, toPage, type Page, type Release} from './api';

// Series (internal/api/series.go; PRD 5.4 同系列):
//   GET /api/series?q=&sort=newest|name|works&limit=&cursor=&lang=
//   GET /api/series/{slug}?lang=
//   GET /api/series/{slug}/releases?sort=episode|newest|oldest&limit=&cursor=&lang=
// `series` is a localized path (lib/api.ts): ?lang= is added and
// display_name / display_title replace name / canonical_title.

export type SeriesSort = 'newest' | 'name' | 'works';
export const SERIES_SORTS: SeriesSort[] = ['newest', 'works', 'name'];
export const parseSeriesSort = (raw: string | null): SeriesSort => (raw === 'name' || raw === 'works' ? raw : 'newest');

export type SeriesListItem = {
  id: number;
  slug: string;
  name: string;
  name_ja?: string;
  maker_name?: string;
  release_count: number;
  first_release_date?: string | null;
  latest_release_date?: string | null;
};

export type SeriesPerformer = {
  id: number;
  slug: string;
  name: string;
  name_ja?: string;
  release_count: number;
  display_name?: string;
};

export type SeriesDetail = SeriesListItem & {
  makers: {name: string; release_count: number}[] | null;
  top_performers: SeriesPerformer[] | null;
};

/** Release order inside a series: by episode number (default) or date. */
export type SeriesReleaseSort = 'episode' | 'newest' | 'oldest';
export const parseSeriesReleaseSort = (raw: string | null): SeriesReleaseSort => (raw === 'newest' || raw === 'oldest' ? raw : 'episode');

export type SeriesRelease = Release & {product_code?: string; episode?: number | null};

/** The performer's name in the content language (nested objects are not mapped by localizeBody). */
export const seriesPerformerName = (p: SeriesPerformer) => p.display_name || p.name;

type Body<T> = {items: T[] | null; next_cursor?: string | null};

export const SERIES_PAGE_SIZE = 24;

export const seriesListPath = (q: string, sort: SeriesSort, cursor?: string | null, limit = SERIES_PAGE_SIZE) =>
  `series${query({q: q.trim() || undefined, sort: sort === 'newest' ? undefined : sort, limit, cursor: cursor || undefined})}`;

export const seriesReleasesPath = (slug: string, sort: SeriesReleaseSort, cursor?: string | null, limit = SERIES_PAGE_SIZE) =>
  `series/${encodeURIComponent(slug)}/releases${query({sort: sort === 'episode' ? undefined : sort, limit, cursor: cursor || undefined})}`;

export const listSeriesPage = (q: string, sort: SeriesSort, cursor?: string | null, signal?: AbortSignal): Promise<Page<SeriesListItem>> =>
  api<Body<SeriesListItem>>(seriesListPath(q, sort, cursor), {signal}).then(toPage);

export const getSeries = (slug: string, signal?: AbortSignal) => api<SeriesDetail>(`series/${encodeURIComponent(slug)}`, {signal});

/** A page of a series' releases; basis is how episode numbers were derived (sort=episode). */
export type SeriesReleasePage = Page<SeriesRelease> & {basis?: string};

export const toSeriesReleasePage = (b: (Body<SeriesRelease> & {episode_basis?: string}) | null): SeriesReleasePage => ({...toPage(b), basis: b?.episode_basis});

export const listSeriesReleases = (slug: string, sort: SeriesReleaseSort, cursor?: string | null, signal?: AbortSignal): Promise<SeriesReleasePage> =>
  api<Body<SeriesRelease> & {episode_basis?: string}>(seriesReleasesPath(slug, sort, cursor), {signal}).then(toSeriesReleasePage);

/** Most releases the player's 选集 drawer and the 同系列 panel list (the API's page maximum). */
export const SERIES_EPISODES_LIMIT = 100;

/** The series' releases in episode order, the first SERIES_EPISODES_LIMIT (选集 · MobilePlayerFull). */
export const listSeriesEpisodes = (slug: string, signal?: AbortSignal): Promise<SeriesReleasePage> =>
  api<Body<SeriesRelease> & {episode_basis?: string}>(seriesReleasesPath(slug, 'episode', null, SERIES_EPISODES_LIMIT), {signal}).then(toSeriesReleasePage);
