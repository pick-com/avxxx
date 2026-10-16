import type {Config} from '@react-router/dev/config';

// SPA mode for now. Route modules avoid touching browser globals during render,
// so SSR can be enabled later (tech plan 3.1 / PRD 13 SEO) by flipping `ssr`
// and adding a server entry.
export default {
  appDirectory: 'app',
  buildDirectory: 'dist',
  ssr: false,
} satisfies Config;
