import type {ReactNode} from 'react';
import {BAND_LABELS} from '../lib/adminApi';

/** Page title row of an admin workbench. */
export function AdminHeader({eyebrow, title, sub, actions}: {eyebrow?: string; title: ReactNode; sub?: ReactNode; actions?: ReactNode}) {
  return (
    <header className="adm-head">
      <div className="adm-head-text">
        {eyebrow && <p className="eyebrow">{eyebrow}</p>}
        <h1>{title}</h1>
        {sub && <p className="adm-sub">{sub}</p>}
      </div>
      {actions && <div className="adm-head-actions">{actions}</div>}
    </header>
  );
}

/** Inline result of the last action: an error or a short confirmation. */
export function AdminNotice({error, notice, onClose}: {error?: string; notice?: string; onClose?: () => void}) {
  if (!error && !notice) return null;
  return (
    <div className={`adm-notice ${error ? 'adm-notice-err' : 'adm-notice-ok'}`} role={error ? 'alert' : 'status'}>
      <span>{error || notice}</span>
      {onClose && (
        <button type="button" className="adm-link" onClick={onClose}>
          关闭
        </button>
      )}
    </div>
  );
}

export type Tone = 'red' | 'amber' | 'green' | 'blue' | 'plain' | 'purple';

export function Pill({tone = 'plain', children, title}: {tone?: Tone; children: ReactNode; title?: string}) {
  return (
    <span className={`adm-pill adm-pill-${tone}`} title={title}>
      {children}
    </span>
  );
}

export const bandTone = (band?: string | null): Tone =>
  band === 'severe' ? 'red' : band === 'degraded' ? 'amber' : band === 'excellent' ? 'green' : band === 'healthy' ? 'plain' : 'blue';

export function BandPill({band, score}: {band?: string | null; score?: number | null}) {
  if (!band) return <Pill>—</Pill>;
  return (
    <Pill tone={bandTone(band)}>
      {score !== null && score !== undefined && band !== 'insufficient_data' ? `${score} · ` : ''}
      {BAND_LABELS[band] ?? band}
    </Pill>
  );
}

export const statusTone = (s: string): Tone =>
  s === 'published' || s === 'active' || s === 'reviewed' || s === 'ok'
    ? 'green'
    : s === 'held_review' || s === 'disabled' || s === 'failed' || s === 'missing'
      ? 'red'
      : s === 'degraded' || s === 'upcoming' || s === 'stale' || s === 'issues' || s === 'machine'
        ? 'amber'
        : s === 'draft'
          ? 'blue'
          : 'plain';

const dtf = new Intl.DateTimeFormat('zh-CN', {year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false});

export function fmtTime(v?: string | null): string {
  if (!v) return '—';
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? v : dtf.format(d);
}

export function fmtDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  return m < 60 ? `${m}m${s % 60 ? ` ${s % 60}s` : ''}` : `${Math.floor(m / 60)}h ${m % 60}m`;
}

export const fmtNum = (n?: number | null) => (n === null || n === undefined ? '—' : n.toLocaleString('zh-CN'));

export function jsonText(v: unknown): string {
  if (v === null || v === undefined) return '—';
  if (typeof v === 'string') return v;
  return JSON.stringify(v);
}

/** "加载更多" for cursor lists. */
export function MoreButton({next, busy, onMore}: {next: string | null; busy?: boolean; onMore: () => void}) {
  if (!next) return null;
  return (
    <div className="adm-more">
      <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={onMore}>
        {busy ? '加载中…' : '加载更多'}
      </button>
    </div>
  );
}

export function EmptyRow({cols, children}: {cols: number; children: ReactNode}) {
  return (
    <tr>
      <td colSpan={cols} className="adm-empty">
        {children}
      </td>
    </tr>
  );
}

/** Shown when the page's backend routes 404 (not deployed yet). */
export function NotDeployed({what}: {what: string}) {
  return (
    <div className="adm-notice adm-notice-warn" role="status">
      后端未部署：{what}。页面以只读方式显示可用数据。
    </div>
  );
}
