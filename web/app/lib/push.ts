import {useCallback, useEffect, useState} from 'react';
import {api, ApiError, tokens} from './api';

// Web Push in the browser (PRD 17; internal/api/push.go, tech plan 3.7).
//
//   GET    /api/push/vapid-public-key     {enabled, public_key}; enabled
//                                         false (or an older backend's 404) =
//                                         push not configured → option hidden.
//   POST   /api/me/push-subscriptions     PushSubscription.toJSON() (201).
//   DELETE /api/me/push-subscriptions     {endpoint} (one device).
//
// The service worker is public/sw.js (scope "/"). Permission is requested
// only from enable(), which callers run from a click handler. A device that
// is already subscribed re-sends its subscription once per page load, so a
// rotated endpoint or a database reset heals itself.

export type PushAvailability = 'checking' | 'unsupported' | 'disabled' | 'available';

const SW_URL = '/sw.js';

export function pushSupported(): boolean {
  return (
    typeof window !== 'undefined' &&
    window.isSecureContext !== false &&
    'serviceWorker' in navigator &&
    'PushManager' in window &&
    'Notification' in window
  );
}

let keyPromise: Promise<string | null> | null = null;

/** VAPID public key (base64url), or null when push is not configured. */
export function vapidPublicKey(): Promise<string | null> {
  keyPromise ??= api<{enabled?: boolean; public_key?: string}>('push/vapid-public-key').then(
    d => (d?.enabled !== false && typeof d?.public_key === 'string' && d.public_key ? d.public_key : null),
    error => {
      // Only a definite "not configured" is cached; a network error retries next time.
      if (!(error instanceof ApiError && error.status === 404)) keyPromise = null;
      return null;
    },
  );
  return keyPromise;
}

function keyBytes(base64url: string): Uint8Array<ArrayBuffer> {
  const padded = (base64url + '='.repeat((4 - (base64url.length % 4)) % 4)).replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(padded);
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

async function registration(): Promise<ServiceWorkerRegistration> {
  const existing = await navigator.serviceWorker.getRegistration('/');
  if (existing) return existing;
  await navigator.serviceWorker.register(SW_URL, {scope: '/'});
  return navigator.serviceWorker.ready;
}

async function currentSubscription(): Promise<PushSubscription | null> {
  const reg = await navigator.serviceWorker.getRegistration('/');
  return reg ? reg.pushManager.getSubscription() : null;
}

const saveSubscription = (sub: PushSubscription) => api<{subscribed: boolean}>('me/push-subscriptions', {method: 'POST', body: sub.toJSON()});

export class PushDeniedError extends Error {
  constructor() {
    super('permission denied');
    this.name = 'PushDeniedError';
  }
}

/** Ask for permission (call from a user gesture), subscribe and store the subscription. */
export async function enablePush(): Promise<void> {
  const key = await vapidPublicKey();
  if (!key) throw new ApiError('push disabled', 404);
  const permission = Notification.permission === 'granted' ? 'granted' : await Notification.requestPermission();
  if (permission !== 'granted') throw new PushDeniedError();
  const reg = await registration();
  let sub = await reg.pushManager.getSubscription();
  // A subscription made with another server key cannot receive our pushes.
  const wanted = keyBytes(key);
  const current = sub?.options.applicationServerKey;
  if (sub && current && !sameBytes(new Uint8Array(current), wanted)) {
    await sub.unsubscribe().catch(() => false);
    sub = null;
  }
  sub ??= await reg.pushManager.subscribe({userVisibleOnly: true, applicationServerKey: wanted});
  await saveSubscription(sub);
}

/** Remove this device's subscription on the server and in the browser. */
export async function disablePush(): Promise<void> {
  const sub = await currentSubscription();
  if (!sub) return;
  await api<{deleted: number}>('me/push-subscriptions', {method: 'DELETE', body: {endpoint: sub.endpoint}});
  await sub.unsubscribe().catch(() => false);
}

function sameBytes(a: Uint8Array, b: Uint8Array) {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}

let resynced = false;

export type PushState = {
  availability: PushAvailability;
  permission: NotificationPermission | 'unsupported';
  /** This browser has a push subscription. */
  subscribed: boolean;
  busy: boolean;
  /** 'denied' | 'failed' | '' */
  error: '' | 'denied' | 'failed';
  enable: () => Promise<boolean>;
  disable: () => Promise<boolean>;
};

/** Push state of this browser for the signed-in user. */
export function usePush(): PushState {
  const [availability, setAvailability] = useState<PushAvailability>('checking');
  const [permission, setPermission] = useState<PushState['permission']>('default');
  const [subscribed, setSubscribed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<PushState['error']>('');

  useEffect(() => {
    let cancelled = false;
    if (!pushSupported()) {
      setAvailability('unsupported');
      setPermission('unsupported');
      return;
    }
    setPermission(Notification.permission);
    void (async () => {
      const key = await vapidPublicKey();
      if (cancelled) return;
      if (!key) {
        setAvailability('disabled');
        return;
      }
      setAvailability('available');
      const sub = await currentSubscription().catch(() => null);
      if (cancelled) return;
      setSubscribed(!!sub && Notification.permission === 'granted');
      if (sub && Notification.permission === 'granted' && !resynced && tokens.user()) {
        resynced = true;
        saveSubscription(sub).catch(() => undefined);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const enable = useCallback(async () => {
    setBusy(true);
    setError('');
    try {
      await enablePush();
      setSubscribed(true);
      return true;
    } catch (cause) {
      setError(cause instanceof PushDeniedError ? 'denied' : 'failed');
      return false;
    } finally {
      if (pushSupported()) setPermission(Notification.permission);
      setBusy(false);
    }
  }, []);

  const disable = useCallback(async () => {
    setBusy(true);
    setError('');
    try {
      await disablePush();
      setSubscribed(false);
      return true;
    } catch {
      setError('failed');
      return false;
    } finally {
      setBusy(false);
    }
  }, []);

  return {availability, permission, subscribed, busy, error, enable, disable};
}
