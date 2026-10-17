import {useState, type FormEvent} from 'react';
import {Link, useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, NotDeployed, Pill, fmtTime, type Tone} from '../components/AdminUI';
import {AdminServiceTokens} from '../components/AdminServiceTokens';
import {isMissingEndpoint} from '../lib/api';
import {useAdmin, useAdminAction, useAdminLoad} from '../lib/adminContext';
import {
  ROLES,
  absoluteLink,
  banSiteUser,
  disableAdmin,
  enableAdmin,
  getAdminUsers,
  getRoleMatrix,
  inviteAdmin,
  isApprovalPending,
  regenerateRecoveryCodes,
  reissueInvite,
  resetAdmin2FA,
  roleLabel,
  searchSiteUsers,
  setAdminRoles,
  unbanSiteUser,
  unmuteSiteUser,
  type AdminUser,
  type SiteUser,
} from '../lib/adminUsersApi';
import '../styles/admin-users.css';

export const meta = () => [{title: 'REEL · 用户与权限'}];

// AdminUsers (PRD 11, tech plan 3.6). Tabs:
//   后台账号  invite (enrollment link), roles, disable / enable, reset 2FA.
//             Role changes, re-enabling and 2FA resets need a second admin:
//             the API answers 202 and the request waits in /admin/approvals.
//   角色矩阵  the 7 roles × route groups (read / write), from GET admin/roles.
//   前台用户  site-user search with ban / unban and lifting mutes (处罚);
//             every change writes review_events and the op log.
//   我的账号  regenerate own recovery codes (needs a current TOTP code).

//   服务令牌  scoped tokens for scripts (SEO check), shown once (AdminServiceTokens).

type Tab = 'admins' | 'matrix' | 'site' | 'me' | 'service';
const TABS: {id: Tab; label: string; perm?: string}[] = [
  {id: 'admins', label: '后台账号', perm: 'admin_users:read'},
  {id: 'service', label: '服务令牌', perm: 'admin_users:read'},
  {id: 'matrix', label: '角色矩阵'},
  {id: 'site', label: '前台用户', perm: 'site_users:read'},
  {id: 'me', label: '我的账号'},
];

const STATUS: Record<AdminUser['status'], {label: string; tone: Tone}> = {
  active: {label: '正常', tone: 'green'},
  invited: {label: '待启用', tone: 'amber'},
  disabled: {label: '已停用', tone: 'plain'},
};

export default function AdminUsersPage() {
  const {can} = useAdmin();
  const [params, setParams] = useSearchParams();
  const visible = TABS.filter(t => !t.perm || can(t.perm));
  const requested = params.get('tab') as Tab | null;
  const tab: Tab = visible.some(t => t.id === requested) ? requested! : visible[0].id;

  return (
    <>
      <AdminHeader eyebrow="ADMIN · USERS" title="用户与权限" sub="后台账号与 7 种角色、前台用户检索与处罚。删除类与权限类操作需要另一位管理员审批。" />
      <div className="adm-seg adm-users-tabs" role="tablist" aria-label="分区">
        {visible.map(t => (
          <button key={t.id} type="button" role="tab" aria-selected={tab === t.id} className={`adm-seg-item ${tab === t.id ? 'on' : ''}`} onClick={() => setParams({tab: t.id}, {replace: true})}>
            {t.label}
          </button>
        ))}
      </div>
      {tab === 'admins' && <AdminAccounts />}
      {tab === 'matrix' && <RoleMatrixView />}
      {tab === 'site' && <SiteUsers />}
      {tab === 'me' && <MyAccount />}
      {tab === 'service' && <AdminServiceTokens />}
    </>
  );
}

/** Shows a one-time enrollment link with a copy button. */
function LinkNotice({link, onClose}: {link: string; onClose: () => void}) {
  const url = absoluteLink(link);
  return (
    <div className="adm-card adm-link-notice" role="status">
      <strong>启用链接（72 小时内有效，只显示这一次）</strong>
      <p className="muted">请通过可信渠道单独发给本人。对方打开后设置密码并绑定身份验证器。</p>
      <code className="mono adm-break">{url}</code>
      <div className="adm-row-actions">
        <button type="button" className="btn btn-secondary btn-sm" onClick={() => void navigator.clipboard?.writeText(url)}>
          复制链接
        </button>
        <button type="button" className="adm-link" onClick={onClose}>
          关闭
        </button>
      </div>
    </div>
  );
}

function AdminAccounts() {
  const {can, me} = useAdmin();
  const list = useAdminLoad(signal => getAdminUsers(signal));
  const act = useAdminAction();
  const [link, setLink] = useState('');
  const [draft, setDraft] = useState({username: '', email: '', display_name: '', roles: [] as string[]});
  const [editing, setEditing] = useState<number | null>(null);
  const [roleDraft, setRoleDraft] = useState<string[]>([]);
  const [reasons, setReasons] = useState<Record<number, string>>({});
  const writable = can('admin_users:write');

  const toggle = (roles: string[], r: string) => (roles.includes(r) ? roles.filter(x => x !== r) : [...roles, r]);
  const pendingText = '已提交审批，等待另一位管理员在「待审批」中批准';

  const invite = async (event: FormEvent) => {
    event.preventDefault();
    const r = await act.attempt('invite', () => inviteAdmin(draft), v => (isApprovalPending(v) ? pendingText : `已邀请 ${v.admin.username}`));
    if (r) {
      if (!isApprovalPending(r)) setLink(r.enroll_url);
      setDraft({username: '', email: '', display_name: '', roles: []});
      list.reload();
    }
  };
  const saveRoles = async (u: AdminUser) => {
    const r = await act.attempt(`roles-${u.id}`, () => setAdminRoles(u.id, roleDraft, reasons[u.id] ?? ''), v => (isApprovalPending(v) ? pendingText : `${u.username} 的角色已更新`));
    if (r) {
      setEditing(null);
      list.reload();
    }
  };
  const reset2FA = async (u: AdminUser) => {
    if (!window.confirm(`重置 ${u.username} 的两步验证？对方需要用新链接重新设置密码和验证器，现有会话全部失效。`)) return;
    const r = await act.attempt(`2fa-${u.id}`, () => resetAdmin2FA(u.id, reasons[u.id] ?? ''), v => (isApprovalPending(v) ? pendingText : '已重置'));
    if (r && !isApprovalPending(r)) setLink(r.enroll_url);
    list.reload();
  };
  const disable = async (u: AdminUser) => {
    if (!window.confirm(`停用 ${u.username}？对方会立即退出登录。`)) return;
    if (await act.attempt(`dis-${u.id}`, () => disableAdmin(u.id, reasons[u.id] ?? ''), `${u.username} 已停用`)) list.reload();
  };
  const enable = async (u: AdminUser) => {
    if (await act.attempt(`en-${u.id}`, () => enableAdmin(u.id, reasons[u.id] ?? ''), v => (isApprovalPending(v) ? pendingText : `${u.username} 已启用`))) list.reload();
  };
  const reinvite = async (u: AdminUser) => {
    const r = await act.attempt(`inv-${u.id}`, () => reissueInvite(u.id), v => (isApprovalPending(v) ? pendingText : '已生成新的启用链接'));
    if (r && !isApprovalPending(r)) setLink(r.enroll_url);
  };

  if (list.failure && isMissingEndpoint(list.failure)) return <NotDeployed what="后台账号接口（admin/admin-users）" />;
  return (
    <>
      <AdminNotice error={list.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      {link && <LinkNotice link={link} onClose={() => setLink('')} />}
      {writable && (
        <section className="adm-card">
          <h2 className="adm-card-title">邀请管理员</h2>
          <form className="adm-users-invite" onSubmit={invite}>
            <label className="adm-field">
              用户名（记入操作日志）
              <input value={draft.username} onChange={e => setDraft({...draft, username: e.target.value})} required pattern="[a-z0-9][a-z0-9._\-]{1,31}" placeholder="例如 nc" />
            </label>
            <label className="adm-field">
              邮箱
              <input type="email" value={draft.email} onChange={e => setDraft({...draft, email: e.target.value})} required />
            </label>
            <label className="adm-field">
              显示名
              <input value={draft.display_name} onChange={e => setDraft({...draft, display_name: e.target.value})} maxLength={64} />
            </label>
            <fieldset className="adm-roles">
              <legend>角色</legend>
              {ROLES.map(r => (
                <label key={r}>
                  <input type="checkbox" checked={draft.roles.includes(r)} onChange={() => setDraft({...draft, roles: toggle(draft.roles, r)})} /> {roleLabel(r)}
                </label>
              ))}
            </fieldset>
            <button type="submit" className="btn btn-primary btn-sm" disabled={act.pending === 'invite' || draft.roles.length === 0}>
              生成邀请链接
            </button>
          </form>
        </section>
      )}
      <section className="adm-card">
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>账号</th>
                <th>角色</th>
                <th>状态</th>
                <th>两步验证</th>
                <th>最近登录</th>
                {writable && <th>操作</th>}
              </tr>
            </thead>
            <tbody>
              {list.data && list.data.length === 0 && <EmptyRow cols={6}>还没有后台账号（用 admin create 创建第一个）</EmptyRow>}
              {(list.data ?? []).map(u => {
                const self = me?.auth === 'session' && me.admin.id === u.id;
                return (
                  <tr key={u.id}>
                    <td>
                      <strong>{u.username}</strong> {self && <Pill tone="blue">我</Pill>}
                      <div className="adm-source-meta">
                        {u.display_name && `${u.display_name} · `}
                        {u.email}
                      </div>
                    </td>
                    <td className="wrap">
                      {editing === u.id ? (
                        <fieldset className="adm-roles">
                          {ROLES.map(r => (
                            <label key={r}>
                              <input type="checkbox" checked={roleDraft.includes(r)} onChange={() => setRoleDraft(d => toggle(d, r))} /> {roleLabel(r)}
                            </label>
                          ))}
                        </fieldset>
                      ) : (
                        u.roles.map(r => (
                          <Pill key={r} tone={r === 'super_admin' ? 'purple' : 'plain'}>
                            {roleLabel(r)}
                          </Pill>
                        ))
                      )}
                    </td>
                    <td>
                      <Pill tone={STATUS[u.status].tone}>{STATUS[u.status].label}</Pill>
                      {u.locked_until && new Date(u.locked_until) > new Date() && <div className="adm-source-meta">锁定至 {fmtTime(u.locked_until)}</div>}
                      {u.status === 'invited' && u.invite_expires_at && <div className="adm-source-meta">链接到期 {fmtTime(u.invite_expires_at)}</div>}
                    </td>
                    <td>
                      {u.totp_enrolled ? '已绑定' : '未绑定'}
                      <div className="adm-source-meta">恢复码剩 {u.recovery_codes_left} 个</div>
                    </td>
                    <td>
                      {fmtTime(u.last_login_at) || '—'}
                      {u.active_sessions > 0 && <div className="adm-source-meta">{u.active_sessions} 个在线会话</div>}
                    </td>
                    {writable && (
                      <td>
                        {self ? (
                          <span className="muted">自己的账号由其他管理员管理</span>
                        ) : (
                          <div className="adm-row-actions">
                            <input
                              className="adm-input"
                              placeholder="原因（记入日志）"
                              value={reasons[u.id] ?? ''}
                              onChange={e => setReasons(x => ({...x, [u.id]: e.target.value}))}
                            />
                            {editing === u.id ? (
                              <>
                                <button type="button" className="btn btn-primary btn-sm" disabled={roleDraft.length === 0 || act.pending === `roles-${u.id}`} onClick={() => void saveRoles(u)}>
                                  提交角色
                                </button>
                                <button type="button" className="adm-link" onClick={() => setEditing(null)}>
                                  取消
                                </button>
                              </>
                            ) : (
                              <button type="button" className="btn btn-secondary btn-sm" onClick={() => (setEditing(u.id), setRoleDraft(u.roles))}>
                                改角色
                              </button>
                            )}
                            {u.status === 'invited' && (
                              <button type="button" className="btn btn-secondary btn-sm" onClick={() => void reinvite(u)}>
                                新链接
                              </button>
                            )}
                            {u.status !== 'disabled' && (
                              <button type="button" className="btn btn-secondary btn-sm" onClick={() => void reset2FA(u)}>
                                重置两步验证
                              </button>
                            )}
                            {u.status === 'disabled' ? (
                              <button type="button" className="btn btn-secondary btn-sm" onClick={() => void enable(u)}>
                                重新启用
                              </button>
                            ) : (
                              <button type="button" className="btn btn-danger btn-sm" onClick={() => void disable(u)}>
                                停用
                              </button>
                            )}
                          </div>
                        )}
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}

const ACCESS_TEXT = {none: '—', read: '查看', write: '编辑'};

function RoleMatrixView() {
  const m = useAdminLoad(signal => getRoleMatrix(signal));
  return (
    <>
      <AdminNotice error={m.error} />
      <section className="adm-card">
        <div className="adm-table-wrap">
          <table className="adm-table adm-matrix">
            <thead>
              <tr>
                <th>功能</th>
                {(m.data?.roles ?? []).map(r => (
                  <th key={r.role}>{r.label}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {(m.data?.matrix ?? []).map(row => (
                <tr key={row.group}>
                  <td>
                    {row.label}
                    <div className="adm-source-meta mono">{row.group}</div>
                  </td>
                  {(m.data?.roles ?? []).map(r => {
                    const a = row.access[r.role] ?? 'none';
                    return (
                      <td key={r.role} className={`adm-access adm-access-${a}`}>
                        {ACCESS_TEXT[a]}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {m.data && (
          <>
            <h3>需要双人审批的操作</h3>
            <ul className="adm-dual-list mono">
              {m.data.dual_approval.map(d => (
                <li key={d}>{d}</li>
              ))}
            </ul>
            <p className="muted">所有已登录管理员都能查看本矩阵、自己的会话和待审批列表；审批时会再检查审批人是否有该操作的权限。</p>
          </>
        )}
      </section>
    </>
  );
}

function SiteUsers() {
  const {can} = useAdmin();
  const [params, setParams] = useSearchParams();
  const q = params.get('q') ?? '';
  const status = params.get('status') ?? '';
  const [draft, setDraft] = useState(q);
  const list = useAdminLoad(signal => searchSiteUsers(q, status, signal), [q, status]);
  const act = useAdminAction();
  const [reasons, setReasons] = useState<Record<number, string>>({});
  const [days, setDays] = useState<Record<number, number>>({});
  const writable = can('site_users:write');
  const canUnmute = can('moderation:write');

  const set = (k: string, v: string) => {
    const p = new URLSearchParams(params);
    p.set('tab', 'site');
    if (v) p.set(k, v);
    else p.delete(k);
    setParams(p, {replace: true});
  };
  const ban = async (u: SiteUser) => {
    const d = days[u.id] ?? 0;
    if (!window.confirm(`${d ? `封禁 ${d} 天` : '永久封禁'} ${u.email}？对方会立即退出登录。`)) return;
    if (await act.attempt(`ban-${u.id}`, () => banSiteUser(u.id, reasons[u.id] ?? '', d), `${u.email} 已封禁`)) list.reload();
  };
  const unban = async (u: SiteUser) => {
    if (await act.attempt(`unban-${u.id}`, () => unbanSiteUser(u.id, reasons[u.id] ?? ''), `${u.email} 已解封`)) list.reload();
  };
  const unmute = async (u: SiteUser) => {
    if (await act.attempt(`unmute-${u.id}`, () => unmuteSiteUser(u.id, reasons[u.id] ?? ''), `${u.email} 已解除禁言`)) list.reload();
  };

  return (
    <>
      <AdminNotice error={list.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <section className="adm-card">
        <form
          className="adm-toolbar"
          onSubmit={e => {
            e.preventDefault();
            set('q', draft.trim());
          }}
        >
          <input className="adm-input" value={draft} onChange={e => setDraft(e.target.value)} placeholder="邮箱前缀、昵称或 #用户 ID" aria-label="搜索前台用户" />
          <button type="submit" className="btn btn-secondary btn-sm">
            搜索
          </button>
          <div className="adm-seg" role="tablist" aria-label="状态">
            {[
              ['', '全部'],
              ['banned', '已封禁'],
              ['muted', '禁言中'],
            ].map(([v, label]) => (
              <button key={v} type="button" role="tab" aria-selected={status === v} className={`adm-seg-item ${status === v ? 'on' : ''}`} onClick={() => set('status', v)}>
                {label}
              </button>
            ))}
          </div>
        </form>
        <p className="muted">查询会记入操作日志。禁言来自「举报与审核」的删除并禁言；封禁会阻止登录（含第三方登录）。</p>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>用户</th>
                <th>注册 / 最近活跃</th>
                <th>状态</th>
                <th>被举报</th>
                {writable && <th>处罚</th>}
              </tr>
            </thead>
            <tbody>
              {list.data && list.data.length === 0 && <EmptyRow cols={5}>没有匹配的用户</EmptyRow>}
              {(list.data ?? []).map(u => (
                <tr key={u.id}>
                  <td>
                    <strong>{u.display_name || '（未设置昵称）'}</strong>
                    <div className="adm-source-meta">
                      #{u.id} · {u.email}
                    </div>
                  </td>
                  <td>
                    {fmtTime(u.created_at)}
                    <div className="adm-source-meta">{u.last_seen_at ? `活跃 ${fmtTime(u.last_seen_at)}` : '无会话'}</div>
                  </td>
                  <td className="wrap">
                    {u.banned && <Pill tone="red">{u.banned_until ? `封禁至 ${fmtTime(u.banned_until)}` : '永久封禁'}</Pill>}
                    {u.muted_until && <Pill tone="amber">禁言至 {fmtTime(u.muted_until)}</Pill>}
                    {u.deletion_requested_at && <Pill tone="plain">注销中</Pill>}
                    {!u.banned && !u.muted_until && !u.deletion_requested_at && <Pill tone="green">正常</Pill>}
                    {u.ban_reason && <div className="adm-source-meta">原因：{u.ban_reason}</div>}
                  </td>
                  <td>{u.reports_against}</td>
                  {writable && (
                    <td>
                      <div className="adm-row-actions">
                        <input className="adm-input" placeholder="原因（必填）" value={reasons[u.id] ?? ''} onChange={e => setReasons(x => ({...x, [u.id]: e.target.value}))} />
                        {u.banned ? (
                          <button type="button" className="btn btn-secondary btn-sm" onClick={() => void unban(u)}>
                            解封
                          </button>
                        ) : (
                          <>
                            <select value={days[u.id] ?? 0} onChange={e => setDays(x => ({...x, [u.id]: Number(e.target.value)}))} aria-label="封禁时长">
                              <option value={7}>7 天</option>
                              <option value={30}>30 天</option>
                              <option value={0}>永久</option>
                            </select>
                            <button type="button" className="btn btn-danger btn-sm" disabled={!(reasons[u.id] ?? '').trim()} onClick={() => void ban(u)}>
                              封禁
                            </button>
                          </>
                        )}
                        {u.muted_until && canUnmute && (
                          <button type="button" className="btn btn-secondary btn-sm" onClick={() => void unmute(u)}>
                            解除禁言
                          </button>
                        )}
                      </div>
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

function MyAccount() {
  const {me} = useAdmin();
  const act = useAdminAction();
  const [code, setCode] = useState('');
  const [codes, setCodes] = useState<string[] | null>(null);
  if (!me) return null;
  if (me.auth === 'token') {
    return (
      <section className="adm-card">
        <p>当前使用紧急令牌（{me.actor}），没有个人账号。请尽快用 <code>admin create</code> 或邀请链接建立个人账号，然后在服务器上设置 ADMIN_TOKEN_DISABLED=true。</p>
      </section>
    );
  }
  const regenerate = async (event: FormEvent) => {
    event.preventDefault();
    const r = await act.attempt('codes', () => regenerateRecoveryCodes(code.replace(/\s/g, '')), '已生成新的恢复码，旧的全部失效');
    setCode('');
    if (r) setCodes(r.recovery_codes);
  };
  return (
    <>
      <AdminNotice error={act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <section className="adm-card">
        <h2 className="adm-card-title">{me.admin.display_name || me.admin.username}</h2>
        <p className="muted">
          {me.admin.email} · {me.admin.roles.map(roleLabel).join('、')} · 会话 {Math.round(me.idle_timeout / 60)} 分钟无操作过期，最长 {Math.round(me.max_age / 3600)} 小时。
        </p>
        <h3>恢复码</h3>
        {codes ? (
          <>
            <p>请保存，离开本页后不会再显示：</p>
            <ol className="adm-codes mono">
              {codes.map(c => (
                <li key={c}>{c}</li>
              ))}
            </ol>
          </>
        ) : (
          <form className="adm-toolbar" onSubmit={regenerate}>
            <input className="adm-input" value={code} onChange={e => setCode(e.target.value)} inputMode="numeric" placeholder="当前 6 位验证码" required maxLength={7} aria-label="验证码" />
            <button type="submit" className="btn btn-secondary btn-sm" disabled={act.pending === 'codes'}>
              重新生成恢复码
            </button>
          </form>
        )}
        <p className="muted">
          手机丢失又没有恢复码时，请另一位超级管理员在「后台账号」里重置你的两步验证（需要双人审批）。也可在 <Link to="/admin/oplog">操作日志</Link> 中核对自己的登录记录。
        </p>
      </section>
    </>
  );
}
