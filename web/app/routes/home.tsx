import {Link} from '../lib/nav';
import {data, useLoaderData, type LoaderFunctionArgs, type MetaFunction} from 'react-router';
import {isGated} from '../lib/ageGate.server';
import {serverApi} from '../lib/api.server';
import {withRegion} from '../lib/region.server';
import {routeLang} from '../lib/langs';
import {compact, pageUrl, rootMeta, seoMeta, type SeoHandle} from '../lib/seo';
import {ReleaseCard, PerformerChip, SectionHeader} from '../components/Cards';
import {Icon} from '../components/Icon';
import {CardGridSkeleton, ErrorBanner} from '../components/States';
import {getWatchHistory, listPerformerPage, listReleases, listTags, toPage, type LangCode, type Page, type PerformerListItem, type Release, type Tag} from '../lib/api';
import {discoverHref} from '../lib/releaseFilter';
import {useAuth} from '../lib/auth';
import {t, useI18n, useT} from '../lib/i18n';
import {formatDate, formatRuntime} from '../lib/format';
import {useAsync} from '../lib/useAsync';
import {getPerformerHeat, getRecs, getTrending, performerName, reasonPerformer, reasonText, recTitle, useRecsAvailable, type RecItem} from '../lib/recs';
import {HomeHero} from '../components/HomeHero';
import {getHomeSlots, getTopRatedShort, releaseTitle, type HomeSlot, type HomeSlots, type TopRatedShort} from '../lib/home';

export const handle: SeoHandle = {seo: true};

// Server rendering (PRD 13 SEO): 主推横幅 and the curated shelf (home_slots),
// 新上线, 高分短片, the performer shelf and the tag chips come from the
// loader, so the home page links every new release and performer without
// JavaScript and the hero never shifts the page. Personal shelves load in
// the browser.
type HomeLoad = {
  lang: LangCode;
  releases?: Release[];
  performers?: Page<PerformerListItem>;
  tags?: Tag[];
  slots?: HomeSlots;
  short?: TopRatedShort;
};

const SHORT_LIMIT = 10;

export async function loader({params, request}: LoaderFunctionArgs) {
  const lang = routeLang(params);
  if (!lang) return null;
  // 18+ gate: the head needs no data; the body is the gate (root.tsx).
  if (isGated(request)) return {lang} satisfies HomeLoad;
  const opts = {lang, signal: request.signal};
  type Items<T> = {items: T[] | null; next_cursor?: string | null};
  // Region-targeted slots: the visitor's country (CF-IPCountry / GEOIP_HEADER, lib/region.server).
  const slotQuery = withRegion(new URLSearchParams({lang}), request);
  const [releases, performers, tags, slots, short] = await Promise.all([
    serverApi<Items<Release>>('releases?limit=10', opts).then(d => d.items ?? [], () => undefined),
    serverApi<Items<PerformerListItem>>('performers?sort=works&limit=12', opts).then(toPage, () => undefined),
    serverApi<Items<Tag>>('tags?limit=12', opts).then(d => d.items ?? [], () => undefined),
    serverApi<HomeSlots>(`home/slots?${slotQuery}`, opts).then(d => ({...d, hero: d?.hero ?? [], shelf: d?.shelf ?? []}), () => undefined),
    serverApi<TopRatedShort>(`home/top-rated-short?lang=${encodeURIComponent(lang)}&limit=${SHORT_LIMIT}`, opts).then(
      d => ({...d, items: d?.items ?? []}),
      () => undefined,
    ),
  ]);
  const body: HomeLoad = {lang, releases, performers, tags, slots, short};
  // Without the catalog the page still renders; 503 tells crawlers to come back.
  return data(body, releases ? undefined : {status: 503});
}

export const clientLoader = () => null;

export const meta: MetaFunction<typeof loader> = ({matches}) => {
  const {lang, origin} = rootMeta(matches);
  const url = pageUrl(origin, '/', lang);
  return seoMeta({
    matches,
    path: '/',
    title: t('title.home'),
    description: t('seo.homeDescription'),
    jsonLd: [
      compact({
        '@context': 'https://schema.org',
        '@type': 'WebSite',
        name: t('seo.siteName'),
        url,
        inLanguage: lang,
        potentialAction: {
          '@type': 'SearchAction',
          target: `${pageUrl(origin, '/search', lang)}?q={search_term_string}`,
          'query-input': 'required name=search_term_string',
        },
      }),
    ],
  });
};

// NavHome / NavHomeVisitor / MobileHome.
//
// PRD 4.1 order: 今晚看什么 + tag chips → 继续观看 → 为你推荐 → 你关注的演员有新作
// → 新上线 → 热门演员. Signed in, 为你推荐 and 关注新作 split one
// GET /api/recs?slot=home page by channel (followed_new); visitors get 本周热门
// (GET /api/trending?period=week) instead. 热门演员 is the week's performer
// heat board. Each recommended card shows its reason and carries its slot.
// Without the recs routes (404 / 503) these shelves are hidden and the page
// keeps 新上线 and the 演员 shelf (sort=works).
//
// 主推横幅 (first, above the chips) is the live hero slots of GET
// /api/home/slots (AdminOps 运营排期); with none scheduled, 新上线 releases
// with a stored cover fill it (推荐补位). Curated shelf slots form 编辑精选.
// Visitors also get 高分短片 (GET /api/home/top-rated-short): short releases
// by Bayesian rating, with a lower vote threshold and then the newest short
// releases when ratings are thin (the sub-title says which).
export default function Home() {
  const t = useT();
  const {status} = useAuth();
  const {contentLang} = useI18n();
  const loaded = useLoaderData<typeof loader>() as HomeLoad | null;
  const own = loaded?.lang === contentLang ? loaded : null;
  const slots = useAsync(signal => getHomeSlots(contentLang, signal), [contentLang], true, own?.slots);
  const short = useAsync(signal => getTopRatedShort(contentLang, SHORT_LIMIT, signal), [contentLang], status !== 'user', own?.short);
  const releases = useAsync(signal => listReleases(10, signal), [], true, own?.releases);
  const performers = useAsync(signal => listPerformerPage('works', 12, null, undefined, signal), [], true, own?.performers);

  const tags = useAsync(signal => listTags(12, signal), [], true, own?.tags);
  const history = useAsync(signal => getWatchHistory(signal), [status], status === 'user');
  const recs = useAsync(signal => getRecs('home', {limit: 30}, signal), [status], status === 'user');
  const trending = useAsync(signal => getTrending('week', {limit: 10}, signal), [], status === 'anon');
  const heat = useAsync(signal => getPerformerHeat('week', {limit: 12}, signal), []);
  const recsOn = useRecsAvailable(true);
  const recItems = recs.data?.items ?? [];
  const isFollowed = (it: RecItem) => it.reason.code === 'followed_new';
  const forYou = recItems.filter(it => !isFollowed(it)).slice(0, 10);
  const followed = recItems.filter(isFollowed).slice(0, 10);
  const followedNames = Array.from(new Set(followed.map(it => reasonPerformer(it.reason, it)).filter(Boolean))).slice(0, 2);
  const weekTop = (trending.data?.items ?? []).flatMap(row => (row.release ? [{row, it: row.release}] : []));
  const hotActors = heat.data?.items ?? [];
  // 热门演员 replaces the sort=works shelf once the heat board answers with rows.
  const showHeat = hotActors.length > 0;
  const curatedHero = slots.data?.hero ?? [];
  const heroFill = curatedHero.length === 0 && !slots.loading ? fillHero(releases.data ?? []) : [];
  const picks = slots.data?.shelf ?? [];
  const shortItems = short.data?.items ?? [];
  const shortSub =
    short.data?.basis === 'fallback'
      ? t('home.shortFallback')
      : short.data?.basis === 'few_votes'
        ? t('home.shortFewVotes')
        : t('home.shortTopSub', {n: short.data?.max_minutes ?? 60});

  return (
    <div className="page home">
      <h1 className="sr-only">{t('seo.homeHeading')}</h1>
      {curatedHero.length > 0 ? <HomeHero slots={curatedHero} /> : heroFill.length > 0 && <HomeHero slots={heroFill} fill />}

      {status === 'anon' && (
        <section className="visitor-card">
          <span className="visitor-text">
            <strong>{t('home.visitorTitle')}</strong>
            <span>{t('home.visitorBody')}</span>
          </span>
          <Link to="/login" className="btn btn-primary">
            {t('home.loginRegister')}
          </Link>
        </section>
      )}

      {/* Tag chips: GET /api/tags; each opens the tag-filtered list
          (/discover?tag=, GET /api/releases?tag=). */}
      <div className="chip-row home-chips">
        {recsOn !== 'no' && (
          <Link to="/tonight" className="chip chip-tonight" aria-keyshortcuts="t" title={t('home.tonightHint')}>
            <Icon name="tonight" size={16} />
            {t('home.tonight')}
          </Link>
        )}
        {tags.data && tags.data.length > 0 && (
          <span className="chip-row-inner" role="group" aria-label={t('home.tags')}>
          {(tags.data ?? []).map(tag => (
            <Link key={tag.slug} to={discoverHref({tag: tag.slug})} className="chip" title={t('home.tagCount', {count: tag.count})}>
              {tag.name}
              <span className="chip-count">{tag.count}</span>
            </Link>
          ))}
          </span>
        )}
      </div>

      {picks.length > 0 && (
        <section className="shelf">
          <SectionHeader title={t('home.picks')} sub={t('home.picksSub')} />
          <div className="card-grid">
            {picks.map((s, col) => (
              <PickCard key={s.id} slot={s} col={col + 1} />
            ))}
          </div>
        </section>
      )}

      {status === 'user' && (history.loading || (history.data && history.data.length > 0) || history.error) && (
        <section className="shelf">
          <SectionHeader title={t('home.continue')} sub={t('home.continueSub')} action={<Link to="/history" className="section-link">{t('home.all')}</Link>} />
          {history.error ? (
            <ErrorBanner error={history.error} onRetry={history.reload} />
          ) : history.slow ? (
            <CardGridSkeleton count={5} />
          ) : (
            <div className="card-grid card-grid-scroll">
              {(history.data ?? []).slice(0, 10).map((item, col) => {
                const ratio = item.duration_seconds > 0 ? item.progress_seconds / item.duration_seconds : undefined;
                const left = item.duration_seconds > 0 ? Math.max(0, Math.ceil((item.duration_seconds - item.progress_seconds) / 60)) : null;
                return (
                  <ReleaseCard
                    slot={`home:continue:c${col + 1}`}
                    key={item.slug}
                    slug={item.slug}
                    title={item.title}
                    progress={ratio ?? 0}
                    meta={left === null ? t('home.continue') : left === 0 ? t('home.finished') : t('home.minutesLeft', {n: left})}
                  />
                );
              })}
            </div>
          )}
        </section>
      )}

      {status === 'user' && forYou.length > 0 && (
        <section className="shelf">
          <SectionHeader title={t('home.forYou')} sub={t('home.forYouSub')} />
          <RecGrid items={forYou} slot="home:recs" />
        </section>
      )}

      {status === 'user' && followed.length > 0 && (
        <section className="shelf">
          <SectionHeader
            title={t('home.followedNew')}
            sub={followedNames.length > 0 ? t('home.followedNewSub', {names: followedNames.join('、')}) : undefined}
            action={<Link to="/following" className="section-link">{t('home.viewAll')}</Link>}
          />
          <RecGrid items={followed} slot="home:followed" />
        </section>
      )}

      {status === 'anon' && weekTop.length > 0 && (
        <section className="shelf">
          <SectionHeader
            title={t('home.weekTrending')}
            sub={t('home.weekTrendingSub')}
            action={<Link to="/trending?period=week" className="section-link">{t('home.viewAll')}</Link>}
          />
          <RecGrid items={weekTop.map(x => x.it)} slot="home:trending" />
        </section>
      )}

      <section className="shelf">
        <SectionHeader title={t('home.new')} sub={t('home.newSub')} action={<Link to="/discover?tab=new" className="section-link">{t('home.viewAll')}</Link>} />
        {releases.error ? (
          <ErrorBanner error={releases.error} onRetry={releases.reload} />
        ) : releases.slow ? (
          <CardGridSkeleton count={10} />
        ) : releases.data && releases.data.length === 0 ? (
          <p className="muted">{t('home.empty')}</p>
        ) : (
          <div className="card-grid">
            {(releases.data ?? []).map((r, col) => (
              <ReleaseCard
                slot={`home:new:c${col + 1}`}
                key={r.id}
                slug={r.slug}
                title={r.canonical_title}
                cover={r.cover}
                badge={r.runtime_minutes ? t('card.minutes', {n: r.runtime_minutes}) : undefined}
                meta={[r.maker_name, formatDate(r.release_date)].filter(Boolean).join(' · ')}
                sub={!r.maker_name && !r.release_date ? formatRuntime(r.runtime_minutes) : undefined}
              />
            ))}
          </div>
        )}
      </section>

      {status !== 'user' && shortItems.length > 0 && (
        <section className="shelf">
          <SectionHeader
            title={t('home.shortTop')}
            sub={shortSub}
            action={<Link to={discoverHref({max_runtime: short.data?.max_minutes ?? 60})} className="section-link">{t('home.viewAll')}</Link>}
          />
          <div className="card-grid">
            {shortItems.map((r, col) => (
              <ReleaseCard
                slot={`home:short:c${col + 1}`}
                key={r.id}
                slug={r.slug}
                title={releaseTitle(r)}
                cover={r.cover}
                badge={r.runtime_minutes ? t('card.minutes', {n: r.runtime_minutes}) : undefined}
                meta={
                  r.score !== null ? (
                    <span className="short-score">{t('home.shortScore', {score: r.score.toFixed(1)})}</span>
                  ) : r.votes > 0 ? (
                    t('home.shortVotes', {n: r.votes})
                  ) : (
                    [r.maker_name, formatDate(r.release_date)].filter(Boolean).join(' · ')
                  )
                }
              />
            ))}
          </div>
        </section>
      )}

      {showHeat && (
        <section className="shelf">
          <SectionHeader title={t('home.hotActors')} action={<Link to="/discover?tab=actors" className="section-link">{t('home.actorLibrary')}</Link>} />
          <div className="avatar-row">
            {hotActors.map((h, i) => (
              <PerformerChip key={h.performer.id} slug={h.performer.slug} name={performerName(h.performer)} slot={`home:hot_actors:c${i + 1}`} />
            ))}
          </div>
        </section>
      )}

      {/* Without performer heat: asked for sort=works; a backend without sorting
          answers by name, so the sub-title follows what came back. */}
      {!showHeat && !heat.loading && (
      <section className="shelf">
        <SectionHeader
          title={t('home.actors')}
          sub={performers.data?.paged ? t('home.actorsSub') : undefined}
          action={<Link to="/discover?tab=actors" className="section-link">{t('home.actorLibrary')}</Link>}
        />
        {performers.error ? (
          <ErrorBanner error={performers.error} onRetry={performers.reload} />
        ) : performers.slow ? (
          <CardGridSkeleton count={8} variant="avatar" />
        ) : (
          <div className="avatar-row">
            {(performers.data?.items ?? []).map(p => (
              <PerformerChip
                key={p.id}
                slug={p.slug}
                name={p.canonical_name}
                sub={typeof p.works_count === 'number' ? t('home.worksCount', {n: p.works_count}) : p.canonical_name_ja}
              />
            ))}
          </div>
        )}
      </section>
      )}
    </div>
  );
}

/** Recommended releases with their one-line reason (PRD 10 可解释). */
function RecGrid({items, slot}: {items: RecItem[]; slot: string}) {
  const t = useT();
  return (
    <div className="card-grid">
      {items.map((it, col) => (
        <ReleaseCard
          slot={`${slot}:c${col + 1}`}
          key={it.id}
          slug={it.slug}
          title={recTitle(it)}
          badge={it.runtime_minutes ? t('card.minutes', {n: it.runtime_minutes}) : undefined}
          meta={[performerName(it.performer), formatDate(it.release_date)].filter(Boolean).join(' · ')}
          reason={reasonText(it.reason, it)}
          reasonCode={it.reason.code}
        />
      ))}
    </div>
  );
}

/** 推荐补位: up to 3 新上线 releases with a stored cover stand in for an empty 主推横幅. */
function fillHero(releases: Release[]): HomeSlot[] {
  return releases
    .filter(r => r.cover)
    .slice(0, 3)
    .map(r => ({
      id: -r.id,
      slot_type: 'hero',
      target_type: 'release',
      title: r.canonical_title,
      href: `/releases/${encodeURIComponent(r.slug)}`,
      release: {id: r.id, slug: r.slug, canonical_title: r.canonical_title, release_date: r.release_date, runtime_minutes: r.runtime_minutes, maker_name: r.maker_name, cover: r.cover},
      cover: r.cover,
      starts_at: '',
      ends_at: null,
      priority: 0,
    }));
}

/** 编辑精选 card: a release or series with its cover, or a link tile. */
function PickCard({slot, col}: {slot: HomeSlot; col: number}) {
  const t = useT();
  const r = slot.release;
  if (slot.target_type === 'release' && r) {
    return (
      <ReleaseCard
        slot={`home:picks:c${col}`}
        slug={r.slug}
        title={slot.title}
        cover={slot.cover}
        badge={r.runtime_minutes ? t('card.minutes', {n: r.runtime_minutes}) : undefined}
        meta={slot.subtitle || [r.maker_name, formatDate(r.release_date)].filter(Boolean).join(' · ')}
      />
    );
  }
  const body = (
    <>
      <span className="card-title">{slot.title}</span>
      {slot.subtitle && <span className="card-meta">{slot.subtitle}</span>}
      {slot.series && <span className="card-meta">{t('home.heroSeriesCount', {n: slot.series.release_count})}</span>}
    </>
  );
  return slot.external ? (
    <a className="home-pick-link" href={slot.href} target="_blank" rel="noopener noreferrer">
      {body}
    </a>
  ) : (
    <Link className="home-pick-link" to={slot.href} state={{slot: `home:picks:c${col}`}}>
      {body}
    </Link>
  );
}
