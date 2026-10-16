import {useState, type PointerEvent} from 'react';
import {Link} from 'react-router';
import {AdminNotice, NotDeployed, fmtNum, fmtTime} from './AdminUI';
import {isMissingEndpoint} from '../lib/api';
import {getSiteMetrics, type SiteDay, type SiteKPI} from '../lib/adminDataApi';
import {useAdminLoad} from '../lib/adminContext';
import '../styles/admin-data.css';

// AdminDashboard 播放 / 日活 / 时长 / 完播 (PRD 11 A1): GET admin/site-metrics,
// the daily aggregates the hourly site_metrics job keeps in
// site_daily_metrics. Each card shows the last closed UTC day, its change
// against the day before and against the 7-day average, and a sparkline of
// the chosen range (today, still counting, drawn dashed). Raw events are kept
// 90 days; the daily rows are kept long-term.

type MetricKey = 'plays' | 'dau' | 'watch_seconds' | 'completion_rate';

const fmtHours = (s: number | null | undefined) => {
  if (s === null || s === undefined) return '—';
  const h = s / 3600;
  return h >= 100 ? `${fmtNum(Math.round(h))} 小时` : `${(Math.round(h * 10) / 10).toLocaleString('zh-CN')} 小时`;
};
const fmtPct = (v: number | null | undefined) => (v === null || v === undefined ? '—' : `${(Math.round(v * 1000) / 10).toFixed(1)}%`);

const METRICS: {key: MetricKey; label: string; fmt: (v: number | null | undefined) => string; hint: string}[] = [
  {key: 'plays', label: '播放', fmt: fmtNum, hint: '当天发起的播放（play_request 去重）'},
  {key: 'dau', label: '日活', fmt: fmtNum, hint: '有任何事件的访客：登录用户，否则设备；未同意分析 Cookie 的访客按会话计'},
  {key: 'watch_seconds', label: '观看时长', fmt: fmtHours, hint: '每次播放的观看秒数（play_end，否则到最后一次心跳），单次最多 6 小时'},
  {key: 'completion_rate', label: '完播率', fmt: fmtPct, hint: '看完（结束原因 completed 或进度 100%）的播放 / 播放'},
];

const dayValue = (d: SiteDay, k: MetricKey): number | null => (k === 'completion_rate' ? d.completion_rate : d[k]);

function Delta({v, label}: {v: number | null; label: string}) {
  if (v === null) return <span className="adm-delta">{label} —</span>;
  const pct = Math.round(v * 1000) / 10;
  const tone = pct > 0 ? 'up' : pct < 0 ? 'down' : '';
  return (
    <span className={`adm-delta ${tone}`}>
      {label} {pct > 0 ? '▲' : pct < 0 ? '▼' : ''}
      {Math.abs(pct).toFixed(1)}%
    </span>
  );
}

function Sparkline({series, metric, fmt}: {series: SiteDay[]; metric: MetricKey; fmt: (v: number | null) => string}) {
  const [hover, setHover] = useState<number | null>(null);
  const W = 200;
  const H = 40;
  const pts = series.map(d => dayValue(d, metric));
  const max = Math.max(0, ...pts.map(v => v ?? 0));
  const top = max > 0 ? max : 1;
  const x = (i: number) => (series.length <= 1 ? W / 2 : (i / (series.length - 1)) * W);
  const y = (v: number) => H - 3 - (v / top) * (H - 6);
  const closed = pts.map((v, i) => ({v, i})).filter(p => p.v !== null && !series[p.i].partial) as {v: number; i: number}[];
  const path = closed.map((p, j) => `${j === 0 ? 'M' : 'L'}${x(p.i).toFixed(1)} ${y(p.v).toFixed(1)}`).join(' ');
  const lastClosed = closed[closed.length - 1];
  const todayIdx = series.findIndex(d => d.partial);
  const todayV = todayIdx >= 0 ? pts[todayIdx] : null;
  const pick = (e: PointerEvent<SVGSVGElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    if (r.width <= 0 || series.length === 0) return;
    setHover(Math.max(0, Math.min(series.length - 1, Math.round(((e.clientX - r.left) / r.width) * (series.length - 1)))));
  };
  const h = hover !== null ? series[hover] : null;
  return (
    <div className="adm-spark">
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img" aria-label={`${series.length} 天走势`} onPointerMove={pick} onPointerDown={pick} onPointerLeave={() => setHover(null)}>
        <path d={path} className="adm-spark-line" vectorEffect="non-scaling-stroke" />
        {lastClosed && todayV !== null && (
          <path d={`M${x(lastClosed.i).toFixed(1)} ${y(lastClosed.v).toFixed(1)} L${x(todayIdx).toFixed(1)} ${y(todayV).toFixed(1)}`} className="adm-spark-line partial" vectorEffect="non-scaling-stroke" />
        )}
        {hover !== null && pts[hover] !== null && (
          <>
            <line x1={x(hover)} x2={x(hover)} y1={0} y2={H} className="adm-curve-cursor" vectorEffect="non-scaling-stroke" />
            <circle cx={x(hover)} cy={y(pts[hover] as number)} r={2.5} className="adm-spark-dot" vectorEffect="non-scaling-stroke" />
          </>
        )}
      </svg>
      <span className="adm-spark-readout mono">{h ? `${h.day}${h.partial ? '（今天，统计中）' : ''} · ${fmt(dayValue(h, metric))}` : ' '}</span>
    </div>
  );
}

export function AdminSiteKPIs() {
  const [days, setDays] = useState(30);
  const load = useAdminLoad(signal => getSiteMetrics(days, signal), [days]);
  const m = load.data;
  if (load.failure && isMissingEndpoint(load.failure)) return <NotDeployed what="全站指标接口（admin/site-metrics）" />;
  const empty = m && m.series.length === 0;
  const lastComputed = m?.series.reduce<string | null>((acc, d) => (!acc || d.computed_at > acc ? d.computed_at : acc), null);
  return (
    <section className="adm-card" aria-label="播放与日活">
      <div className="adm-card-head">
        <div>
          <h2>播放与日活</h2>
          <p className="adm-field-hint">
            {m ? `${m.latest_day}（UTC，最近一个完整日）与前一天、前 7 天平均相比` : '按 UTC 日统计'}
            {lastComputed ? ` · 更新于 ${fmtTime(lastComputed)}` : ''}
          </p>
        </div>
        <div className="adm-chips" role="group" aria-label="范围">
          {[30, 90, 365].map(n => (
            <button key={n} type="button" className="adm-chip" aria-pressed={days === n} onClick={() => setDays(n)}>
              {n} 天
            </button>
          ))}
        </div>
      </div>
      <AdminNotice error={load.error} />
      {empty && (
        <p className="muted">
          还没有每日指标：任务 site_metrics 每小时运行，首次运行会回填事件保留期（{m.retention_days} 天）内的数据。可以在 <Link to="/admin/jobs">夜间任务</Link> 立即运行。
        </p>
      )}
      {m && !empty && (
        <div className="adm-kpis">
          {METRICS.map(def => {
            const k: SiteKPI = m.kpis[def.key];
            return (
              <div key={def.key} className="adm-kpi" title={def.hint}>
                <span className="adm-kpi-label">{def.label}</span>
                <span className="adm-kpi-value">{def.fmt(k.value)}</span>
                <span className="adm-kpi-sub adm-deltas">
                  <Delta v={k.dod} label="日环比" />
                  <Delta v={k.vs_7d} label="较 7 日均" />
                </span>
                <Sparkline series={m.series} metric={def.key} fmt={def.fmt} />
              </div>
            );
          })}
        </div>
      )}
      {m && !empty && <p className="adm-field-hint">原始事件保留 {m.retention_days} 天，每日汇总长期保存；超过保留期的日子不再重算。</p>}
    </section>
  );
}
