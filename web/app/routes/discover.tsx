import {Link, useSearchParams} from 'react-router';
import {PerformerChip, SectionHeader} from '../components/Cards';
import {Icon} from '../components/Icon';
import {LoadMore} from '../components/Paging';
import {FilterBar, ReleaseResults} from '../components/ReleaseFilters';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {listMakers, listPerformerPage, listTags, type PerformerListItem, type PerformerSort, type ReleaseFilter} from '../lib/api';
import {formatCount, formatYear} from '../lib/format';
import {DURATION_BUCKETS, RELEASE_SORTS, discoverHref, durationLabel, parseReleaseFilter, withFilter, yearOptions} from '../lib/releaseFilter';
import {useAsync} from '../lib/useAsync';
import {usePaged} from '../lib/usePaged';

export const meta = () => [{title: 'REEL · 发现'}];

type Tab = 'new' | 'categories' | 'actors';
const tabs: {key: Tab; label: string}[] = [
  {key: 'new', label: '新上线'},
  {key: 'categories', label: '分类'},
  {key: 'actors', label: '演员'},
];

// MobileDiscover sub-tabs and the SideNav 新上线 / 分类 / 演员 destinations,
// limited to what the API serves today. 新上线 doubles as the filtered list
// that 分类 facets, tag chips and maker links open (GET /api/releases filters).
// TODO(tech plan 3.13 trending_snapshots): 热门 tab (NavTrending).
// TODO(tech plan 3.13 releases.status='upcoming' + reservations): 即将上线 / 预约 (NavNew).
// TODO(tech plan 3.4): 类型方块 and 语言 facet (NavCategories) — no genre or language data yet.
// TODO(PRD 18): 今晚看什么 entry.
export default function Discover() {
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab');
  const tab: Tab = raw === 'categories' || raw === 'actors' ? raw : 'new';

  return (
    <div className="page discover">
      <Link to="/search" className="fake-search show-mobile">
        <Icon name="search" size={16} />
        搜索视频、演员
      </Link>
      <div className="tabbar" role="tablist" aria-label="发现">
        {tabs.map(t => (
          <button
            key={t.key}
            type="button"
            role="tab"
            aria-selected={tab === t.key}
            className="tabbar-tab"
            onClick={() => setParams(t.key === 'new' ? {} : {tab: t.key}, {replace: true})}
          >
            {t.label}
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
  const [params, setParams] = useSearchParams();
  const filter = parseReleaseFilter(params);
  // Tag names for the active-filter chip; the URL carries the tag slug.
  const tags = useAsync(signal => listTags(200, signal), [], !!filter.tag);
  const tagName = tags.data?.find(t => t.slug === filter.tag)?.name;
  const filtered = !!(filter.tag || filter.maker || filter.performer || filter.year || filter.min_runtime || filter.max_runtime);

  const update = (patch: Partial<Record<keyof ReleaseFilter, string | number | undefined>>) =>
    setParams(withFilter(params, patch), {replace: true});
  const clear = () => setParams({}, {replace: true});

  const summary = [
    filter.tag && `#${tagName || filter.tag}`,
    filter.maker,
    filter.year && `${filter.year} 年`,
    durationLabel(filter),
  ].filter(Boolean);
  const sortSub = filter.sort === 'oldest' ? '按发行日期，最早在前' : filter.sort === 'runtime' ? '按时长，最长在前' : '按发行日期，最新在前';

  return (
    <section className="shelf">
      <SectionHeader
        title={filtered ? '筛选结果' : '新上线'}
        sub={filtered ? summary.join(' · ') : sortSub}
        action={
          filtered ? (
            <button type="button" className="text-btn" onClick={clear}>
              清除筛选
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
            title={filtered ? '没有符合条件的作品' : '还没有已发布的作品'}
            body={filtered ? '试试放宽时长或年份，或去掉一个筛选条件。' : undefined}
          >
            {filtered && (
              <div className="empty-actions">
                <button type="button" className="btn btn-primary" onClick={clear}>
                  清除筛选
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
  const tags = useAsync(signal => listTags(100, signal), []);
  const makers = useAsync(signal => listMakers(50, signal), []);
  const maxTag = Math.max(1, ...(tags.data ?? []).map(t => t.count));
  return (
    <>
      <p className="muted">点击任一分类进入列表页，可继续叠加筛选。</p>
      <section className="shelf">
        <SectionHeader title="按时长" />
        <div className="chip-row chip-row-wrap">
          {DURATION_BUCKETS.map(b => (
            <Link key={b.key} to={discoverHref({min_runtime: b.min, max_runtime: b.max})} className="chip">
              {b.label}
            </Link>
          ))}
        </div>
      </section>
      <section className="shelf">
        <SectionHeader title="按年代" sub="发行年份" />
        <div className="chip-row chip-row-wrap">
          {yearOptions().map(y => (
            <Link key={y} to={discoverHref({year: y})} className="chip">
              {y}
            </Link>
          ))}
        </div>
      </section>
      <section className="shelf">
        <SectionHeader title="热门标签" sub="字号越大，作品越多" />
        {tags.error ? (
          <ErrorBanner error={tags.error} onRetry={tags.reload} />
        ) : tags.slow ? (
          <CardGridSkeleton count={6} variant="row" />
        ) : (
          <div className="chip-row chip-row-wrap tag-cloud">
            {(tags.data ?? []).map(t => (
              <Link
                key={t.slug}
                to={discoverHref({tag: t.slug})}
                className="chip"
                style={{fontSize: `${13 + Math.round((t.count / maxTag) * 5)}px`}}
              >
                #{t.name}
                <span className="chip-count">{formatCount(t.count)}</span>
              </Link>
            ))}
          </div>
        )}
      </section>
      <section className="shelf">
        <SectionHeader title="片商" sub="按作品数" />
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

const PERFORMER_SORTS: {value: PerformerSort; label: string; sub: string}[] = [
  {value: 'works', label: '作品数', sub: '作品最多在前'},
  {value: 'newest_work', label: '新作', sub: '最近有新作在前'},
  {value: 'name', label: '名字', sub: '按名字排序'},
];

function Actors() {
  const [params, setParams] = useSearchParams();
  const raw = params.get('sort');
  const sort: PerformerSort = raw === 'name' || raw === 'newest_work' ? raw : 'works';
  const list = usePaged((cursor, signal) => listPerformerPage(sort, 60, cursor, undefined, signal), [sort]);
  // A backend without cursor pagination ignores sort and answers by name.
  const sortable = list.paged !== false;
  const shown = sortable ? sort : 'name';

  return (
    <section className="shelf">
      <SectionHeader
        title="演员库"
        sub={PERFORMER_SORTS.find(s => s.value === shown)?.sub}
        action={
          sortable ? (
            <div className="segmented segmented-sm" role="radiogroup" aria-label="排序">
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
                  {s.label}
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
        <EmptyState icon="me" title="演员库还是空的" body="演员资料还在整理中。" actions={[{to: '/', label: '去首页逛逛'}]} />
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
  const works = typeof p.works_count === 'number' ? `${formatCount(p.works_count)} 部作品` : '';
  const latest = sort === 'newest_work' && p.latest_release_date ? `新作 ${formatYear(p.latest_release_date)}` : '';
  return [works, latest].filter(Boolean).join(' · ') || p.canonical_name_ja || undefined;
}
