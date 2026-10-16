import {createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode} from 'react';
import {track, type ConsentCategory} from './analytics';
import {ApiError} from './api';
import {useAuth} from './auth';
import {getMyConsent, postConsent, type ConsentSource, type ServerConsent} from './compliance';

// Cookie consent (PRD 15, CookieBanner / MobileConsent). Four categories:
// 必要 (always on), 分析, 个性化广告, 个性化推荐. The choice is kept in this
// browser (localStorage) and every saved choice is also recorded server-side
// (POST /api/consent → consent_records, append-only): with a random
// per-browser consent id (stored by the server only as a keyed hash), plus
// the account when logged in. A logged-in user's latest choice follows them
// to other devices (GET /api/me/consent): the newer of the local and the
// server choice wins.
//
// GPC: a browser that sends Global Privacy Control (navigator.globalPrivacyControl)
// has personalized ads forced off and the switch locked (PRD 14.5); the
// server enforces the same (Sec-GPC header, gpc field).
//
// consentField() is the PRD 14.1 `consent` array for analytics events
// (internal/events: necessary / analytics / personalized_ads); lib/analytics
// sends it on every event, and saving a choice emits consent_change.

export type ConsentChoice = {
  analytics: boolean;
  personalized_ads: boolean;
  personalized_recs: boolean;
};

type Stored = ConsentChoice & {v: 1; gpc: boolean; saved_at: string};

const KEY = 'consent_v1';
// Random id that lets the server tell one browser's records apart. It is a
// necessary-category identifier (proof of the choice), unrelated to the
// analytics device id, and never sent anywhere else.
const DEVICE_KEY = 'consent_device_id';

export function detectGpc(): boolean {
  try {
    return (navigator as Navigator & {globalPrivacyControl?: boolean}).globalPrivacyControl === true;
  } catch {
    return false;
  }
}

function read(): Stored | null {
  try {
    const raw = window.localStorage.getItem(KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<Stored>;
    if (v?.v !== 1) return null;
    return {
      v: 1,
      analytics: !!v.analytics,
      personalized_ads: !!v.personalized_ads,
      personalized_recs: !!v.personalized_recs,
      gpc: !!v.gpc,
      saved_at: String(v.saved_at ?? ''),
    };
  } catch {
    return null;
  }
}

function write(stored: Stored) {
  try {
    window.localStorage.setItem(KEY, JSON.stringify(stored));
  } catch {
    /* not persisted: the banner returns next visit */
  }
}

function randomId(): string {
  try {
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  } catch {
    /* insecure context */
  }
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
}

function deviceId(): string {
  try {
    let id = window.localStorage.getItem(DEVICE_KEY);
    if (!id || !/^[A-Za-z0-9_-]{16,128}$/.test(id)) {
      id = randomId();
      window.localStorage.setItem(DEVICE_KEY, id);
    }
    return id;
  } catch {
    return randomId(); // storage blocked: a one-off id for this record
  }
}

/** Records a choice server-side; failures only cost the server copy (the local choice stands). */
function record(stored: Stored, source: ConsentSource) {
  postConsent({
    analytics: stored.analytics,
    personalized_ads: stored.personalized_ads,
    personalized_recs: stored.personalized_recs,
    gpc: stored.gpc,
    source,
    device_id: deviceId(),
  }).catch(() => {});
}

const fromServer = (s: ServerConsent, gpcNow: boolean): Stored => ({
  v: 1,
  analytics: s.analytics,
  personalized_ads: s.personalized_ads && !gpcNow,
  personalized_recs: s.personalized_recs,
  gpc: s.gpc || gpcNow,
  saved_at: s.saved_at,
});

const time = (iso: string) => {
  const n = Date.parse(iso);
  return Number.isNaN(n) ? 0 : n;
};

/** PRD 14.1 `consent` field for analytics events, from the stored choice (necessary only before a choice). */
export function consentField(): string[] {
  const c = read();
  const out = ['necessary'];
  if (c?.analytics) out.push('analytics');
  if (c?.personalized_ads && !detectGpc()) out.push('personalized_ads');
  return out;
}

type ConsentValue = {
  /** null until the visitor has chosen (banner shows). */
  choice: Stored | null;
  ready: boolean;
  gpc: boolean;
  prefsOpen: boolean;
  openPrefs: () => void;
  closePrefs: () => void;
  /** Saves a choice; `source` says where it was made (banner or preferences sheet). */
  save: (choice: ConsentChoice, source?: ConsentSource) => void;
};

const ConsentContext = createContext<ConsentValue>({
  choice: null,
  ready: false,
  gpc: false,
  prefsOpen: false,
  openPrefs: () => {},
  closePrefs: () => {},
  save: () => {},
});

export function ConsentProvider({children}: {children: ReactNode}) {
  const {status} = useAuth();
  const [choice, setChoice] = useState<Stored | null>(null);
  const [ready, setReady] = useState(false);
  const [gpc, setGpc] = useState(false);
  const [prefsOpen, setPrefsOpen] = useState(false);

  useEffect(() => {
    const g = detectGpc();
    setGpc(g);
    let stored = read();
    // A GPC signal that appears after an earlier "accept all" still wins,
    // and the change is recorded.
    if (stored && g && stored.personalized_ads) {
      stored = {...stored, personalized_ads: false, gpc: true, saved_at: new Date().toISOString()};
      write(stored);
      record(stored, 'gpc');
    }
    setChoice(stored);
    setReady(true);
  }, []);

  // Cross-device sync for logged-in users: adopt a newer server choice, or
  // upload a newer local one (e.g. chosen before logging in).
  useEffect(() => {
    if (status !== 'user' || !ready) return;
    const ctrl = new AbortController();
    const g = detectGpc();
    getMyConsent(ctrl.signal).then(
      server => {
        if (ctrl.signal.aborted) return;
        const local = read();
        if (!local || time(server.saved_at) > time(local.saved_at)) {
          const adopted = fromServer(server, g);
          write(adopted);
          setChoice(adopted);
          if (g && server.personalized_ads) record(adopted, 'gpc');
        } else if (time(local.saved_at) > time(server.saved_at)) {
          record(local, 'sync');
        }
      },
      e => {
        if (ctrl.signal.aborted) return;
        const local = read();
        if (e instanceof ApiError && e.status === 404 && local) record(local, 'sync');
      },
    );
    return () => ctrl.abort();
  }, [status, ready]);

  const save = useCallback((next: ConsentChoice, source: ConsentSource = 'preferences') => {
    const g = detectGpc();
    const stored: Stored = {
      v: 1,
      analytics: next.analytics,
      personalized_ads: g ? false : next.personalized_ads,
      personalized_recs: next.personalized_recs,
      gpc: g,
      saved_at: new Date().toISOString(),
    };
    write(stored);
    record(stored, source);
    setChoice(stored);
    setPrefsOpen(false);
    track('consent_change', {categories: consentField() as ConsentCategory[], gpc: g});
  }, []);

  const openPrefs = useCallback(() => setPrefsOpen(true), []);
  const closePrefs = useCallback(() => setPrefsOpen(false), []);
  const value = useMemo(() => ({choice, ready, gpc, prefsOpen, openPrefs, closePrefs, save}), [choice, ready, gpc, prefsOpen, openPrefs, closePrefs, save]);
  return <ConsentContext.Provider value={value}>{children}</ConsentContext.Provider>;
}

export const useConsent = () => useContext(ConsentContext);
