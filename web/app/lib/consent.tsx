import {createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode} from 'react';
import {track, type ConsentCategory} from './analytics';

// Cookie consent (PRD 15, CookieBanner / MobileConsent). Four categories:
// 必要 (always on), 分析, 个性化广告, 个性化推荐. Stored only in this browser
// (localStorage) until consent_records exists server-side.
//
// GPC: a browser that sends Global Privacy Control (navigator.globalPrivacyControl)
// has personalized ads forced off and the switch locked (PRD 14.5).
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
  save: (choice: ConsentChoice) => void;
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
  const [choice, setChoice] = useState<Stored | null>(null);
  const [ready, setReady] = useState(false);
  const [gpc, setGpc] = useState(false);
  const [prefsOpen, setPrefsOpen] = useState(false);

  useEffect(() => {
    const g = detectGpc();
    setGpc(g);
    let stored = read();
    // A GPC signal that appears after an earlier "accept all" still wins.
    if (stored && g && stored.personalized_ads) stored = {...stored, personalized_ads: false, gpc: true};
    setChoice(stored);
    setReady(true);
  }, []);

  const save = useCallback((next: ConsentChoice) => {
    const g = detectGpc();
    const stored: Stored = {
      v: 1,
      analytics: next.analytics,
      personalized_ads: g ? false : next.personalized_ads,
      personalized_recs: next.personalized_recs,
      gpc: g,
      saved_at: new Date().toISOString(),
    };
    try {
      window.localStorage.setItem(KEY, JSON.stringify(stored));
    } catch {
      /* not persisted: the banner returns next visit */
    }
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
