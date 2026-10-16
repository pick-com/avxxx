import {Link} from 'react-router';
import {ReleaseCard, PerformerChip, SectionHeader} from '../components/Cards';
import {CardGridSkeleton, ErrorBanner} from '../components/States';
import {getWatchHistory, listPerformerPage, listReleases, listTags} from '../lib/api';
import {discoverHref} from '../lib/releaseFilter';
import {useAuth} from '../lib/auth';
import {formatDate, formatRuntime} from '../lib/format';
import {useAsync} from '../lib/useAsync';

export const meta = () => [{title: 'REEL · 首页'}];

// NavHome / NavHomeVisitor / MobileHome.
//
// Shelves without a backend are left out rather than filled with sample data:
// TODO(tech plan 3.2 home_slots + 3.11 运营排期): 主推横幅.
// TODO(tech plan 3.5 GET /api/recs?slot=home): 为你推荐.
// TODO(tech plan 3.13 follows + GET /api/me/following/feed): 你关注的演员有新作.
// TODO(tech plan 3.13 trending_snapshots / 3.8 ratings): visitor 本周热门 and 高分短片.
// TODO(PRD 18 / tech plan 3.5 slot=tonight): 今晚看什么 entry.
export default function Home() {
  const {status} = useAuth();
  const releases = useAsync(signal => listReleases(10, signal), []);
  const performers = useAsync(signal => listPerformerPage('works', 12, null, undefined, signal), []);
  const tags = useAsync(signal => listTags(12, signal), []);
  const history = useAsync(signal => getWatchHistory(signal), [status], status === 'user');

  return (
    <div className="page home">
      {status === 'anon' && (
        <section className="visitor-card">
          <span className="visitor-text">
            <strong>不用登录也能看全部视频</strong>
            <span>登录后：记住看到哪里，收藏喜欢的作品，换设备也能接着看。</span>
          </span>
          <Link to="/login" className="btn btn-primary">
            登录 / 注册
          </Link>
        </section>
      )}

      {/* Tag chips: GET /api/tags; each opens the tag-filtered list
          (/discover?tag=, GET /api/releases?tag=). */}
      {tags.data && tags.data.length > 0 && (
        <div className="chip-row" aria-label="热门标签">
          {tags.data.map(tag => (
            <Link key={tag.slug} to={discoverHref({tag: tag.slug})} className="chip" title={`${tag.count} 部作品`}>
              {tag.name}
              <span className="chip-count">{tag.count}</span>
            </Link>
          ))}
        </div>
      )}

      {status === 'user' && (history.loading || (history.data && history.data.length > 0) || history.error) && (
        <section className="shelf">
          <SectionHeader title="继续观看" sub="从上次的位置接着看" action={<Link to="/history" className="section-link">全部</Link>} />
          {history.error ? (
            <ErrorBanner error={history.error} onRetry={history.reload} />
          ) : history.slow ? (
            <CardGridSkeleton count={5} />
          ) : (
            <div className="card-grid card-grid-scroll">
              {(history.data ?? []).slice(0, 10).map(item => {
                const ratio = item.duration_seconds > 0 ? item.progress_seconds / item.duration_seconds : undefined;
                const left = item.duration_seconds > 0 ? Math.max(0, Math.ceil((item.duration_seconds - item.progress_seconds) / 60)) : null;
                return (
                  <ReleaseCard
                    key={item.slug}
                    slug={item.slug}
                    title={item.title}
                    progress={ratio ?? 0}
                    meta={left === null ? '继续观看' : left === 0 ? '已看完' : `剩余 ${left} 分钟`}
                  />
                );
              })}
            </div>
          )}
        </section>
      )}

      <section className="shelf">
        <SectionHeader title="新上线" sub="按发行日期" action={<Link to="/discover?tab=new" className="section-link">查看全部</Link>} />
        {releases.error ? (
          <ErrorBanner error={releases.error} onRetry={releases.reload} />
        ) : releases.slow ? (
          <CardGridSkeleton count={10} />
        ) : releases.data && releases.data.length === 0 ? (
          <p className="muted">还没有已发布的作品。</p>
        ) : (
          <div className="card-grid">
            {(releases.data ?? []).map(r => (
              <ReleaseCard
                key={r.id}
                slug={r.slug}
                title={r.canonical_title}
                badge={r.runtime_minutes ? `${r.runtime_minutes} 分` : undefined}
                meta={[r.maker_name, formatDate(r.release_date)].filter(Boolean).join(' · ')}
                sub={!r.maker_name && !r.release_date ? formatRuntime(r.runtime_minutes) : undefined}
              />
            ))}
          </div>
        )}
      </section>

      {/* Asked for sort=works; a backend without sorting answers by name, so the
          sub-title follows what came back. Not 热门演员: there is no popularity
          data until performer_stats exists (tech plan 3.13). */}
      <section className="shelf">
        <SectionHeader
          title="演员"
          sub={performers.data?.paged ? '作品最多' : undefined}
          action={<Link to="/discover?tab=actors" className="section-link">演员库</Link>}
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
                sub={typeof p.works_count === 'number' ? `${p.works_count} 部作品` : p.canonical_name_ja}
              />
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
