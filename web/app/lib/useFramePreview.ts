import {useCallback, useEffect, useRef, useState} from 'react';

// Hover preview frames for the progress bar (PRD 5.1 「进度条悬停显示预览帧」)
// until sprite sheets exist (tech plan 2.4). A hidden second <video
// crossOrigin="anonymous"> on the same progressive source seeks to the hovered
// time and draws the frame to a canvas. Frames are cached per 10 s bucket and
// only one seek runs at a time (the latest wanted bucket goes next).
//
// The stream must be CORS-readable: a load error, or a tainted canvas
// (SecurityError from toDataURL), switches the preview off for this source and
// the tooltip falls back to time + chapter text. Video is never proxied.

const BUCKET_S = 10;
const WIDTH = 160;
const SEEK_TIMEOUT_MS = 8000;
const MAX_FRAMES = 240;

export type FrameStatus = 'off' | 'loading' | 'ready' | 'unsupported';

export function useFramePreview(src: string | undefined, enabled: boolean) {
  const [frame, setFrame] = useState<{bucket: number; url: string} | null>(null);
  const [status, setStatus] = useState<FrameStatus>('off');
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const cache = useRef(new Map<number, string>());
  const busy = useRef<number | null>(null);
  const wanted = useRef<number | null>(null);
  const timer = useRef<number | undefined>(undefined);
  const dead = useRef(false);
  const statusRef = useRef<FrameStatus>('off');
  const setStatusBoth = (s: FrameStatus) => {
    statusRef.current = s;
    setStatus(s);
  };

  // Reset on a new source.
  useEffect(() => {
    dead.current = false;
    cache.current = new Map();
    busy.current = null;
    wanted.current = null;
    setFrame(null);
    setStatusBoth(enabled && src ? 'off' : 'unsupported');
    return () => {
      dead.current = true;
      window.clearTimeout(timer.current);
      const v = videoRef.current;
      videoRef.current = null;
      if (v) {
        v.removeAttribute('src');
        v.load();
      }
    };
  }, [src, enabled]);

  const fail = () => {
    dead.current = true;
    busy.current = null;
    setFrame(null);
    setStatusBoth('unsupported');
  };

  const capture = (bucket: number) => {
    const video = videoRef.current;
    if (!video || dead.current) return;
    busy.current = bucket;
    const duration = Number.isFinite(video.duration) ? video.duration : 0;
    const at = Math.max(0, Math.min(duration - 0.5, bucket * BUCKET_S + BUCKET_S / 2));
    const done = () => {
      video.removeEventListener('seeked', onSeeked);
      window.clearTimeout(timeout);
      busy.current = null;
      const next = wanted.current;
      if (next !== null && next !== bucket && !cache.current.has(next) && !dead.current) {
        // Throttle: at most one seek every ~120 ms.
        timer.current = window.setTimeout(() => capture(next), 120);
      }
    };
    const onSeeked = () => {
      try {
        const h = Math.round((WIDTH * (video.videoHeight || 9)) / (video.videoWidth || 16));
        const canvas = document.createElement('canvas');
        canvas.width = WIDTH;
        canvas.height = h;
        const ctx = canvas.getContext('2d');
        if (!ctx) throw new Error('no 2d context');
        ctx.drawImage(video, 0, 0, WIDTH, h);
        const url = canvas.toDataURL('image/jpeg', 0.7); // SecurityError when tainted
        if (cache.current.size >= MAX_FRAMES) {
          const first = cache.current.keys().next().value;
          if (first !== undefined) cache.current.delete(first);
        }
        cache.current.set(bucket, url);
        if (statusRef.current !== 'ready') setStatusBoth('ready');
        if (wanted.current === bucket) setFrame({bucket, url});
      } catch {
        fail();
        return;
      }
      done();
    };
    const timeout = window.setTimeout(() => {
      video.removeEventListener('seeked', onSeeked);
      done();
    }, SEEK_TIMEOUT_MS);
    video.addEventListener('seeked', onSeeked);
    video.currentTime = at;
  };

  const ensureVideo = (onReady: () => void) => {
    let video = videoRef.current;
    if (video) {
      if (video.readyState >= 1) onReady();
      return;
    }
    if (!src) return;
    video = document.createElement('video');
    video.crossOrigin = 'anonymous';
    video.muted = true;
    video.playsInline = true;
    video.preload = 'metadata';
    video.addEventListener('error', fail, {once: true});
    video.addEventListener(
      'loadedmetadata',
      () => {
        if (!dead.current) onReady();
      },
      {once: true},
    );
    videoRef.current = video;
    setStatusBoth('loading');
    video.src = src;
  };

  /** Ask for the frame at `time` (seconds). */
  const request = useCallback(
    (time: number) => {
      if (!enabled || !src || dead.current) return;
      const bucket = Math.max(0, Math.floor(time / BUCKET_S));
      wanted.current = bucket;
      const hit = cache.current.get(bucket);
      if (hit) {
        setFrame({bucket, url: hit});
        return;
      }
      ensureVideo(() => {
        const next = wanted.current;
        if (busy.current === null && next !== null && !cache.current.has(next)) capture(next);
      });
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [src, enabled],
  );

  return {frame, status, request};
}
