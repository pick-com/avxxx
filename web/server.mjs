// Production web server (PRD 13 SEO): serves the built client assets and
// server-renders every other request with React Router (dist/server).
// node:http only, no framework: `node server.mjs` after `npm run build`.
//
// Environment:
//   PORT / HOST         listen address (default 3000 / 0.0.0.0)
//   API_INTERNAL_URL    Go API base for SSR loaders and the /api proxy
//                       (default http://127.0.0.1:8080; compose http://api:8080)
//   API_PROXY           "false" disables forwarding /api/* to API_INTERNAL_URL.
//                       Production should route /api/ to the API in the
//                       reverse proxy instead (the API sees the client address
//                       only then); the proxy here is for single-upstream setups
//                       and local runs.
//   PUBLIC_BASE_URL     public origin for canonical / hreflang / sitemap URLs
//   LOG_REQUESTS        "false" silences the per-request JSON log line
//
// Paths: /healthz (liveness, no API call), /assets/* (immutable), other files
// in dist/client (1 h), /api/* (proxy, see above), everything else -> SSR.

import {createReadStream, statSync} from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createRequestListener} from '@react-router/node';

const dir = path.dirname(fileURLToPath(import.meta.url));
const clientDir = path.join(dir, 'dist', 'client');
const build = await import(path.join(dir, 'dist', 'server', 'index.js'));

const port = Number(process.env.PORT || 3000);
const host = process.env.HOST || '0.0.0.0';
const apiBase = new URL(process.env.API_INTERNAL_URL || 'http://127.0.0.1:8080');
const proxyApi = process.env.API_PROXY !== 'false';
const logRequests = process.env.LOG_REQUESTS !== 'false';

const MIME = {
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json',
  '.map': 'application/json',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.webp': 'image/webp',
  '.avif': 'image/avif',
  '.gif': 'image/gif',
  '.ico': 'image/x-icon',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.txt': 'text/plain; charset=utf-8',
  '.xml': 'application/xml; charset=utf-8',
  '.webmanifest': 'application/manifest+json',
};

/** A file under dist/client for this URL path, or null. */
function staticFile(pathname) {
  let decoded;
  try {
    decoded = decodeURIComponent(pathname);
  } catch {
    return null;
  }
  if (decoded.includes('\0')) return null;
  const file = path.normalize(path.join(clientDir, decoded));
  if (!file.startsWith(clientDir + path.sep)) return null;
  try {
    const st = statSync(file);
    return st.isFile() ? {file, size: st.size, mtime: st.mtime} : null;
  } catch {
    return null;
  }
}

function serveStatic(req, res, hit, pathname) {
  const ext = path.extname(hit.file).toLowerCase();
  const headers = {
    'Content-Type': MIME[ext] || 'application/octet-stream',
    'Content-Length': hit.size,
    'Last-Modified': hit.mtime.toUTCString(),
    'X-Content-Type-Options': 'nosniff',
    // Hashed build output never changes; the service worker must always be fresh.
    'Cache-Control': pathname.startsWith('/assets/') ? 'public, max-age=31536000, immutable' : pathname === '/sw.js' ? 'no-cache' : 'public, max-age=3600',
  };
  if (req.headers['if-modified-since'] && new Date(req.headers['if-modified-since']) >= new Date(hit.mtime.toUTCString())) {
    res.writeHead(304, headers);
    res.end();
    return;
  }
  res.writeHead(200, headers);
  if (req.method === 'HEAD') res.end();
  else createReadStream(hit.file).pipe(res);
}

const HOP = new Set(['connection', 'keep-alive', 'proxy-connection', 'transfer-encoding', 'upgrade', 'te', 'trailer', 'host']);

function proxy(req, res) {
  const headers = {};
  for (const [k, v] of Object.entries(req.headers)) if (!HOP.has(k)) headers[k] = v;
  const remote = req.socket.remoteAddress || '';
  headers['x-forwarded-for'] = req.headers['x-forwarded-for'] ? `${req.headers['x-forwarded-for']}, ${remote}` : remote;
  headers['x-forwarded-host'] = req.headers.host || '';
  headers['x-forwarded-proto'] = req.headers['x-forwarded-proto'] || 'http';
  headers.host = apiBase.host;
  const client = apiBase.protocol === 'https:' ? https : http;
  const upstream = client.request(
    {protocol: apiBase.protocol, hostname: apiBase.hostname, port: apiBase.port, method: req.method, path: req.url, headers},
    up => {
      res.writeHead(up.statusCode || 502, up.headers);
      up.pipe(res);
    },
  );
  upstream.on('error', () => {
    if (!res.headersSent) res.writeHead(502, {'Content-Type': 'application/json'});
    res.end('{"error":"api unavailable"}');
  });
  req.pipe(upstream);
}

const ssr = createRequestListener({build, mode: process.env.NODE_ENV || 'production'});

const server = http.createServer((req, res) => {
  const started = process.hrtime.bigint();
  const url = new URL(req.url || '/', 'http://localhost');
  if (logRequests) {
    res.on('finish', () => {
      const ms = Number(process.hrtime.bigint() - started) / 1e6;
      process.stdout.write(
        JSON.stringify({time: new Date().toISOString(), msg: 'http_request', method: req.method, path: url.pathname, status: res.statusCode, duration_ms: Math.round(ms * 10) / 10}) + '\n',
      );
    });
  }
  if (url.pathname === '/healthz') {
    res.writeHead(200, {'Content-Type': 'text/plain', 'Cache-Control': 'no-store'});
    res.end('ok\n');
    return;
  }
  if (url.pathname === '/api' || url.pathname.startsWith('/api/')) {
    if (proxyApi) return proxy(req, res);
    res.writeHead(404, {'Content-Type': 'application/json'});
    res.end('{"error":"not found"}');
    return;
  }
  if (req.method === 'GET' || req.method === 'HEAD') {
    const hit = url.pathname !== '/' && staticFile(url.pathname);
    if (hit) return serveStatic(req, res, hit, url.pathname);
    if (url.pathname.startsWith('/assets/')) {
      res.writeHead(404, {'Content-Type': 'text/plain', 'Cache-Control': 'no-store'});
      res.end('not found\n');
      return;
    }
  }
  ssr(req, res);
});

server.listen(port, host, () => {
  process.stdout.write(JSON.stringify({msg: 'web listening', addr: `${host}:${port}`, api: apiBase.origin, api_proxy: proxyApi}) + '\n');
});

const stop = () => server.close(() => process.exit(0));
process.on('SIGTERM', stop);
process.on('SIGINT', stop);
