import {useEffect, useRef, type ReactNode} from 'react';
import {Link, Links, Meta, Outlet, Scripts, ScrollRestoration, isRouteErrorResponse, useLocation, useRouteError} from 'react-router';
import {CookieBanner} from './components/CookieBanner';
import {pageName, setPage, startAnalytics} from './lib/analytics';
import {AuthProvider} from './lib/auth';
import {I18nProvider, LangBoundary, t} from './lib/i18n';
import {ConsentProvider} from './lib/consent';
import {SettingsProvider} from './lib/settings';
import {SocialProvider} from './lib/social';
import {ThemeProvider, themeInitScript} from './lib/theme';
import './styles/app.css';
import './styles/beta.css';

export const links = () => [
  {rel: 'preconnect', href: 'https://fonts.googleapis.com'},
  {rel: 'preconnect', href: 'https://fonts.gstatic.com', crossOrigin: 'anonymous' as const},
  {
    rel: 'stylesheet',
    href: 'https://fonts.googleapis.com/css2?family=Noto+Sans+SC:wght@400;500;700&family=IBM+Plex+Mono:wght@400;500&display=swap',
  },
];

export const meta = () => [{title: 'REEL'}, {name: 'description', content: 'REEL：看视频时随时认识演员，认识演员后一路看下去。'}];

export function Layout({children}: {children: ReactNode}) {
  return (
    <html lang="zh-CN">
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
        <meta name="color-scheme" content="dark light" />
        <script dangerouslySetInnerHTML={{__html: themeInitScript}} />
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
  return (
    <ThemeProvider>
      <AnalyticsRoutes />
      <AuthProvider>
        <SettingsProvider>
          <I18nProvider>
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
