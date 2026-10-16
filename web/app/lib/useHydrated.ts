import {useSyncExternalStore} from 'react';

const subscribe = () => () => undefined;

/**
 * False on the server and during hydration, true afterwards (and on every
 * client-only render). For UI that only exists in the browser, so the
 * hydrating render matches the server HTML.
 */
export function useHydrated(): boolean {
  return useSyncExternalStore(
    subscribe,
    () => true,
    () => false,
  );
}
