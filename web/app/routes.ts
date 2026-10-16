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
    route('*', 'routes/not-found.tsx'),
  ]),
  route('login', 'routes/login.tsx'),
  route('admin/review', 'routes/admin-review.tsx'),
] satisfies RouteConfig;
