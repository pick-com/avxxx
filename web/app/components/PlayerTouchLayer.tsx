import {useEffect, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent, type RefObject} from 'react';
import {formatClock} from '../lib/format';
import {Icon} from './Icon';

const TAP_MS = 250;
const DOUBLE_TAP_MS = 300;
const LONG_PRESS_MS = 500;
const MOVE_PX = 12;
const HIDE_CONTROLS_MS = 3000;
const BRIGHTNESS_MIN = 0.3;
const BRIGHTNESS_MAX = 1.5;
/** A full-width horizontal drag seeks this far (or the whole video if shorter). */
const SEEK_SPAN_S = 240;

type Mode = 'none' | 'seek' | 'brightness' | 'volume';
type Hud =
  | {kind: 'brightness' | 'volume'; value: number}
  | {kind: 'seek'; target: number; delta: number}
  | {kind: 'boost'}
  | {kind: 'skip'; side: 'left' | 'right'; key: number}
  | null;

const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, n));

function useVideoClock(videoRef: RefObject<HTMLVideoElement | null>) {
  const [state, setState] = useState({time: 0, duration: 0, paused: true});
  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    const update = () =>
      setState({time: video.currentTime, duration: Number.isFinite(video.duration) ? video.duration : 0, paused: video.paused});
    update();
    const events = ['timeupdate', 'durationchange', 'play', 'pause', 'seeked', 'loadedmetadata'];
    events.forEach(e => video.addEventListener(e, update));
    return () => events.forEach(e => video.removeEventListener(e, update));
  }, [videoRef]);
  return state;
}

/**
 * MobilePlayerFull: custom controls and gestures over the video while it is
 * fullscreen on a touch device (PRD 5.1). Native controls are off in this mode
 * only; desktop and inline mobile keep the browser's controls.
 *
 * - left half vertical swipe: brightness (CSS filter on the video)
 * - right half vertical swipe: volume
 * - double tap left / right third: -10 s / +10 s
 * - long press: 2× while held
 * - horizontal drag: seek, with a time preview; applied on release
 * - single tap: show / hide controls; lock button freezes all gestures
 */
export function PlayerTouchLayer({videoRef, title, rate, qualityLabel, onExit, onOpenSettings, reducedMotion}: {
  videoRef: RefObject<HTMLVideoElement | null>;
  title: string;
  rate: number;
  qualityLabel: string;
  onExit: () => void;
  onOpenSettings: (tab: 'quality' | 'rate' | 'source') => void;
  reducedMotion: boolean;
}) {
  const clock = useVideoClock(videoRef);
  const [controls, setControls] = useState(true);
  const [locked, setLocked] = useState(false);
  const [hud, setHud] = useState<Hud>(null);
  const [brightness, setBrightness] = useState(1);
  const layerRef = useRef<HTMLDivElement>(null);
  const gesture = useRef<{
    id: number;
    x: number;
    y: number;
    at: number;
    mode: Mode;
    startBrightness: number;
    startVolume: number;
    startTime: number;
    seekTarget: number;
    longPress: number | undefined;
    boosted: boolean;
  } | null>(null);
  const lastTap = useRef<{at: number; side: 'left' | 'right' | 'center'} | null>(null);
  const singleTapTimer = useRef<number | undefined>(undefined);
  const hideTimer = useRef<number | undefined>(undefined);
  const hudTimer = useRef<number | undefined>(undefined);

  // Brightness lives on the video element and is reset when leaving fullscreen.
  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    video.style.filter = brightness === 1 ? '' : `brightness(${brightness})`;
  }, [brightness, videoRef]);
  useEffect(() => {
    const video = videoRef.current;
    return () => {
      if (video) video.style.filter = '';
      window.clearTimeout(singleTapTimer.current);
      window.clearTimeout(hideTimer.current);
      window.clearTimeout(hudTimer.current);
    };
  }, [videoRef]);

  // Auto-hide controls while playing.
  useEffect(() => {
    window.clearTimeout(hideTimer.current);
    if (controls && !clock.paused) {
      hideTimer.current = window.setTimeout(() => setControls(false), HIDE_CONTROLS_MS);
    }
    return () => window.clearTimeout(hideTimer.current);
  }, [controls, clock.paused, hud]);

  const flashHud = (next: Hud, ms = 600) => {
    window.clearTimeout(hudTimer.current);
    setHud(next);
    hudTimer.current = window.setTimeout(() => setHud(null), ms);
  };

  const sideOf = (clientX: number): 'left' | 'right' | 'center' => {
    const rect = layerRef.current?.getBoundingClientRect();
    if (!rect) return 'center';
    const x = (clientX - rect.left) / rect.width;
    return x < 1 / 3 ? 'left' : x > 2 / 3 ? 'right' : 'center';
  };

  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (!event.isPrimary) return;
    if ((event.target as HTMLElement).closest('button, input, a')) return;
    const video = videoRef.current;
    if (!video) return;
    try {
      event.currentTarget.setPointerCapture(event.pointerId);
    } catch {
      /* pointer already gone */
    }
    const g = {
      id: event.pointerId,
      x: event.clientX,
      y: event.clientY,
      at: performance.now(),
      mode: 'none' as Mode,
      startBrightness: brightness,
      startVolume: video.muted ? 0 : video.volume,
      startTime: video.currentTime,
      seekTarget: video.currentTime,
      longPress: undefined as number | undefined,
      boosted: false,
    };
    if (!locked) {
      g.longPress = window.setTimeout(() => {
        if (gesture.current !== g || g.mode !== 'none' || video.paused) return;
        g.boosted = true;
        video.playbackRate = 2;
        window.clearTimeout(hudTimer.current);
        setHud({kind: 'boost'});
      }, LONG_PRESS_MS);
    }
    gesture.current = g;
  };

  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const g = gesture.current;
    const video = videoRef.current;
    const rect = layerRef.current?.getBoundingClientRect();
    if (!g || g.id !== event.pointerId || !video || !rect || locked || g.boosted) return;
    const dx = event.clientX - g.x;
    const dy = event.clientY - g.y;
    if (g.mode === 'none') {
      if (Math.hypot(dx, dy) < MOVE_PX) return;
      window.clearTimeout(g.longPress);
      if (Math.abs(dx) > Math.abs(dy)) g.mode = clock.duration > 0 ? 'seek' : 'none';
      else g.mode = g.x - rect.left < rect.width / 2 ? 'brightness' : 'volume';
      if (g.mode === 'none') return;
    }
    window.clearTimeout(hudTimer.current);
    if (g.mode === 'seek') {
      const span = Math.min(clock.duration, SEEK_SPAN_S);
      const target = clamp(g.startTime + (dx / rect.width) * span, 0, clock.duration);
      g.seekTarget = target;
      setHud({kind: 'seek', target, delta: target - g.startTime});
    } else if (g.mode === 'brightness') {
      const value = clamp(g.startBrightness - (dy / (rect.height * 0.8)) * (BRIGHTNESS_MAX - BRIGHTNESS_MIN), BRIGHTNESS_MIN, BRIGHTNESS_MAX);
      setBrightness(value);
      setHud({kind: 'brightness', value: (value - BRIGHTNESS_MIN) / (BRIGHTNESS_MAX - BRIGHTNESS_MIN)});
    } else {
      const value = clamp(g.startVolume - dy / (rect.height * 0.8), 0, 1);
      video.volume = value;
      video.muted = value === 0;
      setHud({kind: 'volume', value});
    }
  };

  const endGesture = (event: ReactPointerEvent<HTMLDivElement>, cancelled: boolean) => {
    const g = gesture.current;
    const video = videoRef.current;
    if (!g || g.id !== event.pointerId) return;
    gesture.current = null;
    window.clearTimeout(g.longPress);
    if (!video) return;

    if (g.boosted) {
      video.playbackRate = rate;
      setHud(null);
      return;
    }
    if (g.mode === 'seek') {
      if (!cancelled) video.currentTime = g.seekTarget;
      setHud(null);
      return;
    }
    if (g.mode !== 'none') {
      hudTimer.current = window.setTimeout(() => setHud(null), 500);
      return;
    }
    if (cancelled || performance.now() - g.at > TAP_MS) return;

    // A tap.
    const side = sideOf(event.clientX);
    const now = performance.now();
    const previous = lastTap.current;
    if (!locked && side !== 'center' && previous && previous.side === side && now - previous.at < DOUBLE_TAP_MS) {
      window.clearTimeout(singleTapTimer.current);
      lastTap.current = {at: now, side};
      const delta = side === 'left' ? -10 : 10;
      video.dataset.seekMethod = 'double_tap';
      video.currentTime = clamp(video.currentTime + delta, 0, clock.duration || video.currentTime + delta);
      flashHud({kind: 'skip', side, key: now}, 600);
      return;
    }
    lastTap.current = {at: now, side};
    window.clearTimeout(singleTapTimer.current);
    if (side === 'center' || locked) setControls(c => !c);
    else singleTapTimer.current = window.setTimeout(() => setControls(c => !c), DOUBLE_TAP_MS);
  };

  const togglePlay = () => {
    const video = videoRef.current;
    if (!video) return;
    if (video.paused) void video.play().catch(() => undefined);
    else video.pause();
  };

  const showControls = controls && !locked;
  const progress = clock.duration > 0 ? clock.time / clock.duration : 0;

  return (
    <div
      ref={layerRef}
      className="touch-layer"
      data-controls={showControls ? 'true' : 'false'}
      data-motion={reducedMotion ? 'reduce' : 'full'}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={event => endGesture(event, false)}
      onPointerCancel={event => endGesture(event, true)}
      onContextMenu={event => event.preventDefault()}
    >
      {showControls && (
        <>
          <div className="touch-top">
            <button type="button" className="touch-btn" aria-label="退出全屏" onClick={onExit}>
              <Icon name="back" size={22} />
            </button>
            <span className="touch-title">{title}</span>
            <button type="button" className="touch-text-btn" onClick={() => onOpenSettings('source')}>
              片源
            </button>
            <button type="button" className="touch-text-btn" onClick={() => onOpenSettings('quality')}>
              {qualityLabel}
            </button>
          </div>

          <div className="touch-center">
            <button type="button" className="touch-play" aria-label={clock.paused ? '播放' : '暂停'} onClick={togglePlay}>
              <Icon name={clock.paused ? 'play' : 'pause'} size={28} filled={clock.paused} />
            </button>
          </div>

          <div className="touch-bottom">
            <input
              type="range"
              className="touch-seek"
              aria-label="播放进度"
              aria-valuetext={`${formatClock(clock.time)} / ${formatClock(clock.duration)}`}
              min={0}
              max={clock.duration || 0}
              step={1}
              value={Math.min(clock.time, clock.duration || 0)}
              style={{'--progress': `${progress * 100}%`} as CSSProperties}
              onChange={event => {
                const video = videoRef.current;
                if (video) video.currentTime = Number(event.currentTarget.value);
              }}
            />
            <div className="touch-row">
              <span className="touch-time">
                {formatClock(clock.time)} / {formatClock(clock.duration)}
              </span>
              <span className="section-spacer" />
              <button type="button" className="touch-text-btn" onClick={() => onOpenSettings('rate')}>
                {rate === 1 ? '倍速' : `${rate}×`}
              </button>
            </div>
          </div>
        </>
      )}

      {controls && (
        <button
          type="button"
          className="touch-lock"
          aria-label={locked ? '解锁屏幕' : '锁定屏幕'}
          aria-pressed={locked}
          onClick={() => {
            setLocked(l => !l);
            setControls(true);
          }}
        >
          <Icon name={locked ? 'lock' : 'unlock'} size={20} />
        </button>
      )}

      {hud && (
        <div className={`touch-hud touch-hud-${hud.kind}${hud.kind === 'skip' ? ` touch-hud-${hud.side}` : ''}`} aria-hidden="true">
          {hud.kind === 'brightness' || hud.kind === 'volume' ? (
            <>
              <Icon name={hud.kind === 'brightness' ? 'brightness' : hud.value === 0 ? 'mute' : 'volume'} size={20} />
              <span className="touch-meter">
                <span style={{width: `${Math.round(hud.value * 100)}%`}} />
              </span>
            </>
          ) : hud.kind === 'seek' ? (
            <>
              <strong>{formatClock(hud.target)}</strong>
              <span>
                / {formatClock(clock.duration)} · {hud.delta >= 0 ? '+' : '-'}
                {formatClock(Math.abs(hud.delta))}
              </span>
            </>
          ) : hud.kind === 'boost' ? (
            <>
              <Icon name="forward" size={18} />
              <span>2× 倍速中</span>
            </>
          ) : hud.kind === 'skip' ? (
            <>
              <Icon name={hud.side === 'left' ? 'rewind' : 'forward'} size={20} />
              <span>{hud.side === 'left' ? '后退 10 秒' : '前进 10 秒'}</span>
            </>
          ) : null}
        </div>
      )}

      {locked && controls && <span className="touch-locked-note">屏幕已锁定</span>}
    </div>
  );
}
