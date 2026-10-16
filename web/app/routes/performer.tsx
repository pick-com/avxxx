import {useEffect, useState} from 'react';
import {data, redirect, useLoaderData, useParams, type LoaderFunctionArgs, type MetaFunction} from 'react-router';
import {absoluteImageUrl, Avatar, ReleaseCard, ReleaseRow, SectionHeader} from '../components/Cards';
import {Icon} from '../components/Icon';
import {LoadMore} from '../components/Paging';
import {SimilarPerformers} from '../components/PerformerRecs';
import {FollowButton, LaterButton, ReserveButton} from '../components/SocialButtons';
import {SourceCredits} from '../components/SourceCredits';
import {SourceEvidence} from '../components/SourceEvidence';
import {BlockSkeleton, CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {ApiError, getPerformer, getPerformerReleases, listPerformerPage, listReleasePage, listUpcoming, toPage, type CreditRole, type LangCode, type Page, type PerformerDetail, type Release} from '../lib/api';
import {isGone, serverApi} from '../lib/api.server';
import {serverCredits} from '../lib/credits.server';
import {useCredits, type Attribution} from '../lib/evidence';
import {localizePath, routeLang} from '../lib/langs';
import {breadcrumbs, clip, compact, isoDate, pageUrl, rootMeta, seoMeta, type SeoHandle} from '../lib/seo';
import {ageAt, formatCompact, formatCount, formatDate, formatSchedule, formatYear} from '../lib/format';
import {t, useI18n, useT} from '../lib/i18n';
import {LANG_LABELS, isLang} from '../lib/langs';
import {useNavigate} from '../lib/nav';
import {startQueue} from '../lib/queue';
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
  /** Source credits (GET /api/attributions?performer=), shown at the end of the page. */
  credits?: Attribution[];
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
    const creditsP = serverCredits({kind: 'performer', slug}, request.signal);
    const works = await serverApi<{items: Release[] | null; next_cursor?: string | null}>(`releases?performer=${enc}&limit=${PAGE_SIZE}`, {lang, signal: request.signal})
      .then(toPage)
      .catch(() => undefined);
    // A backend without the performer filter answers every release: let the page fall back.
    return {lang, slug, detail, works: works?.paged ? works : undefined, credits: await creditsP} satisfies PerformerLoad;
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
  // Stored portrait only (never a source's hotlinked image).
  const image = p.portrait?.url ? absoluteImageUrl(origin, p.portrait.url) : null;
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
    image: image || undefined,
  });
  return seoMeta({
    matches,
    path,
    title: t('title.named', {name: p.canonical_name}),
    description,
    ogType: 'profile',
    image,
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
// Works: GET /api/releases?performer={slug} (newest first, cursor paged), with
// 全部 / 主演 / 客串 (&role=lead|guest: lead credits follow is_primary or a lead
// role, else everyone on a release without a marked lead). On a backend
// without that filter (no next_cursor) the page falls back to
// GET /api/performers/{slug}/releases?limit=100.
// Stats (作品 / 总播放 / 粉丝 / 30 天热度) and 别名 / 多语言名 come with
// GET /api/performers/{slug} (总播放 and 热度 from the hourly performer stats);
// an older backend falls back to works_count from GET /api/performers?q={name}.
// 即将上线: GET /api/upcoming?performer= with 预约.
// 随机播放全部: shuffles up to 100 works into the 'performer' play queue.
// 关注 + bell (NewWorkAlert) via PUT/DELETE /api/me/follows/{slug}.
// 相似演员: GET /api/recs?slot=similar_performers (hidden without the recs routes).
// Not shown: 主照片 beyond the stored portrait and 照片集 (no media for them yet).
export default function PerformerPage() {
  const t = useT();
  const {slug = ''} = useParams();
  const {contentLang} = useI18n();
  const navigate = useNavigate();
  const loaded = useLoaderData<typeof loader>() as PerformerLoad | null;
  const own = loaded && loaded.slug === slug && loaded.lang === contentLang ? loaded : null;
  const missingOnServer = !!(loaded && loaded.slug === slug && loaded.missing);
  const detail = useAsync(signal => getPerformer(slug, signal), [slug], !missingOnServer, own?.detail);
  const [role, setRole] = useState<CreditRole | ''>('');
  const works = usePaged(
    async (cursor, signal) => {
      const page = await listReleasePage({performer: slug, role: role || undefined}, PAGE_SIZE, cursor, signal);
      if (page.paged) return page;
      // Old backend ignored ?performer= and returned every release.
      const items = await getPerformerReleases(slug, LEGACY_LIMIT, signal);
      return {items, nextCursor: null, paged: false};
    },
    [slug, role],
    !missingOnServer,
    own?.detail && !role ? own.works : undefined,
  );
  const upcoming = usePaged((cursor, signal) => listUpcoming({performer: slug, limit: 12}, cursor, signal), [slug], !missingOnServer);
  const name = detail.data?.canonical_name;
  const serverStats = detail.data?.stats ?? null;
  const stats = useAsync(
    signal => listPerformerPage('works', 20, null, name, signal).then(page => page.items.find(p => p.slug === slug)?.works_count),
    [slug, name],
    !!name && !serverStats,
  );
  const [tab, setTab] = useState<'works' | 'info'>('works');
  const [shuffleError, setShuffleError] = useState('');
  // DMM and other sources that require a credit where their data is shown.
  const credits = useCredits({kind: 'performer', slug}, loaded && loaded.slug === slug ? loaded.credits : undefined);

  useCanonicalSlug('performers', slug, detail.data?.slug);
  useEffect(() => {
    setTab('works');
    setRole('');
  }, [slug]);
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
  const worksCount = serverStats ? serverStats.works_count : stats.data;
  const countLabel =
    typeof worksCount === 'number'
      ? formatCount(worksCount)
      : works.loading
        ? ''
        : works.hasMore || (works.paged === false && loadedCount >= LEGACY_LIMIT)
          ? `${loadedCount}+`
          : String(loadedCount);
  const guestCount = serverStats ? serverStats.works_count - serverStats.lead_count : 0;
  const roleTabs = serverStats && serverStats.lead_count > 0 && guestCount > 0;

  // 随机播放全部: up to 100 works, shuffled, as the 'performer' queue.
  const shuffleAll = async () => {
    if (!p) return;
    setShuffleError('');
    try {
      const page = await listReleasePage({performer: slug}, 100);
      const list = page.paged ? page.items : await getPerformerReleases(slug, LEGACY_LIMIT);
      if (list.length === 0) return;
      const order = shuffled(list);
      startQueue(
        'performer',
        order.map(r => r.slug),
        {title: t('performer.queueTitle', {name: p.canonical_name}), titles: Object.fromEntries(order.map(r => [r.slug, r.canonical_title]))},
      );
      navigate(`/releases/${encodeURIComponent(order[0].slug)}?list=performer`);
    } catch (cause) {
      setShuffleError(cause instanceof Error ? cause.message : t('performer.shuffleFailed'));
    }
  };

  return (
    <div className="page performer-page" data-tab={tab}>
      {!p ? (
        detail.slow && <BlockSkeleton lines={4} />
      ) : (
        <section className="performer-hero">
          <div className="performer-photo">
            <Avatar seed={p.slug} name={p.canonical_name} size={120} image={p.portrait} />
          </div>
          <div className="performer-head">
            <h1>{p.canonical_name}</h1>
            {p.canonical_name_ja && <p className="muted">{p.canonical_name_ja}</p>}
            {(p.aliases?.length ?? 0) > 0 && (
              <p className="performer-aliases">{t('performer.aliasesLine', {names: p.aliases!.map(a => a.name).join(t('common.listSep'))})}</p>
            )}
            <div className="performer-actions">
              <FollowButton slug={p.slug} name={p.canonical_name} performerId={p.id} source="actor_page" />
              {loadedCount > 0 && (
                <button type="button" className="btn btn-secondary" onClick={shuffleAll}>
                  <Icon name="shuffle" size={16} />
                  {t('performer.shuffleAll')}
                </button>
              )}
            </div>
            {shuffleError && <ErrorBanner error={shuffleError} />}
            {(countLabel || serverStats) && (
              <div className="stat-row">
                {countLabel && (
                  <div className="stat">
                    <span className="stat-value">{countLabel}</span>
                    <span className="stat-label">{t('performer.works')}</span>
                  </div>
                )}
                {serverStats && serverStats.computed_at && (
                  <div className="stat">
                    <span className="stat-value">{formatCompact(serverStats.total_plays)}</span>
                    <span className="stat-label">{t('performer.totalPlays')}</span>
                  </div>
                )}
                {serverStats && (
                  <div className="stat">
                    <span className="stat-value">{formatCompact(serverStats.followers)}</span>
                    <span className="stat-label">{t('performer.followers')}</span>
                  </div>
                )}
                {serverStats && serverStats.computed_at && (
                  <div className="stat" title={serverStats.heat_rank ? t('performer.heatRank', {n: serverStats.heat_rank}) : undefined}>
                    <span className="stat-value">{formatCompact(Math.round(serverStats.heat / 10))}</span>
                    <span className="stat-label">{t('performer.heat30d')}</span>
                  </div>
                )}
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
          <SourceEvidence kind="performer" slug={p.slug} />
        </section>
      )}

      {upcoming.items.length > 0 && (
        <section className="performer-works shelf">
          <SectionHeader title={t('performer.upcoming')} sub={t('performer.upcomingSub')} />
          <div className="upcoming-list">
            {upcoming.items.map(it => {
              const title = it.display_title || it.canonical_title;
              return (
                <ReleaseRow key={it.id} slug={it.slug} title={title} cover={it.cover} sub={formatSchedule(it.scheduled_at, it.release_date)} slot="actor:upcoming">
                  {it.reservable ? <ReserveButton slug={it.slug} title={title} initial={it.reserved} size="sm" /> : <LaterButton slug={it.slug} title={title} variant="icon" />}
                </ReleaseRow>
              );
            })}
          </div>
          <LoadMore state={upcoming} />
        </section>
      )}

      <section className="performer-works shelf">
        <SectionHeader
          title={countLabel ? t('performer.filmographyCount', {count: countLabel}) : t('performer.filmography')}
          sub={t('discover.sortNewest')}
          action={
            roleTabs ? (
              <div className="segmented segmented-sm" role="radiogroup" aria-label={t('performer.roleFilter')}>
                {(
                  [
                    ['', t('performer.roleAll')],
                    ['lead', t('performer.roleLead', {n: formatCount(serverStats!.lead_count)})],
                    ['guest', t('performer.roleGuest', {n: formatCount(guestCount)})],
                  ] as [CreditRole | '', string][]
                ).map(([value, label]) => (
                  <button key={value || 'all'} type="button" role="radio" aria-checked={role === value} className="segmented-item" onClick={() => setRole(value)}>
                    {label}
                  </button>
                ))}
              </div>
            ) : undefined
          }
        />
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
                    cover={r.cover}
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
      <SourceCredits items={credits} />
    </div>
  );
}

function shuffled<T>(list: T[]): T[] {
  const out = [...list];
  for (let i = out.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [out[i], out[j]] = [out[j], out[i]];
  }
  return out;
}

const present = (value: unknown) => value !== null && value !== undefined && String(value).trim() !== '';

/** Language label for a reviewed name translation (lib/langs). */
const nameLang = (lang: string) => (isLang(lang) ? LANG_LABELS[lang] : lang);

/** Same fields as the previous PerformerMetadata, plus 别名 and 多语言名. */
function PerformerMetadata({detail}: {detail: PerformerDetail}) {
  const t = useT();
  const height = present(detail.height_cm) ? `${Number.parseFloat(String(detail.height_cm))} cm` : '';
  const measurements = detail.bust_cm && detail.waist_cm && detail.hip_cm ? `B${detail.bust_cm} W${detail.waist_cm} H${detail.hip_cm}` : '';
  const names = (detail.names ?? []).filter(n => n.name !== detail.canonical_name).map(n => t('performer.nameInLang', {name: n.name, lang: nameLang(n.lang)}));
  const fields: [string, unknown][] = [
    [t('performer.nameJa'), detail.canonical_name_ja],
    [t('performer.otherNames'), names.join(t('common.listSep'))],
    [t('performer.aliases'), (detail.aliases ?? []).map(a => a.name).join(t('common.listSep'))],
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
