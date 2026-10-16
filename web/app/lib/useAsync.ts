import {useCallback, useEffect, useRef, useState} from 'react';
import {isAbort} from './api';

export type AsyncState<T> = {
  data: T | undefined;
  error: Error | undefined;
  loading: boolean;
  /** True only once loading has taken longer than 300 ms (PRD 12: no skeleton flash). */
  slow: boolean;
  reload: () => void;
};

const SKELETON_DELAY_MS = 300;

/**
 * Runs `load` whenever `deps` change (and on reload()). Stale responses are
 * dropped via AbortController. Pass enabled=false to skip loading entirely.
 *
 * `initial` is data a route loader already fetched for these deps (server
 * rendering): the first render shows it, so the hydrating client matches the
 * server HTML, and no request is made until deps change or reload() runs.
 */
const sameDeps = (a: unknown[] | undefined, b: unknown[]) => !!a && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));

export function useAsync<T>(load: (signal: AbortSignal) => Promise<T>, deps: unknown[], enabled = true, initial?: T): AsyncState<T> {
  const seeded = initial !== undefined && enabled;
  const [state, setState] = useState<{data?: T; error?: Error; loading: boolean; deps?: unknown[]}>(() =>
    seeded ? {data: initial, loading: false, deps: [...deps, enabled]} : {loading: enabled},
  );
  const seedDeps = useRef<unknown[] | null>(seeded ? [...deps, enabled] : null);
  const [slow, setSlow] = useState(false);
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    if (seedDeps.current) {
      if (nonce === 0 && sameDeps(seedDeps.current, [...deps, enabled])) return;
      seedDeps.current = null;
    }
    if (!enabled) {
      setState({loading: false});
      setSlow(false);
      return;
    }
    const controller = new AbortController();
    const key = [...deps, enabled];
    setState({loading: true, deps: key});
    setSlow(false);
    const timer = window.setTimeout(() => setSlow(true), SKELETON_DELAY_MS);
    load(controller.signal).then(
      data => {
        if (!controller.signal.aborted) setState({data, loading: false, deps: key});
      },
      error => {
        if (controller.signal.aborted || isAbort(error)) return;
        setState({error: error instanceof Error ? error : new Error(String(error)), loading: false, deps: key});
      },
    ).finally(() => window.clearTimeout(timer));
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce, enabled]);

  const reload = useCallback(() => setNonce(n => n + 1), []);
  // In the render right after deps change (before the effect runs) the last
  // result belongs to the old deps: report loading instead of stale data, so
  // e.g. useCanonicalSlug never compares a new URL slug with the old entity.
  if (enabled && state.deps && !sameDeps(state.deps, [...deps, enabled])) {
    return {data: undefined, error: undefined, loading: true, slow: false, reload};
  }
  return {data: state.data, error: state.error, loading: state.loading, slow: slow && state.loading, reload};
}
