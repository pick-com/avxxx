import {useEffect, useRef} from 'react';
import {PositionGate, artworkFor, positionStateOf, seekByTarget, seekToTarget, type ArtworkSource} from './mediaSession';

// Media Session for the Player's <video> (lock screen, headset / Bluetooth
// remotes, keyboard media keys, OS media controls). Pure parts live in
// lib/mediaSession.ts.
//
// - metadata (title / artist / album / artwork) is set once the video plays,
//   and cleared when this video goes away (source switch renders a new
//   <video>, the release changes, the Player unmounts) unless something else
//   replaced it meanwhile;
// - play / pause / stop / seekbackward / seekforward / seekto act on the
//   video; previoustrack / nexttrack only while the page passes a target,
//   else they are unset so the OS hides those buttons;
// - playbackState and setPositionState follow the media events (throttled
//   on timeupdate, see PositionGate).
// PlayerHost's mini player / PiP parking keep the same Player and <video>
// mounted, so the session simply stays while it plays there (the page-only
// previous / next callbacks are dropped with the other page props).
// Everything is feature-detected; nothing runs during SSR (effects only).

type Session = MediaSession & {setPositionState?: (state?: MediaPositionState) => void};

function session(): Session | null {
  return typeof navigator !== 'undefined' && 'mediaSession' in navigator ? (navigator.mediaSession as Session) : null;
}

function setHandler(s: Session, action: MediaSessionAction, handler: MediaSessionActionHandler | null) {
  try {
    s.setActionHandler(action, handler);
  } catch {
    /* action not supported by this browser */
  }
}

function setPosition(s: Session, state?: MediaPositionState) {
  try {
    s.setPositionState?.(state);
  } catch {
    /* invalid state (e.g. duration changed under us): the next event resends */
  }
}

export type MediaSessionMeta = {title: string; artist?: string; album?: string; artwork?: ArtworkSource | null};

export function useMediaSession(
  video: HTMLVideoElement | null,
  meta: MediaSessionMeta,
  tracks: {onPrevious?: () => void; onNext?: () => void} = {},
) {
  const {title, artist = '', album = ''} = meta;
  const artworkKey = meta.artwork ? JSON.stringify(meta.artwork) : '';
  const tracksRef = useRef(tracks);
  tracksRef.current = tracks;

  // Metadata: set when this video plays; cleared on teardown if still ours.
  useEffect(() => {
    const s = session();
    if (!s || !video || typeof MediaMetadata === 'undefined') return;
    let mine: MediaMetadata | null = null;
    const apply = () => {
      try {
        const artwork = artworkFor(artworkKey ? (JSON.parse(artworkKey) as ArtworkSource) : null, window.location.href);
        mine = new MediaMetadata({title, artist, album, artwork});
        s.metadata = mine;
      } catch {
        /* bad artwork URL: the OS keeps the page title */
      }
    };
    if (!video.paused) apply();
    video.addEventListener('play', apply);
    return () => {
      video.removeEventListener('play', apply);
      if (mine && s.metadata === mine) s.metadata = null;
    };
  }, [video, title, artist, album, artworkKey]);

  // play / pause / stop / seeking.
  useEffect(() => {
    const s = session();
    if (!s || !video) return;
    const duration = () => (Number.isFinite(video.duration) ? video.duration : 0);
    const seek = (to: number, fast = false) => {
      // Analytics: closest bucket for a remote / media-key seek.
      video.dataset.seekMethod = 'keyboard';
      const v = video as HTMLVideoElement & {fastSeek?: (t: number) => void};
      if (fast && typeof v.fastSeek === 'function') v.fastSeek(to);
      else video.currentTime = to;
    };
    setHandler(s, 'play', () => {
      if (video.ended) {
        video.dataset.seekMethod = 'skip';
        video.currentTime = 0;
      }
      void video.play().catch(() => undefined);
    });
    setHandler(s, 'pause', () => video.pause());
    // Stop pauses in place: resetting to 0 would lose the resume point the
    // pause just saved, and the Player stays on the page anyway.
    setHandler(s, 'stop', () => {
      video.pause();
      s.playbackState = 'paused';
    });
    setHandler(s, 'seekbackward', details => seek(seekByTarget('seekbackward', details.seekOffset ?? undefined, video.currentTime, duration())));
    setHandler(s, 'seekforward', details => seek(seekByTarget('seekforward', details.seekOffset ?? undefined, video.currentTime, duration())));
    setHandler(s, 'seekto', details => {
      const to = seekToTarget(details.seekTime ?? undefined, duration());
      if (to !== null) seek(to, details.fastSeek === true);
    });
    return () => {
      for (const action of ['play', 'pause', 'stop', 'seekbackward', 'seekforward', 'seekto'] as MediaSessionAction[]) setHandler(s, action, null);
    };
  }, [video]);

  // previoustrack / nexttrack only while the page has a target.
  const hasPrevious = !!tracks.onPrevious;
  const hasNext = !!tracks.onNext;
  useEffect(() => {
    const s = session();
    if (!s || !video) return;
    setHandler(s, 'previoustrack', hasPrevious ? () => tracksRef.current.onPrevious?.() : null);
    setHandler(s, 'nexttrack', hasNext ? () => tracksRef.current.onNext?.() : null);
    return () => {
      setHandler(s, 'previoustrack', null);
      setHandler(s, 'nexttrack', null);
    };
  }, [video, hasPrevious, hasNext]);

  // playbackState + position state.
  useEffect(() => {
    const s = session();
    if (!s || !video) return;
    const gate = new PositionGate();
    const sync = (force: boolean) => {
      if (!s.setPositionState) return;
      const state = positionStateOf(video.duration, video.currentTime, video.playbackRate);
      if (!gate.check(state, !video.paused, Date.now(), force)) return;
      setPosition(s, state ?? undefined);
    };
    const onPlay = () => {
      s.playbackState = 'playing';
      sync(true);
    };
    const onPause = () => {
      s.playbackState = 'paused';
      sync(true);
    };
    const onChange = () => sync(true);
    const onTime = () => sync(false);
    s.playbackState = video.paused ? 'paused' : 'playing';
    sync(true);
    const changes = ['loadedmetadata', 'durationchange', 'ratechange', 'seeked', 'waiting', 'playing'];
    video.addEventListener('play', onPlay);
    video.addEventListener('pause', onPause);
    video.addEventListener('timeupdate', onTime);
    changes.forEach(e => video.addEventListener(e, onChange));
    return () => {
      video.removeEventListener('play', onPlay);
      video.removeEventListener('pause', onPause);
      video.removeEventListener('timeupdate', onTime);
      changes.forEach(e => video.removeEventListener(e, onChange));
      s.playbackState = 'none';
      setPosition(s);
    };
  }, [video]);
}
