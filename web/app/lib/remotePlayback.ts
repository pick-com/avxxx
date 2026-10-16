// Pure helpers for casting (Remote Playback API / Safari AirPlay); the hook
// is lib/useRemotePlayback.ts. Tested in scripts/remote-playback.test.mjs.

/** RemotePlayback.state, or Safari's webkitCurrentPlaybackTargetIsWireless mapped onto it. */
export type RemoteConnection = 'disconnected' | 'connecting' | 'connected';
/** What the cast button shows: nothing, the idle icon, or the connecting / connected state. */
export type CastButtonState = 'hidden' | 'idle' | 'connecting' | 'connected';

/**
 * Whether a source can be handed to a cast device.
 *
 * The cast / AirPlay receiver gets the media element's URL and fetches it
 * itself. A stream played through hls.js is a MediaSource (blob:) URL that
 * only exists inside this page, so it cannot be cast. We pick the safe
 * option: such a source sets disableRemotePlayback and shows no cast button.
 * Swapping the element to the native .m3u8 on cast would only help in a
 * browser that plays HLS natively, and the Player already gives those the
 * native URL (Safari, Chrome on Android, recent desktop Chrome), so that path
 * is never MSE-backed.
 * Progressive files and natively played HLS cast normally.
 */
export function castableSource(source: {hls: boolean; nativeHls: boolean} | null): boolean {
  if (!source) return false;
  return !source.hls || source.nativeHls;
}

/**
 * The cast button: shown while a device is available, and kept while a
 * session is connecting or connected (availability can drop to false once
 * the only device is in use).
 */
export function castButtonState(castable: boolean, available: boolean, connection: RemoteConnection): CastButtonState {
  if (!castable) return 'hidden';
  if (connection === 'connecting' || connection === 'connected') return connection;
  return available ? 'idle' : 'hidden';
}
