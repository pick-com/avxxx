import {useCallback, useEffect, useState} from 'react';
import {castButtonState, type CastButtonState, type RemoteConnection} from './remotePlayback';

// Casting for the Player's <video> (Remote Playback API: the cast menu on
// Chrome / Android, AirPlay on Safari; Safari's older
// webkitShowPlaybackTargetPicker as a fallback).
//
// The button shows only while the browser reports a device:
// video.remote.watchAvailability, else Safari's
// webkitplaybacktargetavailabilitychanged event. A browser that cannot
// monitor availability (watchAvailability rejects, e.g. some Android devices
// in power-saving mode) gets no button; its own media menu still casts.
// Non-castable sources (hls.js / MSE, see castableSource) set
// disableRemotePlayback instead.
//
// Limit: the receiver fetches the stream itself, without this page's
// Referer, cookies or session. Sources bound to those (hotlink protection,
// cookie auth, short signed URLs tied to the viewer's IP) may fail on the
// device even though they play here. The Google Cast SDK (custom receiver,
// sender-side token refresh) is not used; see docs/PRODUCT_REVIEW_AND_TECH_PLAN.md 3.3.

type WebkitVideo = HTMLVideoElement & {
  webkitShowPlaybackTargetPicker?: () => void;
  webkitCurrentPlaybackTargetIsWireless?: boolean;
};
type AvailabilityEvent = Event & {availability?: 'available' | 'not-available'};

export function useRemotePlayback(video: HTMLVideoElement | null, castable: boolean): {state: CastButtonState; prompt: () => void} {
  const [available, setAvailable] = useState(false);
  const [connection, setConnection] = useState<RemoteConnection>('disconnected');

  useEffect(() => {
    setAvailable(false);
    setConnection('disconnected');
    if (!video) return;
    if ('disableRemotePlayback' in video) video.disableRemotePlayback = !castable;
    if (!castable) return;
    const v = video as WebkitVideo;
    let cancelled = false;
    const undo: (() => void)[] = [];

    const watchWebkit = () => {
      if (typeof v.webkitShowPlaybackTargetPicker !== 'function') return;
      const onAvailability = (event: Event) => setAvailable((event as AvailabilityEvent).availability === 'available');
      const onWireless = () => setConnection(v.webkitCurrentPlaybackTargetIsWireless ? 'connected' : 'disconnected');
      v.addEventListener('webkitplaybacktargetavailabilitychanged', onAvailability);
      v.addEventListener('webkitcurrentplaybacktargetiswirelesschanged', onWireless);
      onWireless();
      undo.push(() => {
        v.removeEventListener('webkitplaybacktargetavailabilitychanged', onAvailability);
        v.removeEventListener('webkitcurrentplaybacktargetiswirelesschanged', onWireless);
      });
    };

    const remote = video.remote;
    if (remote && typeof remote.watchAvailability === 'function') {
      const onState = () => setConnection(remote.state);
      onState();
      remote.addEventListener('connecting', onState);
      remote.addEventListener('connect', onState);
      remote.addEventListener('disconnect', onState);
      undo.push(() => {
        remote.removeEventListener('connecting', onState);
        remote.removeEventListener('connect', onState);
        remote.removeEventListener('disconnect', onState);
      });
      remote
        .watchAvailability(a => {
          if (!cancelled) setAvailable(a);
        })
        .then(id => {
          if (cancelled) remote.cancelWatchAvailability(id).catch(() => undefined);
          else undo.push(() => void remote.cancelWatchAvailability(id).catch(() => undefined));
        })
        .catch(() => {
          // Availability monitoring unsupported here: Safari's own event, if any.
          if (!cancelled) watchWebkit();
        });
    } else {
      watchWebkit();
    }
    return () => {
      cancelled = true;
      undo.forEach(fn => fn());
    };
  }, [video, castable]);

  const prompt = useCallback(() => {
    if (!video) return;
    const v = video as WebkitVideo;
    if (video.remote && typeof video.remote.prompt === 'function') {
      video.remote.prompt().catch((error: unknown) => {
        // Cancelled / no device: nothing to do. Unsupported: Safari's own picker.
        if (error instanceof DOMException && error.name === 'NotSupportedError') v.webkitShowPlaybackTargetPicker?.();
      });
      return;
    }
    v.webkitShowPlaybackTargetPicker?.();
  }, [video]);

  return {state: castButtonState(castable && !!video, available, connection), prompt};
}
