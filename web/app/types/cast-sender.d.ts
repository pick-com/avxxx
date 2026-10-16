// Types for the parts of the Google Cast Web Sender SDK (CAF,
// cast_sender.js?loadCastFramework=1) that lib/useGoogleCast.ts uses.
// Hand-written instead of @types/chromecast-caf-sender: that package pulls in
// @types/chrome and declares the `chrome` / `cast` globals for the whole app,
// while the SDK only exists after the lazy script load. Here nothing is
// global: the hook reads window.cast / window.chrome.cast through CastWindow.
// Reference: https://developers.google.com/cast/docs/reference/web_sender

export type CastStateName = 'NO_DEVICES_AVAILABLE' | 'NOT_CONNECTED' | 'CONNECTING' | 'CONNECTED';
export type SessionStateName =
  | 'NO_SESSION'
  | 'SESSION_STARTING'
  | 'SESSION_STARTED'
  | 'SESSION_START_FAILED'
  | 'SESSION_ENDING'
  | 'SESSION_ENDED'
  | 'SESSION_RESUMED';

export interface CastImage {
  url: string;
  width?: number | null;
  height?: number | null;
}

export interface CastMovieMetadata {
  metadataType: number;
  title?: string;
  subtitle?: string;
  studio?: string;
  images?: CastImage[];
}

export interface CastMediaInfo {
  contentId: string;
  contentUrl?: string;
  contentType: string;
  streamType: string;
  metadata: CastMovieMetadata | null;
  hlsSegmentFormat?: string;
  hlsVideoSegmentFormat?: string;
}

export interface CastLoadRequest {
  media: CastMediaInfo;
  currentTime: number | null;
  autoplay: boolean | null;
}

export interface CastMediaSession {
  playerState: string;
  idleReason?: string | null;
  media?: CastMediaInfo | null;
}

export interface CastSession {
  getCastDevice(): {friendlyName: string};
  getMediaSession(): CastMediaSession | null;
  /** Resolves with nothing on success; rejects with a chrome.cast.ErrorCode string. */
  loadMedia(request: CastLoadRequest): Promise<string | undefined | void>;
}

export interface CastContextInstance {
  setOptions(options: {receiverApplicationId: string; autoJoinPolicy?: string}): void;
  getCastState(): CastStateName;
  getCurrentSession(): CastSession | null;
  requestSession(): Promise<string | undefined | void>;
  endCurrentSession(stopCasting: boolean): void;
  addEventListener(type: string, handler: (event: {castState?: CastStateName; sessionState?: SessionStateName}) => void): void;
  removeEventListener(type: string, handler: (event: {castState?: CastStateName; sessionState?: SessionStateName}) => void): void;
}

export interface CastRemotePlayer {
  isConnected: boolean;
  isMediaLoaded: boolean;
  isPaused: boolean;
  currentTime: number;
  duration: number;
  volumeLevel: number;
  isMuted: boolean;
  playerState: string | null;
  mediaInfo: CastMediaInfo | null;
}

export interface CastRemotePlayerController {
  addEventListener(type: string, handler: (event: {field: string; value: unknown}) => void): void;
  removeEventListener(type: string, handler: (event: {field: string; value: unknown}) => void): void;
  playOrPause(): void;
  seek(): void;
  muteOrUnmute(): void;
  setVolumeLevel(): void;
}

export interface CastFramework {
  CastContext: {getInstance(): CastContextInstance};
  RemotePlayer: new () => CastRemotePlayer;
  RemotePlayerController: new (player: CastRemotePlayer) => CastRemotePlayerController;
  CastContextEventType: {CAST_STATE_CHANGED: string; SESSION_STATE_CHANGED: string};
  RemotePlayerEventType: {ANY_CHANGE: string};
}

export interface ChromeCastApi {
  AutoJoinPolicy: {ORIGIN_SCOPED: string};
  Image: new (url: string) => CastImage;
  media: {
    DEFAULT_MEDIA_RECEIVER_APP_ID: string;
    MediaInfo: new (contentId: string, contentType: string) => CastMediaInfo;
    LoadRequest: new (media: CastMediaInfo) => CastLoadRequest;
    MovieMediaMetadata: new () => CastMovieMetadata;
    StreamType: {BUFFERED: string};
  };
}

export interface CastWindow {
  chrome?: {cast?: ChromeCastApi};
  cast?: {framework?: CastFramework};
  __onGCastApiAvailable?: (available: boolean, reason?: string) => void;
}
