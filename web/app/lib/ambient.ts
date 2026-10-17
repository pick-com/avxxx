// 氛围模式 (ambient mode, YouTube parity): a blurred, low-opacity glow behind
// the player sampled from the video (components/AmbientGlow.tsx). The
// toggle is remembered in this browser, default off. Kept free of imports
// so scripts/ambient.test.mjs runs it under plain Node.

export const AMBIENT_KEY = 'player_ambient_v1';

/** How often a playing video is sampled. */
export const AMBIENT_SAMPLE_MS = 500;

/** The canvas is tiny: the glow is blurred anyway, and drawImage stays cheap. */
export const AMBIENT_SIZE = {width: 32, height: 18};

type StorageLike = {getItem(key: string): string | null; setItem(key: string, value: string): void};

export function readAmbientPref(storage: StorageLike | null | undefined): boolean {
  try {
    return storage?.getItem(AMBIENT_KEY) === '1';
  } catch {
    return false;
  }
}

export function writeAmbientPref(storage: StorageLike | null | undefined, on: boolean): void {
  try {
    storage?.setItem(AMBIENT_KEY, on ? '1' : '0');
  } catch {
    /* storage blocked: the toggle lasts for this page only */
  }
}

/**
 * Whether the glow is drawn: the viewer turned it on, the source allows
 * frame reads (not `blocked`), and the player is inline. Never in
 * fullscreen, in the mini player, or while casting (the local <video>
 * shows no picture then).
 */
export function ambientActive(s: {pref: boolean; blocked: boolean; video: boolean; fullscreen: boolean; mini: boolean; casting: boolean}): boolean {
  return s.pref && s.video && !s.blocked && !s.fullscreen && !s.mini && !s.casting;
}

/**
 * Sampling runs only for a visible page and a playing video; reduced
 * motion draws single frames (on load / seek / pause) instead of a loop.
 */
export function ambientSampling(s: {hidden: boolean; paused: boolean; reducedMotion: boolean}): boolean {
  return !s.hidden && !s.paused && !s.reducedMotion;
}

/** A cross-origin frame read: the canvas is tainted (SecurityError). */
export function isTaintError(error: unknown): boolean {
  return typeof error === 'object' && error !== null && (error as {name?: unknown}).name === 'SecurityError';
}
