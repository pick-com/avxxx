import {useRef, useState} from 'react';
import {Link} from '../lib/nav';
import {formatCompact, formatDate, formatRuntime} from '../lib/format';
import {useT} from '../lib/i18n';
import {getTrending, movementLabel, PERIODS, performerName, reasonText, recTitle, recsMissing, type HeatItem, type Movement, type Period, type TrendingItem} from '../lib/recs';
import {usePaged} from '../lib/usePaged';
import {Avatar, CardReason, SectionHeader, Thumb} from './Cards';
import {LoadMore} from './Paging';
import {CardGridSkeleton, EmptyState, ErrorBanner} from './States';
import {HotSearches} from './HotSearches';

// 热门 · NavTrending (PRD 4.2): 今日 / 本周 / 本月 / 年度 boards from
// GET /api/trending?period= (precomputed hourly by the recs trending job),
// with ▲ ▼ 新上榜 movement against the previous run. The first page also
// carries the top performer heat rows (演员热度榜, right column).
//
// 热搜词 (right column, under 演员热度榜): GET /api/search/hot, hidden when
// the route is not deployed or the board is empty.
// TODO(tech plan 3.4): genre chips (全部 / 剧情 / 悬疑 …) — the board has no
// per-genre variant.

type Row = TrendingItem & {id: number};

export function parsePeriod(v: string | null): Period {
  return v === 'today' || v === 'month' || v === 'year' ? v : 'week';
}

export function PeriodTabs({period, onChange}: {period: Period; onChange: (p: Period) => void}) {
  const t = useT();
  return (
    <div className="segmented segmented-sm trending-periods" role="tablist" aria-label={t('trending.periods')}>
      {PERIODS.map(p => (
        <button key={p.value} type="button" role="tab" aria-selected={p.value === period} className="segmented-item" onClick={() => onChange(p.value)}>
          {t(p.label)}
        </button>
      ))}
    </div>
  );
}

function movementTitle(t: ReturnType<typeof useT>, m: Movement, delta: number) {
  if (m === 'new') return t('trending.new');
  if (m === 'up') return t('trending.movementUp', {n: delta});
  if (m === 'down') return t('trending.movementDown', {n: Math.abs(delta)});
  return t('trending.movementSame');
}

export function MovementBadge({movement, delta}: {movement: Movement; delta: number}) {
  const t = useT();
  return (
    <span className={`movement movement-${movement}`} title={movementTitle(t, movement, delta)}>
      <span aria-hidden="true">{movementLabel(movement, delta)}</span>
      <span className="sr-only">{movementTitle(t, movement, delta)}</span>
    </span>
  );
}

/**
 * The board for one period. `onMissing` fires when the backend has no
 * trending route, so the caller can fall back.
 */
export function TrendingBoard({period, onMissing, showHeat = true}: {period: Period; onMissing?: () => void; showHeat?: boolean}) {
  const t = useT();
  const [heat, setHeat] = useState<HeatItem[]>([]);
  const missingRef = useRef(onMissing);
  missingRef.current = onMissing;
  const list = usePaged<Row>(
    async (cursor, signal) => {
      try {
        const page = await getTrending(period, {limit: 20, cursor}, signal);
        if (!cursor) setHeat(page.performers);
        // usePaged de-duplicates by id; board rows key on the release.
        return {...page, items: page.items.map(x => ({...x, id: x.release?.id ?? -x.rank}))};
      } catch (error) {
        if (recsMissing(error)) missingRef.current?.();
        throw error;
      }
    },
    [period],
  );

  const rows = list.items.filter(r => r.release);
  return (
    <div className="trending-layout">
      <section className="trending-main" aria-label={t('trending.title')}>
        {list.error ? (
          recsMissing(list.error) ? null : <ErrorBanner error={list.error} onRetry={list.reload} />
        ) : list.loading ? (
          list.slow && <CardGridSkeleton count={6} variant="row" />
        ) : rows.length === 0 ? (
          <EmptyState icon="trending" title={t('trending.empty')} body={t('trending.emptyBody')} actions={[{to: '/discover?tab=new', label: t('nav.new')}]} />
        ) : (
          <>
            <ol className="trending-list">
              {rows.map(row => (
                <TrendingRow key={row.id} row={row} period={period} />
              ))}
            </ol>
            <LoadMore state={list} label={t('trending.more')} />
          </>
        )}
      </section>
      {showHeat && (
        <aside className="trending-aside">
          {heat.length > 0 && (
            <div className="side-card">
              <SectionHeader title={t('trending.actors')} action={<Link to="/discover?tab=actors" className="section-link">{t('trending.actorsAll')}</Link>} />
              <ol className="heat-list">
                {heat.map(h => (
                  <li key={h.performer.id}>
                    <Link to={`/performers/${encodeURIComponent(h.performer.slug)}`} className="heat-row" state={{slot: `trending:${period}:heat:r${h.rank}`}}>
                      <span className={h.rank <= 3 ? 'rank-num rank-top' : 'rank-num'}>{h.rank}</span>
                      <Avatar seed={h.performer.slug} name={performerName(h.performer)} size={40} />
                      <span className="heat-name">{performerName(h.performer)}</span>
                      <MovementBadge movement={h.movement} delta={h.delta} />
                    </Link>
                  </li>
                ))}
              </ol>
            </div>
          )}
          <HotSearches variant="side" />
        </aside>
      )}
    </div>
  );
}

function TrendingRow({row, period}: {row: Row; period: Period}) {
  const t = useT();
  const it = row.release!;
  const stats = [row.plays > 0 && t('trending.plays', {n: formatCompact(row.plays)}), row.completion > 0 && t('trending.completion', {n: Math.round(row.completion * 100)})]
    .filter(Boolean)
    .join(' · ');
  return (
    <li>
      <Link
        to={`/releases/${encodeURIComponent(it.slug)}`}
        className={row.rank <= 3 ? 'trending-row is-top' : 'trending-row'}
        state={{slot: `trending:${period}:r${row.rank}`, reason: it.reason.code}}
      >
        <span className={row.rank <= 3 ? 'rank-num rank-big rank-top' : 'rank-num rank-big'} aria-label={t('trending.rank', {n: row.rank})}>
          {row.rank}
        </span>
        <Thumb seed={it.slug} badge={it.runtime_minutes ? formatRuntime(it.runtime_minutes) : undefined} className="thumb-trending" />
        <span className="trending-text">
          <span className="card-title">{recTitle(it)}</span>
          <span className="card-meta">{[performerName(it.performer), it.maker_name, formatDate(it.release_date)].filter(Boolean).join(' · ')}</span>
          {stats ? <span className="card-meta">{stats}</span> : <CardReason text={reasonText(it.reason, it)} />}
        </span>
        <MovementBadge movement={row.movement} delta={row.delta} />
      </Link>
    </li>
  );
}
