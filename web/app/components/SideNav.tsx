import {Link, useLocation} from 'react-router';
import {useAuth} from '../lib/auth';
import {Icon, type IconName} from './Icon';

type NavItem = {key: string; label: string; short: string; to: string; icon: IconName};

// Only destinations with a backend today. Omitted until their APIs exist:
// TODO(tech plan 3.13 trending_snapshots): 热门.
// TODO(tech plan 3.13 watch_later): 稍后观看.
// TODO(tech plan 3.13 follows + GET /api/me/following/feed): 关注的演员 list with new-work dots.
const mainItems: NavItem[] = [
  {key: 'home', label: '首页', short: '首页', to: '/', icon: 'home'},
  {key: 'new', label: '新上线', short: '新上线', to: '/discover?tab=new', icon: 'new'},
  {key: 'categories', label: '分类', short: '分类', to: '/discover?tab=categories', icon: 'categories'},
  {key: 'actors', label: '演员', short: '演员', to: '/discover?tab=actors', icon: 'actors'},
];
const mineItems: NavItem[] = [
  {key: 'history', label: '观看记录', short: '记录', to: '/history', icon: 'history'},
  {key: 'favorites', label: '收藏夹', short: '收藏', to: '/favorites', icon: 'favorites'},
];

function activeKey(pathname: string, search: string): string {
  if (pathname === '/') return 'home';
  if (pathname === '/discover') {
    const tab = new URLSearchParams(search).get('tab');
    return tab === 'categories' || tab === 'actors' ? tab : 'new';
  }
  if (pathname === '/history') return 'history';
  if (pathname === '/favorites') return 'favorites';
  return '';
}

export type NavVariant = 'full' | 'rail' | 'overlay' | 'hidden';

/** SideNav artboard ('full' / 'overlay') and the BrowseRail icon rail ('rail'). */
export function SideNav({variant}: {variant: NavVariant}) {
  const {status} = useAuth();
  const location = useLocation();
  if (variant === 'hidden') return null;
  const current = activeKey(location.pathname, location.search);
  const renderItem = (item: NavItem) => (
    <Link key={item.key} to={item.to} className="nav-item" aria-current={item.key === current ? 'page' : undefined}>
      <Icon name={item.icon} />
      <span className="nav-label">{item.label}</span>
      <span className="nav-short" aria-hidden="true">
        {item.short}
      </span>
    </Link>
  );
  return (
    <nav className="sidenav" data-variant={variant} aria-label="主导航">
      {mainItems.map(renderItem)}
      <div className="nav-divider" />
      <span className="nav-heading">我的</span>
      {mineItems.map(renderItem)}
      {status === 'anon' && (
        <div className="nav-login-card">
          <span>登录后可以同步观看记录和收藏，换设备也能接着看。</span>
          <Link to="/login" className="btn btn-outline-accent">
            登录
          </Link>
        </div>
      )}
    </nav>
  );
}
