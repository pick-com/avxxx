import {useEffect, useState} from 'react';
import {useParams} from 'react-router';
import {Avatar, ReleaseCard, SectionHeader} from '../components/Cards';
import {LoadMore} from '../components/Paging';
import {BlockSkeleton, CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {getPerformer, getPerformerReleases, listPerformerPage, listReleasePage, type PerformerDetail} from '../lib/api';
import {ageAt, formatCount, formatDate, formatYear} from '../lib/format';
import {useAsync} from '../lib/useAsync';
import {useCanonicalSlug} from '../lib/useCanonicalSlug';
import {usePaged} from '../lib/usePaged';

export const meta = () => [{title: 'REEL · 演员'}];

const PAGE_SIZE = 24;
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
// TODO(tech plan 3.13 follows): 关注 button.
// TODO(tech plan 3.13 performer_stats): 总播放 / 粉丝 / 热度 stats.
// TODO(tech plan 3.13 release_performers.role_type): 主演 / 客串 filters; 最热 sort.
// TODO(tech plan 3.5 performer_similar): 相似演员.
// TODO(tech plan 2.4 media_assets): 主照片 and 照片集.
// TODO: aliases and multi-language names beyond canonical_name_ja are not in GET /api/performers/{slug}.
export default function PerformerPage() {
  const {slug = ''} = useParams();
  const detail = useAsync(signal => getPerformer(slug, signal), [slug]);
  const works = usePaged(async (cursor, signal) => {
    const page = await listReleasePage({performer: slug}, PAGE_SIZE, cursor, signal);
    if (page.paged) return page;
    // Old backend ignored ?performer= and returned every release.
    const items = await getPerformerReleases(slug, LEGACY_LIMIT, signal);
    return {items, nextCursor: null, paged: false};
  }, [slug]);
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
    if (detail.data) document.title = `${detail.data.canonical_name} · REEL`;
  }, [detail.data]);

  if (detail.error) {
    const missing = detail.error.message === '内容不存在或已下架';
    return (
      <div className="page">
        {missing ? (
          <EmptyState icon="me" title="没有找到这位演员" body="演员资料可能已合并或下架。" actions={[{to: '/discover?tab=actors', label: '浏览演员库'}]} />
        ) : (
          <ErrorBanner error={detail.error} onRetry={detail.reload} />
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
            {countLabel && (
              <div className="stat-row">
                <div className="stat">
                  <span className="stat-value">{countLabel}</span>
                  <span className="stat-label">作品</span>
                </div>
              </div>
            )}
          </div>
        </section>
      )}

      <div className="tabbar show-mobile" role="tablist" aria-label="演员页内容">
        <button type="button" role="tab" aria-selected={tab === 'works'} className="tabbar-tab" onClick={() => setTab('works')}>
          作品
        </button>
        <button type="button" role="tab" aria-selected={tab === 'info'} className="tabbar-tab" onClick={() => setTab('info')}>
          资料
        </button>
      </div>

      {p && (
        <section className="performer-info">
          <PerformerMetadata detail={p} />
        </section>
      )}

      <section className="performer-works shelf">
        <SectionHeader title={countLabel ? `出演作品 · ${countLabel}` : '出演作品'} sub="按发行日期，最新在前" />
        {works.error ? (
          <ErrorBanner error={works.error} onRetry={works.reload} />
        ) : works.loading ? (
          works.slow && <CardGridSkeleton count={10} />
        ) : works.items.length === 0 ? (
          <EmptyState title="还没有收录作品" body="这位演员的作品还在整理中。" actions={[{to: '/', label: '去首页逛逛'}]} />
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
                    badge={r.runtime_minutes ? `${r.runtime_minutes} 分` : undefined}
                    meta={[r.maker_name, formatDate(r.release_date)].filter(Boolean).join(' · ')}
                    sub={year && age !== null ? `${year} 年发行 · 约 ${age} 岁` : undefined}
                  />
                );
              })}
            </div>
            <LoadMore state={works} label="更多作品" />
          </>
        )}
      </section>
    </div>
  );
}

const present = (value: unknown) => value !== null && value !== undefined && String(value).trim() !== '';

/** Same fields as the previous PerformerMetadata. */
function PerformerMetadata({detail}: {detail: PerformerDetail}) {
  const height = present(detail.height_cm) ? `${Number.parseFloat(String(detail.height_cm))} cm` : '';
  const measurements = detail.bust_cm && detail.waist_cm && detail.hip_cm ? `B${detail.bust_cm} W${detail.waist_cm} H${detail.hip_cm}` : '';
  const fields: [string, unknown][] = [
    ['日文名', detail.canonical_name_ja],
    ['出生日期', formatDate(detail.birth_date) || detail.birth_date],
    ['出身地', detail.birth_place],
    ['出道日期', formatDate(detail.debut_date) || detail.debut_date],
    ['身高', height],
    ['三围', measurements],
    ['罩杯', detail.cup_size],
    ['血型', detail.blood_type],
    ['活动时间', detail.activity_start || detail.activity_end ? `${detail.activity_start || '?'} – ${detail.activity_end || '至今'}` : ''],
  ];
  const rows = fields.filter(([, v]) => present(v));
  if (rows.length === 0 && !detail.biography) return <p className="muted">暂无资料。</p>;
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
