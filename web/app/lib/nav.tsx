import {forwardRef, useCallback, useMemo, type ComponentProps} from 'react';
import {
  Link as RouterLink,
  NavLink as RouterNavLink,
  useLocation as useRouterLocation,
  useNavigate as useRouterNavigate,
  type NavigateOptions,
  type To,
} from 'react-router';
import {useI18n} from './i18n';
import {localizePath, stripLang} from './langs';
import type {LangCode} from './api';

// Language-aware routing helpers for app pages (PRD 8 / lib/langs.ts).
//
// Pages write the unprefixed paths they always used (/releases/x, /me) and
// these wrappers keep the current page's language: on /en/... a link to
// /releases/x goes to /en/releases/x. useLocation() reports the pathname
// without the prefix, so `pathname === '/discover'` checks keep working.
// The back office imports react-router directly (it has no prefixes).

function localizeTo(to: To, lang: LangCode): To {
  if (typeof to === 'string') return to.startsWith('/') ? localizePath(to, lang) : to;
  if (to.pathname?.startsWith('/')) return {...to, pathname: localizePath(to.pathname, lang)};
  return to;
}

export const Link = forwardRef<HTMLAnchorElement, ComponentProps<typeof RouterLink>>(function Link({to, ...props}, ref) {
  const {uiLang} = useI18n();
  return <RouterLink ref={ref} to={localizeTo(to, uiLang)} {...props} />;
});

export const NavLink = forwardRef<HTMLAnchorElement, ComponentProps<typeof RouterNavLink>>(function NavLink({to, ...props}, ref) {
  const {uiLang} = useI18n();
  return <RouterNavLink ref={ref} to={localizeTo(to, uiLang)} {...props} />;
});

/** useNavigate() that keeps the current language prefix on absolute paths. */
export function useNavigate() {
  const navigate = useRouterNavigate();
  const {uiLang} = useI18n();
  return useCallback(
    (to: To | number, options?: NavigateOptions) => {
      if (typeof to === 'number') return navigate(to);
      return navigate(localizeTo(to, uiLang), options);
    },
    [navigate, uiLang],
  ) as {(to: To, options?: NavigateOptions): void | Promise<void>; (delta: number): void | Promise<void>};
}

/** useLocation() with the language prefix removed from pathname. */
export function useLocation() {
  const location = useRouterLocation();
  return useMemo(() => ({...location, pathname: stripLang(location.pathname)}), [location]);
}

/** Path for `path` in the current language (for hrefs outside <Link>). */
export function useLocalizedPath() {
  const {uiLang} = useI18n();
  return useCallback((path: string) => localizePath(path, uiLang), [uiLang]);
}
