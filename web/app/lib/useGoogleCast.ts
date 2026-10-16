import {useCallback, useEffect, useRef, useSyncExternalStore} from 'react';
import type {CastContextInstance, CastFramework, CastRemotePlayer, CastRemotePlayerController, CastStateName, CastWindow, ChromeCastApi} from '../types/cast-sender';
import {CAST_SDK_TIMEOUT_MS, CAST_SENDER_URL, buildCastLoad, castButtonFromState, castErrorKind, remoteSnapshot, shouldLoadCastSdk, type CastLoadSpec, type CastMediaInput} from './googleCast';
import {emitShadow, isCastShadowed, realMedia, shadowMedia, type ShadowBackend} from './castShadow';
import type {CastButtonState} from './remotePlayback';

// Google Cast for the Player (Chrome-family browsers): the Web Sender SDK
// with the Default Media Receiver, so a Google TV / Chromecast plays the
// stream itself and stays controllable from the page, the TV remote and the
// Google Home app (the Remote Playback API of lib/useRemotePlayback.ts hands
// the element off with no control or status channel).
//
// - The sender script loads lazily, once, client-side, only where
//   shouldLoadCastSdk (window.chrome, secure context) and only when a Player
//   mounts. A blocked / offline / failing load leaves sdk 'unavailable' and
//   the Player falls back to Remote Playback; nothing is logged.
// - The 投屏 button follows CastContext's cast state (shown while a device is
//   available or a session is up); it calls requestSession (picker, or the
//   stop / switch dialog while connected).
// - While connected the Player's <video> is shadowed (lib/castShadow.ts):
//   paused and muted locally, it reads and drives the RemotePlayer, and
//   receiver changes arrive as media events. The page controls, Media
//   Session and progress saving therefore follow the TV, including pauses /
//   seeks from the TV remote. Nothing here reacts to page visibility.
// - A new stream (source / quality switch, next release) loads on the
//   receiver; a receiver load error ends the session with a message and the
//   page continues locally; the session also ends when the page has nothing
//   castable any more (`dead`). When the session ends the local video
//   resumes at the receiver's last position.

type Sdk = 'loading' | 'ready' | 'unavailable';
type Store = {sdk: Sdk; castState: CastStateName | null; device: string};

let store: Store = {sdk: 'loading', castState: null, device: ''};
const listeners = new Set<() => void>();
function setStore(patch: Partial<Store>) {
  const next = {...store, ...patch};
  if (next.sdk === store.sdk && next.castState === store.castState && next.device === store.device) return;
  store = next;
  listeners.forEach(fn => fn());
}
function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}
const SERVER_STORE: Store = {sdk: 'loading', castState: null, device: ''};

type Api = {chrome: ChromeCastApi; framework: CastFramework; context: CastContextInstance; player: CastRemotePlayer; controller: CastRemotePlayerController};
let api: Api | null = null;
let loading: Promise<boolean> | null = null;

// The receiver as the page last saw it (kept after a disconnect: the resume point).
type Remote = {time: number; duration: number; paused: boolean; ended: boolean; failed: boolean; volume: number; muted: boolean; loaded: boolean; contentId: string};
const remote: Remote = {time: 0, duration: 0, paused: true, ended: false, failed: false, volume: 1, muted: false, loaded: false, contentId: ''};
const remoteListeners = new Set<(fields: Set<string>) => void>();

function sessionDevice(): string {
  try {
    return api?.context.getCurrentSession()?.getCastDevice().friendlyName ?? '';
  } catch {
    return '';
  }
}

function readRemote(): Set<string> {
  const changed = new Set<string>();
  if (!api || !api.player.isConnected) return changed;
  const p = api.player;
  let idleReason: string | null | undefined;
  try {
    idleReason = api.context.getCurrentSession()?.getMediaSession()?.idleReason;
  } catch {
    idleReason = null;
  }
  const snap = remoteSnapshot({playerState: p.playerState, idleReason, isPaused: p.isPaused, isMediaLoaded: p.isMediaLoaded, currentTime: p.currentTime, duration: p.duration});
  const set = <K extends keyof Remote>(key: K, value: Remote[K]) => {
    if (remote[key] !== value) {
      remote[key] = value;
      changed.add(key);
    }
  };
  set('loaded', p.isMediaLoaded);
  if (p.isMediaLoaded) {
    set('time', snap.time);
    if (snap.duration) set('duration', snap.duration);
    set('contentId', p.mediaInfo?.contentId ?? '');
  }
  // While a LOAD is in flight (not loaded, not failed) keep the optimistic state.
  if (p.isMediaLoaded || snap.ended || snap.failed) {
    set('paused', snap.paused);
    set('ended', snap.ended);
    set('failed', snap.failed);
  }
  set('volume', Number.isFinite(p.volumeLevel) ? p.volumeLevel : 1);
  set('muted', !!p.isMuted);
  return changed;
}

function init(): boolean {
  const w = window as unknown as CastWindow;
  const chrome = w.chrome?.cast;
  const framework = w.cast?.framework;
  if (!chrome?.media || !framework) return false;
  const context = framework.CastContext.getInstance();
  context.setOptions({receiverApplicationId: chrome.media.DEFAULT_MEDIA_RECEIVER_APP_ID, autoJoinPolicy: chrome.AutoJoinPolicy.ORIGIN_SCOPED});
  const player = new framework.RemotePlayer();
  const controller = new framework.RemotePlayerController(player);
  api = {chrome, framework, context, player, controller};
  const onCastState = () => setStore({castState: context.getCastState(), device: sessionDevice()});
  context.addEventListener(framework.CastContextEventType.CAST_STATE_CHANGED, onCastState);
  context.addEventListener(framework.CastContextEventType.SESSION_STATE_CHANGED, onCastState);
  controller.addEventListener(framework.RemotePlayerEventType.ANY_CHANGE, () => {
    const changed = readRemote();
    if (changed.size) remoteListeners.forEach(fn => fn(changed));
  });
  readRemote();
  setStore({sdk: 'ready', castState: context.getCastState(), device: sessionDevice()});
  return true;
}

function loadSdk(): Promise<boolean> {
  if (loading) return loading;
  loading = new Promise<boolean>(resolve => {
    const w = window as unknown as CastWindow;
    if (w.cast?.framework && w.chrome?.cast?.media) {
      resolve(true);
      return;
    }
    const timer = window.setTimeout(() => resolve(false), CAST_SDK_TIMEOUT_MS);
    w.__onGCastApiAvailable = available => {
      window.clearTimeout(timer);
      resolve(available);
    };
    const script = document.createElement('script');
    script.src = CAST_SENDER_URL;
    script.async = true;
    script.onerror = () => {
      window.clearTimeout(timer);
      resolve(false);
    };
    document.head.appendChild(script);
  })
    .then(ok => {
      try {
        return ok && init();
      } catch {
        return false;
      }
    })
    .then(ok => {
      if (!ok) setStore({sdk: 'unavailable'});
      return ok;
    });
  return loading;
}

function toLoadRequest(a: Api, spec: CastLoadSpec) {
  const info = new a.chrome.media.MediaInfo(spec.contentId, spec.contentType);
  info.contentUrl = spec.contentId;
  info.streamType = a.chrome.media.StreamType.BUFFERED;
  if (spec.hlsSegmentFormat) info.hlsSegmentFormat = spec.hlsSegmentFormat;
  if (spec.hlsVideoSegmentFormat) info.hlsVideoSegmentFormat = spec.hlsVideoSegmentFormat;
  const meta = new a.chrome.media.MovieMediaMetadata();
  meta.title = spec.metadata.title;
  if (spec.metadata.subtitle) meta.subtitle = spec.metadata.subtitle;
  if (spec.metadata.studio) meta.studio = spec.metadata.studio;
  meta.images = spec.metadata.images.map(img => {
    const image = new a.chrome.Image(img.url);
    if (img.width && img.height) {
      image.width = img.width;
      image.height = img.height;
    }
    return image;
  });
  info.metadata = meta;
  const request = new a.chrome.media.LoadRequest(info);
  request.currentTime = spec.currentTime;
  request.autoplay = spec.autoplay;
  return request;
}

type HlsLike = {stopLoad(): void; startLoad(startPosition?: number): void};

export type GoogleCastOptions = {
  /** The Player's <video> while a video source is shown. */
  video: HTMLVideoElement | null;
  /** What to cast for the current source; null when nothing castable is shown. */
  media: CastMediaInput | null;
  /** Nothing castable will come (failed, embed / partner page, no source): ends a session. */
  dead: boolean;
  /** Where a newly shown source starts (resume point / shared position). */
  startTime: () => number;
  /** The Player's hls.js instance: loading stops while the TV plays. */
  hls?: {current: HlsLike | null};
  /** Messages for the viewer (the Player's notice). */
  onMessage: (kind: 'sourceFailed' | 'failed' | 'noStream') => void;
};

export type GoogleCast = {
  sdk: Sdk;
  /** The 投屏 button state ('hidden' unless the SDK is ready). */
  state: CastButtonState;
  /** The connected device's name ('' when unknown). */
  device: string;
  /** A session is up (the <video> stands in for the receiver). */
  connected: boolean;
  /** `connected` for callbacks created before the next render. */
  connectedRef: {current: boolean};
  prompt: () => void;
};

export function useGoogleCast({video, media, dead, startTime, hls, onMessage}: GoogleCastOptions): GoogleCast {
  const snap = useSyncExternalStore(subscribe, () => store, () => SERVER_STORE);
  const connected = snap.sdk === 'ready' && snap.castState === 'CONNECTED';
  const connectedRef = useRef(connected);
  connectedRef.current = connected;
  const latest = useRef({media, startTime, hls, onMessage});
  latest.current = {media, startTime, hls, onMessage};

  // Load the SDK when a Player mounts (Chrome-family, secure context only).
  useEffect(() => {
    if (store.sdk !== 'loading') return;
    if (!shouldLoadCastSdk(window as unknown as {chrome?: unknown; isSecureContext?: boolean})) {
      setStore({sdk: 'unavailable'});
      return;
    }
    void loadSdk();
  }, []);

  const mediaKey = media ? JSON.stringify(media) : '';
  const contentUrl = media?.url ?? '';
  // The LOAD this hook last sent (contentId), so re-renders do not reload.
  const sent = useRef('');

  const end = useCallback((kind: 'sourceFailed' | 'failed' | 'noStream' | null) => {
    if (kind) latest.current.onMessage(kind);
    try {
      api?.context.endCurrentSession(true);
    } catch {
      /* already gone */
    }
  }, []);

  const load = useCallback(
    (startAt: number) => {
      const a = api;
      const m = latest.current.media;
      if (!a || !m) return;
      const spec = buildCastLoad(m, startAt, window.location.href);
      const session = a.context.getCurrentSession();
      if (!spec || !session) return;
      sent.current = spec.contentId;
      Object.assign(remote, {time: spec.currentTime, paused: false, ended: false, failed: false, loaded: false});
      session.loadMedia(toLoadRequest(a, spec)).then(
        () => undefined,
        (code: unknown) => {
          // A newer LOAD (another source) replaced this one: not a failure.
          if (sent.current !== spec.contentId) return;
          const kind = castErrorKind(code, 'load');
          if (kind === 'ignore') return;
          end(kind === 'source' ? 'sourceFailed' : 'failed');
        },
      );
    },
    [end],
  );

  // Shadow the <video> while connected; resume locally when the session ends.
  useEffect(() => {
    if (!connected || !video || !media) return;
    const real = realMedia(video);
    const localTime = Number(real.get('currentTime')) || 0;
    const backend: ShadowBackend = {
      time: () => remote.time,
      duration: () => remote.duration,
      paused: () => remote.paused,
      ended: () => remote.ended,
      volume: () => remote.volume,
      muted: () => remote.muted,
      play: () => {
        const a = api;
        if (!a) return;
        if (!a.player.isMediaLoaded || remote.ended) {
          // Finished / idle receiver (replay, 从头播放): load again where the page points.
          load(remote.ended ? 0 : remote.time);
          return;
        }
        if (a.player.isPaused) a.controller.playOrPause();
      },
      pause: () => {
        const a = api;
        if (a?.player.isMediaLoaded && !a.player.isPaused) a.controller.playOrPause();
      },
      seek: to => {
        const a = api;
        if (!Number.isFinite(to)) return;
        remote.time = Math.max(0, to);
        if (to <= 0 && remote.ended) remote.ended = false;
        if (a?.player.isMediaLoaded) {
          a.player.currentTime = remote.time;
          a.controller.seek();
        }
        emitShadow(video, 'timeupdate');
      },
      setVolume: v => {
        const a = api;
        remote.volume = v;
        if (a) {
          a.player.volumeLevel = v;
          a.controller.setVolumeLevel();
        }
        emitShadow(video, 'volumechange');
      },
      setMuted: m => {
        const a = api;
        if (a && a.player.isMuted !== m) a.controller.muteOrUnmute();
        remote.muted = m;
        emitShadow(video, 'volumechange');
      },
    };
    // Same stream already on the TV (auto-joined session after a reload): keep it.
    const already = api?.player.isMediaLoaded && api.player.mediaInfo?.contentId === buildCastLoad(media, 0, window.location.href)?.contentId;
    const restore = shadowMedia(video, backend);
    latest.current.hls?.current?.stopLoad();
    if (already) {
      sent.current = api?.player.mediaInfo?.contentId ?? '';
      readRemote();
    } else {
      load(localTime > 0 ? localTime : latest.current.startTime());
    }
    ['durationchange', 'timeupdate', 'volumechange', remote.paused ? 'pause' : 'play'].forEach(type => emitShadow(video, type));

    const onRemote = (fields: Set<string>) => {
      if (fields.has('failed') && remote.failed) {
        end('sourceFailed');
        return;
      }
      if (fields.has('duration')) emitShadow(video, 'durationchange');
      if (fields.has('time')) emitShadow(video, 'timeupdate');
      if (fields.has('volume') || fields.has('muted')) emitShadow(video, 'volumechange');
      if (fields.has('paused')) {
        emitShadow(video, remote.paused ? 'pause' : 'play');
        if (!remote.paused) emitShadow(video, 'playing');
      }
      if (fields.has('ended') && remote.ended) emitShadow(video, 'ended');
    };
    remoteListeners.add(onRemote);
    return () => {
      remoteListeners.delete(onRemote);
      restore();
      // Session over (not a source switch / unmount): continue here.
      if (!connectedRef.current && video.isConnected) {
        const at = remote.time;
        const hlsNow = latest.current.hls?.current;
        if (at > 0) real.set('currentTime', at);
        hlsNow?.startLoad(at > 0 ? at : undefined);
        if (!remote.ended) void (real.call('play') as Promise<void> | undefined)?.catch(() => undefined);
        sent.current = '';
      }
    };
    // `media` itself is read through `latest`: a new object for the same
    // stream is not a change; a different stream renders a new <video>.
  }, [connected, video, !!media, load, end]);

  // A quality / variant change on the same <video>: reload at the TV's position.
  useEffect(() => {
    if (!connected || !video || !contentUrl || !isCastShadowed(video) || !sent.current) return;
    const spec = latest.current.media && buildCastLoad(latest.current.media, 0, window.location.href);
    if (!spec || spec.contentId === sent.current) return;
    load(remote.time);
  }, [connected, video, contentUrl, mediaKey, load]);

  // Nothing castable any more (the next release has no stream): end gracefully.
  useEffect(() => {
    if (connected && dead) end('noStream');
  }, [connected, dead, end]);

  const prompt = useCallback(() => {
    api?.context.requestSession().catch((code: unknown) => {
      if (castErrorKind(code) === 'session') latest.current.onMessage('failed');
    });
  }, []);

  return {
    sdk: snap.sdk,
    state: snap.sdk === 'ready' ? castButtonFromState(snap.castState, !!media) : 'hidden',
    device: connected ? snap.device : '',
    connected,
    connectedRef,
    prompt,
  };
}
