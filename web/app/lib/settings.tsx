import {createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode} from 'react';
import {ApiError, getSettings, isAbort, isMissingEndpoint, patchSettings, type UserSettings} from './api';
import {useAuth} from './auth';
import {readThemePref, useTheme} from './theme';

// GET / PATCH /api/me/settings (store.UserSettings; PRD 7, 17, 20).
//
// Optimistic: each patch is an overlay applied on top of the last server
// response until its own response arrives; a failure drops the overlay and
// records an error under the field key. The server applies patches in order
// and answers with the full settings, so overlays of other in-flight patches
// keep showing on top of each response.
//
// Theme: the server value wins when settings load (cross-device sync), except
// that a local choice made before the first sync is pushed up when the server
// still has the default. Later toggles anywhere (TopBar, 我的, 设置) are sent
// as PATCH theme.
//
// Timezone: quiet hours and the per-day merge run in settings.timezone. While
// the server still has the default (UTC) the browser's IANA zone is sent once.

export type SettingsStatus = 'idle' | 'loading' | 'ready' | 'missing' | 'error';

type Overlay = {id: number; key: string; apply: (s: UserSettings) => UserSettings};

type SettingsValue = {
  status: SettingsStatus;
  settings: UserSettings | null;
  error: Error | null;
  /** field key -> message for the last failed save of that field */
  fieldErrors: Record<string, string>;
  pending: Set<string>;
  /**
   * Send `body` (a PATCH object) with `key` naming the field for errors and
   * `apply` producing the optimistic value. Resolves true on success.
   */
  save: (key: string, body: Record<string, unknown>, apply: (s: UserSettings) => UserSettings) => Promise<boolean>;
  reload: () => void;
};

const SettingsContext = createContext<SettingsValue>({
  status: 'idle',
  settings: null,
  error: null,
  fieldErrors: {},
  pending: new Set(),
  save: async () => false,
  reload: () => {},
});

export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

// The API reports validation failures as {"error": "invalid settings: …"} in English.
function saveError(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 0) return cause.message;
    if (cause.status === 400) return '这个值没有被接受，请换一个再试';
    if (cause.status === 401) return '登录已过期，请重新登录';
    return `保存失败（${cause.status || '网络'}），请重试`;
  }
  return '保存失败，请重试';
}

export function SettingsProvider({children}: {children: ReactNode}) {
  const {status: auth} = useAuth();
  const {pref, setPref, userChange} = useTheme();
  const [confirmed, setConfirmed] = useState<UserSettings | null>(null);
  const [overlays, setOverlays] = useState<Overlay[]>([]);
  const [status, setStatus] = useState<SettingsStatus>('idle');
  const [error, setError] = useState<Error | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [nonce, setNonce] = useState(0);
  const seq = useRef(0);
  const prefRef = useRef(pref);
  prefRef.current = pref;
  const syncedChange = useRef<number>(0);
  const userChangeRef = useRef(userChange);
  userChangeRef.current = userChange;

  const save = useCallback(async (key: string, body: Record<string, unknown>, apply: (s: UserSettings) => UserSettings) => {
    const id = ++seq.current;
    setOverlays(prev => [...prev, {id, key, apply}]);
    setFieldErrors(prev => {
      if (!(key in prev)) return prev;
      const next = {...prev};
      delete next[key];
      return next;
    });
    try {
      const fresh = await patchSettings(body);
      if (fresh && typeof fresh === 'object') setConfirmed(fresh);
      return true;
    } catch (cause) {
      setFieldErrors(prev => ({...prev, [key]: saveError(cause)}));
      return false;
    } finally {
      setOverlays(prev => prev.filter(o => o.id !== id));
    }
  }, []);

  useEffect(() => {
    if (auth !== 'user') {
      setConfirmed(null);
      setOverlays([]);
      setFieldErrors({});
      setStatus('idle');
      return;
    }
    const controller = new AbortController();
    setStatus('loading');
    setError(null);
    getSettings(controller.signal).then(
      data => {
        setConfirmed(data);
        setStatus('ready');
        syncedChange.current = userChangeRef.current?.n ?? 0; // changes before load are handled here
        // Theme: server wins unless it still has the default and this device chose one.
        const local = readThemePref();
        if (data.theme === 'system' && local !== 'system') {
          save('theme', {theme: local}, s => ({...s, theme: local}));
        } else if (data.theme !== prefRef.current) {
          setPref(data.theme, 'sync');
        }
        const tz = browserTimezone();
        if (data.timezone === 'UTC' && tz !== 'UTC') save('timezone', {timezone: tz}, s => ({...s, timezone: tz}));
      },
      cause => {
        if (isAbort(cause) || controller.signal.aborted) return;
        if (isMissingEndpoint(cause)) setStatus('missing');
        else {
          setStatus('error');
          setError(cause instanceof Error ? cause : new Error(String(cause)));
        }
      },
    );
    return () => controller.abort();
  }, [auth, nonce, save, setPref]);

  // Push user-originated theme changes made after the settings loaded.
  useEffect(() => {
    if (!userChange || status !== 'ready') return;
    if (userChange.n <= syncedChange.current) return;
    syncedChange.current = userChange.n;
    const theme = userChange.pref;
    save('theme', {theme}, s => ({...s, theme}));
  }, [userChange, status, save]);

  const settings = useMemo(() => (confirmed ? overlays.reduce((s, o) => o.apply(s), confirmed) : null), [confirmed, overlays]);
  const pending = useMemo(() => new Set(overlays.map(o => o.key)), [overlays]);
  const reload = useCallback(() => setNonce(n => n + 1), []);

  const value = useMemo(
    () => ({status, settings, error, fieldErrors, pending, save, reload}),
    [status, settings, error, fieldErrors, pending, save, reload],
  );
  return <SettingsContext.Provider value={value}>{children}</SettingsContext.Provider>;
}

export const useSettings = () => useContext(SettingsContext);
