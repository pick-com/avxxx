// Where page-level overlays (the sign-in sheet, its toast) render while the
// document is in fullscreen. Only the fullscreen element and its children
// are visible then, so an overlay portalled to document.body would open out
// of view (the player shell in fullscreen: 发送弹幕 as a guest).

import {useSyncExternalStore} from 'react';

type FullscreenLike = {tagName?: string} | null | undefined;

/**
 * The element an overlay should portal into: the fullscreen element when it
 * can hold children, otherwise null (document.body). A <video> or <iframe> in
 * element fullscreen shows none of our DOM, so the caller exits fullscreen
 * first in that case (needsFullscreenExit).
 */
export function overlayHost<T extends FullscreenLike>(fullscreen: T): T | null {
  if (!fullscreen || typeof fullscreen.tagName !== 'string') return null;
  return /^(VIDEO|IFRAME|IMG|CANVAS)$/i.test(fullscreen.tagName) ? null : fullscreen;
}

/** True when an overlay cannot be shown inside the current fullscreen element. */
export function needsFullscreenExit(fullscreen: FullscreenLike): boolean {
  return !!fullscreen && overlayHost(fullscreen) === null;
}

function subscribe(onChange: () => void) {
  document.addEventListener('fullscreenchange', onChange);
  return () => document.removeEventListener('fullscreenchange', onChange);
}

/** The overlay host for the current fullscreen state (null outside fullscreen and on the server). */
export function useOverlayHost(): HTMLElement | null {
  return useSyncExternalStore(
    subscribe,
    () => overlayHost(document.fullscreenElement as HTMLElement | null),
    () => null,
  );
}
