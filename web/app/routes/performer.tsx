import {useEffect, useState} from 'react';
import {data, redirect, useLoaderData, useParams, type LoaderFunctionArgs, type MetaFunction} from 'react-router';
import {Avatar, ReleaseCard, SectionHeader} from '../components/Cards';
import {LoadMore} from '../components/Paging';
import {SimilarPerformers} from '../components/PerformerRecs';
import {FollowButton} from '../components/SocialButtons';
import {BlockSkeleton, CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {ApiError, getPerformer, getPerformerReleases, listPerformerPage, listReleasePage, toPage, type LangCode, type Page, type PerformerDetail, type Release} from '../lib/api';
import {isGone, serverApi} from '../lib/api.server';
import {localizePath, routeLang} from '../lib/langs';
import {breadcrumbs, clip, compact, isoDate, pageUrl, rootMeta, seoMeta, type SeoHandle} from '../lib/seo';
import {ageAt, formatCount, formatDate, formatYear} from '../lib/format';
import {t, useI18n, useT} from '../lib/i18n';
import {useAsync} from '../lib/useAsync';
import {useCanonicalSlug} from '../lib/useCanonicalSlug';
import {usePaged} from '../lib/usePaged';

export const handle: SeoHandle = {seo: true};

const PAGE_SIZE = 24;

// Server rendering (PRD 13 SEO): profile and the first page of works come
// from the loader; client-side navigations fetch in the page (clientLoader).
type PerformerLoad = {
  lang: LangCode;
  slug: string;
  detail?: PerformerDetail;
  works?: Page<Release>;
  missing?: boolean;
  unavailable?: boolean;
};

export async function loader({params, request}: LoaderFunctionArgs) {
  const lang = routeLang(params);
  const slug = params.slug ?? '';
  if (!lang) return null;
  const enc = encodeURIComponent(slug);
  try {
    const detail = await serverApi<PerformerDetail>(`performers/${enc}`, {lang, signal: request.signal});
    if (detail.slug && detail.slug !== slug) {
      throw redirect(localizePath(`/performers/${encodeURIComponent(detail.slug)}`, lang) + new URL(request.url).search, 301);
    }
    const works = await serverApi<{items: Release[] | null; next_cursor?: string | null}>(`releases?performer=${enc}&limit=${PAGE_SIZE}`, {lang, signal: request.signal})
      .then(toPage)
      .catch(() => undefined);
    // A backend without the performer filter answers every release: let the page fall back.
    return {lang, slug, detail, works: works?.paged ? works : undefined} satisfies PerformerLoad;
  } catch (e) {
    if (e instanceof Response) throw e;
    if (isGone(e)) return data({lang, slug, missing: true} satisfies PerformerLoad, {status: 404});
    console.error('performer loader', slug, e);
    return data({lang, slug, unavailable: true} satisfies PerformerLoad, {status: 503});
  }
}

export const clientLoader = () => null;

export const meta: MetaFunction<typeof loader> = ({data: d, params, matches}) => {
  const path = `/performers/${encodeURIComponent(params.slug ?? '')}`;
  const load = d as PerformerLoad | null | undefined;
  const p = load?.detail;
  if (!p) {
    const title = load?.missing ? t('title.named', {name: t('performer.notFound')}) : t('title.performer');
    return seoMeta({matches, path, title, noindex: true});
  }
  const count = load?.works?.items.length ?? 0;
  const works = count ? t('seo.performerWorks', {n: load?.works?.nextCursor ? `${count}+` : String(count)}) : '';
  const description = p.biography?.trim() ? clip(p.biography) : t('seo.performerDescription', {name: p.canonical_name, works});
  const {lang, origin} = rootMeta(matches);
  // Person: public facts only. A hidden birthday (birth_date_hidden) is never stated.
  const person = compact({
    '@context': 'https://schema.org',
    '@type': 'Person',
    name: p.canonical_name,
    alternateName: p.canonical_name_ja && p.canonical_name_ja !== p.canonical_name ? p.canonical_name_ja : undefined,
    description,
    url: pageUrl(origin, path, lang),
    birthDate: p.birth_date_hidden === false ? isoDate(p.birth_date) : undefined,
    birthPlace: p.birth_place || undefined,
  });
  return seoMeta({
    matches,
    path,
    title: t('title.named', {name: p.canonical_name}),
    description,
    ogType: 'profile',
    jsonLd: [
      person,
      breadcrumbs(matches, [
        [t('seo.breadcrumbHome'), '/'],
        [t('seo.breadcrumbActors'), '/discover?tab=actors'],
        [p.canonical_name, path],
      ]),
    ],
  });
};
const LEGACY_LIMIT = 100;

// Actor / MobileActor.
//
// Works: GET /api/releases?performer={slug} (newest first, cursor paged). On a
// backend without that filter (no next_cursor) the page falls back to
// GET /api/performers/{slug}/releases?limit=100.
// works_count: from GET /api/performers?q={name} (catalog-filters backend);
// otherwise the number of works loaded.
//
// Omitted for lack of backend:
// 关注 + bell (NewWorkAlert) via PUT/DELETE /api/me/follows/{slug}.
// TODO(PRD 17): 即将上线 shelf — there is no public endpoint for a performer's upcoming works yet.
// TODO(tech plan 3.13 performer_stats): 总播放 / 粉丝 / 热度 stats.
// TODO(tech plan 3.13 release_performers.role_type): 主演 / 客串 filters; 最热 sort.
// 相似演员: GET /api/recs?slot=similar_performers (hidden without the recs routes).
// TODO(tech plan 2.4 media_assets): 主照片 and 照片集.
// TODO: aliases and multi-language names beyond canonical_name_ja are not in GET /api/performers/{slug}.
export default function PerformerPage() {
  const t = useT();
  const {slug = ''} = useParams();
  const {contentLang} = useI18n();
  const loaded = useLoaderData<typeof loader>() as PerformerLoad | null;
  const own = loaded && loaded.slug === slug && loaded.lang === contentLang ? loaded : null;
  const missingOnServer = !!(loaded && loaded.slug === slug && loaded.missing);
  const detail = useAsync(signal => getPerformer(slug, signal), [slug], !missingOnServer, own?.detail);
  const works = usePaged(
    async (cursor, signal) => {
      const page = await listReleasePage({performer: slug}, PAGE_SIZE, cursor, signal);
      if (page.paged) return page;
      // Old backend ignored ?performer= and returned every release.
      const items = await getPerformerReleases(slug, LEGACY_LIMIT, signal);
      return {items, nextCursor: null, paged: false};
    },
    [slug],
    !missingOnServer,
    own?.detail ? own.works : undefined,
  );
  const name = detail.data?.canonical_name;
  const stats = useAsync(
    signal => listPerformerPage('works', 20, null, name, signal).then(page => page.items.find(p => p.slug === slug)?.works_count),
    [slug, name],
    !!name,
  );
  const [tab, setTab] = useState<'works' | 'info'>('works');

  useCanonicalSlug('performers', slug, detail.data?.slug);
  useEffect(() => setTab('works'), [slug]);
  useEffect(() => {
    if (detail.data) document.title = t('title.named', {name: detail.data.canonical_name});
  }, [detail.data]);

  if (detail.error || missingOnServer) {
    const missing = missingOnServer || (detail.error instanceof ApiError && detail.error.status === 404);
    return (
      <div className="page">
        {missing ? (
          <EmptyState icon="me" title={t('performer.notFound')} body={t('performer.notFoundBody')} actions={[{to: '/discover?tab=actors', label: t('common.browseActors')}]} />
        ) : (
          <ErrorBanner error={detail.error!} onRetry={detail.reload} />
        )}
      </div>
    );
  }

  const p = detail.data;
  const loadedCount = works.items.length;
  const countLabel =
    typeof stats.data === 'number'
      ? formatCount(stats.data)
      : works.loading
        ? ''
        : works.hasMore || (works.paged === false && loadedCount >= LEGACY_LIMIT)
          ? `${loadedCount}+`
          : String(loadedCount);

  return (
    <div className="page performer-page" data-tab={tab}>
      {!p ? (
        detail.slow && <BlockSkeleton lines={4} />
      ) : (
        <section className="performer-hero">
          <div className="performer-photo">
            <Avatar seed={p.slug} name={p.canonical_name} size={120} />
          </div>
          <div className="performer-head">
            <h1>{p.canonical_name}</h1>
            {p.canonical_name_ja && <p className="muted">{p.canonical_name_ja}</p>}
            <FollowButton slug={p.slug} name={p.canonical_name} performerId={p.id} source="actor_page" />
            {countLabel && (
              <div className="stat-row">
                <div className="stat">
                  <span className="stat-value">{countLabel}</span>
                  <span className="stat-label">{t('performer.works')}</span>
                </div>
              </div>
            )}
          </div>
        </section>
      )}

      <div className="tabbar show-mobile" role="tablist" aria-label={t('performer.tabs')}>
        <button type="button" role="tab" aria-selected={tab === 'works'} className="tabbar-tab" onClick={() => setTab('works')}>
          {t('performer.works')}
        </button>
        <button type="button" role="tab" aria-selected={tab === 'info'} className="tabbar-tab" onClick={() => setTab('info')}>
          {t('performer.info')}
        </button>
      </div>

      {p && (
        <section className="performer-info">
          <PerformerMetadata detail={p} />
        </section>
      )}

      <section className="performer-works shelf">
        <SectionHeader title={countLabel ? t('performer.filmographyCount', {count: countLabel}) : t('performer.filmography')} sub={t('discover.sortNewest')} />
        {works.error ? (
          <ErrorBanner error={works.error} onRetry={works.reload} />
        ) : works.loading ? (
          works.slow && <CardGridSkeleton count={10} />
        ) : works.items.length === 0 ? (
          <EmptyState title={t('performer.noWorks')} body={t('performer.noWorksBody')} actions={[{to: '/', label: t('common.goHome')}]} />
        ) : (
          <>
            <div className="card-grid">
              {works.items.map(r => {
                const age = ageAt(p?.birth_date, r.release_date);
                const year = formatYear(r.release_date);
                return (
                  <ReleaseCard
                    key={r.id}
                    slug={r.slug}
                    title={r.canonical_title}
                    badge={r.runtime_minutes ? t('card.minutes', {n: r.runtime_minutes}) : undefined}
                    meta={[r.maker_name, formatDate(r.release_date)].filter(Boolean).join(' · ')}
                    sub={year && age !== null ? t('performer.workSub', {year, age}) : undefined}
                  />
                );
              })}
            </div>
            <LoadMore state={works} label={t('performer.moreWorks')} />
          </>
        )}
      </section>

      <SimilarPerformers slug={slug} />
    </div>
  );
}

const present = (value: unknown) => value !== null && value !== undefined && String(value).trim() !== '';

/** Same fields as the previous PerformerMetadata. */
function PerformerMetadata({detail}: {detail: PerformerDetail}) {
  const t = useT();
  const height = present(detail.height_cm) ? `${Number.parseFloat(String(detail.height_cm))} cm` : '';
  const measurements = detail.bust_cm && detail.waist_cm && detail.hip_cm ? `B${detail.bust_cm} W${detail.waist_cm} H${detail.hip_cm}` : '';
  const fields: [string, unknown][] = [
    [t('performer.nameJa'), detail.canonical_name_ja],
    [t('performer.birthDate'), formatDate(detail.birth_date) || detail.birth_date],
    [t('performer.birthPlace'), detail.birth_place],
    [t('performer.debutDate'), formatDate(detail.debut_date) || detail.debut_date],
    [t('performer.height'), height],
    [t('performer.measurements'), measurements],
    [t('performer.cup'), detail.cup_size],
    [t('performer.bloodType'), detail.blood_type],
    [
      t('performer.activity'),
      detail.activity_start || detail.activity_end
        ? t('performer.activityRange', {from: detail.activity_start || '?', to: detail.activity_end || t('performer.present')})
        : '',
    ],
  ];
  const rows = fields.filter(([, v]) => present(v));
  if (rows.length === 0 && !detail.biography) return <p className="muted">{t('performer.noInfo')}</p>;
  return (
    <div className="info-card">
      <dl className="meta-grid">
        {rows.map(([label, value]) => (
          <div className="meta-item" key={label}>
            <dt>{label}</dt>
            <dd>{String(value)}</dd>
          </div>
        ))}
      </dl>
      {detail.biography && <p className="bio">{detail.biography}</p>}
    </div>
  );
}
