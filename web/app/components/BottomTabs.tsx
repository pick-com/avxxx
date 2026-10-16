import {Link, useLocation} from 'react-router';
import {Icon, type IconName} from './Icon';

type Tab = {key: string; label: string; to: string; icon: IconName; match: (path: string) => boolean};

const tabs: Tab[] = [
  {key: 'home', label: '首页', to: '/', icon: 'home', match: p => p === '/'},
  {key: 'discover', label: '发现', to: '/discover', icon: 'discover', match: p => p === '/discover' || p === '/search'},
  {key: 'following', label: '关注', to: '/following', icon: 'following', match: p => p === '/following'},
  {key: 'me', label: '我的', to: '/me', icon: 'me', match: p => p === '/me' || p === '/history' || p === '/favorites'},
];

/** Mobile bottom tab bar (MobileHome), shown under 768px. */
export function BottomTabs() {
  const {pathname} = useLocation();
  return (
    <nav className="bottom-tabs" aria-label="主导航">
      {tabs.map(tab => {
        const on = tab.match(pathname);
        return (
          <Link key={tab.key} to={tab.to} className="tab" aria-current={on ? 'page' : undefined}>
            <Icon name={tab.icon} size={22} />
            <span>{tab.label}</span>
          </Link>
        );
      })}
    </nav>
  );
}
