// Play queue for 全部播放 / 随机播放 (NavLater, Actor 随机播放全部). Kept in
// sessionStorage so the video page can show 下一个 and advance when a video
// ends; ?list=<name> on the video URL opts the page into the queue. A queue
// other than 'later' carries its own panel title and the works' titles.

export type Queue = {name: string; slugs: string[]; title?: string; titles?: Record<string, string>};
const KEY = 'play_queue';

/** Queues the video page follows (?list=). */
export const QUEUE_LISTS = ['later', 'performer'] as const;

export function startQueue(name: string, slugs: string[], extra: {title?: string; titles?: Record<string, string>} = {}) {
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
