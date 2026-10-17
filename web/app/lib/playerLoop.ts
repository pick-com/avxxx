// 循环播放 and A-B loop for the Player (YouTube parity). Per video, never
// persisted; no keyboard shortcut (YouTube has none). The Player applies
// the decisions through the <video> API, which the Google Cast shadow
// forwards to the TV (lib/castShadow.ts), so both also work while casting.
// Kept free of imports so scripts/player-loop.test.mjs runs it under plain
// Node.

/** A-B points in seconds; null = not set. */
export type AbLoop = {a: number | null; b: number | null};

export const NO_AB: AbLoop = {a: null, b: null};

/** Shortest A-B span: anything tighter would seek on every timeupdate. */
export const MIN_AB_SPAN = 1;

const finite = (n: number) => (Number.isFinite(n) ? n : 0);

/**
 * Sets A or B from the current time. Times are clamped into [0, duration]
 * (duration 0 / NaN = unknown, no upper clamp). A point that would leave
 * the pair out of order or tighter than MIN_AB_SPAN drops the other point,
 * so the viewer can always move on (set A again, then B).
 */
export function setAbPoint(ab: AbLoop, which: 'a' | 'b', time: number, duration: number): AbLoop {
  const end = finite(duration) > 0 ? duration : Infinity;
  const at = Math.min(end, Math.max(0, finite(time)));
  if (which === 'a') {
    const b = ab.b !== null && ab.b - at >= MIN_AB_SPAN ? ab.b : null;
    return {a: at, b};
  }
  const a = ab.a ?? 0;
  if (at - a < MIN_AB_SPAN) return {a: ab.a, b: null};
  return {a, b: at};
}

/** True when both points are set and in order. */
export function abActive(ab: AbLoop): ab is {a: number; b: number} {
  return ab.a !== null && ab.b !== null && ab.b - ab.a >= MIN_AB_SPAN;
}

/**
 * On timeupdate: where to seek, or null. Reaching B jumps back to A. A
 * position before A (the viewer seeked there) is left alone, as on YouTube.
 */
export function abSeekTarget(ab: AbLoop, time: number): number | null {
  if (!abActive(ab)) return null;
  return time >= ab.b ? ab.a : null;
}

/**
 * When the video ends: 'a' jumps back to A (B at or past the end),
 * 'restart' plays again from 0 (循环播放), 'end' lets the end screen and
 * autoplay-next run.
 */
export function endAction(loop: boolean, ab: AbLoop): 'a' | 'restart' | 'end' {
  if (abActive(ab)) return 'a';
  return loop ? 'restart' : 'end';
}

/** Progress-bar markers as fractions of the duration (null when unknown or unset). */
export function abMarkers(ab: AbLoop, duration: number): {a: number | null; b: number | null} {
  const d = finite(duration);
  if (d <= 0) return {a: null, b: null};
  const f = (x: number | null) => (x === null ? null : Math.min(1, Math.max(0, x / d)));
  return {a: f(ab.a), b: f(ab.b)};
}
