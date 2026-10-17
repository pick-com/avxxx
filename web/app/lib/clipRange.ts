// 片段 (clips) range rules for the 剪辑 panel and the clip page, mirroring
// internal/store/clips.go (ValidateClipRange, ClipTitleMax): 5–60 s, inside
// the video when its duration is known, title at most 100 characters.
// Kept free of imports so scripts/clip-range.test.mjs runs it under plain
// Node.

export const CLIP_MIN_S = 5;
export const CLIP_MAX_S = 60;
export const CLIP_DEFAULT_S = 15;
export const CLIP_TITLE_MAX = 100;

/** The panel's zoomed timeline: this many seconds around the clip. */
export const CLIP_WINDOW_S = 180;

export type ClipRange = {start: number; end: number};

const known = (d: number) => (Number.isFinite(d) && d > 0 ? d : Infinity);
const round1 = (n: number) => Math.round(n * 10) / 10;

/** Opening the panel: the current position + 15 s, pulled back to fit a known end. */
export function defaultClipRange(time: number, duration: number): ClipRange {
  const end = known(duration);
  const len = Math.min(CLIP_DEFAULT_S, end === Infinity ? CLIP_DEFAULT_S : Math.max(0, end));
  let start = Math.max(0, Number.isFinite(time) ? time : 0);
  if (start + len > end) start = Math.max(0, end - len);
  return {start: round1(start), end: round1(Math.min(end, start + len))};
}

/**
 * Drags one handle to `to` (seconds). The other handle follows when the
 * length would leave 5–60 s, so the range stays valid; everything stays
 * inside [0, duration].
 */
export function moveClipHandle(range: ClipRange, which: 'start' | 'end', to: number, duration: number): ClipRange {
  const end = known(duration);
  const t = round1(Math.min(end, Math.max(0, Number.isFinite(to) ? to : 0)));
  if (which === 'start') {
    const start = Math.min(t, Math.max(0, end - CLIP_MIN_S));
    let e = range.end;
    if (e - start < CLIP_MIN_S) e = start + CLIP_MIN_S;
    if (e - start > CLIP_MAX_S) e = start + CLIP_MAX_S;
    return {start, end: round1(Math.min(end, e))};
  }
  const e = Math.max(t, Math.min(end, CLIP_MIN_S));
  let start = range.start;
  if (e - start < CLIP_MIN_S) start = e - CLIP_MIN_S;
  if (e - start > CLIP_MAX_S) start = e - CLIP_MAX_S;
  return {start: round1(Math.max(0, start)), end: e};
}

/** The timeline window shown around a range: CLIP_WINDOW_S wide, inside the video. */
export function clipWindow(range: ClipRange, duration: number): ClipRange {
  const end = known(duration);
  if (end <= CLIP_WINDOW_S) return {start: 0, end: end === Infinity ? CLIP_WINDOW_S : end};
  const mid = (range.start + range.end) / 2;
  const start = Math.min(end - CLIP_WINDOW_S, Math.max(0, mid - CLIP_WINDOW_S / 2));
  return {start, end: start + CLIP_WINDOW_S};
}

/** What the API checks, in milliseconds; null when valid, else an error code. */
export function clipRangeError(startMs: number, endMs: number, durationMs: number): 'order' | 'length' | 'end' | null {
  if (!(startMs >= 0) || !(endMs > startMs)) return 'order';
  const len = endMs - startMs;
  if (len < CLIP_MIN_S * 1000 || len > CLIP_MAX_S * 1000) return 'length';
  if (durationMs > 0 && endMs > durationMs) return 'end';
  return null;
}

/** The request body for a range picked in seconds. */
export function clipRequest(slug: string, range: ClipRange, title: string): {release: string; start_ms: number; end_ms: number; title: string} {
  return {release: slug, start_ms: Math.round(range.start * 1000), end_ms: Math.round(range.end * 1000), title: title.trim()};
}

/** Title length in characters (code points, like the API's rune count). */
export function clipTitleLength(title: string): number {
  return Array.from(title.trim()).length;
}
