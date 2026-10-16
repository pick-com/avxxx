import {useMemo, useState, type FormEvent} from 'react';
import {AdminNotice, EmptyRow, NotDeployed, Pill, fmtTime, type Tone} from './AdminUI';
import {isMissingEndpoint} from '../lib/api';
import {useAdmin, useAdminAction, useAdminLoad} from '../lib/adminContext';
import {createServiceToken, getServiceTokens, revokeServiceToken, type ServiceToken} from '../lib/adminUsersApi';

// 服务令牌 (internal/api/service_tokens.go): scoped bearer tokens for scripts
// such as the daily SEO check, instead of the break-glass ADMIN_TOKEN. A
// super admin picks route groups (read / write) and / or single routes; the
// API refuses admin-account, op-log, approval and dual-approval routes. The
// token value is shown once.

const STATUS: Record<ServiceToken['status'], {label: string; tone: Tone}> = {
  active: {label: '有效', tone: 'green'},
  expired: {label: '已过期', tone: 'amber'},
  revoked: {label: '已撤销', tone: 'plain'},
};

const SEO_ROUTE = 'POST /api/admin/seo/report';
const empty = {name: '', description: '', days: 365, groups: {} as Record<string, '' | 'read' | 'write'>, routes: [] as string[]};

export function AdminServiceTokens() {
  const {can} = useAdmin();
  const list = useAdminLoad(signal => getServiceTokens(signal));
  const act = useAdminAction();
  const [draft, setDraft] = useState(empty);
  const [filter, setFilter] = useState('');
  const [secret, setSecret] = useState<{name: string; token: string} | null>(null);
  const writable = can('admin_users:write');

  const grantable = list.data?.grantable;
  const routes = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return (grantable?.routes ?? []).filter(r => !f || r.toLowerCase().includes(f)).slice(0, 80);
  }, [grantable, filter]);
  const scopes = [...Object.entries(draft.groups).filter(([, a]) => a).map(([g, a]) => `${g}:${a}`), ...draft.routes];

  const toggleRoute = (r: string) => setDraft(d => ({...d, routes: d.routes.includes(r) ? d.routes.filter(x => x !== r) : [...d.routes, r]}));
  const presetSEO = () => setDraft({...empty, name: 'seo-check', description: '每日 SEO 检查上报收录健康分（cron）', routes: [SEO_ROUTE]});

  const create = async (e: FormEvent) => {
    e.preventDefault();
    const r = await act.attempt(
      'create',
      () => createServiceToken({name: draft.name, description: draft.description, scopes, expires_in_days: draft.days}),
      v => `已创建服务令牌 ${v.service_token.name}`,
    );
    if (r) {
      setSecret({name: r.service_token.name, token: r.token});
      setDraft(empty);
      list.reload();
    }
  };
  const revoke = async (t: ServiceToken) => {
    if (!window.confirm(`撤销服务令牌 ${t.name}？使用它的脚本会立即收到 401。`)) return;
    if (await act.attempt(`rv-${t.id}`, () => revokeServiceToken(t.id), `${t.name} 已撤销`)) list.reload();
  };

  if (list.failure && isMissingEndpoint(list.failure)) return <NotDeployed what="服务令牌接口（admin/service-tokens）" />;
  return (
    <>
      <AdminNotice error={list.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      {secret && (
        <div className="adm-card adm-link-notice" role="status">
          <strong>服务令牌 {secret.name}（只显示这一次）</strong>
          <p className="muted">现在复制并放进 vault / 脚本环境变量（SEO 检查用 seo_report_token → SEO_REPORT_TOKEN）。库里只保存哈希，之后无法再查看。</p>
          <code className="mono adm-break">{secret.token}</code>
          <div className="adm-row-actions">
            <button type="button" className="btn btn-secondary btn-sm" onClick={() => void navigator.clipboard?.writeText(secret.token)}>
              复制令牌
            </button>
            <button type="button" className="adm-link" onClick={() => setSecret(null)}>
              我已保存，关闭
            </button>
          </div>
        </div>
      )}
      {writable && grantable && (
        <section className="adm-card">
          <div className="adm-card-head">
            <h2 className="adm-card-title">新建服务令牌</h2>
            <button type="button" className="btn btn-secondary btn-sm" onClick={presetSEO}>
              填入 SEO 检查预设
            </button>
          </div>
          <p className="adm-field-hint">
            服务令牌只能调用勾选的范围；后台账号、操作日志、待审批和需要双人审批的接口永远不能用。每次请求都记入操作日志（service:名称）。
          </p>
          <form onSubmit={create}>
            <div className="adm-form-grid">
              <label className="adm-field">
                名称（记入操作日志）
                <input value={draft.name} onChange={e => setDraft({...draft, name: e.target.value})} required pattern="[a-z0-9][a-z0-9._\-]{1,47}" placeholder="seo-check" />
              </label>
              <label className="adm-field">
                有效期（天）
                <input type="number" min={1} max={list.data?.max_days ?? 730} value={draft.days} onChange={e => setDraft({...draft, days: Number(e.target.value) || 0})} required />
              </label>
              <label className="adm-field span-all">
                用途说明
                <input value={draft.description} maxLength={200} onChange={e => setDraft({...draft, description: e.target.value})} />
              </label>
            </div>
            <fieldset className="adm-field">
              <legend>按路由组授权</legend>
              <div className="adm-form-grid">
                {grantable.groups.map(g => (
                  <label key={g.group} className="adm-field">
                    {g.label}
                    <select value={draft.groups[g.group] ?? ''} onChange={e => setDraft({...draft, groups: {...draft.groups, [g.group]: e.target.value as '' | 'read' | 'write'}})}>
                      <option value="">不授权</option>
                      <option value="read">只读</option>
                      <option value="write">读写</option>
                    </select>
                  </label>
                ))}
              </div>
            </fieldset>
            <fieldset className="adm-field">
              <legend>或按单个路由授权（已选 {draft.routes.length}）</legend>
              <input className="adm-input" placeholder="筛选路由，例如 seo" value={filter} onChange={e => setFilter(e.target.value)} />
              <div className="adm-roles" style={{maxHeight: 220, overflow: 'auto'}}>
                {[...new Set([...draft.routes, ...routes])].map(r => (
                  <label key={r} className="mono">
                    <input type="checkbox" checked={draft.routes.includes(r)} onChange={() => toggleRoute(r)} /> {r}
                  </label>
                ))}
              </div>
            </fieldset>
            <div className="adm-form-actions">
              <span className="muted">范围：{scopes.length ? scopes.join('、') : '未选择'}</span>
              <button type="submit" className="btn btn-primary btn-sm" disabled={act.pending === 'create' || scopes.length === 0}>
                创建并显示令牌
              </button>
            </div>
          </form>
        </section>
      )}
      <section className="adm-card">
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>名称</th>
                <th>范围</th>
                <th>状态</th>
                <th>最近使用</th>
                <th>创建</th>
                {writable && <th>操作</th>}
              </tr>
            </thead>
            <tbody>
              {list.data && list.data.items.length === 0 && <EmptyRow cols={6}>还没有服务令牌</EmptyRow>}
              {(list.data?.items ?? []).map(t => (
                <tr key={t.id}>
                  <td>
                    <strong>{t.name}</strong>
                    <div className="adm-source-meta mono">{t.token_prefix}…</div>
                    {t.description && <div className="adm-source-meta">{t.description}</div>}
                  </td>
                  <td className="wrap">
                    {t.scopes.map(s => (
                      <Pill key={s} tone="plain">
                        {s}
                      </Pill>
                    ))}
                  </td>
                  <td>
                    <Pill tone={STATUS[t.status].tone}>{STATUS[t.status].label}</Pill>
                    <div className="adm-source-meta">
                      {t.status === 'revoked' ? `${t.revoked_by ?? ''} 撤销于 ${fmtTime(t.revoked_at)}` : `到期 ${fmtTime(t.expires_at)}`}
                    </div>
                  </td>
                  <td>{fmtTime(t.last_used_at) || '从未使用'}</td>
                  <td>
                    {t.created_by}
                    <div className="adm-source-meta">{fmtTime(t.created_at)}</div>
                  </td>
                  {writable && (
                    <td>
                      {t.status !== 'revoked' && (
                        <button type="button" className="btn btn-danger btn-sm" disabled={act.pending === `rv-${t.id}`} onClick={() => void revoke(t)}>
                          撤销
                        </button>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}
