import {type RouteConfig, index, layout, route} from '@react-router/dev/routes';

export default [
  layout('routes/shell.tsx', [
    index('routes/home.tsx'),
    route('releases/:slug', 'routes/release.tsx'),
    route('performers/:slug', 'routes/performer.tsx'),
    route('search', 'routes/search.tsx'),
    route('discover', 'routes/discover.tsx'),
    route('following', 'routes/following.tsx'),
    route('me', 'routes/me.tsx'),
    route('history', 'routes/history.tsx'),
    route('favorites', 'routes/favorites.tsx'),
    route('later', 'routes/later.tsx'),
    route('reservations', 'routes/reservations.tsx'),
    route('notifications', 'routes/notifications.tsx'),
    route('settings', 'routes/settings.tsx'),
    route('legal/:doc', 'routes/legal.tsx'),
    // PRD 8 language prefix as an entry URL (/en/..., /zh-TW/...).
    route(':lang/*', 'routes/lang-prefix.tsx'),
    route('*', 'routes/not-found.tsx'),
  ]),
  route('login', 'routes/login.tsx'),
  route('welcome', 'routes/welcome.tsx'),
  route('admin/review', 'routes/admin-review.tsx'),
  route('admin/dedupe', 'routes/admin-dedupe.tsx'),
] satisfies RouteConfig;
