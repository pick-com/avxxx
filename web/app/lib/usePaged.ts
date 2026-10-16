import {useCallback, useEffect, useRef, useState} from 'react';
import {isAbort, type Page} from './api';

const SKELETON_DELAY_MS = 300;

export type PagedState<T> = {
  items: T[];
  /** First page still loading. */
  loading: boolean;
  /** First page has taken longer than 300 ms (PRD 12). */
  slow: boolean;
  /** First page failed. */
  error: Error | undefined;
  /** False when the backend has no cursor pagination (see Page.paged). Undefined until the first page arrives. */
  paged: boolean | undefined;
  hasMore: boolean;
  loadingMore: boolean;
  moreError: Error | undefined;
  loadMore: () => void;
  reload: () => void;
};

/**
 * Cursor pagination: loads the first page whenever `deps` change, then appends
 * pages on loadMore() using the previous page's next_cursor. Stale responses
 * are dropped via AbortController.
 */
export function usePaged<T>(
  load: (cursor: string | null, signal: AbortSignal) => Promise<Page<T>>,
  deps: unknown[],
  enabled = true,
  /** First page already fetched by a route loader for these deps (server rendering). */
  initial?: Page<T>,
): PagedState<T> {
  const seeded = !!initial && enabled;
  const [items, setItems] = useState<T[]>(() => (seeded ? initial.items : []));
  const [next, setNext] = useState<string | null>(() => (seeded ? initial.nextCursor : null));
  const [paged, setPaged] = useState<boolean | undefined>(() => (seeded ? initial.paged : undefined));
  const [loading, setLoading] = useState(enabled && !seeded);
  const seedDeps = useRef<unknown[] | null>(seeded ? [...deps, enabled] : null);
  const [slow, setSlow] = useState(false);
  const [error, setError] = useState<Error | undefined>();
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Error | undefined>();
  const [nonce, setNonce] = useState(0);
  const generation = useRef<AbortController | null>(null);
  // In-flight guard for loadMore(); reset whenever a new first page starts.
  const busy = useRef(false);
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    if (seedDeps.current) {
      const same = seedDeps.current.length === deps.length + 1 && [...deps, enabled].every((v, i) => Object.is(v, seedDeps.current![i]));
      if (nonce === 0 && same) {
        // Keep the loader's first page; loadMore() continues from its cursor.
        const controller = new AbortController();
        generation.current = controller;
        return () => controller.abort();
      }
      seedDeps.current = null;
    }
    const controller = new AbortController();
    generation.current?.abort();
    generation.current = controller;
    busy.current = false;
    setItems([]);
    setNext(null);
    setPaged(undefined);
    setError(undefined);
    setMoreError(undefined);
    setLoadingMore(false);
    setSlow(false);
    if (!enabled) {
      setLoading(false);
      return () => controller.abort();
    }
    setLoading(true);
    const timer = window.setTimeout(() => setSlow(true), SKELETON_DELAY_MS);
    loadRef
      .current(null, controller.signal)
      .then(
        page => {
          if (controller.signal.aborted) return;
          setItems(page.items);
          setNext(page.nextCursor);
          setPaged(page.paged);
          setLoading(false);
        },
        cause => {
          if (controller.signal.aborted || isAbort(cause)) return;
          setError(cause instanceof Error ? cause : new Error(String(cause)));
          setLoading(false);
        },
      )
      .finally(() => window.clearTimeout(timer));
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce, enabled]);

  const loadMore = useCallback(() => {
    const controller = generation.current;
    if (!controller || !next || busy.current) return;
    busy.current = true;
    setLoadingMore(true);
    setMoreError(undefined);
    loadRef
      .current(next, controller.signal)
      .then(
        page => {
          if (controller.signal.aborted) return;
          setItems(prev => {
            // Keyset pages never overlap, but guard against duplicates after a retry.
            const seen = new Set(prev.map(x => (x as {id?: unknown}).id));
            return [...prev, ...page.items.filter(x => !seen.has((x as {id?: unknown}).id))];
          });
          setNext(page.nextCursor);
        },
        cause => {
          if (controller.signal.aborted || isAbort(cause)) return;
          setMoreError(cause instanceof Error ? cause : new Error(String(cause)));
        },
      )
      .finally(() => {
        busy.current = false;
        if (!controller.signal.aborted) setLoadingMore(false);
      });
  }, [next]);

  const reload = useCallback(() => setNonce(n => n + 1), []);

  return {
    items,
    loading,
    slow: slow && loading,
    error,
    paged,
    hasMore: !!next,
    loadingMore,
    moreError,
    loadMore,
    reload,
  };
}
