import {readFileSync} from 'node:fs';
import {reactRouter} from '@react-router/dev/vite';
import {defineConfig} from 'vite';

// app_ver for analytics events (PRD 14.1) comes from package.json.
const {version} = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8'));

export default defineConfig({
  plugins: [reactRouter()],
  define: {
    __APP_VERSION__: JSON.stringify(version),
  },
  server: {
    proxy: {
      '/api': process.env.API_PROXY_TARGET || 'http://127.0.0.1:8080'
    }
  }
});
