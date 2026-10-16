// Play queue for 全部播放 / 随机播放 (NavLater, Actor 随机播放全部). Kept in
// sessionStorage so the video page can show 下一个 and advance when a video
// ends; ?list=<name> on the video URL opts the page into the queue. A queue
// other than 'later' carries its own panel title and the works' titles, and
// optionally a manage link (a 收藏夹's queue: its page).
//
// 播放队列 (YouTube's Add to queue): ?list=queue is an ad-hoc queue the
// viewer builds from release cards' menus. It lives under its own key, so
// starting another queue does not wipe it, and works signed out (client
// only). The pure helpers below (queueAdd / queueRemove / queueMove) are its
// model (node --test); lib/useQueue.ts subscribes components to it.

export type Queue = {name: string; slugs: string[]; title?: string; titles?: Record<string, string>; manage?: string};
const KEY = 'play_queue';

/** The ad-hoc 播放队列's ?list= name. */
export const ADHOC_QUEUE = 'queue';
const ADHOC_KEY = 'play_queue_adhoc';
/** Upper bound on the ad-hoc queue (sessionStorage is small and shared). */
export const ADHOC_MAX = 200;
/** Fired on window when the ad-hoc queue changes in this tab. */
export const ADHOC_EVENT = 'play-queue-change';

/** Queues the video page follows (?list=). */
export const QUEUE_LISTS = ['later', 'performer', ADHOC_QUEUE] as const;

/** A 收藏夹's queue name: playlist:<id> (?list=playlist:<id>). */
export const playlistQueue = (id: number) => `playlist:${id}`;

/** Whether ?list= names a queue the video page follows: a QUEUE_LISTS name or playlist:<id>. */
export function isQueueList(name: string | null | undefined): name is string {
  return !!name && ((QUEUE_LISTS as readonly string[]).includes(name) || /^playlist:[1-9]\d{0,18}$/.test(name));
}

/** The video page URL that starts a queue at its first item. */
export const queueStartPath = (name: string, firstSlug: string) => `/releases/${encodeURIComponent(firstSlug)}?list=${encodeURIComponent(name)}`;

export function startQueue(name: string, slugs: string[], extra: {title?: string; titles?: Record<string, string>; manage?: string} = {}) {
  try {
    window.sessionStorage.setItem(KEY, JSON.stringify({name, slugs, ...extra}));
  } catch {
    /* no queue: each video still plays on its own */
  }
}

export function readQueue(name: string): Queue | null {
  if (name === ADHOC_QUEUE) {
    const q = readAdhocQueue();
    return q.slugs.length ? q : null;
  }
  try {
    const raw = window.sessionStorage.getItem(KEY);
    if (!raw) return null;
    const q = JSON.parse(raw) as Queue;
    return q && q.name === name && Array.isArray(q.slugs) ? q : null;
  } catch {
    return null;
  }
}

// ---- 播放队列 model (pure) ----

/** An empty ad-hoc queue. */
export const emptyQueue = (): Queue => ({name: ADHOC_QUEUE, slugs: [], titles: {}});

/** A stored value as an ad-hoc queue: unknown shapes become empty, duplicates and extras drop. */
export function normalizeQueue(raw: unknown): Queue {
  const q = raw as Partial<Queue> | null;
  if (!q || typeof q !== 'object' || !Array.isArray(q.slugs)) return emptyQueue();
  const slugs = Array.from(new Set(q.slugs.filter((s): s is string => typeof s === 'string' && s !== ''))).slice(0, ADHOC_MAX);
  const titles: Record<string, string> = {};
  for (const s of slugs) {
    const title = q.titles?.[s];
    if (typeof title === 'string' && title) titles[s] = title;
  }
  return {name: ADHOC_QUEUE, slugs, titles};
}

/** Appends `slug` (YouTube: to the end). Already queued, or full: unchanged (`added` false). */
export function queueAdd(q: Queue, slug: string, title?: string): {queue: Queue; added: boolean} {
  if (!slug || q.slugs.includes(slug) || q.slugs.length >= ADHOC_MAX) return {queue: q, added: false};
  return {queue: {...q, slugs: [...q.slugs, slug], titles: title ? {...q.titles, [slug]: title} : {...q.titles}}, added: true};
}

export function queueRemove(q: Queue, slug: string): Queue {
  if (!q.slugs.includes(slug)) return q;
  const titles = {...q.titles};
  delete titles[slug];
  return {...q, slugs: q.slugs.filter(s => s !== slug), titles};
}

/** Moves `slug` by `delta` places (clamped); unchanged when it is not queued. */
export function queueMove(q: Queue, slug: string, delta: number): Queue {
  const from = q.slugs.indexOf(slug);
  if (from < 0) return q;
  const to = Math.min(q.slugs.length - 1, Math.max(0, from + delta));
  if (to === from) return q;
  const slugs = q.slugs.slice();
  slugs.splice(from, 1);
  slugs.splice(to, 0, slug);
  return {...q, slugs};
}

// ---- 播放队列 storage ----

// Without sessionStorage (blocked) the queue lasts until the page reloads.
let memory: Queue | null = null;

export function readAdhocQueue(): Queue {
  try {
    const raw = window.sessionStorage.getItem(ADHOC_KEY);
    return raw ? normalizeQueue(JSON.parse(raw)) : emptyQueue();
  } catch {
    return memory ?? emptyQueue();
  }
}

export function writeAdhocQueue(q: Queue) {
  memory = q;
  try {
    if (q.slugs.length) window.sessionStorage.setItem(ADHOC_KEY, JSON.stringify({slugs: q.slugs, titles: q.titles}));
    else window.sessionStorage.removeItem(ADHOC_KEY);
  } catch {
    /* storage blocked: kept in memory */
  }
  window.dispatchEvent(new Event(ADHOC_EVENT));
}
