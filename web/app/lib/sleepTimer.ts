// 定时停止 (sleep timer) for the Player: off / 15 / 30 / 45 / 60 minutes of
// wall-clock time, or 播完本集 (stop at the end of this video instead of
// autoplay-next). Over the last FADE_MS the volume fades out, then the video
// pauses and the volume is restored. Timed choices survive source switches
// and end on another release; 播完本集 stays until it fires.
// Kept free of imports so scripts/sleep-timer.test.mjs runs it under plain
// Node.

export type SleepChoice = 'off' | 15 | 30 | 45 | 60 | 'episode';

export const SLEEP_CHOICES: SleepChoice[] = ['off', 15, 30, 45, 60, 'episode'];

export const FADE_MS = 10_000;

export type SleepState = {choice: SleepChoice; deadline: number | null};

export const SLEEP_OFF: SleepState = {choice: 'off', deadline: null};

/** The state after picking `choice` at `now` (ms). */
export function startSleep(choice: SleepChoice, now: number): SleepState {
  if (typeof choice === 'number') return {choice, deadline: now + choice * 60_000};
  return {choice, deadline: null};
}

/** Milliseconds left of a timed choice (never negative); null for off / 播完本集. */
export function sleepRemaining(state: SleepState, now: number): number | null {
  if (state.deadline === null) return null;
  return Math.max(0, state.deadline - now);
}

/** Volume multiplier over the fade: 1 until FADE_MS before the end, then linear to 0. */
export function fadeFactor(remainingMs: number): number {
  if (!Number.isFinite(remainingMs)) return 1;
  return Math.min(1, Math.max(0, remainingMs / FADE_MS));
}

/**
 * One timer tick: 'idle' (nothing timed), 'wait' (counting, full volume),
 * 'fade' (inside the last FADE_MS, apply `factor`), 'fire' (pause now).
 */
export function sleepTick(state: SleepState, now: number): {phase: 'idle' | 'wait' | 'fade' | 'fire'; factor: number} {
  const left = sleepRemaining(state, now);
  if (left === null) return {phase: 'idle', factor: 1};
  if (left <= 0) return {phase: 'fire', factor: 0};
  if (left <= FADE_MS) return {phase: 'fade', factor: fadeFactor(left)};
  return {phase: 'wait', factor: 1};
}

/** What a release change keeps: 播完本集 stays, a timed choice ends. */
export function sleepOnNavigate(state: SleepState): SleepState {
  return state.choice === 'episode' ? state : SLEEP_OFF;
}

/** Whole seconds left for the menu (rounded up so it never shows 0:00 while running). */
export function sleepSecondsLeft(state: SleepState, now: number): number | null {
  const left = sleepRemaining(state, now);
  return left === null ? null : Math.ceil(left / 1000);
}
