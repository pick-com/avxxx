// Play queue for 全部播放 / 随机播放 (NavLater). Kept in sessionStorage so the
// video page can show 下一个 and advance when a video ends; ?list=<name> on the
// video URL opts the page into the queue.

export type Queue = {name: string; slugs: string[]};
const KEY = 'play_queue';

export function startQueue(name: string, slugs: string[]) {
  try {
    window.sessionStorage.setItem(KEY, JSON.stringify({name, slugs}));
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
