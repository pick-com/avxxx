import {useCallback, useSyncExternalStore} from 'react';
import {ADHOC_EVENT, queueAdd, queueMove, queueRemove, readAdhocQueue, writeAdhocQueue, emptyQueue, type Queue} from './queue';

// The 播放队列 (lib/queue ADHOC_QUEUE) as React state: every component that
// shows or edits it re-renders when it changes in this tab.

let cache: {raw: Queue; at: number} | null = null;
let version = 0;
if (typeof window !== 'undefined') window.addEventListener(ADHOC_EVENT, () => version++);

const subscribe = (fn: () => void) => {
  window.addEventListener(ADHOC_EVENT, fn);
  return () => window.removeEventListener(ADHOC_EVENT, fn);
};

// useSyncExternalStore needs a stable snapshot between changes.
const snapshot = (): Queue => {
  if (!cache || cache.at !== version) cache = {raw: readAdhocQueue(), at: version};
  return cache.raw;
};
const EMPTY = emptyQueue();
const serverSnapshot = () => EMPTY;

export function useAdhocQueue() {
  const queue = useSyncExternalStore(subscribe, snapshot, serverSnapshot);
  const add = useCallback((slug: string, title?: string) => {
    const {queue: next, added} = queueAdd(readAdhocQueue(), slug, title);
    if (added) writeAdhocQueue(next);
    return added;
  }, []);
  const remove = useCallback((slug: string) => writeAdhocQueue(queueRemove(readAdhocQueue(), slug)), []);
  const move = useCallback((slug: string, delta: number) => writeAdhocQueue(queueMove(readAdhocQueue(), slug, delta)), []);
  const clear = useCallback(() => writeAdhocQueue(emptyQueue()), []);
  return {queue, add, remove, move, clear};
}
