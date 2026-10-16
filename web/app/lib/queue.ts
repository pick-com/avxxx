// Play queue for 全部播放 / 随机播放 (NavLater, Actor 随机播放全部). Kept in
// sessionStorage so the video page can show 下一个 and advance when a video
// ends; ?list=<name> on the video URL opts the page into the queue. A queue
// other than 'later' carries its own panel title and the works' titles, and
// optionally a manage link (a 收藏夹's queue: its page).

export type Queue = {name: string; slugs: string[]; title?: string; titles?: Record<string, string>; manage?: string};
const KEY = 'play_queue';

/** Queues the video page follows (?list=). */
export const QUEUE_LISTS = ['later', 'performer'] as const;

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
  try {
    const raw = window.sessionStorage.getItem(KEY);
    if (!raw) return null;
    const q = JSON.parse(raw) as Queue;
    return q && q.name === name && Array.isArray(q.slugs) ? q : null;
  } catch {
    return null;
  }
}
