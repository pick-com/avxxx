import {useEffect, useState} from 'react';
import {Outlet, useLocation, useMatches} from 'react-router';
import {BottomTabs} from '../components/BottomTabs';
import {SideNav, type NavVariant} from '../components/SideNav';
import {TopBar} from '../components/TopBar';
import {useMediaQuery} from '../lib/useMediaQuery';

export type ShellHandle = {
  /** Video-style page: no side nav on desktop; no top bar or tabs on mobile (MobileVideo). */
  immersive?: boolean;
};

type NavMode = 'auto' | 'open' | 'collapsed';

/**
 * App shell: TopBar + SideNav (≥1200px), BrowseRail icon rail (768–1199px),
 * bottom tab bar (<768px). The menu button collapses the wide nav to the rail,
 * or opens the full nav as an overlay at narrower widths and on the video page.
 */
export default function Shell() {
  const matches = useMatches();
  const immersive = matches.some(m => (m.handle as ShellHandle | undefined)?.immersive);
  const location = useLocation();
  const wide = useMediaQuery('(min-width: 1200px)');
  const mobile = useMediaQuery('(max-width: 767px)');
  const [mode, setMode] = useState<NavMode>('auto');

  // The overlay closes on navigation.
  useEffect(() => setMode(m => (m === 'open' ? 'auto' : m)), [location.pathname, location.search]);

  const inlineWide = wide && !immersive;
  let variant: NavVariant;
  if (mobile) variant = 'hidden';
  else if (inlineWide) variant = mode === 'collapsed' ? 'rail' : 'full';
  else if (mode === 'open') variant = 'overlay';
  else variant = immersive ? 'hidden' : 'rail';

  const toggle = () => {
    if (inlineWide) setMode(m => (m === 'collapsed' ? 'auto' : 'collapsed'));
    else setMode(m => (m === 'open' ? 'auto' : 'open'));
  };

  return (
    <div className="shell" data-immersive={immersive ? 'true' : 'false'}>
      <a href="#main" className="skip-link">
        跳到主要内容
      </a>
      <TopBar onToggleNav={toggle} navExpanded={variant === 'full' || variant === 'overlay'} />
      <div className="shell-body">
        <SideNav variant={variant} />
        {variant === 'overlay' && <button type="button" className="nav-backdrop" aria-label="关闭导航菜单" onClick={() => setMode('auto')} />}
        <main id="main" className="shell-main" tabIndex={-1}>
          <Outlet />
        </main>
      </div>
      <BottomTabs />
    </div>
  );
}
