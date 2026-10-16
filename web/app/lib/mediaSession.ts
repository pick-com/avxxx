// Pure helpers for the Media Session API (lock screen, headset / Bluetooth
// remotes, keyboard media keys, OS media controls). The hook that talks to
// navigator.mediaSession is lib/useMediaSession.ts; everything here is plain
// data in, data out so it runs under node --test (scripts/media-session.test.mjs).

/** Default skip for seekbackward / seekforward when the OS sends no seekOffset. */
export const SEEK_STEP_S = 10;
/** setPositionState is re-sent on timeupdate at most this often while nothing else changed. */
export const POSITION_RESYNC_MS = 5_000;
/** ... or sooner when the playhead drifted this far from where the OS extrapolates it. */
export const POSITION_DRIFT_S = 1.5;

export type ArtworkSource = {url: string; width?: number; height?: number; srcset?: {url: string; width: number; height: number}[] | null};
export type Artwork = {src: string; sizes?: string; type?: string};

const MIME_BY_EXT: Record<string, string> = {jpg: 'image/jpeg', jpeg: 'image/jpeg', png: 'image/png', webp: 'image/webp', avif: 'image/avif', gif: 'image/gif'};

function mimeOf(url: string): string | undefined {
  const ext = /\.([a-z0-9]+)(?:[?#]|$)/i.exec(url)?.[1]?.toLowerCase();
  return ext ? MIME_BY_EXT[ext] : undefined;
}

function absolute(url: string, base: string): string | null {
  try {
    return new URL(url, base).href;
  } catch {
    return null;
  }
}

/**
 * MediaMetadata.artwork from the release cover: absolute URLs (the OS fetches
 * them outside the page), with `sizes` when the stored size is known: every
 * srcset width once plus the original, smallest first like srcset (the OS
 * picks the best fit).
 */
export function artworkFor(cover: ArtworkSource | null | undefined, base: string): Artwork[] {
  if (!cover?.url) return [];
  const all: {url: string; width?: number; height?: number}[] = [...(cover.srcset ?? []), {url: cover.url, width: cover.width, height: cover.height}];
  const seen = new Set<string>();
  const out: {art: Artwork; width: number}[] = [];
  for (const img of all) {
    const src = img.url ? absolute(img.url, base) : null;
    if (!src || seen.has(src)) continue;
    seen.add(src);
    const art: Artwork = {src};
    if (img.width && img.height && img.width > 0 && img.height > 0) art.sizes = `${Math.round(img.width)}x${Math.round(img.height)}`;
    const type = mimeOf(src);
    if (type) art.type = type;
    out.push({art, width: img.width ?? Number.MAX_SAFE_INTEGER});
  }
  return out.sort((a, b) => a.width - b.width).map(x => x.art);
}

/** Lead performers for MediaMetadata.artist: the first few names, deduplicated. */
export function artistLine(names: (string | null | undefined)[], max = 3): string {
  return [...new Set(names.map(n => (n ?? '').trim()).filter(Boolean))].slice(0, max).join(', ');
}

/**
 * Where seekbackward / seekforward lands: details.seekOffset (seconds) when
 * the OS sends one, else SEEK_STEP_S, clamped to [0, duration] (no upper
 * bound while the duration is unknown).
 */
export function seekByTarget(action: 'seekbackward' | 'seekforward', seekOffset: number | undefined, current: number, duration: number, step = SEEK_STEP_S): number {
  const offset = typeof seekOffset === 'number' && Number.isFinite(seekOffset) && seekOffset > 0 ? seekOffset : step;
  const to = current + (action === 'seekbackward' ? -offset : offset);
  const end = Number.isFinite(duration) && duration > 0 ? duration : Infinity;
  return Math.min(end, Math.max(0, to));
}

/** The seekto target clamped to the media, or null when seekTime is missing. */
export function seekToTarget(seekTime: number | undefined, duration: number): number | null {
  if (typeof seekTime !== 'number' || !Number.isFinite(seekTime)) return null;
  const end = Number.isFinite(duration) && duration > 0 ? duration : Infinity;
  return Math.min(end, Math.max(0, seekTime));
}

export type PositionState = {duration: number; position: number; playbackRate: number};

/**
 * A valid setPositionState argument, or null when the media has no finite
 * duration yet (or is live): the caller then clears the position state.
 * setPositionState throws on position > duration or a zero rate.
 */
export function positionStateOf(duration: number, position: number, rate: number): PositionState | null {
  if (!Number.isFinite(duration) || duration <= 0) return null;
  const pos = Number.isFinite(position) ? Math.min(duration, Math.max(0, position)) : 0;
  const playbackRate = Number.isFinite(rate) && rate !== 0 ? rate : 1;
  return {duration, position: pos, playbackRate};
}

/**
 * Throttle for setPositionState. The OS extrapolates the position from the
 * last state and the rate, so the state goes out on every discrete change
 * (play / pause / seeked / ratechange / durationchange: `force`), and on
 * timeupdate only when the duration or rate changed, the playhead drifted
 * (a stall while buffering), or POSITION_RESYNC_MS passed.
 */
export class PositionGate {
  private last: (PositionState & {at: number; playing: boolean}) | null = null;
  private readonly resyncMs: number;
  private readonly driftS: number;

  // No parameter properties: node --test only strips types.
  constructor(resyncMs = POSITION_RESYNC_MS, driftS = POSITION_DRIFT_S) {
    this.resyncMs = resyncMs;
    this.driftS = driftS;
  }

  /** Whether `next` should be sent now; records it when so. */
  check(next: PositionState | null, playing: boolean, now: number, force = false): boolean {
    const last = this.last;
    if (!next) {
      // Nothing valid to send: clear once, then stay quiet.
      if (last === null && !force) return false;
      this.last = null;
      return true;
    }
    let send = force || !last || last.duration !== next.duration || last.playbackRate !== next.playbackRate || last.playing !== playing || now - last.at >= this.resyncMs;
    if (!send && last) {
      const expected = last.position + (last.playing ? ((now - last.at) / 1000) * last.playbackRate : 0);
      send = Math.abs(next.position - expected) > this.driftS;
    }
    if (send) this.last = {...next, at: now, playing};
    return send;
  }

  reset() {
    this.last = null;
  }
}

/** A release the previoustrack / nexttrack media keys navigate to. */
export type TrackTarget = {slug: string; query: string; reason?: string};

/**
 * previoustrack / nexttrack targets, in the release page's own order:
 *   1. the play queue (?list=): its previous / next item;
 *   2. the series (选集): the episode before / after this one (?list= kept
 *      when that episode is in the queue);
 *   3. next only: the 接下来播放 work autoplay would start.
 * A missing side means the OS hides (or greys out) that button.
 */
export function adjacentTracks({slug, queue, episodes, related}: {
  slug: string;
  queue?: {name: string; slugs: string[]} | null;
  episodes?: string[] | null;
  related?: {slug: string; reason?: string} | null;
}): {previous?: TrackTarget; next?: TrackTarget} {
  const out: {previous?: TrackTarget; next?: TrackTarget} = {};
  const listQuery = (target: string) => (queue && queue.slugs.includes(target) ? `?list=${queue.name}` : '');
  const qi = queue ? queue.slugs.indexOf(slug) : -1;
  if (queue && qi >= 0) {
    if (qi > 0) out.previous = {slug: queue.slugs[qi - 1], query: listQuery(queue.slugs[qi - 1])};
    if (qi + 1 < queue.slugs.length) out.next = {slug: queue.slugs[qi + 1], query: listQuery(queue.slugs[qi + 1])};
  }
  const ei = episodes ? episodes.indexOf(slug) : -1;
  if (episodes && ei >= 0) {
    if (!out.previous && ei > 0) out.previous = {slug: episodes[ei - 1], query: listQuery(episodes[ei - 1]), reason: 'same_series'};
    if (!out.next && ei + 1 < episodes.length) out.next = {slug: episodes[ei + 1], query: listQuery(episodes[ei + 1]), reason: 'same_series'};
  }
  if (!out.next && related?.slug && related.slug !== slug) out.next = {slug: related.slug, query: '', reason: related.reason};
  return out;
}
