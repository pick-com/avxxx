import {reactRouter} from '@react-router/dev/vite';
import {defineConfig} from 'vite';

export default defineConfig({
  plugins: [reactRouter()],
  server: {
    proxy: {
      '/api': process.env.API_PROXY_TARGET || 'http://127.0.0.1:8080'
    }
  }
});
