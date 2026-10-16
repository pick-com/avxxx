import {Link, useLocation} from '../lib/nav';
import {useAuth} from '../lib/auth';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {useRecsAvailable} from '../lib/recs';
import {useSocial} from '../lib/social';
import {Avatar} from './Cards';
import {Icon, type IconName} from './Icon';

type NavItem = {key: string; label: MessageKey; short: MessageKey; to: string; icon: IconName};

// Only destinations with a backend today. 热门 needs GET /api/trending (recs
// backend) and is dropped once that route is known to be missing.
const mainItems: NavItem[] = [
  {key: 'home', label: 'nav.home', short: 'nav.home', to: '/', icon: 'home'},
  {key: 'trending', label: 'nav.trending', short: 'nav.trending', to: '/trending', icon: 'trending'},
  {key: 'new', label: 'nav.new', short: 'nav.new', to: '/discover?tab=new', icon: 'new'},
  {key: 'categories', label: 'nav.categories', short: 'nav.categories', to: '/discover?tab=categories', icon: 'categories'},
  {key: 'actors', label: 'nav.actors', short: 'nav.actors', to: '/discover?tab=actors', icon: 'actors'},
];
const mineItems: NavItem[] = [
  {key: 'history', label: 'nav.history', short: 'nav.historyShort', to: '/history', icon: 'history'},
  {key: 'later', label: 'nav.later', short: 'nav.laterShort', to: '/later', icon: 'later'},
  {key: 'favorites', label: 'nav.favorites', short: 'nav.favoritesShort', to: '/favorites', icon: 'favorites'},
];

function activeKey(pathname: string, search: string): string {
  if (pathname === '/') return 'home';
  if (pathname === '/trending') return 'trending';
  if (pathname === '/discover') {
    const tab = new URLSearchParams(search).get('tab');
    return tab === 'categories' || tab === 'actors' || tab === 'trending' ? tab : 'new';
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
  const t = useT();
  const {status} = useAuth();
  const {follows, available} = useSocial();
  const location = useLocation();
  const recs = useRecsAvailable(variant !== 'hidden');
  if (variant === 'hidden') return null;
  const current = activeKey(location.pathname, location.search);
  const renderItem = (item: NavItem) => (
    <Link key={item.key} to={item.to} className="nav-item" aria-current={item.key === current ? 'page' : undefined}>
      <Icon name={item.icon} />
      <span className="nav-label">{t(item.label)}</span>
      <span className="nav-short" aria-hidden="true">
        {t(item.short)}
      </span>
    </Link>
  );
  return (
    <nav className="sidenav" data-variant={variant} aria-label={t('nav.main')}>
      {mainItems.filter(item => item.key !== 'trending' || recs !== 'no').map(renderItem)}
      <div className="nav-divider" />
      <span className="nav-heading">{t('nav.mine')}</span>
      {mineItems.filter(item => item.key !== 'later' || (status === 'user' && available.later !== 'no')).map(renderItem)}
      {status === 'user' && available.follows !== 'no' && variant !== 'rail' && (
        <>
          <div className="nav-divider" />
          <Link to="/following" className="nav-heading nav-heading-link" aria-current={current === 'following' ? 'page' : undefined}>
            {t('nav.following')}
            {follows.some(f => f.new_count > 0) && <span className="sr-only">{t('nav.hasNew')}</span>}
          </Link>
          {follows.length === 0 ? (
            <Link to="/discover?tab=actors" className="nav-item nav-item-muted">
              <Icon name="plus" />
              <span className="nav-label">{t('nav.goFollow')}</span>
            </Link>
          ) : (
            follows.slice(0, FOLLOW_LIMIT).map(f => (
              <Link
                key={f.slug}
                to={`/performers/${encodeURIComponent(f.slug)}`}
                className="nav-item nav-follow"
                aria-label={f.new_count > 0 ? t('nav.newWorks', {name: f.name, count: f.new_count}) : f.name}
              >
                <Avatar seed={f.slug} name={f.name} size={24} />
                <span className="nav-label">{f.name}</span>
                {f.new_count > 0 && <span className="new-dot" aria-hidden="true" />}
              </Link>
            ))
          )}
          {follows.length > FOLLOW_LIMIT && (
            <Link to="/following" className="nav-item nav-item-muted">
              <span className="nav-label">{t('nav.viewAll', {count: follows.length})}</span>
            </Link>
          )}
        </>
      )}
      {status === 'anon' && (
        <div className="nav-login-card">
          <span>{t('nav.loginPitch')}</span>
          <Link to="/login" className="btn btn-outline-accent">
            {t('auth.login')}
          </Link>
        </div>
      )}
    </nav>
  );
}
