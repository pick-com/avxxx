import {useEffect, useState, type FormEvent} from 'react';
import {data, useLoaderData, useSearchParams, type LoaderFunctionArgs, type MetaFunction} from 'react-router';
import {LoadMore} from '../components/Paging';
import {SourceCredits} from '../components/SourceCredits';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {query, toPage, type LangCode, type Page} from '../lib/api';
import {isGated} from '../lib/ageGate.server';
import {serverApi} from '../lib/api.server';
import {serverCredits} from '../lib/credits.server';
import {useCredits, type Attribution} from '../lib/evidence';
import {formatCount, formatDate} from '../lib/format';
import {t, useI18n, useT} from '../lib/i18n';
import {routeLang} from '../lib/langs';
import type {MessageKey} from '../lib/messages.zh-CN';
import {Link} from '../lib/nav';
import {breadcrumbs, pageUrl, rootMeta, seoMeta, type SeoHandle} from '../lib/seo';
import {listSeriesPage, parseSeriesSort, SERIES_SORTS, seriesListPath, type SeriesListItem, type SeriesSort} from '../lib/series';
import {usePaged} from '../lib/usePaged';
import '../styles/series.css';

export const handle: SeoHandle = {seo: true};

// /series: every series with a published release (GET /api/series), sorted
// by 最近更新 (default), 作品最多 or 名称, with a name search. Server rendered
// (PRD 13): the loader fetches the first page for the URL's q / sort.
type SeriesListLoad = {
  lang: LangCode;
  search: string;
  page?: Page<SeriesListItem>;
  credits?: Attribution[];
};

const SORT_LABELS: Record<SeriesSort, MessageKey> = {
  newest: 'series.sortNewest',
  works: 'series.sortWorks',
  name: 'series.sortName',
};

export async function loader({params, request}: LoaderFunctionArgs) {
  const lang = routeLang(params);
  if (!lang) return null;
  const url = new URL(request.url);
  const q = url.searchParams.get('q') ?? '';
  const sort = parseSeriesSort(url.searchParams.get('sort'));
  const gated = isGated(request);
  const [page, credits] = await Promise.all([
    serverApi<{items: SeriesListItem[] | null; next_cursor?: string | null}>(seriesListPath(q, sort), {lang, signal: request.signal}).then(toPage, () => undefined),
    gated ? undefined : serverCredits({kind: null}, request.signal),
  ]);
  // 18+ gate: the list only as the names and links of the head's ItemList.
  const listed = gated && page ? {...page, items: page.items.map(s => ({slug: s.slug, name: s.name}) as SeriesListItem)} : page;
  const body: SeriesListLoad = {lang, search: url.searchParams.toString(), page: listed, credits};
  return page ? body : data(body, {status: 503});
}

export const clientLoader = () => null;

export const meta: MetaFunction<typeof loader> = ({location, matches, data: d}) => {
  const params = new URLSearchParams(location.search);
  const load = d as SeriesListLoad | null | undefined;
  const {lang, origin} = rootMeta(matches);
  const path = '/series';
  const items = load?.page?.items ?? [];
  const list = {
    '@context': 'https://schema.org',
    '@type': 'ItemList',
    name: t('seo.seriesListTitle'),
    itemListElement: items.map((s, i) => ({'@type': 'ListItem', position: i + 1, name: s.name, url: pageUrl(origin, `/series/${encodeURIComponent(s.slug)}`, lang)})),
  };
  return seoMeta({
    matches,
    path,
    title: t('title.named', {name: t('seo.seriesListTitle')}),
    description: t('seo.seriesListDescription'),
    // Searches, other sorts and empty lists are not pages of their own.
    noindex: params.has('q') || params.has('sort') || params.has('cursor') || items.length === 0,
    jsonLd: [
      list,
      breadcrumbs(matches, [
        [t('seo.breadcrumbHome'), '/'],
        [t('seo.breadcrumbSeries'), path],
      ]),
    ],
  });
};

export default function SeriesListPage() {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const {contentLang} = useI18n();
  const q = params.get('q') ?? '';
  const sort = parseSeriesSort(params.get('sort'));
  const loaded = useLoaderData<typeof loader>() as SeriesListLoad | null;
  const own = loaded && loaded.lang === contentLang && loaded.search === params.toString() ? loaded : null;
  const list = usePaged((cursor, signal) => listSeriesPage(q, sort, cursor, signal), [q, sort], true, own?.page);
  const credits = useCredits({kind: null}, loaded?.credits);
  const [draft, setDraft] = useState(q);
  useEffect(() => setDraft(q), [q]);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const next = new URLSearchParams(params);
    if (draft.trim()) next.set('q', draft.trim());
    else next.delete('q');
    setParams(next, {replace: true});
  };
  const sortHref = (s: SeriesSort) => `/series${query({q: q || undefined, sort: s === 'newest' ? undefined : s})}`;

  return (
    <div className="page series-list-page">
      <header className="series-list-head">
        <h1>{t('series.title')}</h1>
        {/* Without JavaScript the form submits ?q= to this URL. */}
        <form role="search" className="series-search" onSubmit={submit}>
          <label className="sr-only" htmlFor="series-q">
            {t('series.searchLabel')}
          </label>
          <input id="series-q" name="q" type="search" value={draft} placeholder={t('series.searchPlaceholder')} onChange={e => setDraft(e.target.value)} />
          {sort !== 'newest' && <input type="hidden" name="sort" value={sort} />}
          <button type="submit" className="btn btn-secondary btn-sm">
            {t('series.search')}
          </button>
        </form>
      </header>
      <nav className="chip-row" aria-label={t('series.sortLabel')}>
        {SERIES_SORTS.map(s => (
          <Link key={s} to={sortHref(s)} replace className={s === sort ? 'chip chip-on' : 'chip'} aria-current={s === sort ? 'true' : undefined}>
            {t(SORT_LABELS[s])}
          </Link>
        ))}
      </nav>

      {list.error ? (
        <ErrorBanner error={list.error} onRetry={list.reload} />
      ) : list.loading ? (
        list.slow && <CardGridSkeleton count={8} variant="row" />
      ) : list.items.length === 0 ? (
        q ? (
          <EmptyState title={t('series.noResults', {q})} body={t('series.noResultsBody')} actions={[{to: '/series', label: t('series.clearSearch')}]} />
        ) : (
          <EmptyState title={t('series.empty')} body={t('series.emptyBody')} actions={[{to: '/discover', label: t('seo.breadcrumbDiscover')}]} />
        )
      ) : (
        <>
          <ul className="series-grid">
            {list.items.map(s => (
              <li key={s.id}>
                <SeriesCard series={s} />
              </li>
            ))}
          </ul>
          <LoadMore state={list} label={t('series.more')} />
        </>
      )}
      <SourceCredits items={credits} />
    </div>
  );
}

function SeriesCard({series: s}: {series: SeriesListItem}) {
  const t = useT();
  const first = formatDate(s.first_release_date);
  const latest = formatDate(s.latest_release_date);
  const range = first && latest ? (first === latest ? first : t('series.dateRange', {from: first, to: latest})) : first || latest;
  return (
    <Link to={`/series/${encodeURIComponent(s.slug)}`} className="series-card">
      <span className="series-card-name">{s.name}</span>
      {s.name_ja && s.name_ja !== s.name && <span className="series-card-sub">{s.name_ja}</span>}
      <span className="series-card-meta">
        <strong>{t('series.works', {n: formatCount(s.release_count)})}</strong>
        {s.maker_name && <span>{s.maker_name}</span>}
        {range && <span>{range}</span>}
      </span>
    </Link>
  );
}
