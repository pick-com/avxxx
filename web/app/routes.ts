import {type RouteConfig, layout, route} from '@react-router/dev/routes';

// PRD 8 / 13: every app page has one URL per language. `:lang?` is an
// optional first segment (/en/releases/x, /zh-TW/discover); without it the
// page is 简体中文. The root loader validates it (lib/langs.ts
// parseLangParam): /zh-CN/... and non-canonical spellings (/EN, /zh-tw)
// answer 301, anything that is not a language is a 404. The back office has
// no prefixes.
const L = (path: string) => (path ? `:lang?/${path}` : ':lang?');

export default [
  // Crawler files (resource routes, no UI).
  route('robots.txt', 'routes/robots.ts'),
  route('sitemap.xml', 'routes/sitemap-index.ts'),
  route('sitemaps/:file', 'routes/sitemap-file.ts'),

  layout('routes/shell.tsx', [
    route(L(''), 'routes/home.tsx', {id: 'routes/home'}),
    route(L('releases/:slug'), 'routes/release.tsx'),
    route(L('performers/:slug'), 'routes/performer.tsx'),
    route(L('search'), 'routes/search.tsx'),
    route(L('discover'), 'routes/discover.tsx'),
    route(L('trending'), 'routes/trending.tsx'),
    route(L('tonight'), 'routes/tonight.tsx'),
    route(L('following'), 'routes/following.tsx'),
    route(L('me'), 'routes/me.tsx'),
    route(L('history'), 'routes/history.tsx'),
    route(L('favorites'), 'routes/favorites.tsx'),
    route(L('later'), 'routes/later.tsx'),
    route(L('reservations'), 'routes/reservations.tsx'),
    route(L('notifications'), 'routes/notifications.tsx'),
    route(L('settings'), 'routes/settings.tsx'),
    // Deep link used by notification emails (internal/notify.SettingsPath).
    route(L('settings/notifications'), 'routes/settings-notifications.tsx'),
    route(L('legal/:doc'), 'routes/legal.tsx'),
    route('*', 'routes/not-found.tsx'),
  ]),
  route(L('login'), 'routes/login.tsx'),
  route(L('welcome'), 'routes/welcome.tsx'),
  // Back office (PRD 11): AdminNav shell with the shared token and 操作人.
  layout('routes/admin-layout.tsx', [
    route('admin', 'routes/admin-dashboard.tsx'),
    route('admin/videos', 'routes/admin-videos.tsx'),
    route('admin/videos/:id', 'routes/admin-video.tsx'),
    route('admin/pipeline', 'routes/admin-pipeline.tsx'),
    route('admin/audit', 'routes/admin-audit.tsx'),
    route('admin/moderation', 'routes/admin-moderation.tsx'),
    route('admin/i18n', 'routes/admin-i18n.tsx'),
    route('admin/review', 'routes/admin-review.tsx'),
    route('admin/dedupe', 'routes/admin-dedupe.tsx'),
    route('admin/actors', 'routes/admin-actors.tsx'),
    route('admin/search', 'routes/admin-search.tsx'),
    route('admin/field-policies', 'routes/admin-field-policies.tsx'),
    route('admin/media-links', 'routes/admin-media-links.tsx'),
  ]),
] satisfies RouteConfig;
