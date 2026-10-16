import {Link, useLocation, useSearchParams} from 'react-router';
import {useAuth} from '../lib/auth';
import {themeLabels, useTheme, type ThemePref} from '../lib/theme';
import {Icon, type IconName} from './Icon';
import {SearchBox} from './SearchBox';

const nextPref: Record<ThemePref, ThemePref> = {system: 'dark', dark: 'light', light: 'system'};
const prefIcon: Record<ThemePref, IconName> = {system: 'auto', dark: 'moon', light: 'sun'};

/** Cycles 跟随系统 → 深色 → 浅色 (PRD 8). */
export function ThemeToggle() {
  const {pref, setPref} = useTheme();
  const label = `外观：${themeLabels[pref]}，点击切换为${themeLabels[nextPref[pref]]}`;
  return (
    <button type="button" className="icon-btn" aria-label={label} title={label} onClick={() => setPref(nextPref[pref])}>
      <Icon name={prefIcon[pref]} />
    </button>
  );
}

export function UserSlot() {
  const {status, user} = useAuth();
  const location = useLocation();
  if (status === 'user' && user) {
    return (
      <Link to="/me" className="avatar-btn" aria-label={`我的（${user.email}）`}>
        <span className="avatar-initial">{user.email.slice(0, 1).toUpperCase()}</span>
      </Link>
    );
  }
  if (status === 'anon') {
    const next = location.pathname + location.search;
    return (
      <Link to={next === '/' ? '/login' : `/login?next=${encodeURIComponent(next)}`} className="btn btn-outline btn-sm">
        登录
      </Link>
    );
  }
  return <span className="avatar-btn" aria-hidden="true" />;
}

/** TopBar artboard: menu, logo, search, theme, language, account. */
export function TopBar({onToggleNav, navExpanded}: {onToggleNav: () => void; navExpanded: boolean}) {
  const [params] = useSearchParams();
  const location = useLocation();
  const query = location.pathname === '/search' ? params.get('q') ?? '' : '';
  return (
    <header className="topbar">
      <button type="button" className="icon-btn hide-mobile" aria-label={navExpanded ? '收起导航菜单' : '打开导航菜单'} aria-expanded={navExpanded} onClick={onToggleNav}>
        <Icon name="menu" />
      </button>
      <Link to="/" className="logo" aria-label="REEL 首页">
        REEL<span className="logo-dot">.</span>
      </Link>
      <SearchBox initialQuery={query} className="topbar-search hide-mobile" />
      <span className="topbar-spacer show-mobile" />
      <Link to="/search" className="icon-btn show-mobile" aria-label="搜索">
        <Icon name="search" />
      </Link>
      <ThemeToggle />
      {/* TODO(tech plan 3.7): notification bell once GET /api/me/notifications/unread-count exists. */}
      {/* TODO(tech plan 3.10): language switcher; only 简体中文 ships in this wave. */}
      <button type="button" className="lang-pill hide-mobile" aria-disabled="true" title="多语种即将支持" aria-label="切换语言（即将支持），当前简体中文">
        <Icon name="globe" size={16} />
        简体中文
      </button>
      <UserSlot />
    </header>
  );
}
