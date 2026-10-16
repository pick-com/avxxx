import {useId, useRef, useState} from 'react';
import {useSearchParams} from 'react-router';
import {Link, useLocation} from '../lib/nav';
import {useAuth} from '../lib/auth';
import {useI18n, useT} from '../lib/i18n';
import {LANGS, LANG_LABELS} from '../lib/langs';
import {themeLabels, useTheme, type ThemePref} from '../lib/theme';
import {Icon, type IconName} from './Icon';
import {NotificationBell} from './Notifications';
import {SearchBox} from './SearchBox';
import {useDismiss} from './SocialButtons';

const nextPref: Record<ThemePref, ThemePref> = {system: 'dark', dark: 'light', light: 'system'};
const prefIcon: Record<ThemePref, IconName> = {system: 'auto', dark: 'moon', light: 'sun'};

/** Cycles 跟随系统 → 深色 → 浅色 (PRD 8). */
export function ThemeToggle() {
  const t = useT();
  const {pref, setPref} = useTheme();
  const label = t('theme.toggle', {current: themeLabels[pref], next: themeLabels[nextPref[pref]]});
  return (
    <button type="button" className="icon-btn" aria-label={label} title={label} onClick={() => setPref(nextPref[pref])}>
      <Icon name={prefIcon[pref]} />
    </button>
  );
}

export function UserSlot() {
  const t = useT();
  const {status, user} = useAuth();
  const location = useLocation();
  if (status === 'user' && user) {
    return (
      <Link to="/me" className="avatar-btn" aria-label={t('topbar.me', {email: user.email})}>
        <span className="avatar-initial">{user.email.slice(0, 1).toUpperCase()}</span>
      </Link>
    );
  }
  if (status === 'anon') {
    const next = location.pathname + location.search;
    return (
      <Link to={next === '/' ? '/login' : `/login?next=${encodeURIComponent(next)}`} className="btn btn-outline btn-sm">
        {t('auth.login')}
      </Link>
    );
  }
  return <span className="avatar-btn" aria-hidden="true" />;
}

/** UI language switcher (PRD 8): the eight launch languages, each in its own script. */
export function LanguageSwitcher() {
  const t = useT();
  const {uiLang, setUiLang} = useI18n();
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuId = useId();
  useDismiss(open, () => setOpen(false), rootRef, buttonRef);
  return (
    <div className="lang-switch hide-mobile" ref={rootRef}>
      <button
        ref={buttonRef}
        type="button"
        className="lang-pill"
        aria-haspopup="true"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={t('lang.switch', {lang: LANG_LABELS[uiLang]})}
        onClick={() => setOpen(o => !o)}
      >
        <Icon name="globe" size={16} />
        <span lang={uiLang}>{LANG_LABELS[uiLang]}</span>
      </button>
      {open && (
        <div className="popover lang-menu" id={menuId} role="group" aria-label={t('lang.menu')}>
          <span className="popover-title">{t('lang.menu')}</span>
          <div role="radiogroup" aria-label={t('lang.menu')}>
            {LANGS.map(l => (
              <button
                key={l}
                type="button"
                role="radio"
                aria-checked={l === uiLang}
                className="menu-choice"
                lang={l}
                onClick={() => {
                  setOpen(false);
                  buttonRef.current?.focus();
                  if (l !== uiLang) setUiLang(l);
                }}
              >
                <span className="menu-choice-text">
                  <span>{LANG_LABELS[l]}</span>
                </span>
                {l === uiLang && <Icon name="check" size={16} />}
              </button>
            ))}
          </div>
          <span className="popover-note">{t('lang.contentNote')}</span>
        </div>
      )}
    </div>
  );
}

/** TopBar artboard: menu, logo, search, theme, language, account. */
export function TopBar({onToggleNav, navExpanded}: {onToggleNav: () => void; navExpanded: boolean}) {
  const t = useT();
  const [params] = useSearchParams();
  const location = useLocation();
  const query = location.pathname === '/search' ? params.get('q') ?? '' : '';
  return (
    <header className="topbar">
      <button
        type="button"
        className="icon-btn hide-mobile"
        aria-label={navExpanded ? t('topbar.collapseNav') : t('topbar.openNav')}
        aria-expanded={navExpanded}
        onClick={onToggleNav}
      >
        <Icon name="menu" />
      </button>
      <Link to="/" className="logo" aria-label={t('topbar.home')}>
        REEL<span className="logo-dot">.</span>
      </Link>
      <SearchBox initialQuery={query} className="topbar-search hide-mobile" />
      <span className="topbar-spacer show-mobile" />
      <Link to="/search" className="icon-btn show-mobile" aria-label={t('topbar.search')}>
        <Icon name="search" />
      </Link>
      <ThemeToggle />
      <NotificationBell />
      <LanguageSwitcher />
      <UserSlot />
    </header>
  );
}
