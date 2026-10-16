import {createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode} from 'react';

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

type ThemeValue = {pref: ThemePref; setPref: (pref: ThemePref) => void};
const ThemeContext = createContext<ThemeValue>({pref: 'system', setPref: () => {}});

export function ThemeProvider({children}: {children: ReactNode}) {
  const [pref, setPrefState] = useState<ThemePref>('system');

  useEffect(() => setPrefState(readPref()), []);

  const setPref = useCallback((next: ThemePref) => {
    setPrefState(next);
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

  const value = useMemo(() => ({pref, setPref}), [pref, setPref]);
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export const useTheme = () => useContext(ThemeContext);

export const themeLabels: Record<ThemePref, string> = {system: '跟随系统', dark: '深色', light: '浅色'};
