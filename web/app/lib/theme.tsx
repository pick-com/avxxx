import {createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode} from 'react';
import {track} from './analytics';
import {t} from './i18n';

export type ThemePref = 'system' | 'dark' | 'light';
const STORAGE_KEY = 'theme';

// Inlined into <head> so the stored theme applies before first paint.
export const themeInitScript = `try{var t=localStorage.getItem('${STORAGE_KEY}');if(t==='dark'||t==='light')document.documentElement.setAttribute('data-theme',t)}catch(e){}`;

function readPref(): ThemePref {
  try {
    const value = window.localStorage.getItem(STORAGE_KEY);
    return value === 'dark' || value === 'light' ? value : 'system';
  } catch {
    return 'system';
  }
}

/**
 * origin 'user' marks a choice made on this device (toggle, settings); the
 * settings sync pushes those to GET/PATCH /api/me/settings. 'sync' applies a
 * value that came from the server and is not echoed back.
 */
type ThemeValue = {
  pref: ThemePref;
  setPref: (pref: ThemePref, origin?: 'user' | 'sync') => void;
  /** Bumps on every user-originated change. */
  userChange: {pref: ThemePref; n: number} | null;
};
const ThemeContext = createContext<ThemeValue>({pref: 'system', setPref: () => {}, userChange: null});

export function ThemeProvider({children}: {children: ReactNode}) {
  const [pref, setPrefState] = useState<ThemePref>('system');
  const [userChange, setUserChange] = useState<ThemeValue['userChange']>(null);

  useEffect(() => setPrefState(readPref()), []);

  const setPref = useCallback((next: ThemePref, origin: 'user' | 'sync' = 'user') => {
    setPrefState(next);
    if (origin === 'user') {
      setUserChange(prev => ({pref: next, n: (prev?.n ?? 0) + 1}));
      track('theme_change', {theme: next});
    }
    const root = document.documentElement;
    if (next === 'system') root.removeAttribute('data-theme');
    else root.setAttribute('data-theme', next);
    try {
      if (next === 'system') window.localStorage.removeItem(STORAGE_KEY);
      else window.localStorage.setItem(STORAGE_KEY, next);
    } catch {
      /* not persisted */
    }
  }, []);

  const value = useMemo(() => ({pref, setPref, userChange}), [pref, setPref, userChange]);
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export const useTheme = () => useContext(ThemeContext);

/** Localized labels (read at render time, so they follow the UI language). */
export const themeLabels: Record<ThemePref, string> = {
  get system() {
    return t('theme.system');
  },
  get dark() {
    return t('theme.dark');
  },
  get light() {
    return t('theme.light');
  },
};

/** Local preference without React (for the first settings sync). */
export const readThemePref = readPref;
