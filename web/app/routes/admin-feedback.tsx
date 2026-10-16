import {useState} from 'react';
import {useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, MoreButton, NotDeployed, Pill, fmtTime, type Tone} from '../components/AdminUI';
import {isMissingEndpoint} from '../lib/api';
import {FEEDBACK_STATUSES, getAdminFeedback, setFeedbackStatus, type AdminFeedback, type FeedbackStatus} from '../lib/adminComplianceApi';
import {FEEDBACK_CATEGORIES} from '../lib/compliance';
import {useAdminAction, useAdminLoad} from '../lib/adminContext';
import '../styles/admin-compliance.css';

export const meta = () => [{title: 'REEL · 用户反馈'}];

// 用户反馈 (tech plan 3.13 feedback): submissions from /feedback, newest
// first. Contact emails are shown only here; status changes write
// review_events (entity_type 'feedback') with the 操作人.

const STATUS_LABELS: Record<FeedbackStatus, string> = {open: '待处理', in_progress: '处理中', resolved: '已解决', dismissed: '不处理'};
const STATUS_TONES: Record<FeedbackStatus, Tone> = {open: 'amber', in_progress: 'blue', resolved: 'green', dismissed: 'plain'};
const CATEGORY_LABELS: Record<string, string> = {
  bug: '页面出错',
  playback: '播放问题',
  content: '内容信息有误',
  account: '账号与登录',
  suggestion: '功能建议',
  partnership: '合作',
  other: '其他',
};

export default function AdminFeedbackPage() {
  const [params, setParams] = useSearchParams();
  const status = params.get('status') ?? 'open';
  const category = params.get('category') ?? '';
  const list = useAdminLoad(() => getAdminFeedback({status: status === 'all' ? '' : status, category, limit: 50}), [status, category]);
  const act = useAdminAction();
  const [extra, setExtra] = useState<{items: AdminFeedback[]; next: string | null} | null>(null);
  const [moreBusy, setMoreBusy] = useState(false);
  const [notes, setNotes] = useState<Record<number, string>>({});

  const items = [...(list.data?.items ?? []), ...(extra?.items ?? [])];
  const next = extra ? extra.next : (list.data?.next_cursor ?? null);
  const reload = () => {
    setExtra(null);
    list.reload();
  };
  const more = async () => {
    if (!next) return;
    setMoreBusy(true);
    try {
      const p = await getAdminFeedback({status: status === 'all' ? '' : status, category, cursor: next, limit: 50});
      setExtra(prev => ({items: [...(prev?.items ?? []), ...p.items], next: p.next_cursor}));
    } catch (cause) {
      act.setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setMoreBusy(false);
    }
  };
  const setFilter = (k: string, v: string) => {
    const p = new URLSearchParams(params);
    if (v) p.set(k, v);
    else p.delete(k);
    setExtra(null);
    setParams(p, {replace: true});
  };
  const decide = (f: AdminFeedback, to: FeedbackStatus) =>
    void act.attempt(`fb-${f.id}`, () => setFeedbackStatus(f.id, to, notes[f.id]), `#${f.id} → ${STATUS_LABELS[to]}`).then(v => v && reload());
  const missing = list.failure && isMissingEndpoint(list.failure);

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · FEEDBACK"
        title="用户反馈"
        sub={`来自页脚「反馈问题」。待处理 ${list.data?.open ?? '—'} 条（含处理中）。邮箱只用于回复该反馈。`}
        actions={
          <button type="button" className="btn btn-secondary btn-sm" disabled={list.busy} onClick={reload}>
            {list.busy ? '刷新中…' : '刷新'}
          </button>
        }
      />
      {missing ? <NotDeployed what="反馈接口（admin/feedback）" /> : <AdminNotice error={list.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />}
      <section className="adm-card">
        <div className="adm-toolbar">
          <div className="adm-seg" role="tablist" aria-label="状态">
            {[...FEEDBACK_STATUSES, 'all' as const].map(s => (
              <button key={s} type="button" role="tab" aria-selected={status === s} className={`adm-seg-item ${status === s ? 'on' : ''}`} onClick={() => setFilter('status', s === 'open' ? '' : s)}>
                {s === 'all' ? '全部' : STATUS_LABELS[s]}
              </button>
            ))}
          </div>
          <label className="adm-field">
            类型
            <select value={category} onChange={e => setFilter('category', e.target.value)}>
              <option value="">全部</option>
              {FEEDBACK_CATEGORIES.map(c => (
                <option key={c} value={c}>
                  {CATEGORY_LABELS[c] ?? c}
                </option>
              ))}
            </select>
          </label>
        </div>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>时间</th>
                <th>类型</th>
                <th>内容</th>
                <th>联系 / 页面</th>
                <th>状态</th>
                <th>处理</th>
              </tr>
            </thead>
            <tbody>
              {list.data && items.length === 0 && <EmptyRow cols={6}>没有符合条件的反馈</EmptyRow>}
              {items.map(f => (
                <tr key={f.id}>
                  <td>
                    #{f.id}
                    <div className="adm-source-meta">{fmtTime(f.created_at)}</div>
                  </td>
                  <td>{CATEGORY_LABELS[f.category] ?? f.category}</td>
                  <td className="wrap" style={{maxWidth: 420}}>
                    <p className="adm-quote">{f.message}</p>
                    {f.admin_note && <div className="adm-source-meta">备注：{f.admin_note}</div>}
                  </td>
                  <td className="wrap">
                    {f.email ? <a href={`mailto:${f.email}`}>{f.email}</a> : <span className="muted">未留邮箱</span>}
                    {f.page_url && <div className="adm-source-meta mono">{f.page_url}</div>}
                    {f.lang && <div className="adm-source-meta">{f.lang}</div>}
                  </td>
                  <td>
                    <Pill tone={STATUS_TONES[f.status]}>{STATUS_LABELS[f.status] ?? f.status}</Pill>
                    {f.handled_by && (
                      <div className="adm-source-meta">
                        {f.handled_by} · {fmtTime(f.handled_at)}
                      </div>
                    )}
                  </td>
                  <td>
                    <div className="adm-row-actions">
                      <input className="adm-input" placeholder="备注（可空）" value={notes[f.id] ?? ''} onChange={e => setNotes(n => ({...n, [f.id]: e.target.value}))} />
                      {FEEDBACK_STATUSES.filter(s => s !== f.status).map(s => (
                        <button key={s} type="button" className="btn btn-secondary btn-sm" disabled={act.pending === `fb-${f.id}`} onClick={() => decide(f, s)}>
                          {STATUS_LABELS[s]}
                        </button>
                      ))}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <MoreButton next={next} busy={moreBusy} onMore={() => void more()} />
      </section>
    </>
  );
}
