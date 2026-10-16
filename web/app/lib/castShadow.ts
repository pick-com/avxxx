// While a Google Cast session plays the Player's stream on a TV, the page's
// <video> stays mounted (paused and muted) and stands in for the receiver:
// its playback properties and methods are shadowed by own properties that
// read the receiver's state and send commands to it, and receiver changes
// are dispatched on it as ordinary media events (timeupdate, play, pause,
// durationchange, volumechange, ended).
//
// Everything that already drives or watches the <video> (the control bar,
// the touch layer, keyboard shortcuts, Media Session, watch-progress saving,
// the resume toast, danmaku, analytics) therefore controls and follows the
// TV without knowing about casting. restore() deletes the own properties so
// the prototype's real ones apply again.
//
// No DOM types are required (node --test drives it with a fake element; see
// scripts/google-cast.test.mjs).

export type ShadowBackend = {
  time(): number;
  duration(): number;
  paused(): boolean;
  ended(): boolean;
  volume(): number;
  muted(): boolean;
  play(): void;
  pause(): void;
  seek(to: number): void;
  setVolume(volume: number): void;
  setMuted(muted: boolean): void;
};

type MediaLike = EventTarget & {currentTime: number; paused: boolean; muted: boolean; autoplay?: boolean; pause(): void; play(): Promise<void> | void};

type Real = {
  get(name: string): unknown;
  set(name: string, value: unknown): void;
  call(name: string): unknown;
};

const SHADOWED = ['currentTime', 'duration', 'paused', 'ended', 'volume', 'muted', 'buffered', 'play', 'pause'] as const;

const shadows = new WeakMap<object, {backend: ShadowBackend; restore: () => RestoreInfo}>();

/** Descriptor from the prototype chain, skipping the element's own (shadow) properties. */
function protoDescriptor(target: object, name: string): PropertyDescriptor | undefined {
  for (let p = Object.getPrototypeOf(target); p; p = Object.getPrototypeOf(p)) {
    const d = Object.getOwnPropertyDescriptor(p, name);
    if (d) return d;
  }
  return undefined;
}

/** The element's real (prototype) properties, also while shadowed. */
export function realMedia(target: object): Real {
  return {
    get: name => protoDescriptor(target, name)?.get?.call(target),
    set: (name, value) => protoDescriptor(target, name)?.set?.call(target, value),
    call: name => {
      const fn = protoDescriptor(target, name)?.value;
      return typeof fn === 'function' ? fn.call(target) : undefined;
    },
  };
}

export function isCastShadowed(target: object | null | undefined): boolean {
  return !!target && shadows.has(target);
}

const EMPTY_RANGES = {
  length: 0,
  start(): number {
    throw new RangeError('empty');
  },
  end(): number {
    throw new RangeError('empty');
  },
};

export type RestoreInfo = {wasMuted: boolean};

/**
 * Shadows `media` with `backend`. Pauses and mutes the real element first
 * (and turns autoplay off so a freshly loaded element does not start), and
 * keeps it paused if something native starts it. Returns restore(), which
 * also unmutes the real element when it was not muted before. Calling it
 * twice on the same element returns the existing restore().
 */
export function shadowMedia(media: MediaLike, backend: ShadowBackend): () => RestoreInfo {
  const existing = shadows.get(media);
  if (existing) return existing.restore;
  const real = realMedia(media);
  const wasMuted = real.get('muted') === true;
  real.set('autoplay', false);
  real.call('pause');
  real.set('muted', true);

  // Native playback while shadowed (autoplay that slipped through, a
  // browser media control): stop it again.
  const keepPaused = (event: Event) => {
    if (event.isTrusted !== false && real.get('paused') === false) real.call('pause');
  };
  media.addEventListener('playing', keepPaused);

  const own = (name: string, d: PropertyDescriptor) => Object.defineProperty(media, name, {configurable: true, enumerable: false, ...d});
  own('currentTime', {get: () => backend.time(), set: (v: number) => backend.seek(Number(v))});
  own('duration', {get: () => backend.duration() || NaN});
  own('paused', {get: () => backend.paused()});
  own('ended', {get: () => backend.ended()});
  own('volume', {get: () => backend.volume(), set: (v: number) => backend.setVolume(Math.min(1, Math.max(0, Number(v))))});
  own('muted', {get: () => backend.muted(), set: (v: boolean) => backend.setMuted(!!v)});
  own('buffered', {get: () => EMPTY_RANGES});
  own('play', {
    writable: true,
    value: () => {
      backend.play();
      return Promise.resolve();
    },
  });
  own('pause', {writable: true, value: () => backend.pause()});

  let done = false;
  const restore = (): RestoreInfo => {
    if (!done) {
      done = true;
      media.removeEventListener('playing', keepPaused);
      for (const name of SHADOWED) delete (media as unknown as Record<string, unknown>)[name];
      shadows.delete(media);
      if (!wasMuted) real.set('muted', false);
    }
    return {wasMuted};
  };
  shadows.set(media, {backend, restore});
  return restore;
}

/** Dispatches a receiver change on the shadowed element (no-op when not shadowed). */
export function emitShadow(media: EventTarget | null | undefined, type: string): void {
  if (!media || !shadows.has(media)) return;
  media.dispatchEvent(new Event(type));
}
