import {data, useLoaderData, useSearchParams, type LoaderFunctionArgs, type MetaFunction} from 'react-router';
import {serverApi} from '../lib/api.server';
import {routeLang} from '../lib/langs';
import {breadcrumbs, seoMeta, type SeoHandle} from '../lib/seo';
import {Link} from '../lib/nav';
import {PerformerChip, SectionHeader} from '../components/Cards';
import {RecommendedPerformers} from '../components/PerformerRecs';
import {parsePeriod, PeriodTabs, TrendingBoard} from '../components/Trending';
import {Icon} from '../components/Icon';
import {LoadMore} from '../components/Paging';
import {FilterBar, RELEASE_PAGE_SIZE, ReleaseResults} from '../components/ReleaseFilters';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {listMakers, listPerformerPage, listTags, query, toPage, type LangCode, type Maker, type Page, type PerformerListItem, type PerformerSort, type Release, type ReleaseFilter, type Tag} from '../lib/api';
import {formatCount, formatYear} from '../lib/format';
import {t, useI18n, useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {DURATION_BUCKETS, RELEASE_SORTS, discoverHref, durationLabel, parseReleaseFilter, withFilter, yearOptions} from '../lib/releaseFilter';
import {useRecsAvailable} from '../lib/recs';
import {useAsync} from '../lib/useAsync';
import {usePaged} from '../lib/usePaged';

export const handle: SeoHandle = {seo: true};

type Tab = 'trending' | 'new' | 'categories' | 'actors';

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
  return raw === 'categories' || raw === 'actors' || raw === 'trending' ? raw : 'new';
};
const actorSort = (params: URLSearchParams): PerformerSort => {
  const raw = params.get('sort');
  return raw === 'name' || raw === 'newest_work' ? raw : 'works';
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
    body.performers = await serverApi<Items<PerformerListItem>>(`performers${query({sort: sort === 'name' ? undefined : sort, limit: 60})}`, opts).then(toPage, fail);
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
  const facets = [filter.tag, filter.maker, filter.performer, filter.year, filter.min_runtime, filter.max_runtime].filter(v => v !== undefined).length;
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
    return seoMeta({matches, path, title: t('title.named', {name: t('seo.discoverActorsTitle')}), description: t('seo.discoverActorsDescription'), noindex: params.has('sort'), jsonLd: [crumbs(t('seo.discoverActorsTitle'), path)]});
  }
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
  {key: 'categories', label: 'nav.categories'},
  {key: 'actors', label: 'nav.actors'},
];

// MobileDiscover sub-tabs and the SideNav 新上线 / 分类 / 演员 destinations,
// limited to what the API serves today. 新上线 doubles as the filtered list
// that 分类 facets, tag chips and maker links open (GET /api/releases filters).
// 热门 (MobileDiscover sub-tab; desktop has /trending in the side nav) and the
// 今晚看什么 entry on top (mobile) need the recs routes and are hidden without them.
// TODO(tech plan 3.13 releases.status='upcoming' + reservations): 即将上线 / 预约 (NavNew).
// TODO(tech plan 3.4): 类型方块 and 语言 facet (NavCategories) — no genre or language data yet.
export default function Discover() {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab');
  const recs = useRecsAvailable(true);
  const tab: Tab = raw === 'categories' || raw === 'actors' || (raw === 'trending' && recs !== 'no') ? raw : 'new';
  const heading = t(tabs.find(x => x.key === tab)?.label ?? 'nav.new');

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
  const filtered = !!(filter.tag || filter.maker || filter.performer || filter.year || filter.min_runtime || filter.max_runtime);

  const update = (patch: Partial<Record<keyof ReleaseFilter, string | number | undefined>>) =>
    setParams(withFilter(params, patch), {replace: true});
  const clear = () => setParams({}, {replace: true});

  const summary = [
    filter.tag && `#${tagName || filter.tag}`,
    filter.maker,
    filter.year && t('common.year', {y: filter.year}),
    durationLabel(filter),
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

function Categories() {
  const t = useT();
  const load = useDiscoverLoad();
  const tags = useAsync(signal => listTags(100, signal), [], true, load?.tags);
  const makers = useAsync(signal => listMakers(50, signal), [], true, load?.makers);
  const maxTag = Math.max(1, ...(tags.data ?? []).map(tag => tag.count));
  return (
    <>
      <p className="muted">{t('discover.categoriesHint')}</p>
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
  {value: 'works', label: 'discover.sortWorks', sub: 'discover.sortWorksSub'},
  {value: 'newest_work', label: 'discover.sortNewWork', sub: 'discover.sortNewWorkSub'},
  {value: 'name', label: 'discover.sortName', sub: 'discover.sortNameSub'},
];

function Actors() {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const raw = params.get('sort');
  const sort: PerformerSort = raw === 'name' || raw === 'newest_work' ? raw : 'works';
  const load = useDiscoverLoad();
  const list = usePaged((cursor, signal) => listPerformerPage(sort, 60, cursor, undefined, signal), [sort], true, load?.performers);
  // A backend without cursor pagination ignores sort and answers by name.
  const sortable = list.paged !== false;
  const shown = sortable ? sort : 'name';
  const shownSort = PERFORMER_SORTS.find(s => s.value === shown);

  return (
    <>
    <RecommendedPerformers />
    <section className="shelf">
      <SectionHeader
        title={t('home.actorLibrary')}
        sub={shownSort && t(shownSort.sub)}
        action={
          sortable ? (
            <div className="segmented segmented-sm" role="radiogroup" aria-label={t('common.sort')}>
              {PERFORMER_SORTS.map(s => (
                <button
                  key={s.value}
                  type="button"
                  role="radio"
                  aria-checked={s.value === sort}
                  className="segmented-item"
                  onClick={() => {
                    const next = new URLSearchParams(params);
                    next.set('tab', 'actors');
                    if (s.value === 'works') next.delete('sort');
                    else next.set('sort', s.value);
                    setParams(next, {replace: true});
                  }}
                >
                  {t(s.label)}
                </button>
              ))}
            </div>
          ) : undefined
        }
      />
      {list.error ? (
        <ErrorBanner error={list.error} onRetry={list.reload} />
      ) : list.loading ? (
        list.slow && <CardGridSkeleton count={12} variant="avatar" />
      ) : list.items.length === 0 ? (
        <EmptyState icon="me" title={t('discover.actorsEmpty')} body={t('discover.actorsEmptyBody')} actions={[{to: '/', label: t('common.goHome')}]} />
      ) : (
        <>
          <div className="avatar-grid">
            {list.items.map(p => (
              <PerformerChip key={p.id} slug={p.slug} name={p.canonical_name} sub={performerSub(p, shown)} />
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
  const latest = sort === 'newest_work' && p.latest_release_date ? t('discover.latestWork', {year: formatYear(p.latest_release_date)}) : '';
  return [works, latest].filter(Boolean).join(' · ') || p.canonical_name_ja || undefined;
}
