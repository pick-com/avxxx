import {useState} from 'react';
import {Link} from '../lib/nav';
import {PerformerChip} from '../components/Cards';
import {Icon} from '../components/Icon';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {getWatchStats, WATCH_STATS_RANGES, type WatchStats, type WatchStatsRange} from '../lib/api';
import {useAuth} from '../lib/auth';
import {formatCount, formatDate, timeAgo} from '../lib/format';
import {t, useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {useAsync} from '../lib/useAsync';
import '../styles/library.css';
import '../styles/stats.css';

export const meta = () => [{title: t('title.meStats')}];

// WatchStats (PRD 20) on GET /api/me/watch-stats: stat cards, the daily bar
// chart, the weekday × hour heatmap, top performers and genres, and the
// streak badge. Only the owner sees it; the page renders on the server like
// the other /me pages and asks signed-out visitors to log in.

const RANGE_LABEL: Record<WatchStatsRange, MessageKey> = {
  '7d': 'meStats.range7d',
  '30d': 'meStats.range30d',
  '365d': 'meStats.range365d',
};

/** Heatmap rows, Monday first (the API's weekday order). */
const WEEKDAYS: MessageKey[] = ['meStats.wd0', 'meStats.wd1', 'meStats.wd2', 'meStats.wd3', 'meStats.wd4', 'meStats.wd5', 'meStats.wd6'];

/** 2 小时 5 分钟 / 12 分钟. */
function formatWatchTime(seconds: number): string {
  const minutes = Math.round(Math.max(0, seconds) / 60);
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return h > 0 ? t('meStats.hoursMinutes', {h: formatCount(h), m}) : t('meStats.minutes', {m});
}

export default function MeStats() {
  const t = useT();
  const {status} = useAuth();
  const [range, setRange] = useState<WatchStatsRange>('7d');
  const stats = useAsync(signal => getWatchStats(range, signal), [status, range], status === 'user');

  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="stats" title={t('meStats.anonTitle')} body={t('meStats.anonBody')} actions={[{to: '/login?next=%2Fme%2Fstats', label: t('auth.login')}]} />
      </div>
    );
  }

  const data = stats.data;
  return (
    <div className="page stats-page">
      <header className="stats-head">
        <div>
          <h1 className="page-title">{t('meStats.title')}</h1>
          <p className="muted stats-privacy">{t('meStats.privacy')}</p>
        </div>
        {data?.has_data && <StreakBadge streak={data.streak} />}
      </header>
      <div className="segmented" role="radiogroup" aria-label={t('meStats.range')}>
        {WATCH_STATS_RANGES.map(r => (
          <button key={r} type="button" role="radio" aria-checked={range === r} className="segmented-item" onClick={() => setRange(r)}>
            {t(RANGE_LABEL[r])}
          </button>
        ))}
      </div>
      {stats.error ? (
        <ErrorBanner error={stats.error} onRetry={stats.reload} />
      ) : !data ? (
        (status === 'unknown' || stats.slow) && <CardGridSkeleton count={3} variant="row" />
      ) : !data.has_data ? (
        <EmptyState icon="stats" title={t('meStats.emptyTitle')} body={t('meStats.emptyBody')} actions={[{to: '/discover', label: t('meStats.browse')}]} />
      ) : (
        <StatsBody data={data} />
      )}
    </div>
  );
}

function StreakBadge({streak}: {streak: WatchStats['streak']}) {
  const t = useT();
  return (
    <div className={`stats-streak${streak.current > 0 ? ' on' : ''}`}>
      <Icon name="trending" size={18} />
      <span>
        <strong>{streak.current > 0 ? t('meStats.streak', {n: streak.current}) : t('meStats.streakNone')}</strong>
        {streak.longest > 0 && <span className="stats-streak-sub">{t('meStats.streakLongest', {n: streak.longest})}</span>}
      </span>
    </div>
  );
}

function StatsBody({data}: {data: WatchStats}) {
  const t = useT();
  const {totals} = data;
  return (
    <>
      <section className="stats-cards" aria-label={t('meStats.title')}>
        <StatCard label={t('meStats.watchTime')} value={formatWatchTime(totals.seconds)} sub={t('meStats.plays', {n: formatCount(totals.plays)})} />
        <StatCard label={t('meStats.finished')} value={formatCount(totals.finished)} sub={t('meStats.titlesSub', {n: formatCount(totals.titles)})} />
        <StatCard
          label={t('meStats.completionRate')}
          value={`${Math.round(totals.completion_rate * 100)}%`}
          sub={t('meStats.activeDays', {n: formatCount(totals.active_days)})}
        />
      </section>

      <section className="shelf">
        <h2 className="section-title">{t('meStats.daily')}</h2>
        <DailyBars daily={data.daily} />
      </section>

      <section className="shelf">
        <h2 className="section-title">{t('meStats.heatmap')}</h2>
        <Heatmap cells={data.heatmap} />
      </section>

      <div className="stats-tops">
        <section className="shelf">
          <h2 className="section-title">{t('meStats.topPerformers')}</h2>
          {data.performers.length === 0 ? (
            <p className="muted">{t('meStats.noneYet')}</p>
          ) : (
            <div className="stats-performers">
              {data.performers.map(p => (
                <PerformerChip key={p.slug} slug={p.slug} name={p.name} size={64} portrait={p.portrait} sub={formatWatchTime(p.seconds)} />
              ))}
            </div>
          )}
        </section>
        <section className="shelf">
          <h2 className="section-title">{t('meStats.topGenres')}</h2>
          {data.genres.length === 0 ? (
            <p className="muted">{t('meStats.noneYet')}</p>
          ) : (
            <ol className="stats-genres">
              {data.genres.map(g => (
                <li key={g.slug}>
                  <Link to={`/discover?tag=${encodeURIComponent(g.slug)}`} className="stats-genre">
                    <span className="stats-genre-name">#{g.name}</span>
                    <span className="stats-genre-bar" aria-hidden="true">
                      <span style={{width: `${Math.max(4, Math.round((g.seconds / data.genres[0].seconds) * 100))}%`}} />
                    </span>
                    <span className="stats-genre-time">{formatWatchTime(g.seconds)}</span>
                  </Link>
                </li>
              ))}
            </ol>
          )}
        </section>
      </div>

      <p className="muted stats-foot">
        {t('meStats.tzNote', {tz: data.tz})}
        {data.updated_at && <> · {t('meStats.updated', {time: timeAgo(data.updated_at)})}</>}
      </p>
    </>
  );
}

function StatCard({label, value, sub}: {label: string; value: string; sub: string}) {
  return (
    <div className="stats-card">
      <span className="stats-card-label">{label}</span>
      <strong className="stats-card-value">{value}</strong>
      <span className="stats-card-sub">{sub}</span>
    </div>
  );
}

/** Inline-SVG bar per day; the tallest day fills the chart. */
function DailyBars({daily}: {daily: WatchStats['daily']}) {
  const t = useT();
  const height = 120;
  const slot = daily.length > 60 ? 3 : daily.length > 14 ? 10 : 28;
  const gap = slot >= 28 ? Math.round(slot * 0.45) : slot >= 10 ? Math.round(slot * 0.3) : 1;
  const width = Math.max(1, daily.length * slot);
  const max = Math.max(1, ...daily.map(d => d.seconds));
  return (
    <figure className="stats-chart">
      <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" role="img" aria-label={t('meStats.dailyLabel')} className="stats-bars">
        <line x1={0} y1={height - 0.5} x2={width} y2={height - 0.5} className="stats-axis" />
        {daily.map((d, i) => {
          const h = d.seconds > 0 ? Math.max(2, Math.round((d.seconds / max) * (height - 4))) : 0;
          return (
            <rect key={d.day} x={i * slot + gap / 2} y={height - h} width={slot - gap} height={h} rx={slot >= 10 ? 2 : 0} className="stats-bar">
              <title>{t('meStats.barTitle', {day: formatDate(d.day), time: formatWatchTime(d.seconds)})}</title>
            </rect>
          );
        })}
      </svg>
      <figcaption className="stats-chart-x">
        <span>{formatDate(daily[0]?.day)}</span>
        <span>{formatDate(daily[daily.length - 1]?.day)}</span>
      </figcaption>
    </figure>
  );
}

/** 7 × 24 heatmap: rows Monday … Sunday, columns local hours. */
function Heatmap({cells}: {cells: number[][]}) {
  const t = useT();
  const cell = 14;
  const pad = 2;
  const left = 36;
  const top = 16;
  const max = Math.max(1, ...cells.flat());
  const width = left + 24 * cell;
  const height = top + 7 * cell;
  return (
    <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label={t('meStats.heatmapLabel')} className="stats-heat">
      {[0, 6, 12, 18].map(h => (
        <text key={h} x={left + h * cell} y={10} className="stats-heat-label">
          {h}
        </text>
      ))}
      {cells.map((row, w) => (
        <g key={w}>
          <text x={0} y={top + w * cell + cell - 4} className="stats-heat-label">
            {t(WEEKDAYS[w])}
          </text>
          {row.map((v, h) => (
            <rect
              key={h}
              x={left + h * cell + pad / 2}
              y={top + w * cell + pad / 2}
              width={cell - pad}
              height={cell - pad}
              rx={2}
              className="stats-heat-cell"
              style={{fillOpacity: v > 0 ? 0.2 + 0.8 * (v / max) : 1}}
              data-empty={v > 0 ? undefined : ''}
            >
              <title>{t('meStats.heatmapCell', {day: t(WEEKDAYS[w]), hour: h, time: formatWatchTime(v)})}</title>
            </rect>
          ))}
        </g>
      ))}
    </svg>
  );
}
