import {useState, type FormEvent} from 'react';
import {useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, MoreButton, Pill, fmtTime, jsonText} from '../components/AdminUI';
import {useAdminLoad} from '../lib/adminContext';
import {getAuditLog, type AuditEntry, type AuditQuery} from '../lib/adminUsersApi';
import '../styles/admin-users.css';

export const meta = () => [{title: 'REEL · 操作日志'}];

// Admin op log (AdminUsers 操作日志, admin_audit_log): every admin write,
// every break-glass request, reads of personal data, sign-ins and failed
// sign-ins, with the route, entity, result and a keyed hash of the client IP
// (LOG_HASH_KEY; the address itself is never stored). Searchable; field
// changes themselves stay in review_events.

const FIELDS: {key: keyof AuditQuery; label: string; placeholder: string}[] = [
  {key: 'q', label: '关键词', placeholder: '路由、动作、实体 ID…'},
  {key: 'actor', label: '操作人', placeholder: '用户名或 token:标签'},
  {key: 'entity_type', label: '实体类型', placeholder: 'releases、admin-users…'},
  {key: 'entity_id', label: '实体 ID', placeholder: ''},
  {key: 'from', label: '从', placeholder: 'YYYY-MM-DD'},
  {key: 'to', label: '到', placeholder: 'YYYY-MM-DD'},
];

const AUTH: Record<AuditEntry['auth'], string> = {session: '账号', token: '紧急令牌', anonymous: '未登录'};

export default function AdminOplogPage() {
  const [params, setParams] = useSearchParams();
  const query: AuditQuery = {};
  for (const f of [...FIELDS.map(f => f.key), 'auth' as const]) {
    const v = params.get(f);
    if (v) query[f] = v;
  }
  const key = params.toString();
  const [draft, setDraft] = useState<AuditQuery>(query);
  const list = useAdminLoad(signal => getAuditLog(query, signal), [key]);
  const [extra, setExtra] = useState<{items: AuditEntry[]; next: string | null; key: string} | null>(null);
  const [moreBusy, setMoreBusy] = useState(false);
  const [moreError, setMoreError] = useState('');

  const more_ = extra?.key === key ? extra : null;
  const items = [...(list.data?.items ?? []), ...(more_?.items ?? [])];
  const next = more_ ? more_.next : (list.data?.next_cursor ?? null);

  const search = (event: FormEvent) => {
    event.preventDefault();
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(draft)) if (v && String(v).trim()) p.set(k, String(v).trim());
    setParams(p, {replace: true});
  };
  const more = async () => {
    if (!next) return;
    setMoreBusy(true);
    try {
      const p = await getAuditLog({...query, cursor: next});
      setExtra({items: [...(more_?.items ?? []), ...p.items], next: p.next_cursor, key});
    } catch (cause) {
      setMoreError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setMoreBusy(false);
    }
  };
  const filterBy = (k: keyof AuditQuery, v: string) => {
    const p = new URLSearchParams(params);
    p.set(k, v);
    setDraft(d => ({...d, [k]: v}));
    setParams(p, {replace: true});
  };

  return (
    <>
      <AdminHeader eyebrow="ADMIN · OP LOG" title="操作日志" sub="谁、在什么时候、对哪个对象做了什么。IP 只保存带密钥的摘要。" />
      <AdminNotice error={list.error || moreError} />
      <section className="adm-card">
        <form className="adm-oplog-filters" onSubmit={search}>
          {FIELDS.map(f => (
            <label key={f.key} className="adm-field">
              {f.label}
              <input value={draft[f.key] ?? ''} placeholder={f.placeholder} onChange={e => setDraft(d => ({...d, [f.key]: e.target.value}))} />
            </label>
          ))}
          <label className="adm-field">
            身份
            <select value={draft.auth ?? ''} onChange={e => setDraft(d => ({...d, auth: e.target.value}))}>
              <option value="">全部</option>
              <option value="session">账号</option>
              <option value="token">紧急令牌</option>
              <option value="anonymous">未登录（登录失败）</option>
            </select>
          </label>
          <button type="submit" className="btn btn-secondary btn-sm">
            搜索
          </button>
        </form>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>时间</th>
                <th>操作人</th>
                <th>动作 / 路由</th>
                <th>对象</th>
                <th>结果</th>
                <th>IP 摘要</th>
              </tr>
            </thead>
            <tbody>
              {list.data && items.length === 0 && <EmptyRow cols={6}>没有匹配的记录</EmptyRow>}
              {items.map(e => (
                <tr key={e.id}>
                  <td>{fmtTime(e.created_at)}</td>
                  <td>
                    <button type="button" className="adm-link" onClick={() => filterBy('actor', e.actor)}>
                      {e.actor}
                    </button>
                    <div className="adm-source-meta">{AUTH[e.auth]}</div>
                  </td>
                  <td className="wrap">
                    {e.action}
                    <div className="adm-source-meta mono">{e.route || e.path}</div>
                    {e.approval_id && (
                      <div className="adm-source-meta">
                        审批 #{e.approval_id}
                        {typeof e.detail?.approved_by === 'string' && ` · 批准人 ${e.detail.approved_by}`}
                      </div>
                    )}
                    {Object.keys(e.detail ?? {}).filter(k => k !== 'approved_by').length > 0 && (
                      <details>
                        <summary className="adm-source-meta">详情</summary>
                        <pre className="adm-json">{jsonText(e.detail)}</pre>
                      </details>
                    )}
                  </td>
                  <td>
                    {e.entity_type ? (
                      <button type="button" className="adm-link" onClick={() => (filterBy('entity_type', e.entity_type), e.entity_id && filterBy('entity_id', e.entity_id))}>
                        {e.entity_type}
                        {e.entity_id && ` #${e.entity_id}`}
                      </button>
                    ) : (
                      '—'
                    )}
                  </td>
                  <td>
                    <Pill tone={e.status >= 400 ? 'red' : e.status === 202 ? 'amber' : 'green'}>{e.status || '—'}</Pill>
                  </td>
                  <td className="mono">{e.ip_hash || '—'}</td>
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
