// Pure helpers for Google Cast (Web Sender SDK, Default Media Receiver). The
// hook that talks to the SDK is lib/useGoogleCast.ts; everything here is plain
// data in, data out so it runs under node --test (scripts/google-cast.test.mjs).

import type {ArtworkSource} from './mediaSession';
import type {CastButtonState, RemoteConnection} from './remotePlayback';

export const CAST_SENDER_URL = 'https://www.gstatic.com/cv/js/sender/v1/cast_sender.js?loadCastFramework=1';
/** The SDK reports availability through window.__onGCastApiAvailable; give up after this. */
export const CAST_SDK_TIMEOUT_MS = 15_000;

/**
 * Whether to load the Cast sender at all: Chrome-family browsers only
 * (window.chrome; Chrome, Edge, Opera, Brave, Android Chrome) in a secure
 * context (https or localhost; the SDK refuses to start elsewhere).
 */
export function shouldLoadCastSdk(win: {chrome?: unknown; isSecureContext?: boolean} | null | undefined): boolean {
  return !!win && typeof win.chrome === 'object' && win.chrome !== null && win.isSecureContext === true;
}

/** An absolute http(s) URL the receiver can fetch itself, or null (blob:, data:, garbage). */
export function castableUrl(url: string | null | undefined, base: string): string | null {
  if (!url) return null;
  try {
    const u = new URL(url, base);
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : null;
  } catch {
    return null;
  }
}

const TYPE_BY_EXT: Record<string, string> = {
  m3u8: 'application/x-mpegURL',
  mpd: 'application/dash+xml',
  mp4: 'video/mp4',
  m4v: 'video/mp4',
  webm: 'video/webm',
};

/** MediaInfo.contentType: HLS when the source says so, else by extension, else MP4. */
export function castContentType(url: string, hls: boolean): string {
  if (hls) return TYPE_BY_EXT.m3u8;
  let path = url;
  try {
    path = new URL(url).pathname;
  } catch {
    /* relative: use as is */
  }
  const ext = /\.([a-z0-9]+)$/i.exec(path)?.[1]?.toLowerCase();
  return (ext && TYPE_BY_EXT[ext]) || 'video/mp4';
}

/**
 * HLS segment container from a media segment URL (hls.js knows them once a
 * level playlist loaded). The Default Media Receiver assumes MPEG-TS for HLS,
 * so fMP4 streams must say so. Unknown: undefined (receiver default).
 */
export function hlsSegmentFormatOf(segmentUrl: string | null | undefined): 'ts' | 'fmp4' | undefined {
  if (!segmentUrl) return undefined;
  let path = segmentUrl;
  try {
    path = new URL(segmentUrl, 'https://x.invalid/').pathname;
  } catch {
    /* keep the raw string */
  }
  const ext = /\.([a-z0-9]+)$/i.exec(path)?.[1]?.toLowerCase();
  if (ext === 'ts') return 'ts';
  if (ext === 'm4s' || ext === 'mp4' || ext === 'm4v' || ext === 'cmfv') return 'fmp4';
  return undefined;
}

export type CastMediaInput = {
  url: string;
  hls: boolean;
  title: string;
  artist?: string;
  album?: string;
  artwork?: ArtworkSource | null;
  /** hlsSegmentFormatOf(...) when known. */
  segmentFormat?: 'ts' | 'fmp4';
};

/** Plain description of the LOAD request; the hook turns it into chrome.cast objects. */
export type CastLoadSpec = {
  contentId: string;
  contentType: string;
  streamType: 'BUFFERED';
  hlsSegmentFormat?: 'ts' | 'fmp4';
  hlsVideoSegmentFormat?: 'mpeg2_ts' | 'fmp4';
  metadata: {title: string; subtitle?: string; studio?: string; images: {url: string; width?: number; height?: number}[]};
  currentTime: number;
  autoplay: true;
};

/**
 * The LOAD request for the receiver: absolute stream URL and content type,
 * movie metadata (title, performers as subtitle, maker / series as studio,
 * cover images largest first: the receiver shows the first), the start
 * position (never negative; 0 when unknown) and autoplay.
 * Null when the URL cannot be fetched by the receiver (blob:, data:).
 */
export function buildCastLoad(input: CastMediaInput, startAt: number, base: string): CastLoadSpec | null {
  const url = castableUrl(input.url, base);
  if (!url) return null;
  const contentType = castContentType(url, input.hls);
  const isHls = contentType === TYPE_BY_EXT.m3u8;
  const images = castImages(input.artwork, base);
  const metadata: CastLoadSpec['metadata'] = {title: input.title, images};
  if (input.artist) metadata.subtitle = input.artist;
  if (input.album) metadata.studio = input.album;
  const spec: CastLoadSpec = {
    contentId: url,
    contentType,
    streamType: 'BUFFERED',
    metadata,
    currentTime: Number.isFinite(startAt) && startAt > 0 ? startAt : 0,
    autoplay: true,
  };
  if (isHls && input.segmentFormat === 'fmp4') {
    spec.hlsSegmentFormat = 'fmp4';
    spec.hlsVideoSegmentFormat = 'fmp4';
  } else if (isHls && input.segmentFormat === 'ts') {
    spec.hlsSegmentFormat = 'ts';
  }
  return spec;
}

/**
 * Receiver images from the release cover: absolute URLs, each once, largest
 * first (the receiver shows the first), with the stored size when known.
 */
export function castImages(cover: ArtworkSource | null | undefined, base: string): {url: string; width?: number; height?: number}[] {
  if (!cover?.url) return [];
  const all = [...(cover.srcset ?? []), {url: cover.url, width: cover.width, height: cover.height}];
  const seen = new Set<string>();
  const out: {url: string; width?: number; height?: number}[] = [];
  for (const img of all) {
    const url = castableUrl(img.url, base);
    if (!url || seen.has(url)) continue;
    seen.add(url);
    out.push(img.width && img.height && img.width > 0 && img.height > 0 ? {url, width: Math.round(img.width), height: Math.round(img.height)} : {url});
  }
  return out.sort((a, b) => (b.width ?? 0) - (a.width ?? 0));
}

/**
 * The URL to cast for an hls.js quality pick: the chosen variant playlist,
 * so the TV plays that quality, unless it is Auto or the variant takes its
 * audio from a separate rendition group (a variant alone would be silent);
 * then the master playlist and the receiver's own ABR.
 */
export function castVariantUrl(master: string, variant: {url?: string; hasAudioGroup?: boolean} | null | undefined): string {
  if (!variant?.url || variant.hasAudioGroup) return master;
  return variant.url;
}

/** CastContext cast state → the shared 投屏 button state (lib/remotePlayback.ts). */
export function castConnection(castState: string | null | undefined): RemoteConnection {
  if (castState === 'CONNECTED') return 'connected';
  if (castState === 'CONNECTING') return 'connecting';
  return 'disconnected';
}

export function castButtonFromState(castState: string | null | undefined, castable: boolean): CastButtonState {
  if (!castable || !castState) return 'hidden';
  const connection = castConnection(castState);
  if (connection !== 'disconnected') return connection;
  return castState === 'NOT_CONNECTED' ? 'idle' : 'hidden';
}

export type RemoteSnapshot = {time: number; duration: number; paused: boolean; ended: boolean; failed: boolean; buffering: boolean};

/**
 * What the page shows for the receiver's state (RemotePlayer fields plus the
 * media session's idleReason): paused unless PLAYING / BUFFERING; ended on
 * IDLE + FINISHED; failed on IDLE + ERROR (a media error after LOAD).
 */
export function remoteSnapshot(p: {
  playerState?: string | null;
  idleReason?: string | null;
  isPaused?: boolean;
  isMediaLoaded?: boolean;
  currentTime?: number;
  duration?: number;
}): RemoteSnapshot {
  const state = p.playerState ?? 'IDLE';
  const idle = state === 'IDLE';
  return {
    time: Number.isFinite(p.currentTime) && (p.currentTime ?? 0) > 0 ? (p.currentTime as number) : 0,
    duration: Number.isFinite(p.duration) && (p.duration ?? 0) > 0 ? (p.duration as number) : 0,
    paused: idle || state === 'PAUSED' || (p.isPaused === true && state !== 'BUFFERING'),
    ended: idle && p.idleReason === 'FINISHED',
    failed: idle && p.idleReason === 'ERROR',
    buffering: state === 'BUFFERING',
  };
}

/**
 * chrome.cast.ErrorCode from loadMedia / requestSession (or a receiver
 * idleReason / error type):
 * - 'source': the receiver could not play this stream (load_media_failed,
 *   LOAD_FAILED, a media ERROR; usually CORS, Referer / cookie protection or
 *   an unsupported format). loadMedia reports a receiver LOAD_FAILED as
 *   session_error, so that code counts as 'source' for a load;
 * - 'ignore': the viewer closed the device picker, or a newer LOAD replaced
 *   this one;
 * - 'session': anything else (timeout, channel error, receiver gone).
 */
export function castErrorKind(code: unknown, during: 'load' | 'session' = 'session'): 'source' | 'ignore' | 'session' {
  const c = typeof code === 'string' ? code : typeof code === 'object' && code && 'code' in code ? String((code as {code: unknown}).code) : '';
  if (c === 'cancel' || c === 'LOAD_CANCELLED') return 'ignore';
  if (c === 'load_media_failed' || c === 'invalid_parameter' || c === 'LOAD_FAILED' || c === 'ERROR') return 'source';
  if (c === 'session_error' && during === 'load') return 'source';
  return 'session';
}
