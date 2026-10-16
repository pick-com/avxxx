import {useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent, type ReactNode} from 'react';
import {formatClock} from '../lib/format';
import {useT} from '../lib/i18n';
import {useFramePreview} from '../lib/useFramePreview';
import {Icon} from './Icon';

// Custom control bar for <video> sources (PRD 5.1, Video artboard): bottom
// scrim, progress slider (played / buffered / hover, chapter segments, drag
// to scrub with a live tooltip and preview frame), play, volume, time,
// chapter name, then the menus and the PiP / theater / fullscreen buttons.
// The Player decides visibility (auto-hide) and owns every action; this file
// only renders and reports.

export type Chapter = {start: number; title: string};

const SEEK_STEP = 5;

export type VideoClock = {
  time: number;
  duration: number;
  paused: boolean;
  ended: boolean;
  buffered: [number, number][];
  volume: number;
  muted: boolean;
};

const EMPTY: VideoClock = {time: 0, duration: 0, paused: true, ended: false, buffered: [], volume: 1, muted: false};

/** <video> state for the controls; re-renders on media events only. */
export function useVideoClock(video: HTMLVideoElement | null): VideoClock {
  const [clock, setClock] = useState<VideoClock>(EMPTY);
  useEffect(() => {
    if (!video) {
      setClock(EMPTY);
      return;
    }
    const update = () => {
      const ranges: [number, number][] = [];
      try {
        for (let i = 0; i < video.buffered.length; i++) ranges.push([video.buffered.start(i), video.buffered.end(i)]);
      } catch {
        /* buffered unavailable while the source changes */
      }
      setClock({
        time: video.currentTime,
        duration: Number.isFinite(video.duration) ? video.duration : 0,
        paused: video.paused,
        ended: video.ended,
        buffered: ranges,
        volume: video.volume,
        muted: video.muted,
      });
    };
    update();
    const events = ['timeupdate', 'durationchange', 'loadedmetadata', 'play', 'pause', 'ended', 'seeking', 'seeked', 'progress', 'volumechange', 'emptied'];
    events.forEach(e => video.addEventListener(e, update));
    return () => events.forEach(e => video.removeEventListener(e, update));
  }, [video]);
  return clock;
}

export type CastControl = {state: 'idle' | 'connecting' | 'connected'; prompt: () => void};

/** 投屏 button (control bar and touch controls): opens the device picker; shows connecting / connected. */
export function CastButton({cast, className}: {cast: CastControl; className: string}) {
  const t = useT();
  const label = cast.state === 'connecting' ? t('player.castConnecting') : cast.state === 'connected' ? t('player.castManage') : t('player.cast');
  return (
    <button
      type="button"
      className={className}
      data-cast={cast.state}
      aria-pressed={cast.state === 'connected'}
      aria-haspopup="dialog"
      aria-label={label}
      title={label}
      onClick={cast.prompt}
    >
      <Icon name={cast.state === 'idle' ? 'cast' : 'castOn'} size={20} />
    </button>
  );
}

export function chapterAt(chapters: Chapter[], time: number): {index: number; chapter: Chapter} | null {
  let found = -1;
  for (let i = 0; i < chapters.length; i++) if (chapters[i].start <= time) found = i;
  return found >= 0 ? {index: found, chapter: chapters[found]} : null;
}

const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, n));

export function PlayerControls({
  video,
  clock,
  visible,
  chapters,
  previewSrc,
  previewEnabled,
  peak,
  menus,
  captions,
  pip,
  cast,
  theater,
  fullscreen,
  onTogglePlay,
  onOpenSettings,
  onHoldChange,
  showVolumeSlider,
}: {
  video: HTMLVideoElement | null;
  clock: VideoClock;
  visible: boolean;
  chapters: Chapter[];
  /** Progressive source for hover frames; undefined for HLS (text-only tooltip). */
  previewSrc?: string;
  previewEnabled: boolean;
  /** PeakCurve, aligned above the progress bar. */
  peak?: ReactNode;
  /** Desktop inline menus (清晰度 / 倍速). */
  menus?: ReactNode;
  captions: {available: boolean; on: boolean; toggle: () => void};
  pip?: {active: boolean; toggle: () => void};
  /** 投屏: only while a device is available (or a session is up). */
  cast?: CastControl;
  theater?: {active: boolean; toggle: () => void};
  fullscreen: {active: boolean; toggle: () => void};
  onTogglePlay: () => void;
  onOpenSettings: () => void;
  /** True while the pointer is over the bar or the slider is being dragged (keeps it shown). */
  onHoldChange: (hold: boolean) => void;
  showVolumeSlider: boolean;
}) {
  const t = useT();
  const [remaining, setRemaining] = useState(false);
  const {time, duration, paused} = clock;
  const current = chapterAt(chapters, time);
  const timeText = remaining && duration ? `-${formatClock(Math.max(0, duration - time))}` : formatClock(time);

  return (
    <div className="pc" data-visible={visible ? 'true' : 'false'} onPointerEnter={() => onHoldChange(true)} onPointerLeave={() => onHoldChange(false)}>
      {peak && <div className="pc-peak">{peak}</div>}
      <ProgressBar
        video={video}
        clock={clock}
        chapters={chapters}
        previewSrc={previewSrc}
        previewEnabled={previewEnabled}
        onScrub={active => onHoldChange(active)}
      />
      <div className="pc-row" role="toolbar" aria-label={t('pc.controls')}>
        <button type="button" className="pc-btn" aria-label={paused ? t('player.play') : t('player.pause')} title={paused ? t('pc.playKey') : t('pc.pauseKey')} onClick={onTogglePlay}>
          <Icon name={paused ? 'play' : 'pause'} size={22} filled={paused} />
        </button>
        <VolumeControl video={video} volume={clock.volume} muted={clock.muted} showSlider={showVolumeSlider} />
        <button
          type="button"
          className="pc-time"
          aria-label={remaining ? t('pc.showElapsed') : t('pc.showRemaining')}
          title={remaining ? t('pc.showElapsed') : t('pc.showRemaining')}
          onClick={() => setRemaining(r => !r)}
        >
          <span aria-hidden="true">
            {timeText} / {formatClock(duration)}
          </span>
        </button>
        {current && <span className="pc-chapter hide-mobile">· {current.chapter.title}</span>}
        <span className="pc-spacer" />
        {menus && <div className="pc-menus hide-mobile">{menus}</div>}
        <button
          type="button"
          className="pc-btn"
          aria-pressed={captions.on}
          aria-disabled={!captions.available}
          aria-label={t('player.captions')}
          title={!captions.available ? t('player.noCaptionsShort') : captions.on ? t('pc.captionsOff') : t('pc.captionsOn')}
          onClick={captions.toggle}
        >
          <Icon name="cc" size={20} />
        </button>
        <button type="button" className="pc-btn" aria-label={t('pc.settings')} aria-haspopup="dialog" title={t('pc.settings')} onClick={onOpenSettings}>
          <Icon name="settings" size={20} />
        </button>
        {cast && <CastButton cast={cast} className="pc-btn" />}
        {pip && (
          <button type="button" className="pc-btn hide-mobile" aria-label={pip.active ? t('player.exitPip') : t('player.pip')} aria-pressed={pip.active} title={t('player.pip')} onClick={pip.toggle}>
            <Icon name="pip" size={20} />
          </button>
        )}
        {theater && !fullscreen.active && (
          <button
            type="button"
            className="pc-btn hide-mobile"
            aria-label={theater.active ? t('player.exitTheater') : t('player.theater')}
            aria-pressed={theater.active}
            title={t('player.theater')}
            onClick={theater.toggle}
          >
            <Icon name="theater" size={20} />
          </button>
        )}
        <button
          type="button"
          className="pc-btn"
          aria-label={fullscreen.active ? t('player.exitFullscreen') : t('player.fullscreen')}
          title={t('player.fullscreenKey')}
          onClick={fullscreen.toggle}
        >
          <Icon name={fullscreen.active ? 'exitFullscreen' : 'fullscreen'} size={20} />
        </button>
      </div>
    </div>
  );
}

function ProgressBar({video, clock, chapters, previewSrc, previewEnabled, onScrub}: {
  video: HTMLVideoElement | null;
  clock: VideoClock;
  chapters: Chapter[];
  previewSrc?: string;
  previewEnabled: boolean;
  onScrub: (active: boolean) => void;
}) {
  const t = useT();
  const trackRef = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<{fraction: number; px: number; width: number} | null>(null);
  const [scrub, setScrub] = useState<number | null>(null);
  const dragging = useRef<number | null>(null);
  const preview = useFramePreview(previewSrc, previewEnabled);
  const {duration, time, buffered} = clock;
  const shown = scrub ?? (duration ? time / duration : 0);

  const segments = useMemo(() => {
    if (!duration || chapters.length < 2) return [];
    return chapters.filter(c => c.start > 0 && c.start < duration).map(c => c.start / duration);
  }, [chapters, duration]);

  const measure = (clientX: number) => {
    const box = trackRef.current?.getBoundingClientRect();
    if (!box || box.width === 0) return null;
    const fraction = clamp((clientX - box.left) / box.width, 0, 1);
    return {fraction, px: fraction * box.width, width: box.width};
  };

  const point = (clientX: number) => {
    const m = measure(clientX);
    if (!m) return null;
    setHover(m);
    if (duration) preview.request(m.fraction * duration);
    return m;
  };

  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (!event.isPrimary || event.button !== 0 || !duration) return;
    const m = point(event.clientX);
    if (!m) return;
    try {
      event.currentTarget.setPointerCapture(event.pointerId);
    } catch {
      /* pointer gone */
    }
    dragging.current = event.pointerId;
    setScrub(m.fraction);
    onScrub(true);
  };
  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const m = point(event.clientX);
    if (m && dragging.current === event.pointerId) setScrub(m.fraction);
  };
  const finish = (event: ReactPointerEvent<HTMLDivElement>, commit: boolean) => {
    if (dragging.current !== event.pointerId) return;
    dragging.current = null;
    const m = measure(event.clientX);
    if (commit && m && video && duration) {
      // No seekMethod: the tracker reports a bar seek as 'drag'.
      video.currentTime = Math.min(duration - 0.05, m.fraction * duration);
    }
    setScrub(null);
    onScrub(false);
    if (event.pointerType !== 'mouse') setHover(null);
  };

  const seekTo = (to: number) => {
    if (!video || !duration) return;
    video.dataset.seekMethod = 'keyboard';
    video.currentTime = clamp(to, 0, duration);
  };
  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (!duration) return;
    let to: number | null = null;
    switch (event.key) {
      case 'ArrowLeft':
      case 'ArrowDown':
        to = time - SEEK_STEP;
        break;
      case 'ArrowRight':
      case 'ArrowUp':
        to = time + SEEK_STEP;
        break;
      case 'PageDown':
        to = time - duration / 10;
        break;
      case 'PageUp':
        to = time + duration / 10;
        break;
      case 'Home':
        to = 0;
        break;
      case 'End':
        to = duration;
        break;
      default:
        return;
    }
    event.preventDefault();
    seekTo(to);
  };

  const tipTime = hover ? hover.fraction * duration : 0;
  const tipChapter = hover ? chapterAt(chapters, tipTime) : null;
  const showFrame = preview.status !== 'unsupported' && preview.frame;
  const tipWidth = showFrame ? 168 : 120;
  const tipLeft = hover ? clamp(hover.px, tipWidth / 2, Math.max(tipWidth / 2, hover.width - tipWidth / 2)) : 0;
  const current = chapterAt(chapters, time);
  const valueText = current
    ? t('pc.seekValueChapter', {time: formatClock(time), duration: formatClock(duration), chapter: current.chapter.title})
    : t('pc.seekValue', {time: formatClock(time), duration: formatClock(duration)});

  return (
    <div
      className="pc-progress"
      data-active={hover || scrub !== null ? 'true' : 'false'}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={event => finish(event, true)}
      onPointerCancel={event => finish(event, false)}
      onPointerLeave={event => {
        if (dragging.current === null && event.pointerType === 'mouse') setHover(null);
      }}
    >
      <div
        ref={trackRef}
        className="pc-track"
        role="slider"
        tabIndex={0}
        aria-label={t('pc.seek')}
        aria-valuemin={0}
        aria-valuemax={Math.round(duration)}
        aria-valuenow={Math.round(time)}
        aria-valuetext={valueText}
        onKeyDown={onKeyDown}
      >
        <div className="pc-rail" />
        {duration > 0 &&
          buffered.map(([a, b], i) => (
            <div key={i} className="pc-buffered" style={{left: `${(a / duration) * 100}%`, width: `${((b - a) / duration) * 100}%`}} />
          ))}
        {hover && <div className="pc-hover" style={{width: `${hover.fraction * 100}%`}} />}
        <div className="pc-played" style={{transform: `scaleX(${shown})`}} />
        {segments.map(f => (
          <div key={f} className="pc-gap" style={{left: `${f * 100}%`}} />
        ))}
        <div className="pc-thumb" style={{left: `${shown * 100}%`}} />
      </div>
      {hover && duration > 0 && (
        <div className="pc-tip" style={{left: tipLeft}} aria-hidden="true">
          {showFrame && <img className="pc-tip-frame" src={preview.frame!.url} alt="" width={160} height={90} />}
          <span className="pc-tip-text">
            {formatClock(tipTime)}
            {tipChapter && ` · ${t('pc.chapter', {n: tipChapter.index + 1, title: tipChapter.chapter.title})}`}
          </span>
        </div>
      )}
    </div>
  );
}

function VolumeControl({video, volume, muted, showSlider}: {video: HTMLVideoElement | null; volume: number; muted: boolean; showSlider: boolean}) {
  const t = useT();
  const level = muted ? 0 : Math.round(volume * 100);
  const set = (next: number) => {
    if (!video) return;
    video.volume = clamp(next / 100, 0, 1);
    video.muted = next === 0;
  };
  const toggle = () => {
    if (!video) return;
    video.muted = !video.muted;
    if (!video.muted && video.volume === 0) video.volume = 0.5;
  };
  return (
    <div className="pc-volume">
      <button type="button" className="pc-btn" aria-label={muted || volume === 0 ? t('pc.unmute') : t('pc.mute')} title={muted ? t('pc.unmute') : t('pc.mute')} onClick={toggle}>
        <Icon name={muted || volume === 0 ? 'mute' : 'volume'} size={22} />
      </button>
      {showSlider && (
        <input
          type="range"
          className="pc-volume-slider"
          min={0}
          max={100}
          step={5}
          value={level}
          aria-label={t('pc.volume')}
          aria-valuetext={t('pc.volumeValue', {n: level})}
          style={{['--level' as string]: `${level}%`}}
          onChange={event => set(Number(event.currentTarget.value))}
        />
      )}
    </div>
  );
}
