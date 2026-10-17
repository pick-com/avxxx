// GET /api/releases/{slug}/playback (the player's legacy path). A release
// with no mapping answers 200 {source, available: false} instead of 404, so
// no-source release pages log no console error. Pure (node --test).

/** The "no mapping" answer. */
export type PlaybackNone = {source: string; available: false};

/** The mapping, or null for the no-source answer (or an empty body). */
export function playbackOrNone<T extends {resolve_path?: string; available?: boolean}>(body: T | PlaybackNone | null | undefined): T | null {
  if (!body || typeof body !== 'object') return null;
  if (body.available === false) return null;
  if (typeof (body as T).resolve_path !== 'string' || !(body as T).resolve_path) return null;
  return body as T;
}
