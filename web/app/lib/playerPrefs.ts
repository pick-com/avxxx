import {useEffect, useMemo} from 'react';
import {tokens, type QualityPref, type UserSettings} from './api';
import {useSettings} from './settings';

// Playback preferences for the Player (PRD 5.1, 5.4, 9; store.UserSettings):
// quality_pref, data_saver, autoplay, remember_rate. Signed-in users get them
// from GET /api/me/settings; the last values are cached in this browser so the
// first play-config request already carries them. Visitors (and a backend
// without /api/me/settings) use the store defaults.

export type PlayerPrefs = {
  qualityPref: QualityPref;
  dataSaver: boolean;
  autoplay: boolean;
  rememberRate: boolean;
};

const CACHE_KEY = 'player_prefs_v1';
const RATES_KEY = 'playback_rates_v1';
const MAX_RATES = 200;

/** store.DefaultSettings(). */
export const DEFAULT_PREFS: PlayerPrefs = {qualityPref: 'auto', dataSaver: false, autoplay: true, rememberRate: true};

const QUALITIES: QualityPref[] = ['auto', '2160p', '1080p', '720p', '480p', '360p'];

function fromSettings(s: UserSettings): PlayerPrefs {
  return {
    qualityPref: QUALITIES.includes(s.quality_pref) ? s.quality_pref : 'auto',
    dataSaver: !!s.data_saver,
    autoplay: s.autoplay !== false,
    rememberRate: s.remember_rate !== false,
  };
}

function readCache(): PlayerPrefs | null {
  try {
    const raw = window.localStorage.getItem(CACHE_KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<PlayerPrefs>;
    return {
      qualityPref: QUALITIES.includes(v.qualityPref as QualityPref) ? (v.qualityPref as QualityPref) : 'auto',
      dataSaver: !!v.dataSaver,
      autoplay: v.autoplay !== false,
      rememberRate: v.rememberRate !== false,
    };
  } catch {
    return null;
  }
}

export function usePlayerPrefs(): PlayerPrefs {
  const {status, settings} = useSettings();
  const prefs = useMemo<PlayerPrefs>(() => {
    if (settings) return fromSettings(settings);
    // Signed in, settings not loaded yet: the last known values.
    if (status !== 'missing' && typeof window !== 'undefined' && tokens.user()) return readCache() ?? DEFAULT_PREFS;
    return DEFAULT_PREFS;
  }, [settings, status]);
  useEffect(() => {
    if (!settings) return;
    try {
      window.localStorage.setItem(CACHE_KEY, JSON.stringify(prefs));
    } catch {
      /* not cached */
    }
  }, [settings, prefs]);
  return prefs;
}

// ---- Network: data saver / cellular cap (PRD 9: 流量下默认 720p) ----

type Connection = {type?: string; effectiveType?: string; saveData?: boolean; addEventListener?: (t: string, fn: () => void) => void; removeEventListener?: (t: string, fn: () => void) => void};

export function connection(): Connection | undefined {
  if (typeof navigator === 'undefined') return undefined;
  return (navigator as Navigator & {connection?: Connection}).connection;
}

/**
 * Highest rendition height allowed on this connection, or Infinity.
 * Only when the browser says so: cellular (Network Information API) caps at
 * 720p, or 480p with data_saver; the browser's own Save-Data flag caps at 480p.
 */
export function heightCap(prefs: PlayerPrefs): number {
  const c = connection();
  if (!c) return Infinity;
  if (c.saveData) return 480;
  if (c.type === 'cellular') return prefs.dataSaver ? 480 : 720;
  return Infinity;
}

/** quality_pref as a height (Infinity for auto). */
export function prefHeight(pref: QualityPref): number {
  return pref === 'auto' ? Infinity : Number.parseInt(pref, 10) || Infinity;
}

/** play-config ?quality= (preferred max height), or undefined for no preference. */
export function qualityParam(prefs: PlayerPrefs): number | undefined {
  const h = Math.min(prefHeight(prefs.qualityPref), heightCap(prefs));
  return Number.isFinite(h) ? h : undefined;
}

// ---- remember_rate: playback rate per release, this browser only ----

function readRates(): Record<string, number> {
  try {
    const raw = window.localStorage.getItem(RATES_KEY);
    const v = raw ? (JSON.parse(raw) as Record<string, number>) : {};
    return v && typeof v === 'object' ? v : {};
  } catch {
    return {};
  }
}

export function rememberedRate(slug: string): number | null {
  const r = readRates()[slug];
  return typeof r === 'number' && r > 0 && r <= 4 ? r : null;
}

export function rememberRate(slug: string, rate: number) {
  try {
    const rates = readRates();
    delete rates[slug]; // re-insert last so the oldest entries go first
    if (rate !== 1) rates[slug] = rate;
    const keys = Object.keys(rates);
    for (const k of keys.slice(0, Math.max(0, keys.length - MAX_RATES))) delete rates[k];
    window.localStorage.setItem(RATES_KEY, JSON.stringify(rates));
  } catch {
    /* not persisted */
  }
}
