import {useCallback, useEffect, useMemo, useState, type FormEvent} from 'react';
import {Link, NavLink, Outlet} from 'react-router';
import {ThemeToggle} from '../components/TopBar';
import {tokens, validAdminActor} from '../lib/api';
import {getDashboard, getModerationOverview, getPerformerRequests, type Dashboard, type QueueCounts} from '../lib/adminApi';
import {AdminContext} from '../lib/adminContext';
import '../styles/admin.css';
import '../styles/admin-intel.css';
import '../styles/admin-gov.css';

export const meta = () => [{title: 'REEL · 后台'}];

// AdminNav (PRD 11) sidebar shell for every /admin page: section links with
// work-queue badges, the shared admin token (sent only to admin/*), and the
// 操作人 name sent as X-Admin-User on every admin write. Desktop-first: PRD 3
// does not require the back office on phones.

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
};

type Badge = {n: number; tone: 'red' | 'amber' | 'plain'} | null;
type NavItem = {to: string; label: string; icon: keyof typeof ICONS; end?: boolean; badge?: Badge};

const badge = (n: number | undefined, tone: 'red' | 'amber' | 'plain'): Badge => (n && n > 0 ? {n, tone} : null);

function failedJobs(d: Dashboard | null) {
  if (!d) return 0;
  return Object.values(d.jobs ?? {}).reduce((sum, j) => sum + (j.failed_24h || 0), 0);
}

export default function AdminLayout() {
  const [hasToken, setHasToken] = useState(false);
  const [epoch, setEpoch] = useState(0);
  const [tokenDraft, setTokenDraft] = useState('');
  const [editingToken, setEditingToken] = useState(false);
  const [actor, setActor] = useState('');
  const [actorDraft, setActorDraft] = useState('');
  const [actorError, setActorError] = useState('');
  const [dash, setDash] = useState<Dashboard | null>(null);
  const [counts, setCounts] = useState<QueueCounts | null>(null);
  const [badgeTick, setBadgeTick] = useState(0);
  const [openRequests, setOpenRequests] = useState<number | null>(null);

  // Read storage after mount: route modules avoid browser globals during render.
  useEffect(() => {
    setHasToken(!!tokens.admin());
    const a = tokens.adminActor();
    setActor(a);
    setActorDraft(a);
  }, []);

  const refreshBadges = useCallback(() => setBadgeTick(t => t + 1), []);

  useEffect(() => {
    if (!hasToken) return;
    let live = true;
    getDashboard()
      .then(d => live && setDash(d))
      .catch(() => live && setDash(null));
    getModerationOverview(1)
      .then(m => live && setCounts(m.counts))
      .catch(() => live && setCounts(null));
    // 更正与隐藏申请 (admin-performers backend; absent → no badge).
    getPerformerRequests({status: 'open', limit: 100})
      .then(p => live && setOpenRequests(p.items.length))
      .catch(() => live && setOpenRequests(null));
    return () => {
      live = false;
    };
  }, [hasToken, epoch, badgeTick]);

  const saveToken = (event: FormEvent) => {
    event.preventDefault();
    const v = tokenDraft.trim();
    tokens.setAdmin(v || null);
    setHasToken(!!v);
    setTokenDraft('');
    setEditingToken(false);
    setEpoch(e => e + 1);
  };

  const signOut = () => {
    tokens.setAdmin(null);
    setHasToken(false);
    setDash(null);
    setCounts(null);
    setEpoch(e => e + 1);
  };

  const saveActor = (event: FormEvent) => {
    event.preventDefault();
    const v = actorDraft.trim();
    if (!validAdminActor(v)) {
      setActorError('请用英文字母、数字或拼音（请求头只支持 ASCII），最多 100 个字符');
      return;
    }
    tokens.setAdminActor(v);
    setActor(v);
    setActorError('');
  };

  const groups = useMemo(() => {
    const moderation = counts ? counts.reports + counts.danmaku + counts.reviews + counts.rating_floods : 0;
    const g: {title: string; items: NavItem[]}[] = [
      {title: '概览', items: [{to: '/admin', label: '总览', icon: 'dashboard', end: true}]},
      {
        title: '内容',
        items: [
          {to: '/admin/videos', label: '视频', icon: 'videos'},
          {to: '/admin/actors', label: '演员', icon: 'actors', badge: badge(openRequests ?? undefined, 'amber')},
          {to: '/admin/pipeline', label: '入库流水线', icon: 'pipeline', badge: badge(failedJobs(dash), 'red')},
          {to: '/admin/dedupe', label: '重复与归并', icon: 'dedupe', badge: badge(dash?.identity_candidates_open, 'amber')},
          {to: '/admin/review', label: '字段冲突', icon: 'conflicts', badge: badge(dash?.conflicts_open, 'amber')},
          {
            to: '/admin/audit',
            label: '数据质量',
            icon: 'audit',
            badge: badge(dash?.issues.open, dash?.issues.by_rule?.['R-AGE'] ? 'red' : 'amber'),
          },
        ],
      },
      {
        title: '数据治理',
        items: [
          {to: '/admin/field-policies', label: '字段优先级', icon: 'priority'},
          {to: '/admin/media-links', label: '片源映射', icon: 'links'},
          {to: '/admin/reports/completeness', label: '完整性报告', icon: 'completeness'},
        ],
      },
      {
        title: '社区',
        items: [{to: '/admin/moderation', label: '举报与审核', icon: 'reports', badge: badge(moderation, counts?.reports_overdue ? 'red' : 'amber')}],
      },
      {
        title: '运营',
        items: [{to: '/admin/ops', label: '首页排期与公告', icon: 'ops'}],
      },
      {
        title: '系统',
        items: [
          {to: '/admin/search', label: '搜索词情报', icon: 'search'},
          {to: '/admin/i18n', label: '翻译管理', icon: 'i18n'},
        ],
      },
      {
        title: '合规',
        items: [{to: '/admin/legal', label: '法律文本', icon: 'legal'}],
      },
    ];
    return g;
  }, [dash, counts, openRequests]);

  const session = useMemo(() => ({hasToken, epoch, refreshBadges}), [hasToken, epoch, refreshBadges]);

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
            <form className="adm-actor" onSubmit={saveActor}>
              <label htmlFor="adm-actor">操作人</label>
              <div className="adm-actor-row">
                <input
                  id="adm-actor"
                  value={actorDraft}
                  onChange={e => setActorDraft(e.target.value)}
                  placeholder="例如 nc"
                  maxLength={100}
                  autoComplete="off"
                />
                <button type="submit" className="btn btn-secondary btn-sm" disabled={actorDraft.trim() === actor}>
                  保存
                </button>
              </div>
              {actorError ? (
                <span className="adm-actor-err">{actorError}</span>
              ) : (
                <span className="adm-actor-hint">{actor ? `写操作记为「${actor}」` : '未设置：写操作记为 admin'}</span>
              )}
            </form>
            <div className="adm-token-row">
              <span className={`adm-token-dot ${hasToken ? 'on' : ''}`} aria-hidden="true" />
              <span className="adm-token-state">{hasToken ? '已设置管理员令牌' : '未设置令牌'}</span>
              {hasToken && (
                <>
                  <button type="button" className="adm-link" onClick={() => setEditingToken(v => !v)}>
                    更换
                  </button>
                  <button type="button" className="adm-link" onClick={signOut}>
                    清除
                  </button>
                </>
              )}
            </div>
            <div className="adm-side-tools">
              <ThemeToggle />
              <Link to="/" className="btn btn-secondary btn-sm">
                返回前台
              </Link>
            </div>
          </div>
        </nav>

        <main className="adm-main">
          {!hasToken || editingToken ? (
            <section className="adm-card adm-token-card">
              <h2>管理员令牌</h2>
              <p className="muted">令牌只发送到 /api/admin/*，保存在本浏览器。</p>
              <form className="admin-token" onSubmit={saveToken}>
                <input
                  type="password"
                  value={tokenDraft}
                  onChange={e => setTokenDraft(e.target.value)}
                  placeholder="ADMIN_TOKEN"
                  aria-label="管理员令牌"
                  autoComplete="off"
                  autoFocus
                />
                <button type="submit" className="btn btn-primary">
                  进入后台
                </button>
                {editingToken && (
                  <button type="button" className="btn btn-secondary" onClick={() => setEditingToken(false)}>
                    取消
                  </button>
                )}
              </form>
            </section>
          ) : (
            <Outlet />
          )}
        </main>
      </div>
    </AdminContext.Provider>
  );
}
