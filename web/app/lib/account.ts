// Account and auth client (internal/api/auth.go; PRD 7, 15, 20; tech plan 3.6).
//
// Kept out of lib/api.ts so the shared client only gains the few shapes it
// needs. Routes under me/ and auth/me get the user token from api(); the
// auth/* routes that need it (logout, linking a provider) pass it explicitly.

import {api, ApiError, tokens, type AuthResult, type User} from './api';
import {TERMS_VERSION} from './legal';

/** GET /api/auth/me on a backend with migration 037. Older backends return only id / email. */
export type Me = User & {
  display_name?: string;
  has_password?: boolean;
  created_at?: string;
  onboarded?: boolean;
  /** Latest accepted terms version; "" = none. undefined = backend without terms_acceptances. */
  terms_version?: string;
  providers?: string[];
  /** The 18+ confirmation is on file (migration 046); false = one-time prompt. undefined = older backend. */
  adult_confirmed?: boolean;
};

/**
 * 18+ confirmation sent with registration, an OAuth sign-up and the one-time
 * prompt: the checkbox, plus birth year and month when the gate cookie is not
 * already ok (internal/api/agegate.go adultCheck).
 */
export type AdultInput = {adult_confirmed: boolean; birth_year?: number; birth_month?: number};

export type SignInResult = AuthResult & {remember?: boolean; expires_at?: string; deletion_cancelled?: boolean; created?: boolean};

export type SessionInfo = {
  id: number;
  device: string;
  user_agent: string;
  method: string;
  remember: boolean;
  created_at: string;
  last_seen_at: string;
  expires_at: string;
  current: boolean;
};

export type Identity = {provider: string; email: string; linked_at: string; last_login_at: string | null};
export type OAuthProvider = 'google' | 'apple';

const bearer = (): Record<string, string> => {
  const token = tokens.user();
  return token ? {Authorization: `Bearer ${token}`} : {};
};

export const loginWith = (email: string, password: string, remember: boolean) =>
  api<SignInResult>('auth/login', {method: 'POST', body: {email, password, remember}});

export const registerWith = (email: string, password: string, displayName: string, remember: boolean, termsVersion: string, adult: AdultInput) =>
  api<SignInResult>('auth/register', {method: 'POST', body: {email, password, display_name: displayName, remember, terms_version: termsVersion, ...adult}});

/** POST /api/me/adult-confirm: the one-time 18+ confirmation for older accounts. 403 = under 18. */
export const confirmAdult = (adult: AdultInput) => api<{adult_confirmed: boolean}>('me/adult-confirm', {method: 'POST', body: adult});

export const getAccount = (signal?: AbortSignal) => api<Me>('auth/me', {signal});

/** Revokes the session on the server. Best effort: callers clear the client either way. */
export async function logoutServer(token: string | null): Promise<void> {
  if (!token) return;
  try {
    await api<{ok: boolean}>('auth/logout', {method: 'POST', headers: {Authorization: `Bearer ${token}`}, keepalive: true});
  } catch {
    /* expired already, offline, or an older backend: the token is dropped locally anyway */
  }
}

export const forgotPassword = (email: string) => api<{ok: boolean}>('auth/password/forgot', {method: 'POST', body: {email}});
export const resetPassword = (token: string, password: string) =>
  api<{ok: boolean}>('auth/password/reset', {method: 'POST', body: {token, password}});

/** Configured Google / Apple providers; [] on older backends. */
export async function getAuthProviders(signal?: AbortSignal): Promise<OAuthProvider[]> {
  try {
    const r = await api<{providers: string[]}>('auth/providers', {signal});
    return (r?.providers ?? []).filter((p): p is OAuthProvider => p === 'google' || p === 'apple');
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause;
    return [];
  }
}

/** Starts a sign-in (or, with link, links the provider to the signed-in user) and navigates to the provider. */
export async function startOAuth(provider: OAuthProvider, opts: {next?: string; remember?: boolean; link?: boolean; adult?: AdultInput}) {
  const r = await api<{url: string}>(`auth/oauth/${provider}/start`, {
    method: 'POST',
    body: {next: opts.next ?? '/', remember: opts.remember ?? true, link: !!opts.link, ...opts.adult},
    headers: opts.link ? bearer() : undefined,
  });
  if (!r?.url) throw new ApiError('no redirect', 500);
  window.location.assign(r.url);
}

export const exchangeOAuthCode = (code: string) => api<SignInResult>('auth/oauth/exchange', {method: 'POST', body: {code}});

export const updateProfile = (displayName: string) =>
  api<{display_name: string}>('me/profile', {method: 'PATCH', body: {display_name: displayName}});
export const changePassword = (current: string, next: string) =>
  api<{ok: boolean}>('me/password', {method: 'POST', body: {current_password: current, new_password: next}});
export const getSessions = (signal?: AbortSignal) => api<{items: SessionInfo[]}>('me/sessions', {signal}).then(r => r?.items ?? []);
export const revokeSession = (id: number) => api<{revoked: boolean}>(`me/sessions/${id}`, {method: 'DELETE'});
export const revokeOtherSessions = () => api<{revoked: number}>('me/sessions/revoke-others', {method: 'POST'});
export const acceptTerms = (version: string) => api<{terms_version: string}>('me/terms', {method: 'POST', body: {version}});
export const getIdentities = (signal?: AbortSignal) =>
  api<{items: Identity[]; available: string[]}>('me/identities', {signal}).then(r => ({items: r?.items ?? [], available: r?.available ?? []}));
export const unlinkIdentity = (provider: string) => api<{ok: boolean}>(`me/identities/${encodeURIComponent(provider)}`, {method: 'DELETE'});
export const requestAccountDeletion = (password: string) =>
  api<{purge_after: string}>('me/deletion', {method: 'POST', body: {password, confirm: true}});
export const getInterests = (signal?: AbortSignal) => api<{tags: string[]}>('me/interests', {signal}).then(r => r?.tags ?? []);
export const saveInterests = (tags: string[], onboarded: boolean) =>
  api<{tags: string[]}>('me/interests', {method: 'PUT', body: {tags, onboarded}});

/** Downloads the JSON export (GET /api/me/export) as a file. */
export async function downloadDataExport(): Promise<void> {
  const data = await api<unknown>('me/export');
  const blob = new Blob([JSON.stringify(data, null, 2)], {type: 'application/json'});
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `reel-data-${new Date().toISOString().slice(0, 10)}.json`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

let termsVersionPromise: Promise<string> | null = null;

/**
 * Current terms version: the legal_documents backend (GET
 * /api/legal/terms/current-version) when it exists, else the version string
 * bundled in lib/legal.ts.
 */
export function currentTermsVersion(): Promise<string> {
  if (!termsVersionPromise) {
    termsVersionPromise = api<{version?: string}>('legal/terms/current-version').then(
      r => (typeof r?.version === 'string' && r.version ? r.version : TERMS_VERSION),
      () => TERMS_VERSION,
    );
  }
  return termsVersionPromise;
}

// ---- Remember me ----
//
// A remembered sign-in keeps the token in localStorage for the server's 30
// days. Without it the token is tied to the browser session: a cookie with no
// expiry marks the session, and a token flagged ephemeral is dropped once the
// browser was closed (the cookie is gone). The server bounds it to 24 h.

const EPHEMERAL_KEY = 'user_token_ephemeral';
const SESSION_COOKIE = 'reel_browser_session';

export function markSessionKind(remember: boolean) {
  try {
    if (remember) window.localStorage.removeItem(EPHEMERAL_KEY);
    else window.localStorage.setItem(EPHEMERAL_KEY, '1');
  } catch {
    /* storage unavailable: the token is not persisted either */
  }
  if (!remember) document.cookie = `${SESSION_COOKIE}=1; Path=/; SameSite=Lax${location.protocol === 'https:' ? '; Secure' : ''}`;
}

/** True when a browser-session token outlived its browser session. */
export function ephemeralTokenExpired(): boolean {
  try {
    if (window.localStorage.getItem(EPHEMERAL_KEY) !== '1') return false;
  } catch {
    return false;
  }
  return !document.cookie.split(';').some(c => c.trim().startsWith(`${SESSION_COOKIE}=`));
}

export function clearSessionKind() {
  try {
    window.localStorage.removeItem(EPHEMERAL_KEY);
  } catch {
    /* nothing stored */
  }
}
