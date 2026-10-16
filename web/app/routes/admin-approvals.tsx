import {useState} from 'react';
import {useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, Pill, fmtTime, jsonText, type Tone} from '../components/AdminUI';
import {useAdminAction, useAdminLoad} from '../lib/adminContext';
import {absoluteLink, approveRequest, getApprovals, rejectRequest, type Approval} from '../lib/adminUsersApi';
import '../styles/admin-users.css';

export const meta = () => [{title: 'REEL · 待审批'}];

// Dual approval (AdminUsers 双人审批): destructive requests (role changes,
// re-enabling an admin, 2FA resets, bulk rating removal) are stored when
// made and run only when a different admin with the same permission
// approves them. The request then runs as its requester; the op log records
// both. Requests expire after 72 hours.

const STATUSES: {id: string; label: string}[] = [
  {id: 'pending', label: '待审批'},
  {id: 'executed', label: '已执行'},
  {id: 'failed', label: '执行失败'},
  {id: 'rejected', label: '已驳回'},
  {id: 'cancelled', label: '已撤回'},
  {id: 'expired', label: '已过期'},
  {id: 'all', label: '全部'},
];
const TONES: Record<Approval['status'], Tone> = {
  pending: 'amber',
  executing: 'blue',
  executed: 'green',
  failed: 'red',
  rejected: 'plain',
  cancelled: 'plain',
  expired: 'plain',
};
const label = (s: string) => STATUSES.find(x => x.id === s)?.label ?? s;

export default function AdminApprovalsPage() {
  const [params, setParams] = useSearchParams();
  const status = params.get('status') ?? 'pending';
  const list = useAdminLoad(signal => getApprovals(status, signal), [status]);
  const act = useAdminAction();
  const [notes, setNotes] = useState<Record<number, string>>({});
  const [enroll, setEnroll] = useState('');

  const approve = async (a: Approval) => {
    if (!window.confirm(`批准并执行：${a.summary}？`)) return;
    const r = await act.attempt(`ok-${a.id}`, () => approveRequest(a.id), v => (v.result_status < 300 ? `#${a.id} 已执行` : `#${a.id} 执行失败（${v.result_status}）`));
    const url = (r?.result as {enroll_url?: string} | null)?.enroll_url;
    if (url) setEnroll(absoluteLink(url));
    list.reload();
  };
  const reject = async (a: Approval) => {
    if (await act.attempt(`no-${a.id}`, () => rejectRequest(a.id, notes[a.id] ?? ''), a.mine ? `#${a.id} 已撤回` : `#${a.id} 已驳回`)) list.reload();
  };

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · APPROVALS"
        title="待审批"
        sub="删除类和权限类操作由一位管理员发起、另一位管理员批准后执行。发起人不能批准自己的请求。"
        actions={
          <button type="button" className="btn btn-secondary btn-sm" disabled={list.busy} onClick={list.reload}>
            {list.busy ? '刷新中…' : '刷新'}
          </button>
        }
      />
      <AdminNotice error={list.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      {enroll && (
        <div className="adm-card adm-link-notice" role="status">
          <strong>新的启用链接（只显示这一次）</strong>
          <code className="mono adm-break">{enroll}</code>
          <div className="adm-row-actions">
            <button type="button" className="btn btn-secondary btn-sm" onClick={() => void navigator.clipboard?.writeText(enroll)}>
              复制链接
            </button>
            <button type="button" className="adm-link" onClick={() => setEnroll('')}>
              关闭
            </button>
          </div>
        </div>
      )}
      <section className="adm-card">
        <div className="adm-seg" role="tablist" aria-label="状态">
          {STATUSES.map(s => (
            <button key={s.id} type="button" role="tab" aria-selected={status === s.id} className={`adm-seg-item ${status === s.id ? 'on' : ''}`} onClick={() => setParams(s.id === 'pending' ? {} : {status: s.id}, {replace: true})}>
              {s.label}
            </button>
          ))}
        </div>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>请求</th>
                <th>内容</th>
                <th>发起</th>
                <th>状态</th>
                <th>处理</th>
              </tr>
            </thead>
            <tbody>
              {list.data && list.data.length === 0 && <EmptyRow cols={5}>没有{label(status)}的请求</EmptyRow>}
              {(list.data ?? []).map(a => (
                <tr key={a.id}>
                  <td className="wrap">
                    <strong>#{a.id} {a.summary}</strong>
                    <div className="adm-source-meta mono">
                      {a.method} {a.path}
                      {a.query && `?${a.query}`}
                    </div>
                  </td>
                  <td className="wrap" style={{maxWidth: 360}}>
                    {a.body !== undefined && a.body !== null ? <pre className="adm-json">{jsonText(a.body)}</pre> : <span className="muted">—</span>}
                  </td>
                  <td>
                    {a.requested_by_name}
                    <div className="adm-source-meta">{fmtTime(a.requested_at)}</div>
                    {a.status === 'pending' && <div className="adm-source-meta">到期 {fmtTime(a.expires_at)}</div>}
                  </td>
                  <td>
                    <Pill tone={TONES[a.status]}>{label(a.status)}</Pill>
                    {a.decided_by && (
                      <div className="adm-source-meta">
                        {a.decided_by} · {fmtTime(a.decided_at)}
                      </div>
                    )}
                    {a.result_status !== null && <div className="adm-source-meta">结果 {a.result_status}</div>}
                    {a.decision_note && <div className="adm-source-meta">备注：{a.decision_note}</div>}
                  </td>
                  <td>
                    {a.status === 'pending' && (a.can_approve || a.mine) ? (
                      <div className="adm-row-actions">
                        <input className="adm-input" placeholder="备注（可空）" value={notes[a.id] ?? ''} onChange={e => setNotes(n => ({...n, [a.id]: e.target.value}))} />
                        {a.can_approve && (
                          <button type="button" className="btn btn-primary btn-sm" disabled={act.pending === `ok-${a.id}`} onClick={() => void approve(a)}>
                            批准并执行
                          </button>
                        )}
                        <button type="button" className="btn btn-secondary btn-sm" disabled={act.pending === `no-${a.id}`} onClick={() => void reject(a)}>
                          {a.mine ? '撤回' : '驳回'}
                        </button>
                      </div>
                    ) : a.status === 'pending' ? (
                      <span className="muted">需要有该权限的另一位管理员</span>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}
