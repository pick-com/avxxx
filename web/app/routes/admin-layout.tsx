import {useCallback, useEffect, useMemo, useState, type FormEvent} from 'react';
import {Link, Navigate, NavLink, Outlet, useLocation} from 'react-router';
import {ThemeToggle} from '../components/TopBar';
import {ApiError, isAbort, tokens, validAdminActor} from '../lib/api';
import {adminErrorText, getDashboard, getModerationOverview, getPerformerRequests, type Dashboard, type QueueCounts} from '../lib/adminApi';
import {adminLogout, getAdminSession, getApprovals, roleLabel, type AdminMe} from '../lib/adminUsersApi';
import {AdminContext} from '../lib/adminContext';
import {getAdminTakedowns, type TakedownCounts} from '../lib/adminComplianceApi';
import '../styles/admin.css';
import '../styles/admin-intel.css';
import '../styles/admin-gov.css';

export const meta = () => [{title: 'REEL · 后台'}];

// AdminNav (PRD 11) sidebar shell for every /admin page: section links with
// work-queue badges, filtered by the signed-in admin's permissions (role
// matrix, AdminUsers). Sign-in is /admin/login (password + TOTP; HttpOnly
// session cookie). The ADMIN_TOKEN break-glass path is a link on the
// signed-out card, kept for emergencies only. Desktop-first: PRD 3 does not
// require the back office on phones.

const ICONS = {
  dashboard: 'M4 13h6V4H4zM14 20h6v-9h-6zM4 20h6v-3H4zM14 7h6V4h-6z',
  videos: 'M4 6h16v12H4zM10 9l5 3-5 3z',
  pipeline: 'M3 6h5v4H3zM10 14h5v4h-5zM17 6h4v4h-4zM8 8h9M12 10v4',
  dedupe: 'M8 8h11v11H8zM5 16V5h11',
  conflicts: 'M4 7h10M4 12h16M4 17h7M17 4l3 3-3 3M17 14l3 3-3 3',
  audit: 'M9 12l2 2 4-4M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z',
  reports: 'M5 21V4h11l-1.5 4L16 12H5',
  i18n: 'M12 21a9 9 0 100-18 9 9 0 000 18zM3 12h18M12 3c3 3 3 15 0 18M12 3c-3 3-3 15 0 18',
  actors: 'M12 12a4 4 0 100-8 4 4 0 000 8zM4 21c1-4 4-6 8-6s7 2 8 6',
  search: 'M11 18a7 7 0 100-14 7 7 0 000 14zM20 20l-4-4',
  priority: 'M4 6h10M4 12h7M4 18h4M17 4v16M14 17l3 3 3-3',
  completeness: 'M4 20V10M10 20V4M16 20v-7M22 20H2',
  links: 'M10 14a4 4 0 005.7 0l3-3a4 4 0 00-5.7-5.7l-1 1M14 10a4 4 0 00-5.7 0l-3 3a4 4 0 005.7 5.7l1-1',
  ops: 'M4 5h16v14H4zM4 9h16M9 13h6M9 16h4',
  legal: 'M7 3h7l5 5v13H7zM14 3v5h5M10 13h6M10 17h6',
  feedback: 'M4 5h16v11H9l-5 4zM8 9h8M8 12h5',
  copyright: 'M12 21a9 9 0 100-18 9 9 0 000 18zM15 9.5a3.5 3.5 0 100 5',
  users: 'M9 11a4 4 0 100-8 4 4 0 000 8zM2 21c.8-3.5 3.5-5.5 7-5.5s6.2 2 7 5.5M17 3.5a4 4 0 010 7.5M22 21c-.5-2.6-2-4.4-4-5.2',
  approvals: 'M9 11l2 2 4-4M5 4h14v16H5zM9 17h6',
  oplog: 'M5 4h14v16H5zM8 8h8M8 12h8M8 16h5',
  jobs: 'M12 21a9 9 0 100-18 9 9 0 000 18zM12 7v5l3 2',
};

type Badge = {n: number; tone: 'red' | 'amber' | 'plain'} | null;
// perm: the "group:read" permission the page needs (internal/adminauth roles matrix).
type NavItem = {to: string; label: string; icon: keyof typeof ICONS; end?: boolean; badge?: Badge; perm?: string};

const badge = (n: number | undefined, tone: 'red' | 'amber' | 'plain'): Badge => (n && n > 0 ? {n, tone} : null);

function failedJobs(d: Dashboard | null) {
  if (!d) return 0;
  return Object.values(d.jobs ?? {}).reduce((sum, j) => sum + (j.failed_24h || 0), 0);
}

type Status = 'checking' | 'signed_in' | 'signed_out';

export default function AdminLayout() {
  const location = useLocation();
  const [status, setStatus] = useState<Status>('checking');
  const [me, setMe] = useState<AdminMe | null>(null);
  const [epoch, setEpoch] = useState(0);
  const [glassOpen, setGlassOpen] = useState(false);
  const [tokenDraft, setTokenDraft] = useState('');
  const [labelDraft, setLabelDraft] = useState('');
  const [glassError, setGlassError] = useState('');
  const [dash, setDash] = useState<Dashboard | null>(null);
  const [counts, setCounts] = useState<QueueCounts | null>(null);
  const [badgeTick, setBadgeTick] = useState(0);
  const [openRequests, setOpenRequests] = useState<number | null>(null);
  const [takedowns, setTakedowns] = useState<TakedownCounts | null>(null);
  const [pendingApprovals, setPendingApprovals] = useState<number | null>(null);

  // Who is signed in: the session cookie or a break-glass token (api.ts).
  useEffect(() => {
    const ctrl = new AbortController();
    setStatus('checking');
    getAdminSession(ctrl.signal)
      .then(m => {
        setMe(m);
        setStatus('signed_in');
      })
      .catch(cause => {
        if (isAbort(cause)) return;
        setMe(null);
        setStatus('signed_out');
        if (tokens.admin()) {
          // A stored break-glass token was refused (wrong, or ADMIN_TOKEN_DISABLED).
          tokens.setAdmin(null);
          setGlassError(cause instanceof ApiError && cause.status === 401 ? '紧急令牌无效或已停用' : adminErrorText(cause));
          setGlassOpen(true);
        }
      });
    return () => ctrl.abort();
  }, [epoch]);

  const hasToken = status === 'signed_in';
  const can = useCallback((perm: string) => !!me && (me.auth === 'token' || me.permissions.includes(perm)), [me]);
  const refreshBadges = useCallback(() => setBadgeTick(t => t + 1), []);

  useEffect(() => {
    if (!hasToken) return;
    let live = true;
    if (can('dashboard:read')) {
      getDashboard()
        .then(d => live && setDash(d))
        .catch(() => live && setDash(null));
    }
    if (can('moderation:read')) {
      getModerationOverview(1)
        .then(m => live && setCounts(m.counts))
        .catch(() => live && setCounts(null));
    }
    // 更正与隐藏申请 (admin-performers backend; absent → no badge).
    if (can('catalog:read')) {
      getPerformerRequests({status: 'open', limit: 100})
        .then(p => live && setOpenRequests(p.items.length))
        .catch(() => live && setOpenRequests(null));
    }
    // 版权投诉 (compliance backend; absent → no badge): overdue SLA is red.
    if (can('compliance:read')) {
      getAdminTakedowns({open: true, limit: 1})
        .then(d => live && setTakedowns(d.counts))
        .catch(() => live && setTakedowns(null));
    }
    // 待审批: requests this admin can approve.
    getApprovals('pending')
      .then(list => live && setPendingApprovals(list.filter(a => a.can_approve).length))
      .catch(() => live && setPendingApprovals(null));
    return () => {
      live = false;
    };
  }, [hasToken, can, epoch, badgeTick]);

  const submitBreakGlass = (event: FormEvent) => {
    event.preventDefault();
    const v = tokenDraft.trim();
    const label = labelDraft.trim();
    if (!validAdminActor(label)) {
      setGlassError('标签只能用英文字母、数字和符号，不含空格，最多 64 个字符');
      return;
    }
    setGlassError('');
    tokens.setAdmin(v || null);
    tokens.setAdminActor(label);
    setTokenDraft('');
    setEpoch(e => e + 1);
  };

  const signOut = async () => {
    if (me?.auth === 'session') {
      try {
        await adminLogout();
      } catch {
        /* the cookie may already be gone */
      }
    }
    tokens.setAdmin(null);
    tokens.setAdminActor('');
    setMe(null);
    setDash(null);
    setCounts(null);
    setStatus('signed_out');
    setEpoch(e => e + 1);
  };

  const groups = useMemo(() => {
    const moderation = counts ? counts.reports + counts.danmaku + counts.reviews + counts.rating_floods : 0;
    const g: {title: string; items: NavItem[]}[] = [
      {title: '概览', items: [{to: '/admin', label: '总览', icon: 'dashboard', end: true, perm: 'dashboard:read'}]},
      {
        title: '内容',
        items: [
          {to: '/admin/videos', label: '视频', icon: 'videos', perm: 'catalog:read'},
          {to: '/admin/actors', label: '演员', icon: 'actors', badge: badge(openRequests ?? undefined, 'amber'), perm: 'catalog:read'},
          {to: '/admin/pipeline', label: '入库流水线', icon: 'pipeline', badge: badge(failedJobs(dash), 'red'), perm: 'ingest:read'},
          {to: '/admin/dedupe', label: '重复与归并', icon: 'dedupe', badge: badge(dash?.identity_candidates_open, 'amber'), perm: 'catalog:read'},
          {to: '/admin/review', label: '字段冲突', icon: 'conflicts', badge: badge(dash?.conflicts_open, 'amber'), perm: 'catalog:read'},
          {
            to: '/admin/audit',
            label: '数据质量',
            icon: 'audit',
            badge: badge(dash?.issues.open, dash?.issues.by_rule?.['R-AGE'] ? 'red' : 'amber'),
            perm: 'quality:read',
          },
        ],
      },
      {
        title: '数据治理',
        items: [
          {to: '/admin/field-policies', label: '字段优先级', icon: 'priority', perm: 'ingest:read'},
          {to: '/admin/media-links', label: '片源映射', icon: 'links', perm: 'ingest:read'},
          {to: '/admin/reports/completeness', label: '完整性报告', icon: 'completeness', perm: 'dashboard:read'},
          {to: '/admin/jobs', label: '夜间任务', icon: 'jobs', perm: 'ingest:read'},
        ],
      },
      {
        title: '社区',
        items: [
          {to: '/admin/moderation', label: '举报与审核', icon: 'reports', badge: badge(moderation, counts?.reports_overdue ? 'red' : 'amber'), perm: 'moderation:read'},
        ],
      },
      {
        title: '运营',
        items: [{to: '/admin/ops', label: '首页排期与公告', icon: 'ops', perm: 'ops:read'}],
      },
      {
        title: '系统',
        items: [
          {to: '/admin/search', label: '搜索词情报', icon: 'search', perm: 'search:read'},
          {to: '/admin/i18n', label: '翻译管理', icon: 'i18n', perm: 'i18n:read'},
        ],
      },
      {
        title: '合规',
        items: [
          {to: '/admin/legal', label: '法律文本', icon: 'legal', perm: 'legal:read'},
          {to: '/admin/feedback', label: '用户反馈', icon: 'feedback', perm: 'compliance:read'},
          {
            to: '/admin/takedowns',
            label: '版权投诉',
            icon: 'copyright',
            badge: badge(takedowns?.overdue || takedowns?.open, takedowns?.overdue ? 'red' : 'amber'),
            perm: 'compliance:read',
          },
        ],
      },
      {
        title: '账号与权限',
        items: [
          {to: '/admin/users', label: '用户与权限', icon: 'users'},
          {to: '/admin/approvals', label: '待审批', icon: 'approvals', badge: badge(pendingApprovals ?? undefined, 'amber')},
          {to: '/admin/oplog', label: '操作日志', icon: 'oplog', perm: 'audit_log:read'},
        ],
      },
    ];
    if (!hasToken) return g;
    return g.map(group => ({...group, items: group.items.filter(i => !i.perm || can(i.perm))})).filter(group => group.items.length > 0);
  }, [dash, counts, openRequests, takedowns, pendingApprovals, hasToken, can]);

  const session = useMemo(() => ({hasToken, epoch, refreshBadges, me, can}), [hasToken, epoch, refreshBadges, me, can]);
  // 总览 needs dashboard:read: a role without it (外部译者, 广告销售) lands on
  // its first permitted page instead of a dashboard full of 403 errors.
  const landing = hasToken && location.pathname.replace(/\/+$/, '') === '/admin' && !can('dashboard:read') ? groups[0]?.items[0]?.to : undefined;
  const next = encodeURIComponent(location.pathname + location.search);

  return (
    <AdminContext.Provider value={session}>
      <div className="adm">
        <nav className="adm-side" aria-label="后台导航">
          <Link to="/admin" className="adm-brand">
            <span className="adm-logo">
              REEL<span className="adm-dot">.</span>
            </span>
            <span className="adm-brand-tag">后台</span>
          </Link>
          {groups.map(group => (
            <div key={group.title} className="adm-group">
              <span className="adm-group-title">{group.title}</span>
              {group.items.map(item => (
                <NavLink key={item.to} to={item.to} end={item.end} className="adm-nav-item">
                  <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true">
                    <path d={ICONS[item.icon]} fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinejoin="round" strokeLinecap="round" />
                  </svg>
                  <span className="adm-nav-label">{item.label}</span>
                  {item.badge && <span className={`adm-badge adm-badge-${item.badge.tone}`}>{item.badge.n > 999 ? '999+' : item.badge.n}</span>}
                </NavLink>
              ))}
            </div>
          ))}

          <div className="adm-side-foot">
            <div className="adm-token-row">
              <span className={`adm-token-dot ${hasToken ? 'on' : ''}`} aria-hidden="true" />
              <span className="adm-token-state">
                {status === 'checking'
                  ? '正在确认登录…'
                  : me?.auth === 'token'
                    ? `紧急令牌：${me.actor}`
                    : me
                      ? `${me.admin.display_name || me.admin.username} · ${me.admin.roles.map(roleLabel).join('、')}`
                      : '未登录'}
              </span>
              {hasToken && (
                <button type="button" className="adm-link" onClick={() => void signOut()}>
                  退出
                </button>
              )}
            </div>
            {me?.auth === 'token' && <span className="adm-actor-err">紧急令牌每次使用都会记入操作日志</span>}
            <div className="adm-side-tools">
              <ThemeToggle />
              <Link to="/" className="btn btn-secondary btn-sm">
                返回前台
              </Link>
            </div>
          </div>
        </nav>

        <main className="adm-main">
          {status === 'checking' ? (
            <p className="muted">正在确认登录…</p>
          ) : status === 'signed_out' ? (
            <section className="adm-card adm-token-card">
              <h2>请先登录后台</h2>
              <p className="muted">后台使用个人管理员账号：密码 + 两步验证。会话 30 分钟无操作自动过期。</p>
              <p>
                <Link to={`/admin/login?next=${next}`} className="btn btn-primary">
                  登录后台
                </Link>
              </p>
              {!glassOpen ? (
                <button type="button" className="adm-link" onClick={() => setGlassOpen(true)}>
                  使用紧急令牌（break-glass）
                </button>
              ) : (
                <form className="admin-token adm-glass" onSubmit={submitBreakGlass}>
                  <p className="muted">
                    仅在账号体系不可用时使用 ADMIN_TOKEN。令牌只保存在当前标签页，每次请求都会连同来源 IP 摘要记入操作日志，写操作记为「token:标签」。
                  </p>
                  <input
                    type="password"
                    value={tokenDraft}
                    onChange={e => setTokenDraft(e.target.value)}
                    placeholder="ADMIN_TOKEN"
                    aria-label="紧急令牌"
                    autoComplete="off"
                    autoFocus
                  />
                  <input
                    value={labelDraft}
                    onChange={e => setLabelDraft(e.target.value)}
                    placeholder="标签，例如 nc-oncall"
                    aria-label="紧急令牌标签"
                    maxLength={64}
                    autoComplete="off"
                    required
                  />
                  <button type="submit" className="btn btn-secondary">
                    用紧急令牌进入
                  </button>
                  {glassError && <span className="adm-actor-err">{glassError}</span>}
                </form>
              )}
            </section>
          ) : landing && landing !== '/admin' ? (
            <Navigate to={landing} replace />
          ) : (
            <Outlet />
          )}
        </main>
      </div>
    </AdminContext.Provider>
  );
}
