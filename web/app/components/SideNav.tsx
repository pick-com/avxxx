import {Link, useLocation} from 'react-router';
import {useAuth} from '../lib/auth';
import {useSocial} from '../lib/social';
import {Avatar} from './Cards';
import {Icon, type IconName} from './Icon';

type NavItem = {key: string; label: string; short: string; to: string; icon: IconName};

// Only destinations with a backend today. Omitted until their APIs exist:
// TODO(tech plan 3.13 trending_snapshots): 热门.
const mainItems: NavItem[] = [
  {key: 'home', label: '首页', short: '首页', to: '/', icon: 'home'},
  {key: 'new', label: '新上线', short: '新上线', to: '/discover?tab=new', icon: 'new'},
  {key: 'categories', label: '分类', short: '分类', to: '/discover?tab=categories', icon: 'categories'},
  {key: 'actors', label: '演员', short: '演员', to: '/discover?tab=actors', icon: 'actors'},
];
const mineItems: NavItem[] = [
  {key: 'history', label: '观看记录', short: '记录', to: '/history', icon: 'history'},
  {key: 'later', label: '稍后观看', short: '稍后', to: '/later', icon: 'later'},
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
  if (pathname === '/later') return 'later';
  if (pathname === '/following') return 'following';
  return '';
}

export type NavVariant = 'full' | 'rail' | 'overlay' | 'hidden';

/** SideNav artboard ('full' / 'overlay') and the BrowseRail icon rail ('rail'). */
const FOLLOW_LIMIT = 8;

export function SideNav({variant}: {variant: NavVariant}) {
  const {status} = useAuth();
  const {follows, available} = useSocial();
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
      {mineItems.filter(item => item.key !== 'later' || (status === 'user' && available.later !== 'no')).map(renderItem)}
      {status === 'user' && available.follows !== 'no' && variant !== 'rail' && (
        <>
          <div className="nav-divider" />
          <Link to="/following" className="nav-heading nav-heading-link" aria-current={current === 'following' ? 'page' : undefined}>
            关注的演员
            {follows.some(f => f.new_count > 0) && <span className="sr-only">（有新作）</span>}
          </Link>
          {follows.length === 0 ? (
            <Link to="/discover?tab=actors" className="nav-item nav-item-muted">
              <Icon name="plus" />
              <span className="nav-label">去关注演员</span>
            </Link>
          ) : (
            follows.slice(0, FOLLOW_LIMIT).map(f => (
              <Link
                key={f.slug}
                to={`/performers/${encodeURIComponent(f.slug)}`}
                className="nav-item nav-follow"
                aria-label={f.new_count > 0 ? `${f.name}，${f.new_count} 部新作` : f.name}
              >
                <Avatar seed={f.slug} name={f.name} size={24} />
                <span className="nav-label">{f.name}</span>
                {f.new_count > 0 && <span className="new-dot" aria-hidden="true" />}
              </Link>
            ))
          )}
          {follows.length > FOLLOW_LIMIT && (
            <Link to="/following" className="nav-item nav-item-muted">
              <span className="nav-label">查看全部 {follows.length} 位</span>
            </Link>
          )}
        </>
      )}
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
