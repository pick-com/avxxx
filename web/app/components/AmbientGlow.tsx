import {useEffect, useRef} from 'react';
import {AMBIENT_SAMPLE_MS, AMBIENT_SIZE, ambientSampling, isTaintError} from '../lib/ambient';

/**
 * 氛围模式: draws the video into a tiny canvas every 500 ms while it plays
 * on a visible page; CSS blurs and fades it into a glow behind the player
 * (lib/ambient.ts). A paused video, a hidden page and reduced motion keep
 * the last frame (reduced motion: a single frame on load / seek / pause,
 * no loop). The first frame read checks the canvas: a source that does
 * not allow frame reads (SecurityError, tainted canvas) is reported once
 * through `onBlocked` and nothing is drawn for it.
 */
export function AmbientGlow({video, reducedMotion, onBlocked}: {video: HTMLVideoElement; reducedMotion: boolean; onBlocked: () => void}) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const onBlockedRef = useRef(onBlocked);
  onBlockedRef.current = onBlocked;

  useEffect(() => {
    const canvas = canvasRef.current;
    const ctx = canvas?.getContext('2d');
    if (!canvas || !ctx) return;
    let timer: number | undefined;
    let checked = false;
    let dead = false;
    const stop = () => {
      window.clearInterval(timer);
      timer = undefined;
    };
    const draw = () => {
      // HAVE_CURRENT_DATA: there is a frame to draw.
      if (dead || video.readyState < 2) return;
      try {
        ctx.drawImage(video, 0, 0, canvas.width, canvas.height);
        if (!checked) {
          ctx.getImageData(0, 0, 1, 1);
          checked = true;
        }
      } catch (error) {
        if (!isTaintError(error)) return;
        dead = true;
        stop();
        ctx.clearRect(0, 0, canvas.width, canvas.height);
        onBlockedRef.current();
      }
    };
    const sync = () => {
      const on = ambientSampling({hidden: document.visibilityState === 'hidden', paused: video.paused, reducedMotion});
      if (on && timer === undefined && !dead) {
        draw();
        timer = window.setInterval(draw, AMBIENT_SAMPLE_MS);
      } else if (!on) {
        stop();
      }
    };
    const still = () => {
      draw();
      sync();
    };
    const stills = ['loadeddata', 'seeked', 'pause'];
    stills.forEach(e => video.addEventListener(e, still));
    video.addEventListener('play', sync);
    document.addEventListener('visibilitychange', sync);
    still();
    return () => {
      dead = true;
      stop();
      stills.forEach(e => video.removeEventListener(e, still));
      video.removeEventListener('play', sync);
      document.removeEventListener('visibilitychange', sync);
    };
  }, [video, reducedMotion]);

  return <canvas ref={canvasRef} className="player-ambient" width={AMBIENT_SIZE.width} height={AMBIENT_SIZE.height} aria-hidden="true" />;
}
