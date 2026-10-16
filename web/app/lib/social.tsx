import {createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode} from 'react';
import {
  addWatchLater,
  followPerformer,
  getFollows,
  getReservations,
  getWatchLater,
  isAbort,
  isMissingEndpoint,
  removeWatchLater,
  reserveRelease,
  unfollowPerformer,
  unreserveRelease,
  type FollowItem,
  type NotifyMode,
} from './api';
import {useAuth} from './auth';

// Signed-in user's follows, watch-later and reservation sets, shared by every
// button on the page (performer page, search, hover card, cards, feed) so they
// agree with each other. Mutations are optimistic and roll back on failure.
//
// Availability: each list is loaded once per sign-in. A 404 / 405 means the
// backend predates the social routes; the matching buttons then hide instead
// of failing on click.

type Availability = 'unknown' | 'yes' | 'no';

type SocialValue = {
  follows: FollowItem[];
  followsReady: boolean;
  followMode: (slug: string) => NotifyMode | null;
  later: Set<string>;
  reserved: Set<string>;
  available: {follows: Availability; later: Availability; reservations: Availability};
  /** Follow (or change notify mode). Resolves to an error message, or '' on success. */
  setFollow: (slug: string, mode: NotifyMode | null, name?: string) => Promise<string>;
  setLater: (slug: string, on: boolean) => Promise<string>;
  setReserved: (slug: string, on: boolean) => Promise<string>;
  /** Mark local state from a list the caller loaded itself (e.g. the feed's `reserved`). */
  noteReserved: (slugs: string[]) => void;
  refreshFollows: () => void;
  refreshLater: () => void;
};

const empty = new Set<string>();
const SocialContext = createContext<SocialValue>({
  follows: [],
  followsReady: false,
  followMode: () => null,
  later: empty,
  reserved: empty,
  available: {follows: 'unknown', later: 'unknown', reservations: 'unknown'},
  setFollow: async () => '',
  setLater: async () => '',
  setReserved: async () => '',
  noteReserved: () => {},
  refreshFollows: () => {},
  refreshLater: () => {},
});

const message = (cause: unknown, fallback: string) => (cause instanceof Error && cause.message ? cause.message : fallback);

export function SocialProvider({children}: {children: ReactNode}) {
  const {status} = useAuth();
  const [follows, setFollows] = useState<FollowItem[]>([]);
  const [followsReady, setFollowsReady] = useState(false);
  const [later, setLaterSet] = useState<Set<string>>(empty);
  const [reserved, setReservedSet] = useState<Set<string>>(empty);
  const [available, setAvailable] = useState<SocialValue['available']>({follows: 'unknown', later: 'unknown', reservations: 'unknown'});
  const [followsNonce, setFollowsNonce] = useState(0);
  const [laterNonce, setLaterNonce] = useState(0);
  const followsRef = useRef(follows);
  followsRef.current = follows;

  const mark = useCallback((key: keyof SocialValue['available'], value: Availability) => {
    setAvailable(prev => (prev[key] === value ? prev : {...prev, [key]: value}));
  }, []);

  // Reset on sign-out.
  useEffect(() => {
    if (status === 'user') return;
    setFollows([]);
    setFollowsReady(false);
    setLaterSet(empty);
    setReservedSet(empty);
    setAvailable({follows: 'unknown', later: 'unknown', reservations: 'unknown'});
  }, [status]);

  useEffect(() => {
    if (status !== 'user') return;
    const controller = new AbortController();
    getFollows(controller.signal).then(
      list => {
        setFollows(list);
        setFollowsReady(true);
        mark('follows', 'yes');
      },
      cause => {
        if (isAbort(cause) || controller.signal.aborted) return;
        setFollowsReady(true);
        // Other failures keep the buttons (a click retries the request).
        if (isMissingEndpoint(cause)) mark('follows', 'no');
        else mark('follows', 'yes');
      },
    );
    return () => controller.abort();
  }, [status, followsNonce, mark]);

  useEffect(() => {
    if (status !== 'user') return;
    const controller = new AbortController();
    getWatchLater(controller.signal).then(
      list => {
        setLaterSet(new Set(list.map(i => i.slug)));
        mark('later', 'yes');
      },
      cause => {
        if (isAbort(cause) || controller.signal.aborted) return;
        mark('later', isMissingEndpoint(cause) ? 'no' : 'yes');
      },
    );
    return () => controller.abort();
  }, [status, laterNonce, mark]);

  useEffect(() => {
    if (status !== 'user') return;
    const controller = new AbortController();
    getReservations(controller.signal).then(
      list => {
        setReservedSet(new Set(list.filter(i => i.status === 'upcoming').map(i => i.slug)));
        mark('reservations', 'yes');
      },
      cause => {
        if (isAbort(cause) || controller.signal.aborted) return;
        mark('reservations', isMissingEndpoint(cause) ? 'no' : 'yes');
      },
    );
    return () => controller.abort();
  }, [status, mark]);

  const followMode = useCallback((slug: string) => follows.find(f => f.slug === slug)?.notify_mode ?? null, [follows]);

  const setFollow = useCallback(async (slug: string, mode: NotifyMode | null, name?: string) => {
    const before = followsRef.current;
    const existing = before.find(f => f.slug === slug);
    if (mode === null) setFollows(before.filter(f => f.slug !== slug));
    else if (existing) setFollows(before.map(f => (f.slug === slug ? {...f, notify_mode: mode} : f)));
    else
      setFollows([
        {slug, name: name ?? slug, name_ja: '', notify_mode: mode, created_at: new Date().toISOString(), last_seen_at: null, new_count: 0},
        ...before,
      ]);
    try {
      if (mode === null) await unfollowPerformer(slug);
      else await followPerformer(slug, mode);
      if (!existing && mode !== null) setFollowsNonce(n => n + 1); // pick up the real name / counts
      return '';
    } catch (cause) {
      setFollows(rollbackFollow(followsRef.current, before, slug));
      return message(cause, mode === null ? '取消关注失败' : '关注失败');
    }
  }, []);

  const setLater = useCallback(async (slug: string, on: boolean) => {
    setLaterSet(prev => toggled(prev, slug, on));
    try {
      if (on) await addWatchLater(slug);
      else await removeWatchLater(slug);
      return '';
    } catch (cause) {
      setLaterSet(prev => toggled(prev, slug, !on));
      return message(cause, on ? '加入稍后观看失败' : '移出稍后观看失败');
    }
  }, []);

  const setReserved = useCallback(async (slug: string, on: boolean) => {
    setReservedSet(prev => toggled(prev, slug, on));
    try {
      if (on) await reserveRelease(slug);
      else await unreserveRelease(slug);
      return '';
    } catch (cause) {
      setReservedSet(prev => toggled(prev, slug, !on));
      // 409: the release went live in the meantime.
      if (cause instanceof Error && 'status' in cause && (cause as {status: number}).status === 409) return '这部作品已经上线，无需预约';
      return message(cause, on ? '预约失败' : '取消预约失败');
    }
  }, []);

  const noteReserved = useCallback((slugs: string[]) => {
    if (slugs.length === 0) return;
    setReservedSet(prev => {
      if (slugs.every(s => prev.has(s))) return prev;
      const next = new Set(prev);
      for (const s of slugs) next.add(s);
      return next;
    });
  }, []);

  const refreshFollows = useCallback(() => setFollowsNonce(n => n + 1), []);
  const refreshLater = useCallback(() => setLaterNonce(n => n + 1), []);

  const value = useMemo(
    () => ({follows, followsReady, followMode, later, reserved, available, setFollow, setLater, setReserved, noteReserved, refreshFollows, refreshLater}),
    [follows, followsReady, followMode, later, reserved, available, setFollow, setLater, setReserved, noteReserved, refreshFollows, refreshLater],
  );
  return <SocialContext.Provider value={value}>{children}</SocialContext.Provider>;
}

function toggled(set: Set<string>, slug: string, on: boolean): Set<string> {
  if (set.has(slug) === on) return set;
  const next = new Set(set);
  if (on) next.add(slug);
  else next.delete(slug);
  return next;
}

/** Restore one performer's entry from `before` without discarding other concurrent changes. */
function rollbackFollow(current: FollowItem[], before: FollowItem[], slug: string): FollowItem[] {
  const old = before.find(f => f.slug === slug);
  const rest = current.filter(f => f.slug !== slug);
  return old ? [old, ...rest] : rest;
}

export const useSocial = () => useContext(SocialContext);
