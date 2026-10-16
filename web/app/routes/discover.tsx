import {Link, useSearchParams} from 'react-router';
import {PerformerChip, SectionHeader} from '../components/Cards';
import {Icon} from '../components/Icon';
import {LoadMore} from '../components/Paging';
import {FilterBar, ReleaseResults} from '../components/ReleaseFilters';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {listMakers, listPerformerPage, listTags, type PerformerListItem, type PerformerSort, type ReleaseFilter} from '../lib/api';
import {formatCount, formatYear} from '../lib/format';
import {t, useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {DURATION_BUCKETS, RELEASE_SORTS, discoverHref, durationLabel, parseReleaseFilter, withFilter, yearOptions} from '../lib/releaseFilter';
import {useAsync} from '../lib/useAsync';
import {usePaged} from '../lib/usePaged';

export const meta = () => [{title: t('title.discover')}];

type Tab = 'new' | 'categories' | 'actors';
const tabs: {key: Tab; label: MessageKey}[] = [
  {key: 'new', label: 'nav.new'},
  {key: 'categories', label: 'nav.categories'},
  {key: 'actors', label: 'nav.actors'},
];

// MobileDiscover sub-tabs and the SideNav 新上线 / 分类 / 演员 destinations,
// limited to what the API serves today. 新上线 doubles as the filtered list
// that 分类 facets, tag chips and maker links open (GET /api/releases filters).
// TODO(tech plan 3.13 trending_snapshots): 热门 tab (NavTrending).
// TODO(tech plan 3.13 releases.status='upcoming' + reservations): 即将上线 / 预约 (NavNew).
// TODO(tech plan 3.4): 类型方块 and 语言 facet (NavCategories) — no genre or language data yet.
// TODO(PRD 18): 今晚看什么 entry.
export default function Discover() {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab');
  const tab: Tab = raw === 'categories' || raw === 'actors' ? raw : 'new';

  return (
    <div className="page discover">
      <Link to="/search" className="fake-search show-mobile">
        <Icon name="search" size={16} />
        {t('discover.searchHint')}
      </Link>
      <div className="tabbar" role="tablist" aria-label={t('tabs.discover')}>
        {tabs.map(item => (
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
      {tab === 'new' && <BrowseReleases />}
      {tab === 'categories' && <Categories />}
      {tab === 'actors' && <Actors />}
    </div>
  );
}

function BrowseReleases() {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const filter = parseReleaseFilter(params);
  // Tag names for the active-filter chip; the URL carries the tag slug.
  const tags = useAsync(signal => listTags(200, signal), [], !!filter.tag);
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
  const tags = useAsync(signal => listTags(100, signal), []);
  const makers = useAsync(signal => listMakers(50, signal), []);
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
  const list = usePaged((cursor, signal) => listPerformerPage(sort, 60, cursor, undefined, signal), [sort]);
  // A backend without cursor pagination ignores sort and answers by name.
  const sortable = list.paged !== false;
  const shown = sortable ? sort : 'name';
  const shownSort = PERFORMER_SORTS.find(s => s.value === shown);

  return (
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
  );
}

function performerSub(p: PerformerListItem, sort: PerformerSort): string | undefined {
  const works = typeof p.works_count === 'number' ? t('home.worksCount', {n: formatCount(p.works_count)}) : '';
  const latest = sort === 'newest_work' && p.latest_release_date ? t('discover.latestWork', {year: formatYear(p.latest_release_date)}) : '';
  return [works, latest].filter(Boolean).join(' · ') || p.canonical_name_ja || undefined;
}
