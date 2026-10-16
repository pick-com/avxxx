import {useEffect, useState} from 'react';
import {Outlet, useLoaderData, useMatches, useRouteLoaderData, type LoaderFunctionArgs, type ShouldRevalidateFunctionArgs} from 'react-router';
import type {RootData} from '../root';
import NotFound from './not-found';
import {useLocation, useNavigate} from '../lib/nav';
import {BottomTabs} from '../components/BottomTabs';
import {Footer} from '../components/Footer';
import {NetworkBanner} from '../components/NetworkBanner';
import {AnnouncementBar} from '../components/AnnouncementBar';
import {PlayerHostLayer, PlayerHostProvider, useMiniActive, usePlayerHostStore} from '../components/PlayerHost';
import {SideNav, type NavVariant} from '../components/SideNav';
import {TopBar} from '../components/TopBar';
import {useI18n, useT} from '../lib/i18n';
import {isGated} from '../lib/ageGate.server';
import {serverApi} from '../lib/api.server';
import {withRegion} from '../lib/region.server';
import {routeLang} from '../lib/langs';
import type {Announcement} from '../lib/home';
import {useRecsAvailable} from '../lib/recs';
import {useMediaQuery} from '../lib/useMediaQuery';

export type ShellHandle = {
  /** Video-style page: no side nav on desktop; no top bar or tabs on mobile (MobileVideo). */
  immersive?: boolean;
};

type NavMode = 'auto' | 'open' | 'collapsed';

// Announcement bar (AnnouncementBar): rendered on the server for the page
// language so it never pushes the page down after hydration. The visitor's
// country (CF-IPCountry behind the CDN, or GEOIP_HEADER) is forwarded as ?region= for
// region-targeted announcements. Kept across navigations within a language.
type ShellLoad = {lang: string; announcements?: Announcement[]} | null;

export async function loader({params, request}: LoaderFunctionArgs): Promise<ShellLoad> {
  const lang = routeLang(params);
  if (!lang) return null;
  // 18+ gate: the shell is not rendered (root.tsx), nothing to fetch.
  if (isGated(request)) return {lang};
  const q = withRegion(new URLSearchParams({lang}), request);
  const announcements = await serverApi<{items: Announcement[] | null}>(`announcements?${q}`, {signal: request.signal}).then(
    d => d?.items ?? [],
    () => undefined,
  );
  return {lang, announcements};
}

export const clientLoader = () => null;

export function shouldRevalidate({currentParams, nextParams}: ShouldRevalidateFunctionArgs) {
  return currentParams.lang !== nextParams.lang;
}

/**
 * App shell: TopBar + SideNav (≥1200px), BrowseRail icon rail (768–1199px),
 * bottom tab bar (<768px). The menu button collapses the wide nav to the rail,
 * or opens the full nav as an overlay at narrower widths and on the video page.
 */
export default function Shell() {
  const host = usePlayerHostStore();
  return (
    <PlayerHostProvider value={host}>
      <ShellFrame />
    </PlayerHostProvider>
  );
}

function ShellFrame() {
  const t = useT();
  const {uiLang} = useI18n();
  const shellData = useLoaderData<typeof loader>() as ShellLoad;
  const announcements = shellData?.lang === uiLang ? shellData.announcements : undefined;
  const mini = useMiniActive();
  const matches = useMatches();
  const root = useRouteLoaderData<RootData>('root');
  const immersive = !root?.notFound && matches.some(m => (m.handle as ShellHandle | undefined)?.immersive);
  const location = useLocation();
  const wide = useMediaQuery('(min-width: 1200px)');
  const mobile = useMediaQuery('(max-width: 767px)');
  const [mode, setMode] = useState<NavMode>('auto');

  // 今晚看什么 shortcut T (PRD 18): never while typing, with a modifier, inside
  // a menu or dialog, or on the video page (the player owns the keyboard there).
  const navigate = useNavigate();
  const recs = useRecsAvailable();
  useEffect(() => {
    if (recs === 'no') return;
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey || event.repeat) return;
      if (event.key !== 't' && event.key !== 'T') return;
      const target = event.target instanceof HTMLElement ? event.target : null;
      if (target && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName))) return;
      if (target?.closest('[role="menu"], [role="dialog"], [role="listbox"]')) return;
      if (immersive || location.pathname === '/tonight') return;
      event.preventDefault();
      navigate('/tonight');
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [recs, immersive, location.pathname, navigate]);

  // / focuses the search box (YouTube), from any page — the video page too —
  // but never while typing, with a modifier, in a dialog or in fullscreen.
  // Phones have no box in the bar: / opens /search (it focuses its own box).
  // Esc in the box blurs it (SearchBox).
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== '/' || event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey) return;
      const target = event.target instanceof HTMLElement ? event.target : null;
      if (target && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName))) return;
      if (target?.closest('[role="dialog"]') || document.fullscreenElement) return;
      event.preventDefault();
      const box = Array.from(document.querySelectorAll<HTMLInputElement>('form[role="search"] input')).find(el => el.getClientRects().length > 0);
      if (box) {
        box.focus();
        box.select();
      } else {
        navigate('/search');
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [navigate]);

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
    <div className="shell" data-immersive={immersive ? 'true' : 'false'} data-mini={mini && !immersive ? 'true' : 'false'}>
      <a href="#main" className="skip-link">
        {t('shell.skipToMain')}
      </a>
      <TopBar onToggleNav={toggle} navExpanded={variant === 'full' || variant === 'overlay'} />
      <NetworkBanner />
      <AnnouncementBar initial={announcements} />
      <div className="shell-body">
        <SideNav variant={variant} />
        {variant === 'overlay' && <button type="button" className="nav-backdrop" aria-label={t('nav.closeOverlay')} onClick={() => setMode('auto')} />}
        <main id="main" className="shell-main" tabIndex={-1}>
          {/* /foo/... where foo is not a language: 404 from the root loader. */}
          {root?.notFound ? <NotFound /> : <Outlet />}
          {!immersive && <Footer />}
        </main>
      </div>
      {/* One player instance: the release page's slot, the mini bar or PiP. */}
      <PlayerHostLayer />
      <BottomTabs />
    </div>
  );
}
