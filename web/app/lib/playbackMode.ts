// When the release page resolves a provider stream (POST /api/resolve/...).
// PLAYBACK_RESOLVE_ON (web server env, exposed through the root loader):
//   load (default): on page load, as since #115 (the page auto-starts);
//   play: the page still loads play-config / the playback mapping (DB only)
//         and shows the poster and play button, but resolves on the play
//         click, except for autoplay-next and queue navigation, which must
//         start at once.
// Kept free of imports so scripts/playback-mode.test.mjs runs it under
// plain Node.

export type ResolveOn = 'load' | 'play';

/** PLAYBACK_RESOLVE_ON: "play" (any case, trimmed) or the default "load". */
export function parseResolveOn(v: string | null | undefined): ResolveOn {
  return (v ?? '').trim().toLowerCase() === 'play' ? 'play' : 'load';
}

/**
 * Whether the page should hold the provider resolve until the viewer
 * presses play. `autoplay`: the page was opened by autoplay-next (link
 * state); `queued`: it plays from a queue / list (?list=).
 */
export function deferResolve(mode: ResolveOn, nav: {autoplay: boolean; queued: boolean}): boolean {
  return mode === 'play' && !nav.autoplay && !nav.queued;
}
