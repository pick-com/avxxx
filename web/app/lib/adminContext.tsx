import {createContext, useCallback, useContext, useEffect, useState} from 'react';
import {isAbort} from './api';
import {adminErrorText} from './adminApi';

export type AdminSession = {
  /** True once an admin token is stored (it may still be wrong: pages show the 401). */
  hasToken: boolean;
  /** Bumped whenever the token changes; pages reload on it. */
  epoch: number;
  /** Re-read badge counts in the sidebar after a page changed something. */
  refreshBadges: () => void;
};

export const AdminContext = createContext<AdminSession>({hasToken: false, epoch: 0, refreshBadges: () => {}});
export const useAdmin = () => useContext(AdminContext);

/**
 * Loads one admin resource whenever the token epoch or deps change.
 * Returns the data, the error text, a busy flag and a reload function.
 */
export function useAdminLoad<T>(load: (signal: AbortSignal) => Promise<T>, deps: unknown[] = []) {
  const {hasToken, epoch} = useAdmin();
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState('');
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [tick, setTick] = useState(0);
  const reload = useCallback(() => setTick(t => t + 1), []);

  useEffect(() => {
    if (!hasToken) return;
    const ctrl = new AbortController();
    setBusy(true);
    setError('');
    setFailure(null);
    load(ctrl.signal)
      .then(v => {
        if (!ctrl.signal.aborted) setData(v);
      })
      .catch(cause => {
        if (isAbort(cause) || ctrl.signal.aborted) return;
        setFailure(cause);
        setError(adminErrorText(cause));
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setBusy(false);
      });
    return () => ctrl.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasToken, epoch, tick, ...deps]);

  return {data, setData, error, failure, busy, reload};
}

/** Runs one admin write, with busy / error / notice state for the caller. */
export function useAdminAction() {
  const {refreshBadges} = useAdmin();
  const [pending, setPending] = useState<string | null>(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const run = useCallback(
    async <T,>(key: string, fn: () => Promise<T>, done?: string | ((v: T) => string)): Promise<T | undefined> => {
      setPending(key);
      setError('');
      setNotice('');
      try {
        const v = await fn();
        if (done) setNotice(typeof done === 'function' ? done(v) : done);
        refreshBadges();
        return v;
      } catch (cause) {
        setError(adminErrorText(cause));
        throw cause;
      } finally {
        setPending(null);
      }
    },
    [refreshBadges],
  );
  /** Like run, but swallows the error (it is already in `error`). */
  const attempt = useCallback(
    async <T,>(key: string, fn: () => Promise<T>, done?: string | ((v: T) => string)) => {
      try {
        return await run(key, fn, done);
      } catch {
        return undefined;
      }
    },
    [run],
  );
  return {pending, error, setError, notice, setNotice, run, attempt};
}
