import {useEffect, useRef, type ReactNode} from 'react';
import {
  data,
  isRouteErrorResponse,
  Links,
  Meta,
  Outlet,
  redirect,
  Scripts,
  ScrollRestoration,
  useLoaderData,
  useMatches,
  useRouteError,
  useRouteLoaderData,
  type ClientLoaderFunctionArgs,
  type LoaderFunctionArgs,
  type ShouldRevalidateFunctionArgs,
} from 'react-router';
import {CookieBanner} from './components/CookieBanner';
import {pageName, setPage, startAnalytics} from './lib/analytics';
import {siteOrigin, serverUiDict} from './lib/api.server';
import {AuthProvider} from './lib/auth';
import {I18nProvider, LangBoundary, loadUiDict, primeLang, t, type Messages} from './lib/i18n';
import {ConsentProvider} from './lib/consent';
import {DEFAULT_LANG, isPrefixable, localizePath, parseLangParam, readLangCookie} from './lib/langs';
import {Link, useLocation} from './lib/nav';
import type {SeoHandle} from './lib/seo';
import {SettingsProvider} from './lib/settings';
import {SocialProvider} from './lib/social';
import {ThemeProvider, themeInitScript} from './lib/theme';
import type {LangCode} from './lib/api';
import './styles/app.css';
import './styles/beta.css';
import './styles/community.css';
import './styles/recs.css';
import './styles/player.css';

export const links = () => [
  {rel: 'preconnect', href: 'https://fonts.googleapis.com'},
  {rel: 'preconnect', href: 'https://fonts.gstatic.com', crossOrigin: 'anonymous' as const},
  {
    rel: 'stylesheet',
    href: 'https://fonts.googleapis.com/css2?family=Noto+Sans+SC:wght@400;500;700&family=IBM+Plex+Mono:wght@400;500&display=swap',
  },
];

export const meta = () => [{title: 'REEL'}, {name: 'description', content: t('seo.homeDescription')}];

export type RootData = {
  /** The page's UI language (URL prefix; the default when unprefixed). */
  lang: LangCode;
  dict: Messages;
  /** A `lang` cookie (language choice) was present when the page loaded. */
  hadChoice: boolean;
  /** Public origin for canonical / hreflang URLs. */
  origin: string;
  /** The first path segment is not a language: the shell renders 404. */
  notFound: boolean;
};

/** Path after the raw :lang segment (/zh-tw/releases/x -> /releases/x). */
const afterLang = (pathname: string, raw: string) => pathname.slice(raw.length + 1) || '/';

/**
 * Resolves the page language from the URL (PRD 8 / 13):
 *   /zh-CN/x, /EN/x, /zh-tw/x  -> 301 to the canonical URL;
 *   /foo/x (not a language)    -> 404 (rendered by the shell);
 *   /x with a `lang` cookie    -> 302 to /{cookie}/x (Vary: Cookie; crawlers
 *                                 send no cookie and get the URL's language).
 */
export async function loader({request, params}: LoaderFunctionArgs) {
  const url = new URL(request.url);
  const p = parseLangParam(params.lang);
  if (p.kind === 'redirect') throw redirect(localizePath(afterLang(url.pathname, params.lang ?? ''), p.lang) + url.search, 301);
  const cookieLang = readLangCookie(request.headers.get('Cookie'));
  if (p.kind === 'none' && cookieLang && cookieLang !== DEFAULT_LANG && isPrefixable(url.pathname)) {
    throw redirect(localizePath(url.pathname, cookieLang) + url.search, {status: 302, headers: {Vary: 'Cookie', 'Cache-Control': 'private, no-store'}});
  }
  const lang = p.kind === 'ok' ? p.lang : DEFAULT_LANG;
  const body: RootData = {lang, dict: await serverUiDict(lang), hadChoice: !!cookieLang, origin: siteOrigin(request), notFound: p.kind === 'bad'};
  return data(body, p.kind === 'bad' ? {status: 404} : undefined);
}

export async function clientLoader({request, params}: ClientLoaderFunctionArgs): Promise<RootData> {
  const url = new URL(request.url);
  const p = parseLangParam(params.lang);
  if (p.kind === 'redirect') throw redirect(localizePath(afterLang(url.pathname, params.lang ?? ''), p.lang) + url.search);
  const lang = p.kind === 'ok' ? p.lang : DEFAULT_LANG;
  const dict = await loadUiDict(lang);
  primeLang(lang, dict);
  return {lang, dict, hadChoice: !!readLangCookie(document.cookie), origin: window.location.origin, notFound: p.kind === 'bad'};
}

// The language is the only thing the root loads: keep it across navigations
// within one language.
export function shouldRevalidate({currentParams, nextParams}: ShouldRevalidateFunctionArgs) {
  return currentParams.lang !== nextParams.lang;
}

/** noindex for every page whose route does not handle SEO itself (handle.seo). */
function RobotsMeta() {
  const matches = useMatches();
  const leaf = matches[matches.length - 1];
  if ((leaf?.handle as SeoHandle | undefined)?.seo) return null;
  return <meta name="robots" content="noindex, nofollow" />;
}

export function Layout({children}: {children: ReactNode}) {
  const root = useRouteLoaderData<RootData>('root');
  // Route meta functions (rendered by <Meta> below) translate and format dates.
  if (root) primeLang(root.lang, root.dict);
  return (
    // themeInitScript sets data-theme before hydration; React must not flag it.
    <html lang={root?.lang ?? DEFAULT_LANG} suppressHydrationWarning>
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
        <meta name="color-scheme" content="dark light" />
        <script dangerouslySetInnerHTML={{__html: themeInitScript}} />
        <RobotsMeta />
        <Meta />
        <Links />
      </head>
      <body>
        {children}
        <ScrollRestoration />
        <Scripts />
      </body>
    </html>
  );
}

/** PRD 14.1 page / ref_page / slot: one page view per pathname; links may pass state {slot}. */
function AnalyticsRoutes() {
  const location = useLocation();
  const lastPath = useRef<string | null>(null);
  useEffect(() => startAnalytics(), []);
  useEffect(() => {
    // Replace-navigations on the same path (canonical slug, filters) are not page views.
    if (lastPath.current === location.pathname) return;
    lastPath.current = location.pathname;
    const slot = (location.state as {slot?: unknown} | null)?.slot;
    setPage(pageName(location.pathname), typeof slot === 'string' ? slot : undefined);
  }, [location.pathname, location.state]);
  return null;
}

export default function App() {
  const root = useLoaderData<RootData>();
  return (
    <ThemeProvider>
      <AnalyticsRoutes />
      <AuthProvider>
        <SettingsProvider>
          <I18nProvider route={root}>
            <SocialProvider>
              <ConsentProvider>
                <LangBoundary>
                  <Outlet />
                </LangBoundary>
                <CookieBanner />
              </ConsentProvider>
            </SocialProvider>
          </I18nProvider>
        </SettingsProvider>
      </AuthProvider>
    </ThemeProvider>
  );
}

export function HydrateFallback() {
  return <div className="boot" aria-busy="true" aria-label={t('app.loading')} />;
}

export function ErrorBoundary() {
  const error = useRouteError();
  const title = isRouteErrorResponse(error) && error.status === 404 ? t('error.notFound') : t('error.generic');
  return (
    <main className="fatal">
      <h1>{title}</h1>
      <p>{t('error.retryHint')}</p>
      <Link to="/" className="btn btn-primary">
        {t('error.backHome')}
      </Link>
    </main>
  );
}
