import {Link, useLocation} from '../lib/nav';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {Icon, type IconName} from './Icon';

type Tab = {key: string; label: MessageKey; to: string; icon: IconName; match: (path: string) => boolean};

const tabs: Tab[] = [
  {key: 'home', label: 'tabs.home', to: '/', icon: 'home', match: p => p === '/'},
  {key: 'discover', label: 'tabs.discover', to: '/discover', icon: 'discover', match: p => p === '/discover' || p === '/search' || p === '/trending' || p === '/tonight'},
  {key: 'following', label: 'tabs.following', to: '/following', icon: 'following', match: p => p === '/following'},
  {key: 'me', label: 'tabs.me', to: '/me', icon: 'me', match: p => ['/me', '/history', '/favorites', '/later', '/reservations', '/settings', '/notifications'].includes(p)},
];

/** Mobile bottom tab bar (MobileHome), shown under 768px. */
export function BottomTabs() {
  const t = useT();
  const {pathname} = useLocation();
  return (
    <nav className="bottom-tabs" aria-label={t('nav.main')}>
      {tabs.map(tab => {
        const on = tab.match(pathname);
        return (
          <Link key={tab.key} to={tab.to} className="tab" aria-current={on ? 'page' : undefined}>
            <Icon name={tab.icon} size={22} />
            <span>{t(tab.label)}</span>
          </Link>
        );
      })}
    </nav>
  );
}
