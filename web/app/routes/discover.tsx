import {Link, useSearchParams} from 'react-router';
import {PerformerChip, ReleaseCard, SectionHeader} from '../components/Cards';
import {Icon} from '../components/Icon';
import {CardGridSkeleton, ErrorBanner} from '../components/States';
import {listMakers, listPerformers, listReleases, listTags} from '../lib/api';
import {formatDate} from '../lib/format';
import {useAsync} from '../lib/useAsync';

export const meta = () => [{title: 'REEL · 发现'}];

type Tab = 'new' | 'categories' | 'actors';
const tabs: {key: Tab; label: string}[] = [
  {key: 'new', label: '新上线'},
  {key: 'categories', label: '分类'},
  {key: 'actors', label: '演员'},
];

// MobileDiscover sub-tabs and the SideNav 新上线 / 分类 / 演员 destinations,
// limited to what the API serves today.
// TODO(tech plan 3.13 trending_snapshots): 热门 tab (NavTrending).
// TODO(tech plan 3.13 releases.status='upcoming' + reservations): 即将上线 / 预约 (NavNew).
// TODO(tech plan 3.4): tag and maker filters that open filtered results (NavCategories).
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
      {tab === 'new' && <NewReleases />}
      {tab === 'categories' && <Categories />}
      {tab === 'actors' && <Actors />}
    </div>
  );
}

function NewReleases() {
  const releases = useAsync(signal => listReleases(60, signal), []);
  return (
    <section className="shelf">
      <SectionHeader title="新上线" sub="按发行日期，最新在前" />
      {releases.error ? (
        <ErrorBanner error={releases.error} onRetry={releases.reload} />
      ) : releases.slow ? (
        <CardGridSkeleton count={10} />
      ) : (
        <div className="card-grid">
          {(releases.data ?? []).map(r => (
            <ReleaseCard
              key={r.id}
              slug={r.slug}
              title={r.canonical_title}
              badge={r.runtime_minutes ? `${r.runtime_minutes} 分` : undefined}
              meta={[r.maker_name, formatDate(r.release_date)].filter(Boolean).join(' · ')}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function Categories() {
  const tags = useAsync(signal => listTags(100, signal), []);
  const makers = useAsync(signal => listMakers(50, signal), []);
  return (
    <>
      <section className="shelf">
        <SectionHeader title="标签" sub="按作品数" />
        {tags.error ? (
          <ErrorBanner error={tags.error} onRetry={tags.reload} />
        ) : tags.slow ? (
          <CardGridSkeleton count={6} variant="row" />
        ) : (
          <div className="chip-row chip-row-wrap">
            {(tags.data ?? []).map(t => (
              <span key={t.slug} className="chip chip-static">
                {t.name}
                <span className="chip-count">{t.count}</span>
              </span>
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
              <span key={m.name} className="chip chip-static">
                {m.name}
                <span className="chip-count">{m.count}</span>
              </span>
            ))}
          </div>
        )}
      </section>
    </>
  );
}

function Actors() {
  const performers = useAsync(signal => listPerformers(100, signal), []);
  return (
    <section className="shelf">
      <SectionHeader title="演员库" sub="按名字排序" />
      {performers.error ? (
        <ErrorBanner error={performers.error} onRetry={performers.reload} />
      ) : performers.slow ? (
        <CardGridSkeleton count={12} variant="avatar" />
      ) : (
        <div className="avatar-grid">
          {(performers.data ?? []).map(p => (
            <PerformerChip key={p.id} slug={p.slug} name={p.canonical_name} sub={p.canonical_name_ja} />
          ))}
        </div>
      )}
    </section>
  );
}
