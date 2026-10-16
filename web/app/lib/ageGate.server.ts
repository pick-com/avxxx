// Server side of the site-entry 18+ gate (lib/ageGate.ts): verifies the `ag`
// cookie set by POST /api/age-gate (internal/api/agegate.go) with the same
// HMAC key, so SSR can decide before rendering anything.
//
// AGE_GATE_KEY  shared with the API. Unset (local development): the same
//               development key as the API (ageGateDevKey there).
//
// Cookie value: v1.<ok|denied>.<expiry unix>.<base64url HMAC-SHA256(key, "v1.<verdict>.<expiry>")>.
// A missing, malformed, tampered or expired cookie is `none`.

import {createHmac, randomBytes, timingSafeEqual} from 'node:crypto';
import {isGateExempt, type GateState} from './ageGate';

const COOKIE = 'ag';
const DEV_KEY = 'avxxx-dev-age-gate-key';

// publicHTTPS mirrors internal/api/agegate.go: an https base URL for a
// non-local host means a real deployment, where the well-known DEV_KEY would
// let anyone forge an "ok" cookie.
function publicHTTPS(base: string | undefined): boolean {
  try {
    const u = new URL((base || '').trim());
    const h = u.hostname;
    return u.protocol === 'https:' && h !== '' && h !== 'localhost' && h !== '127.0.0.1' && h !== '[::1]' &&
      !h.endsWith('.localhost') && !h.endsWith('.test');
  } catch {
    return false;
  }
}

let warned = false;
let failClosedKey: string | null = null;
function key(): string {
  const k = (process.env.AGE_GATE_KEY || '').trim();
  if (k) return k;
  if (publicHTTPS(process.env.PUBLIC_BASE_URL) || publicHTTPS(process.env.BASE_URL)) {
    // Fail closed: a random per-process key no cookie can match, so every
    // visitor stays at the gate until AGE_GATE_KEY is configured.
    if (!failClosedKey) {
      failClosedKey = randomBytes(32).toString('hex');
      console.error('AGE_GATE_KEY is not set on a public https deployment: the age gate refuses every cookie until it is configured');
    }
    return failClosedKey;
  }
  if (!warned) {
    warned = true;
    console.warn('AGE_GATE_KEY is not set: age gate cookies use the development key');
  }
  return DEV_KEY;
}

function cookieValue(header: string | null, name: string): string | null {
  if (!header) return null;
  for (const part of header.split(';')) {
    const i = part.indexOf('=');
    if (i > 0 && part.slice(0, i).trim() === name) return part.slice(i + 1).trim();
  }
  return null;
}

/** Verifies a gate cookie value at `now` (ms). */
export function parseGateCookie(value: string | null, now = Date.now(), secret = key()): {state: GateState; until?: string} {
  const parts = (value ?? '').split('.');
  if (parts.length !== 4 || parts[0] !== 'v1' || (parts[1] !== 'ok' && parts[1] !== 'denied') || !/^\d+$/.test(parts[2])) return {state: 'none'};
  const want = createHmac('sha256', secret).update(parts.slice(0, 3).join('.')).digest('base64url');
  const got = Buffer.from(parts[3]);
  if (got.length !== Buffer.byteLength(want) || !timingSafeEqual(got, Buffer.from(want))) return {state: 'none'};
  const exp = Number(parts[2]) * 1000;
  if (!(now < exp)) return {state: 'none'};
  return {state: parts[1] as GateState, until: new Date(exp).toISOString()};
}

/** The request's gate verdict (from its Cookie header). */
export function gateOf(request: Request): {state: GateState; until?: string} {
  return parseGateCookie(cookieValue(request.headers.get('Cookie'), COOKIE));
}

/**
 * True when the page will show the gate (or the restricted page) instead of
 * its content: loaders then fetch only what the document head needs (title,
 * description, canonical, JSON-LD) and nothing for the page body.
 */
export function isGated(request: Request): boolean {
  return !isGateExempt(new URL(request.url).pathname) && gateOf(request).state !== 'ok';
}
