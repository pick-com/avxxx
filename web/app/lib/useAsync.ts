import {useCallback, useEffect, useState} from 'react';
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
 */
export function useAsync<T>(load: (signal: AbortSignal) => Promise<T>, deps: unknown[], enabled = true): AsyncState<T> {
  const [state, setState] = useState<{data?: T; error?: Error; loading: boolean}>({loading: enabled});
  const [slow, setSlow] = useState(false);
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    if (!enabled) {
      setState({loading: false});
      setSlow(false);
      return;
    }
    const controller = new AbortController();
    setState({loading: true});
    setSlow(false);
    const timer = window.setTimeout(() => setSlow(true), SKELETON_DELAY_MS);
    load(controller.signal).then(
      data => {
        if (!controller.signal.aborted) setState({data, loading: false});
      },
      error => {
        if (controller.signal.aborted || isAbort(error)) return;
        setState({error: error instanceof Error ? error : new Error(String(error)), loading: false});
      },
    ).finally(() => window.clearTimeout(timer));
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce, enabled]);

  const reload = useCallback(() => setNonce(n => n + 1), []);
  return {data: state.data, error: state.error, loading: state.loading, slow: slow && state.loading, reload};
}
