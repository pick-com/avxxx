import {useEffect, useState} from 'react';
import {data, useLoaderData, useParams, useSearchParams, type LoaderFunctionArgs, type MetaFunction} from 'react-router';
import {PerformerChip, ReleaseCard, SectionHeader} from '../components/Cards';
import {LoadMore} from '../components/Paging';
import {SourceCredits} from '../components/SourceCredits';
import {BlockSkeleton, CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {ApiError, type LangCode} from '../lib/api';
import {isGone, serverApi} from '../lib/api.server';
import {serverCredits} from '../lib/credits.server';
import {useCredits, type Attribution} from '../lib/evidence';
import {formatCount, formatDate, formatRuntime} from '../lib/format';
import {t, useI18n, useT} from '../lib/i18n';
import {routeLang} from '../lib/langs';
import type {MessageKey} from '../lib/messages.zh-CN';
import {Link} from '../lib/nav';
import {discoverHref} from '../lib/releaseFilter';
import {breadcrumbs, compact, isoDate, pageUrl, rootMeta, seoMeta, type SeoHandle} from '../lib/seo';
import {
  getSeries,
  listSeriesReleases,
  parseSeriesReleaseSort,
  seriesPerformerName,
  seriesReleasesPath,
  type SeriesDetail,
  toSeriesReleasePage,
  type SeriesReleasePage,
  type SeriesReleaseSort,
} from '../lib/series';
import {useAsync} from '../lib/useAsync';
import {usePaged} from '../lib/usePaged';
import '../styles/series.css';

export const handle: SeoHandle = {seo: true};

// /series/{slug} (PRD 5.4 同系列): counts, date range, makers and the most
// credited performers, then the releases in episode order (default) or
// newest / oldest first, cursor paged. Server rendered (PRD 13): the loader
// fetches the series and the first page for the URL's ?sort=.
type SeriesLoad = {
  lang: LangCode;
  slug: string;
  sort: SeriesReleaseSort;
  detail?: SeriesDetail;
  releases?: SeriesReleasePage;
  credits?: Attribution[];
  missing?: boolean;
  unavailable?: boolean;
};

const ORDERS: {key: SeriesReleaseSort; label: MessageKey}[] = [
  {key: 'episode', label: 'series.orderEpisode'},
  {key: 'newest', label: 'series.orderNewest'},
  {key: 'oldest', label: 'series.orderOldest'},
];

const BASIS_LABELS: Record<string, MessageKey> = {
  title: 'series.basisTitle',
  product_code: 'series.basisProductCode',
  release_date: 'series.basisReleaseDate',
};

export async function loader({params, request}: LoaderFunctionArgs) {
  const lang = routeLang(params);
  const slug = params.slug ?? '';
  if (!lang) return null;
  const sort = parseSeriesReleaseSort(new URL(request.url).searchParams.get('sort'));
  const opts = {lang, signal: request.signal};
  try {
    const [detail, releases, credits] = await Promise.all([
      serverApi<SeriesDetail>(`series/${encodeURIComponent(slug)}`, opts),
      serverApi<Parameters<typeof toSeriesReleasePage>[0]>(seriesReleasesPath(slug, sort), opts).then(toSeriesReleasePage, () => undefined),
      serverCredits({kind: 'series', slug}, request.signal),
    ]);
    return {lang, slug, sort, detail, releases, credits} satisfies SeriesLoad;
  } catch (e) {
    if (isGone(e)) return data({lang, slug, sort, missing: true} satisfies SeriesLoad, {status: 404});
    console.error('series loader', slug, e);
    return data({lang, slug, sort, unavailable: true} satisfies SeriesLoad, {status: 503});
  }
}

export const clientLoader = () => null;

function dateRange(first?: string | null, latest?: string | null) {
  const a = formatDate(first);
  const b = formatDate(latest);
  if (!a || !b) return a || b;
  return a === b ? a : t('series.dateRange', {from: a, to: b});
}

export const meta: MetaFunction<typeof loader> = ({data: d, params, matches}) => {
  const path = `/series/${encodeURIComponent(params.slug ?? '')}`;
  const load = d as SeriesLoad | null | undefined;
  const s = load?.detail;
  if (!s) {
    const title = load?.missing ? t('title.named', {name: t('series.notFound')}) : t('title.seriesList');
    return seoMeta({matches, path, title, noindex: true});
  }
  const {lang, origin} = rootMeta(matches);
  const url = pageUrl(origin, path, lang);
  const first = formatDate(s.first_release_date);
  const latest = formatDate(s.latest_release_date);
  const range = first && latest && first !== latest ? t('seo.seriesRange', {from: first, to: latest}) : '';
  const description = t('seo.seriesDescription', {name: s.name, n: formatCount(s.release_count), range});
  const performers = s.top_performers ?? [];
  const series = compact({
    '@context': 'https://schema.org',
    '@type': 'CreativeWorkSeries',
    name: s.name,
    alternateName: s.name_ja && s.name_ja !== s.name ? s.name_ja : undefined,
    description,
    url,
    inLanguage: lang,
    startDate: isoDate(s.first_release_date),
    endDate: isoDate(s.latest_release_date),
    creator: (s.makers ?? []).filter(m => m.name && m.name !== '(unknown)').map(m => ({'@type': 'Organization', name: m.name})),
    actor: performers.map(p => ({'@type': 'Person', name: seriesPerformerName(p), url: pageUrl(origin, `/performers/${encodeURIComponent(p.slug)}`, lang)})),
  });
  const items = load?.releases?.items ?? [];
  const list = {
    '@context': 'https://schema.org',
    '@type': 'ItemList',
    name: s.name,
    itemListOrder: load?.sort === 'newest' ? 'https://schema.org/ItemListOrderDescending' : 'https://schema.org/ItemListOrderAscending',
    numberOfItems: s.release_count,
    itemListElement: items.map((r, i) => ({
      '@type': 'ListItem',
      position: i + 1,
      name: r.canonical_title,
      url: pageUrl(origin, `/releases/${encodeURIComponent(r.slug)}`, lang),
    })),
  };
  return seoMeta({
    matches,
    // ?sort= variants are the same series: canonical is the plain URL.
    path,
    title: t('title.named', {name: s.name}),
    description,
    jsonLd: [
      series,
      list,
      breadcrumbs(matches, [
        [t('seo.breadcrumbHome'), '/'],
        [t('seo.breadcrumbSeries'), '/series'],
        [s.name, path],
      ]),
    ],
  });
};

export default function SeriesPage() {
  const t = useT();
  const {slug = ''} = useParams();
  const [params] = useSearchParams();
  const sort = parseSeriesReleaseSort(params.get('sort'));
  const {contentLang} = useI18n();
  const loaded = useLoaderData<typeof loader>() as SeriesLoad | null;
  const own = loaded && loaded.slug === slug && loaded.lang === contentLang ? loaded : null;
  const missingOnServer = !!(loaded && loaded.slug === slug && loaded.missing);
  const detail = useAsync(signal => getSeries(slug, signal), [slug], !missingOnServer, own?.detail);
  const seeded = own?.detail && own.sort === sort ? own.releases : undefined;
  // Episode numbering basis of the current order (from its first page).
  const [basis, setBasis] = useState<string | undefined>(seeded?.basis);
  const releases = usePaged(
    async (cursor, signal) => {
      const page = await listSeriesReleases(slug, sort, cursor, signal);
      if (!cursor) setBasis(page.basis);
      return page;
    },
    [slug, sort],
    !missingOnServer,
    seeded,
  );
  const credits = useCredits({kind: 'series', slug}, loaded?.slug === slug ? loaded.credits : undefined);

  useEffect(() => {
    // Client-side navigations (sort links included) have no loader data for meta().
    if (detail.data) document.title = t('title.named', {name: detail.data.name});
  }, [detail.data, sort]);

  if (detail.error || missingOnServer) {
    const missing = missingOnServer || (detail.error instanceof ApiError && detail.error.status === 404);
    return (
      <div className="page">
        {missing ? (
          <EmptyState icon="film" title={t('series.notFound')} body={t('series.notFoundBody')} actions={[{to: '/series', label: t('series.browseAll')}]} />
        ) : (
          <ErrorBanner error={detail.error!} onRetry={detail.reload} />
        )}
      </div>
    );
  }

  const s = detail.data;
  const makers = (s?.makers ?? []).filter(m => m.name && m.name !== '(unknown)');
  const performers = s?.top_performers ?? [];
  const orderHref = (key: SeriesReleaseSort) => `/series/${encodeURIComponent(slug)}${key === 'episode' ? '' : `?sort=${key}`}`;

  return (
    <div className="page series-page">
      <nav className="series-crumbs" aria-label={t('seo.breadcrumbSeries')}>
        <Link to="/series" className="text-btn">
          {t('series.allSeries')}
        </Link>
      </nav>
      {!s ? (
        detail.slow && <BlockSkeleton lines={4} />
      ) : (
        <header className="series-hero">
          <h1>{s.name}</h1>
          {s.name_ja && s.name_ja !== s.name && <p className="muted">{s.name_ja}</p>}
          <div className="stat-row">
            <div className="stat">
              <span className="stat-value">{formatCount(s.release_count)}</span>
              <span className="stat-label">{t('series.statWorks')}</span>
            </div>
            {s.first_release_date && (
              <div className="stat">
                <span className="stat-value">{formatDate(s.first_release_date)}</span>
                <span className="stat-label">{t('series.statFirst')}</span>
              </div>
            )}
            {s.latest_release_date && s.latest_release_date !== s.first_release_date && (
              <div className="stat">
                <span className="stat-value">{formatDate(s.latest_release_date)}</span>
                <span className="stat-label">{t('series.statLatest')}</span>
              </div>
            )}
          </div>
          {makers.length > 0 && (
            <div className="series-facet">
              <span className="series-facet-label">{t('series.makers')}</span>
              <div className="chip-row chip-row-wrap">
                {makers.map(m => (
                  <Link key={m.name} to={discoverHref({maker: m.name})} className="chip">
                    {m.name} <span className="chip-count">{formatCount(m.release_count)}</span>
                  </Link>
                ))}
              </div>
            </div>
          )}
        </header>
      )}

      {performers.length > 0 && (
        <section className="series-performers">
          <SectionHeader title={t('series.topPerformers')} />
          <div className="avatar-row">
            {performers.map(p => (
              <PerformerChip key={p.id} slug={p.slug} name={seriesPerformerName(p)} sub={t('series.performerWorks', {n: formatCount(p.release_count)})} size={72} />
            ))}
          </div>
        </section>
      )}

      <section className="series-releases shelf">
        <SectionHeader title={t('series.releases')} sub={sort === 'episode' && basis && BASIS_LABELS[basis] ? t('series.basisHint', {basis: t(BASIS_LABELS[basis])}) : undefined} />
        <nav className="chip-row" aria-label={t('series.orderLabel')}>
          {ORDERS.map(o => (
            <Link key={o.key} to={orderHref(o.key)} replace preventScrollReset className={o.key === sort ? 'chip chip-on' : 'chip'} aria-current={o.key === sort ? 'true' : undefined}>
              {t(o.label)}
            </Link>
          ))}
        </nav>
        {releases.error ? (
          <ErrorBanner error={releases.error} onRetry={releases.reload} />
        ) : releases.loading ? (
          releases.slow && <CardGridSkeleton count={8} />
        ) : releases.items.length === 0 ? (
          <EmptyState title={t('series.noReleases')} actions={[{to: '/series', label: t('series.browseAll')}]} />
        ) : (
          <>
            <ol className="card-grid series-release-grid">
              {releases.items.map(r => (
                <li key={r.id}>
                  <ReleaseCard
                    slug={r.slug}
                    title={r.canonical_title}
                    badge={typeof r.episode === 'number' ? t('series.episode', {n: r.episode}) : undefined}
                    meta={[r.product_code, formatDate(r.release_date)].filter(Boolean).join(' · ')}
                    sub={formatRuntime(r.runtime_minutes) || undefined}
                  />
                </li>
              ))}
            </ol>
            <LoadMore state={releases} label={t('series.moreReleases')} />
          </>
        )}
      </section>
      <SourceCredits items={credits} />
    </div>
  );
}
