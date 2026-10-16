import type {Config} from '@react-router/dev/config';

// Server rendering (PRD 13 SEO; tech plan 3.1): public pages are rendered by
// the Node server (server.mjs) with data from the Go API, then hydrate into
// the same client app. Browser-only state (tokens, theme, consent, player,
// analytics) is read after hydration, never during render.
// Build output: dist/client (static assets) and dist/server/index.js.
export default {
  appDirectory: 'app',
  buildDirectory: 'dist',
  ssr: true,
} satisfies Config;
