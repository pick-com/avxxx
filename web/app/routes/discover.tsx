import {data, useLoaderData, useSearchParams, type LoaderFunctionArgs, type MetaFunction} from 'react-router';
import {isGated} from '../lib/ageGate.server';
import {serverApi} from '../lib/api.server';
import {routeLang} from '../lib/langs';
import {breadcrumbs, seoMeta, type SeoHandle} from '../lib/seo';
import {Link, useLocation, useNavigate} from '../lib/nav';
import {PerformerChip, ReleaseRow, SectionHeader} from '../components/Cards';
import {RecommendedPerformers} from '../components/PerformerRecs';
import {parsePeriod, PeriodTabs, TrendingBoard} from '../components/Trending';
import {Icon} from '../components/Icon';
import {LoadMore} from '../components/Paging';
import {FilterBar, RELEASE_PAGE_SIZE, ReleaseResults} from '../components/ReleaseFilters';
import {LaterButton, ReserveButton} from '../components/SocialButtons';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {Switch} from '../components/Switch';
import {listMakers, listPerformersFiltered, listTags, listUpcoming, query, toPage, type LangCode, type Maker, type Page, type PerformerFilter, type PerformerListItem, type PerformerSort, type Release, type ReleaseFilter, type Tag, type UpcomingItem} from '../lib/api';
import {useAuth} from '../lib/auth';
import {useCatalogFacets} from '../lib/facets';
import {formatCompact, formatCount, formatSchedule, formatYear, languageName, swatch, upcomingDayLabel} from '../lib/format';
import {t, useI18n, useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {DURATION_BUCKETS, RELEASE_SORTS, discoverHref, durationLabel, heightLabel, parseReleaseFilter, withFilter, yearOptions} from '../lib/releaseFilter';
import {useRecsAvailable} from '../lib/recs';
import {useAsync} from '../lib/useAsync';
import {usePaged} from '../lib/usePaged';

export const handle: SeoHandle = {seo: true};

type Tab = 'trending' | 'new' | 'upcoming' | 'categories' | 'actors';

// Server rendering (PRD 13 SEO): the category pages. The loader fetches what
// the requested tab shows first (新上线 / a tag or maker list, 分类 facets,
// 演员库), keyed by the query string so the page only uses it for that URL.
type DiscoverLoad = {
  lang: LangCode;
  search: string;
  releases?: Page<Release>;
  tags?: Tag[];
  makers?: Maker[];
  performers?: Page<PerformerListItem>;
};

const rawTab = (params: URLSearchParams): Tab => {
  const raw = params.get('tab');
  return raw === 'categories' || raw === 'actors' || raw === 'trending' || raw === 'upcoming' ? raw : 'new';
};
const ACTOR_SORTS: PerformerSort[] = ['heat', 'works', 'followers', 'rising', 'newest_work', 'name'];
const actorSort = (params: URLSearchParams): PerformerSort => {
  const raw = params.get('sort') as PerformerSort | null;
  return raw && ACTOR_SORTS.includes(raw) ? raw : 'works';
};
/** NavActors 活跃年代: the decades offered (first year). */
const ERAS = [2020, 2010, 2000, 1990];
/** NavActors filters from the URL: era (活跃年代), genre (擅长类型, a tag slug), audio_lang (作品语言). */
const actorFilter = (params: URLSearchParams): PerformerFilter => {
  const era = Number(params.get('era'));
  const genre = params.get('genre')?.trim();
  const audio = params.get('audio_lang')?.trim();
  return {
    era: ERAS.includes(era) ? era : undefined,
    tag: genre || undefined,
    audio_lang: audio && /^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$/.test(audio) ? audio : undefined,
  };
};

export async function loader({params, request}: LoaderFunctionArgs) {
  const lang = routeLang(params);
  if (!lang) return null;
  const url = new URL(request.url);
  const opts = {lang, signal: request.signal};
  type Items<T> = {items: T[] | null; next_cursor?: string | null};
  const body: DiscoverLoad = {lang, search: url.searchParams.toString()};
  const tab = rawTab(url.searchParams);
  const fail = () => undefined;
  if (isGated(request)) {
    // 18+ gate: only the tag name for the title of a ?tag= page.
    const tag = tab === 'new' ? parseReleaseFilter(url.searchParams).tag : undefined;
    if (tag) body.tags = await serverApi<Items<Tag>>('tags?limit=200', opts).then(d => (d.items ?? []).filter(x => x.slug === tag), fail);
    return body;
  }
  if (tab === 'new') {
    const filter = parseReleaseFilter(url.searchParams);
    const q = query({...filter, sort: filter.sort && filter.sort !== 'newest' ? filter.sort : undefined, limit: RELEASE_PAGE_SIZE});
    [body.releases, body.tags] = await Promise.all([
      serverApi<Items<Release>>(`releases${q}`, opts).then(toPage, fail),
      filter.tag ? serverApi<Items<Tag>>('tags?limit=200', opts).then(d => d.items ?? [], fail) : undefined,
    ]);
    if (!body.releases) return data(body, {status: 503});
  } else if (tab === 'categories') {
    [body.tags, body.makers] = await Promise.all([
      serverApi<Items<Tag>>('tags?limit=100', opts).then(d => d.items ?? [], fail),
      serverApi<Items<Maker>>('makers?limit=50', opts).then(d => d.items ?? [], fail),
    ]);
  } else if (tab === 'actors') {
    const sort = actorSort(url.searchParams);
    body.performers = await serverApi<Items<PerformerListItem>>(`performers${query({...actorFilter(url.searchParams), sort: sort === 'name' ? undefined : sort, limit: 60})}`, opts).then(toPage, fail);
  }
  return body;
}

export const clientLoader = () => null;

/** This URL's loader data, when it was fetched for the current query and content language. */
function useDiscoverLoad(): DiscoverLoad | null {
  const loaded = useLoaderData<typeof loader>() as DiscoverLoad | null;
  const [params] = useSearchParams();
  const {contentLang} = useI18n();
  if (!loaded || loaded.lang !== contentLang) return null;
  return loaded.search === params.toString() ? loaded : null;
}

/**
 * Canonical discover URLs: one per tab, tag or maker. Other combinations
 * (year, duration, sort, several facets) are noindex and point at their
 * canonical list through links only.
 */
export const meta: MetaFunction<typeof loader> = ({location, matches, data: d}) => {
  const params = new URLSearchParams(location.search);
  const tab = rawTab(params);
  const filter = parseReleaseFilter(params);
  const load = d as DiscoverLoad | null | undefined;
  const facets = [filter.tag, filter.maker, filter.performer, filter.year, filter.min_runtime, filter.max_runtime, filter.min_height, filter.subtitle, filter.audio_lang].filter(v => v !== undefined).length;
  const crumbs = (name: string, path: string) =>
    breadcrumbs(matches, [
      [t('seo.breadcrumbHome'), '/'],
      [t('seo.breadcrumbDiscover'), '/discover'],
      [name, path],
    ]);
  if (tab === 'categories') {
    const path = '/discover?tab=categories';
    return seoMeta({matches, path, title: t('title.named', {name: t('seo.discoverCategoriesTitle')}), description: t('seo.discoverCategoriesDescription'), jsonLd: [crumbs(t('seo.discoverCategoriesTitle'), path)]});
  }
  if (tab === 'actors') {
    const path = '/discover?tab=actors';
    const filtered = params.has('sort') || params.has('era') || params.has('genre') || params.has('audio_lang');
    return seoMeta({matches, path, title: t('title.named', {name: t('seo.discoverActorsTitle')}), description: t('seo.discoverActorsDescription'), noindex: filtered, jsonLd: [crumbs(t('seo.discoverActorsTitle'), path)]});
  }
  if (tab === 'upcoming') return seoMeta({matches, path: '/discover?tab=upcoming', title: t('title.named', {name: t('nav.upcoming')}), noindex: true});
  if (tab === 'trending') return seoMeta({matches, path: '/discover?tab=trending', title: t('title.trending'), noindex: true});
  const extra = facets > 1 || !!filter.sort || params.has('cursor');
  if (filter.tag && facets === 1) {
    const name = load?.tags?.find(tag => tag.slug === filter.tag)?.name ?? filter.tag;
    const path = `/discover?tag=${encodeURIComponent(filter.tag)}`;
    const title = t('seo.discoverTagTitle', {tag: name});
    return seoMeta({matches, path, title: t('title.named', {name: title}), description: t('seo.discoverTagDescription', {tag: name}), noindex: extra || (load?.releases && load.releases.items.length === 0), jsonLd: [crumbs(title, path)]});
  }
  if (filter.maker && facets === 1) {
    const path = `/discover?maker=${encodeURIComponent(filter.maker)}`;
    const title = t('seo.discoverMakerTitle', {maker: filter.maker});
    return seoMeta({matches, path, title: t('title.named', {name: title}), description: t('seo.discoverMakerDescription', {maker: filter.maker}), noindex: extra || (load?.releases && load.releases.items.length === 0), jsonLd: [crumbs(title, path)]});
  }
  return seoMeta({matches, path: '/discover', title: t('title.discover'), description: t('seo.discoverDescription'), noindex: facets > 0 || extra});
};
const tabs: {key: Tab; label: MessageKey}[] = [
  {key: 'trending', label: 'nav.trending'},
  {key: 'new', label: 'nav.new'},
  {key: 'upcoming', label: 'nav.upcoming'},
  {key: 'categories', label: 'nav.categories'},
  {key: 'actors', label: 'nav.actors'},
];

// MobileDiscover sub-tabs and the SideNav 新上线 / 分类 / 演员 destinations,
// limited to what the API serves today. 新上线 doubles as the filtered list
// that 分类 facets, tag chips and maker links open (GET /api/releases filters).
// 热门 (MobileDiscover sub-tab; desktop has /trending in the side nav) and the
// 今晚看什么 entry on top (mobile) need the recs routes and are hidden without them.
// 即将上线 (NavNew): GET /api/upcoming grouped by day, 只看关注的演员 (signed in)
// and 预约 per upcoming release (notified on release by the notifier).
// 分类 (NavCategories): 类型方块 are the top tags as tiles; 语言 / 清晰度 / 字幕
// come from GET /api/catalog/facets and only show when sources carry them.
export default function Discover() {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab');
  const recs = useRecsAvailable(true);
  const tab: Tab = raw === 'categories' || raw === 'actors' || raw === 'upcoming' || (raw === 'trending' && recs !== 'no') ? raw : 'new';
  // Page heading (screen readers and crawlers): the tag or maker of a category page, else the tab.
  const load = useDiscoverLoad();
  const facet = tab === 'new' ? parseReleaseFilter(params) : null;
  const tagName = facet?.tag ? load?.tags?.find(x => x.slug === facet.tag)?.name ?? facet.tag : undefined;
  const heading = tagName
    ? t('seo.discoverTagTitle', {tag: tagName})
    : facet?.maker
      ? t('seo.discoverMakerTitle', {maker: facet.maker})
      : t(tabs.find(x => x.key === tab)?.label ?? 'nav.new');

  return (
    <div className="page discover">
      <h1 className="sr-only">{heading}</h1>
      <Link to="/search" className="fake-search show-mobile">
        <Icon name="search" size={16} />
        {t('discover.searchHint')}
      </Link>
      {recs !== 'no' && (
        <Link to="/tonight" className="tonight-entry show-mobile">
          <span className="tonight-entry-icon">
            <Icon name="tonight" size={20} />
          </span>
          <span className="tonight-entry-text">
            <strong>{t('tonight.title')}</strong>
            <span>{t('tonight.entryHint')}</span>
          </span>
          <Icon name="back" size={16} />
        </Link>
      )}
      <div className="tabbar" role="tablist" aria-label={t('tabs.discover')}>
        {tabs.filter(item => item.key !== 'trending' || recs !== 'no').map(item => (
          <button
            key={item.key}
            type="button"
            role="tab"
            aria-selected={tab === item.key}
            className="tabbar-tab"
            onClick={() => setParams(item.key === 'new' ? {} : {tab: item.key}, {replace: true})}
          >
            {t(item.label)}
          </button>
        ))}
      </div>
      {tab === 'trending' && <TrendingTab />}
      {tab === 'new' && <BrowseReleases />}
      {tab === 'upcoming' && <Upcoming />}
      {tab === 'categories' && <Categories />}
      {tab === 'actors' && <Actors />}
    </div>
  );
}

function TrendingTab() {
  const [params, setParams] = useSearchParams();
  const period = parsePeriod(params.get('period'));
  return (
    <section className="shelf">
      <PeriodTabs
        period={period}
        onChange={p => {
          const next = new URLSearchParams(params);
          next.set('tab', 'trending');
          if (p === 'week') next.delete('period');
          else next.set('period', p);
          setParams(next, {replace: true});
        }}
      />
      {/* Without the route recs availability turns 'no' and this tab falls back to 新上线. */}
      <TrendingBoard period={period} />
    </section>
  );
}

function BrowseReleases() {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const filter = parseReleaseFilter(params);
  const load = useDiscoverLoad();
  // Tag names for the active-filter chip; the URL carries the tag slug.
  const tags = useAsync(signal => listTags(200, signal), [], !!filter.tag, load?.tags);
  const tagName = tags.data?.find(tag => tag.slug === filter.tag)?.name;
  const facets = useCatalogFacets();
  const filtered = !!(filter.tag || filter.maker || filter.performer || filter.year || filter.min_runtime || filter.max_runtime || filter.min_height || filter.subtitle || filter.audio_lang);

  const update = (patch: Partial<Record<keyof ReleaseFilter, string | number | undefined>>) =>
    setParams(withFilter(params, patch), {replace: true});
  const clear = () => setParams({}, {replace: true});

  const summary = [
    filter.tag && `#${tagName || filter.tag}`,
    filter.maker,
    filter.year && t('common.year', {y: filter.year}),
    durationLabel(filter),
    filter.min_height && heightLabel(filter.min_height),
    filter.subtitle && (filter.subtitle === 'any' ? t('filter.subtitleAny') : t('filter.subtitleValue', {lang: languageName(filter.subtitle)})),
    filter.audio_lang && t('filter.audioValue', {lang: languageName(filter.audio_lang)}),
  ].filter(Boolean);
  const sortSub = filter.sort === 'oldest' ? t('discover.sortOldest') : filter.sort === 'runtime' ? t('discover.sortRuntime') : t('discover.sortNewest');

  return (
    <section className="shelf">
      <SectionHeader
        title={filtered ? t('discover.filtered') : t('nav.new')}
        sub={filtered ? summary.join(' · ') : sortSub}
        action={
          filtered ? (
            <button type="button" className="text-btn" onClick={clear}>
              {t('common.clearFilters')}
            </button>
          ) : undefined
        }
      />
      <FilterBar
        filter={filter}
        onChange={update}
        tagName={tagName}
        facets={facets}
        sortOptions={RELEASE_SORTS}
        sortValue={filter.sort ?? 'newest'}
        onSort={value => update({sort: value === 'newest' ? undefined : value})}
      />
      <ReleaseResults
        filter={filter}
        initial={load?.releases}
        onClear={clear}
        empty={
          <EmptyState
            icon="search"
            title={filtered ? t('discover.emptyFiltered') : t('discover.empty')}
            body={filtered ? t('discover.emptyFilteredBody') : undefined}
          >
            {filtered && (
              <div className="empty-actions">
                <button type="button" className="btn btn-primary" onClick={clear}>
                  {t('common.clearFilters')}
                </button>
              </div>
            )}
          </EmptyState>
        }
      />
    </section>
  );
}

const UPCOMING_PAGE = 30;

/** Groups the 即将上线 rows by day, keeping the API's soonest-first order; null = 日期待定 (last). */
function byDay(items: UpcomingItem[]): {date: string | null; items: UpcomingItem[]}[] {
  const groups: {date: string | null; items: UpcomingItem[]}[] = [];
  for (const it of items) {
    const last = groups[groups.length - 1];
    if (last && last.date === it.date) last.items.push(it);
    else groups.push({date: it.date, items: [it]});
  }
  return groups;
}

/** NavNew: 即将上线 grouped by day, 只看关注的演员, 预约. */
function Upcoming() {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const {status} = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const followedOnly = params.get('followed') === '1';
  const followed = followedOnly && status === 'user';
  const list = usePaged<UpcomingItem>(
    (cursor, signal) => listUpcoming({followed, limit: UPCOMING_PAGE}, cursor, signal),
    [followed, status],
    status !== 'unknown',
  );
  const toggle = (next: boolean) => {
    if (next && status !== 'user') {
      navigate(`/login?next=${encodeURIComponent(location.pathname + location.search)}`);
      return;
    }
    const p = new URLSearchParams(params);
    p.set('tab', 'upcoming');
    if (next) p.set('followed', '1');
    else p.delete('followed');
    setParams(p, {replace: true});
  };

  return (
    <section className="shelf upcoming">
      <SectionHeader
        title={t('nav.upcoming')}
        sub={t('upcoming.sub')}
        action={
          <label className="upcoming-toggle">
            <span>{t('upcoming.followedOnly')}</span>
            <Switch checked={followed} onChange={toggle} label={t('upcoming.followedOnly')} />
          </label>
        }
      />
      {list.error ? (
        <ErrorBanner error={list.error} onRetry={list.reload} />
      ) : list.loading ? (
        list.slow && <CardGridSkeleton count={6} variant="row" />
      ) : list.items.length === 0 ? (
        followed ? (
          <EmptyState icon="calendar" title={t('upcoming.emptyFollowed')} body={t('upcoming.emptyFollowedBody')} actions={[{to: '/discover?tab=actors', label: t('common.browseActors')}]} />
        ) : (
          <EmptyState icon="calendar" title={t('upcoming.empty')} body={t('upcoming.emptyBody')} actions={[{to: '/discover', label: t('nav.new')}]} />
        )
      ) : (
        <>
          {byDay(list.items).map(group => (
            <div key={group.date ?? 'tba'} className="upcoming-day">
              <h2 className="upcoming-date">{group.date ? upcomingDayLabel(group.date) : t('schedule.tba')}</h2>
              <div className="upcoming-list">
                {group.items.map(it => (
                  <UpcomingRow key={it.id} item={it} />
                ))}
              </div>
            </div>
          ))}
          <LoadMore state={list} />
        </>
      )}
    </section>
  );
}

function UpcomingRow({item}: {item: UpcomingItem}) {
  const t = useT();
  const title = item.display_title || item.canonical_title;
  const who = item.performers.map(p => p.name).join(t('common.listSep'));
  return (
    <ReleaseRow
      slug={item.slug}
      title={title}
      cover={item.cover}
      badge={item.reservable ? t('following.upcoming') : undefined}
      meta={[who, item.maker_name].filter(Boolean).join(' · ') || undefined}
      sub={formatSchedule(item.scheduled_at, item.release_date)}
      slot="discover:upcoming"
    >
      {item.reservable ? (
        <ReserveButton slug={item.slug} title={title} initial={item.reserved} size="sm" />
      ) : (
        <LaterButton slug={item.slug} title={title} variant="icon" />
      )}
    </ReleaseRow>
  );
}

/** 类型方块: the most used tags as tiles (NavCategories). */
const TILE_COUNT = 8;

function Categories() {
  const t = useT();
  const load = useDiscoverLoad();
  const tags = useAsync(signal => listTags(100, signal), [], true, load?.tags);
  const makers = useAsync(signal => listMakers(50, signal), [], true, load?.makers);
  const facets = useCatalogFacets();
  const maxTag = Math.max(1, ...(tags.data ?? []).map(tag => tag.count));
  const tiles = (tags.data ?? []).slice(0, TILE_COUNT);
  return (
    <>
      <p className="muted">{t('discover.categoriesHint')}</p>
      {tiles.length > 0 && (
        <section className="shelf">
          <SectionHeader title={t('discover.genres')} sub={t('discover.genresSub')} />
          <div className="genre-tiles">
            {tiles.map(tag => (
              <Link key={tag.slug} to={discoverHref({tag: tag.slug})} className={`genre-tile ph-${swatch(tag.slug)}`}>
                <span className="genre-tile-name">{tag.name}</span>
                <span className="genre-tile-count">{t('discover.genreCount', {n: formatCompact(tag.count)})}</span>
              </Link>
            ))}
          </div>
        </section>
      )}
      {facets && facets.audio_langs.length > 0 && (
        <section className="shelf">
          <SectionHeader title={t('discover.byLanguage')} sub={t('discover.byLanguageSub')} />
          <div className="chip-row chip-row-wrap">
            {facets.audio_langs.map(l => (
              <Link key={l.value} to={discoverHref({audio_lang: l.value})} className="chip">
                {languageName(l.value)}
                <span className="chip-count">{formatCount(l.count)}</span>
              </Link>
            ))}
            {facets.subtitled > 0 && (
              <Link to={discoverHref({subtitle: 'any'})} className="chip">
                {t('filter.subtitleAny')}
                <span className="chip-count">{formatCount(facets.subtitled)}</span>
              </Link>
            )}
          </div>
        </section>
      )}
      {facets && facets.heights.length > 0 && (
        <section className="shelf">
          <SectionHeader title={t('discover.byQuality')} />
          <div className="chip-row chip-row-wrap">
            {facets.heights.map(h => (
              <Link key={h.value} to={discoverHref({min_height: Number(h.value)})} className="chip">
                {t('filter.heightValue', {label: heightLabel(Number(h.value))})}
                <span className="chip-count">{formatCount(h.count)}</span>
              </Link>
            ))}
          </div>
        </section>
      )}
      <section className="shelf">
        <SectionHeader title={t('discover.byDuration')} />
        <div className="chip-row chip-row-wrap">
          {DURATION_BUCKETS.map(b => (
            <Link key={b.key} to={discoverHref({min_runtime: b.min, max_runtime: b.max})} className="chip">
              {b.label}
            </Link>
          ))}
        </div>
      </section>
      <section className="shelf">
        <SectionHeader title={t('discover.byYear')} sub={t('discover.byYearSub')} />
        <div className="chip-row chip-row-wrap">
          {yearOptions().map(y => (
            <Link key={y} to={discoverHref({year: y})} className="chip">
              {y}
            </Link>
          ))}
        </div>
      </section>
      <section className="shelf">
        <SectionHeader title={t('home.tags')} sub={t('discover.tagsSub')} />
        {tags.error ? (
          <ErrorBanner error={tags.error} onRetry={tags.reload} />
        ) : tags.slow ? (
          <CardGridSkeleton count={6} variant="row" />
        ) : (
          <div className="chip-row chip-row-wrap tag-cloud">
            {(tags.data ?? []).map(tag => (
              <Link
                key={tag.slug}
                to={discoverHref({tag: tag.slug})}
                className="chip"
                style={{fontSize: `${13 + Math.round((tag.count / maxTag) * 5)}px`}}
              >
                #{tag.name}
                <span className="chip-count">{formatCount(tag.count)}</span>
              </Link>
            ))}
          </div>
        )}
      </section>
      <section className="shelf">
        <SectionHeader title={t('discover.makers')} sub={t('discover.makersSub')} />
        {makers.error ? (
          <ErrorBanner error={makers.error} onRetry={makers.reload} />
        ) : makers.slow ? (
          <CardGridSkeleton count={6} variant="row" />
        ) : (
          <div className="chip-row chip-row-wrap">
            {(makers.data ?? []).map(m => (
              <Link key={m.name} to={discoverHref({maker: m.name})} className="chip">
                {m.name}
                <span className="chip-count">{formatCount(m.count)}</span>
              </Link>
            ))}
          </div>
        )}
      </section>
    </>
  );
}

const PERFORMER_SORTS: {value: PerformerSort; label: MessageKey; sub: MessageKey}[] = [
  {value: 'heat', label: 'discover.sortHeat', sub: 'discover.sortHeatSub'},
  {value: 'works', label: 'discover.sortWorks', sub: 'discover.sortWorksSub'},
  {value: 'followers', label: 'discover.sortFollowers', sub: 'discover.sortFollowersSub'},
  {value: 'rising', label: 'discover.sortRising', sub: 'discover.sortRisingSub'},
  {value: 'newest_work', label: 'discover.sortNewWork', sub: 'discover.sortNewWorkSub'},
  {value: 'name', label: 'discover.sortName', sub: 'discover.sortNameSub'},
];

// NavActors: 推荐演员, then the library with 排序 (热度 / 作品数 / 粉丝 / 新晋 /
// 新作 / 名字) and filters 活跃年代 / 擅长类型 / 作品语言 (GET /api/performers;
// heat, followers, rising and the filters read the hourly performer stats).
// 国籍 is not offered: performers carry no nationality data (only a free-text
// birth place), so the filter could not match reliably.
function Actors() {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const sort = actorSort(params);
  const filter = actorFilter(params);
  const filterKey = JSON.stringify(filter);
  const load = useDiscoverLoad();
  const facets = useCatalogFacets();
  const genres = useAsync(signal => listTags(60, signal), [], true);
  const list = usePaged((cursor, signal) => listPerformersFiltered(sort, filter, 60, cursor, signal), [sort, filterKey], true, load?.performers);
  // A backend without cursor pagination ignores sort and filters and answers by name.
  const sortable = list.paged !== false;
  const shown = sortable ? sort : 'name';
  const shownSort = PERFORMER_SORTS.find(s => s.value === shown);
  const filtered = !!(filter.era || filter.tag || filter.audio_lang);

  const update = (key: 'sort' | 'era' | 'genre' | 'audio_lang', value: string | undefined) => {
    const next = new URLSearchParams(params);
    next.set('tab', 'actors');
    if (!value || (key === 'sort' && value === 'works')) next.delete(key);
    else next.set(key, value);
    setParams(next, {replace: true});
  };
  const clear = () => setParams({tab: 'actors', ...(sort !== 'works' ? {sort} : {})}, {replace: true});

  return (
    <>
      <RecommendedPerformers />
      <section className="shelf">
        <SectionHeader
          title={t('home.actorLibrary')}
          sub={shownSort && t(shownSort.sub)}
          action={
            filtered ? (
              <button type="button" className="text-btn" onClick={clear}>
                {t('common.clearFilters')}
              </button>
            ) : undefined
          }
        />
        {sortable && (
          <div className="filter-row actor-filters">
            <label className="filter-select">
              <span className="sr-only">{t('discover.era')}</span>
              <select value={filter.era ?? ''} onChange={e => update('era', e.target.value || undefined)}>
                <option value="">{t('discover.eraAll')}</option>
                {ERAS.map(era => (
                  <option key={era} value={era}>
                    {t('discover.eraValue', {decade: era})}
                  </option>
                ))}
              </select>
            </label>
            <label className="filter-select">
              <span className="sr-only">{t('discover.genre')}</span>
              <select value={filter.tag ?? ''} onChange={e => update('genre', e.target.value || undefined)}>
                <option value="">{t('discover.genreAll')}</option>
                {filter.tag && !(genres.data ?? []).some(g => g.slug === filter.tag) && <option value={filter.tag}>{filter.tag}</option>}
                {(genres.data ?? []).map(g => (
                  <option key={g.slug} value={g.slug}>
                    {g.name}
                  </option>
                ))}
              </select>
            </label>
            {facets && facets.audio_langs.length > 0 && (
              <label className="filter-select">
                <span className="sr-only">{t('discover.workLanguage')}</span>
                <select value={filter.audio_lang ?? ''} onChange={e => update('audio_lang', e.target.value || undefined)}>
                  <option value="">{t('discover.workLanguageAll')}</option>
                  {facets.audio_langs.map(l => (
                    <option key={l.value} value={l.value}>
                      {languageName(l.value)}
                    </option>
                  ))}
                </select>
              </label>
            )}
            <span className="section-spacer" />
            <label className="filter-select">
              <span className="filter-label">{t('common.sort')}</span>{' '}
              <select value={sort} onChange={e => update('sort', e.target.value)} aria-label={t('common.sort')}>
                {PERFORMER_SORTS.map(s => (
                  <option key={s.value} value={s.value}>
                    {t(s.label)}
                  </option>
                ))}
              </select>
            </label>
          </div>
        )}
        {list.error ? (
          <ErrorBanner error={list.error} onRetry={list.reload} />
        ) : list.loading ? (
          list.slow && <CardGridSkeleton count={12} variant="avatar" />
        ) : list.items.length === 0 ? (
          filtered ? (
            <EmptyState icon="me" title={t('discover.actorsEmptyFiltered')} body={t('discover.actorsEmptyFilteredBody')}>
              <div className="empty-actions">
                <button type="button" className="btn btn-primary" onClick={clear}>
                  {t('common.clearFilters')}
                </button>
              </div>
            </EmptyState>
          ) : (
            <EmptyState icon="me" title={t('discover.actorsEmpty')} body={t('discover.actorsEmptyBody')} actions={[{to: '/', label: t('common.goHome')}]} />
          )
        ) : (
          <>
            <div className="avatar-grid">
              {list.items.map(p => (
                <PerformerChip key={p.id} slug={p.slug} name={p.canonical_name} sub={performerSub(p, shown)} portrait={p.portrait} />
              ))}
            </div>
            <LoadMore state={list} />
          </>
        )}
      </section>
    </>
  );
}

function performerSub(p: PerformerListItem, sort: PerformerSort): string | undefined {
  const works = typeof p.works_count === 'number' ? t('home.worksCount', {n: formatCount(p.works_count)}) : '';
  let extra = '';
  if (sort === 'newest_work' && p.latest_release_date) extra = t('discover.latestWork', {year: formatYear(p.latest_release_date)});
  else if (sort === 'heat' && p.heat) extra = t('discover.heatValue', {n: formatCompact(Math.round(p.heat / 10))});
  else if (sort === 'followers' && typeof p.followers === 'number') extra = t('discover.followersValue', {n: formatCompact(p.followers)});
  else if (sort === 'rising' && p.first_work_date) extra = t('discover.debutYear', {year: formatYear(p.first_work_date)});
  return [works, extra].filter(Boolean).join(' · ') || p.canonical_name_ja || undefined;
}
