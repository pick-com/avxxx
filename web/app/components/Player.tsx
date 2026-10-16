import {useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode} from 'react';
import type HlsType from 'hls.js';
import {getPlayConfig, getPlayback, resolvePlayback, saveWatchProgress} from '../lib/api';
import {currentPage, entryFrom, track} from '../lib/analytics';
import {useAuth} from '../lib/auth';
import {formatClock} from '../lib/format';
import {PlayTracker, takeEndReason, takeEntryOverride} from '../lib/playAnalytics';
import {entriesFromConfig, entriesFromStreams, isHlsEntry, type PlayEntry} from '../lib/playSources';
import {useAsync} from '../lib/useAsync';
import {useMediaQuery} from '../lib/useMediaQuery';
import {Icon} from './Icon';
import {PlayerMenu} from './PlayerMenu';
import {PlayerSettingsSheet, type Choice, type SettingsTab} from './PlayerSettingsSheet';
import {PlayerTouchLayer} from './PlayerTouchLayer';

const SAVE_INTERVAL_MS = 15_000;
const FLASH_MS = 700;
const RATES = [0.5, 0.75, 1, 1.25, 1.5, 2];
const AUTO = -1;
const CAPTIONS_OFF = -1;

type Phase = 'idle' | 'resolving' | 'ready' | 'failed';
type Level = {index: number; height: number; bitrate: number};
type AudioTrack = {id: number; name: string};

type FullscreenVideo = HTMLVideoElement & {webkitEnterFullscreen?: () => void; webkitSupportsFullscreen?: boolean};
type LockableOrientation = ScreenOrientation & {lock?: (orientation: string) => Promise<void>; unlock?: () => void};

/**
 * Player area for Video / MobileVideo / MobilePlayerFull. Always dark, also in
 * the light theme (PRD 5.1, 8).
 *
 * Sources: GET play-config (tech plan 3.3) lists versioned sources in play
 * order. video sources play in <video>; official_embed renders in a sandboxed
 * iframe; external_link opens the partner page in a new tab. Switching keeps
 * the position between shared-progress versions (正片 / 高画质 / 配音) and
 * starts alternate versions (其他版本) from 0 (PRD 5.2). A fatal error moves
 * to the next source and says so (States 04). When play-config is missing
 * (404 before the backend lands) or has nothing playable, the player falls
 * back silently to GET playback?source=javtiful + POST resolve_path.
 *
 * HLS: hls.js is loaded (as a separate chunk) only for an HLS stream in a
 * browser without native HLS; Safari plays HLS natively. Quality levels and
 * audio tracks come from hls.js, so they are listed only on that path.
 *
 * Desktop keeps the browser's native controls; the bar under the video adds
 * 清晰度 / 倍速 / 字幕 / 画中画 / 影院 / 全屏. On touch devices in fullscreen the
 * native controls give way to PlayerTouchLayer (gestures, lock).
 *
 * Not built yet: sprite previews and chapters (tech plan 2.4 / 3.2), caption
 * tracks (play-config subtitles are still empty), region / quality hints to
 * play-config.
 *
 * Progress: only shared-progress video sources save watch progress; an
 * alternate version would otherwise overwrite the main version's position
 * (watch_history has one row per release).
 *
 * Analytics (PRD 14.2): a PlayTracker per playback of a play-config <video>
 * source, when the release id is known (lib/playAnalytics.ts).
 */
export function Player({slug, releaseId, title, resumeAt, onBack, theater = false, onToggleTheater, onEnded, autoStart = false, overlay}: {
  slug: string;
  /** releases.id for analytics (play_request / progress); no tracking without it. */
  releaseId?: number;
  title: string;
  resumeAt?: number;
  onBack?: () => void;
  theater?: boolean;
  onToggleTheater?: () => void;
  /** Called when a <video> source plays to the end (play queues, autoplay). */
  onEnded?: () => void;
  /** Start playing as soon as sources are known (autoplay next, PRD 5.4). */
  autoStart?: boolean;
  /** Rendered over the video area (the autoplay countdown). */
  overlay?: ReactNode;
}) {
  const {status} = useAuth();
  // play-config first; null (404 / not deployed / any failure) means legacy.
  const config = useAsync(signal => getPlayConfig(slug, signal), [slug]);
  const configEntries = useMemo(() => entriesFromConfig(config.data), [config.data]);
  const mode: 'pending' | 'config' | 'legacy' = config.loading ? 'pending' : configEntries.length > 0 ? 'config' : 'legacy';
  const info = useAsync(signal => getPlayback(slug, signal), [slug], mode === 'legacy');
  const [phase, setPhase] = useState<Phase>('idle');
  const [entries, setEntries] = useState<PlayEntry[]>([]);
  const [current, setCurrent] = useState(0);
  const [failure, setFailure] = useState('');
  const [notice, setNotice] = useState('');
  const videoRef = useRef<HTMLVideoElement>(null);
  const shellRef = useRef<HTMLDivElement>(null);
  const hlsRef = useRef<HlsType | null>(null);
  const trackerRef = useRef<PlayTracker | null>(null);
  const fullscreenRef = useRef(false);
  const pipRef = useRef(false);
  const levelRef = useRef(AUTO);
  const lastSaved = useRef({at: 0, position: -1});
  // Position to restore once the next video's metadata loads (resume point, or
  // the shared position when switching sources).
  const pendingSeek = useRef(0);
  // Last known position of the shared-progress versions (PRD 5.2).
  const sharedPos = useRef(0);
  // play-config resume (needs the user token) wins over the history-derived prop.
  const resumeFrom = (() => {
    const at = config.data?.resume?.position_seconds ?? resumeAt;
    return at && at > 5 ? at : 0;
  })();

  const entry = phase === 'ready' ? entries[current] : undefined;
  const stream = entry?.render === 'video' ? entry : undefined;
  const entryRef = useRef<PlayEntry | undefined>(undefined);
  entryRef.current = entry;

  const [levels, setLevels] = useState<Level[]>([]);
  const [level, setLevel] = useState(AUTO);
  const [playingLevel, setPlayingLevel] = useState(-1);
  const [audioTracks, setAudioTracks] = useState<AudioTrack[]>([]);
  const [audioTrack, setAudioTrack] = useState(0);
  const [textTracks, setTextTracks] = useState<string[]>([]);
  const [caption, setCaption] = useState(CAPTIONS_OFF);
  const [rate, setRate] = useState(1);
  const [fullscreen, setFullscreen] = useState(false);
  const [pip, setPip] = useState(false);
  const [pipSupported, setPipSupported] = useState(false);
  const [settings, setSettings] = useState<{open: boolean; tab: SettingsTab}>({open: false, tab: 'quality'});
  const rateRef = useRef(rate);
  rateRef.current = rate;
  fullscreenRef.current = fullscreen;
  pipRef.current = pip;
  levelRef.current = level;
  const [flash, setFlash] = useState('');
  const flashTimer = useRef<number | undefined>(undefined);

  const coarse = useMediaQuery('(pointer: coarse)');
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)');
  const touchMode = fullscreen && coarse;

  // play_end when the release changes or the player goes away.
  useEffect(
    () => () => {
      const tracker = trackerRef.current;
      trackerRef.current = null;
      const forced = takeEndReason();
      tracker?.end(forced ?? (tracker.hasEnded ? 'completed' : 'closed'));
    },
    [slug],
  );

  useEffect(() => {
    setPhase('idle');
    setEntries([]);
    setCurrent(0);
    setFailure('');
    setNotice('');
    lastSaved.current = {at: 0, position: -1};
    pendingSeek.current = 0;
    sharedPos.current = 0;
  }, [slug]);

  const showFlash = useCallback((text: string) => {
    window.clearTimeout(flashTimer.current);
    setFlash(text);
    flashTimer.current = window.setTimeout(() => setFlash(''), FLASH_MS);
  }, []);
  useEffect(() => () => window.clearTimeout(flashTimer.current), []);

  // Watch progress: at most every 15 s while playing, plus pause / ended /
  // page hidden / leaving the page (keepalive so the request survives unload).
  const save = useCallback(
    (keepalive: boolean) => {
      if (status !== 'user') return;
      // Alternate versions keep their own progress; never overwrite the shared one.
      const playing = entryRef.current;
      if (!playing || playing.alt || playing.render !== 'video') return;
      const video = videoRef.current;
      if (!video || !video.currentTime) return;
      const position = Math.floor(video.currentTime);
      if (position === lastSaved.current.position) return;
      lastSaved.current = {at: Date.now(), position};
      const duration = Number.isFinite(video.duration) ? video.duration : 0;
      saveWatchProgress(slug, position, duration, keepalive).catch(() => {
        /* progress is best effort */
      });
    },
    [slug, status],
  );

  useEffect(() => {
    const onVisibility = () => {
      if (document.visibilityState === 'hidden') save(true);
    };
    const onPageHide = () => save(true);
    document.addEventListener('visibilitychange', onVisibility);
    window.addEventListener('pagehide', onPageHide);
    return () => {
      document.removeEventListener('visibilitychange', onVisibility);
      window.removeEventListener('pagehide', onPageHide);
      save(true);
    };
  }, [save]);

  const beginTracking = (e: PlayEntry | undefined) => {
    if (!releaseId || !e || e.render !== 'video' || !e.sourceId) return;
    trackerRef.current?.end('closed');
    const playlist = typeof window !== 'undefined' && new URLSearchParams(window.location.search).has('list');
    const override = takeEntryOverride();
    trackerRef.current = new PlayTracker(
      {
        releaseId,
        entry: override ?? entryFrom(currentPage().refPage, {playlist}),
        durationHint: config.data?.release.duration_seconds,
        getFullscreen: () => fullscreenRef.current,
        getPip: () => pipRef.current,
        isAutoQuality: () => levelRef.current === AUTO,
      },
      {id: e.sourceId, alt: e.alt},
      rateRef.current,
    );
  };

  const start = async (index?: number) => {
    setFailure('');
    setNotice('');
    sharedPos.current = resumeFrom;
    if (mode === 'config') {
      const resumeSource = config.data?.resume?.source_id;
      let i = index ?? configEntries.findIndex(e => e.sourceId === resumeSource);
      if (i < 0) i = configEntries.findIndex(e => e.render !== 'external');
      if (i < 0) i = 0;
      pendingSeek.current = configEntries[i]?.alt ? 0 : resumeFrom;
      beginTracking(configEntries[i]);
      setEntries(configEntries);
      setCurrent(i);
      setPhase('ready');
      return;
    }
    if (!info.data) return;
    setPhase('resolving');
    try {
      const result = await resolvePlayback(info.data.resolve_path);
      if (result.length === 0) throw new Error('没有找到可用的播放地址');
      pendingSeek.current = resumeFrom;
      setEntries(entriesFromStreams(result));
      setCurrent(0);
      setPhase('ready');
    } catch (cause) {
      setFailure(cause instanceof Error ? cause.message : String(cause));
      setPhase('failed');
    }
  };

  // Autoplay next (PRD 5.4): start once the sources are known.
  const autoStarted = useRef('');
  useEffect(() => {
    if (!autoStart || phase !== 'idle' || autoStarted.current === slug) return;
    if (mode === 'config' || (mode === 'legacy' && info.data)) {
      autoStarted.current = slug;
      void start();
    }
  });

  // Shared-progress versions continue from the shared position; alternate
  // versions start from 0 (PRD 5.2).
  const switchTo = (index: number, auto = false) => {
    const from = entries[current];
    const to = entries[index];
    if (!to || index === current) return;
    const video = videoRef.current;
    if (from && !from.alt && from.render === 'video' && video && video.currentTime > 0) sharedPos.current = video.currentTime;
    if (!auto) {
      save(false);
      setNotice('');
    }
    const tracker = trackerRef.current;
    if (tracker && !tracker.isClosed && to.render === 'video' && to.sourceId) {
      tracker.switchSource({id: to.sourceId, alt: to.alt}, auto ? 'auto' : 'manual', auto ? 'error' : 'user');
    } else if (tracker && !tracker.isClosed) {
      // Embeds and partner pages cannot be observed: this playback ends here.
      tracker.end('closed');
      trackerRef.current = null;
    } else if (mode === 'config') {
      beginTracking(to);
    }
    pendingSeek.current = to.alt ? 0 : sharedPos.current;
    if (videoRef.current && pendingSeek.current > 0) videoRef.current.dataset.seekMethod = 'skip';
    setCurrent(index);
  };

  const onVideoError = () => {
    // States 04: on failure move to the next playable source and say so.
    // External links need the user, so the automatic chain skips them.
    const from = entries[current];
    const nextIndex = entries.findIndex((e, i) => i > current && e.render !== 'external');
    if (from && nextIndex >= 0) {
      const next = entries[nextIndex];
      setNotice(`${from.name} 加载失败，已自动切到${next.name}${next.alt ? '（其他版本，从头播放）' : ''}`);
      switchTo(nextIndex, true);
      return;
    }
    const code = videoRef.current?.error?.code;
    trackerRef.current?.end('error', code ? `media_err_${code}` : 'all_sources_failed');
    trackerRef.current = null;
    setFailure(mode === 'config' ? '所有片源都加载失败' : '所有线路都加载失败');
    setPhase('failed');
  };
  const onVideoErrorRef = useRef(onVideoError);
  onVideoErrorRef.current = onVideoError;

  // Attach the stream: native src, or hls.js when HLS is not native.
  useEffect(() => {
    const video = videoRef.current;
    if (!stream || !video) return;
    setLevels([]);
    setLevel(AUTO);
    setPlayingLevel(-1);
    setAudioTracks([]);
    setAudioTrack(0);
    if (!isHlsEntry(stream) || video.canPlayType('application/vnd.apple.mpegurl') !== '') {
      video.src = stream.url;
      return;
    }
    let cancelled = false;
    let hls: HlsType | null = null;
    let offlineWait: (() => void) | null = null;
    import('hls.js')
      .then(({default: Hls}) => {
        if (cancelled) return;
        if (!Hls.isSupported()) {
          onVideoErrorRef.current();
          return;
        }
        const instance = new Hls({capLevelToPlayerSize: true});
        hls = instance;
        hlsRef.current = instance;
        let mediaRecovered = false;
        instance.on(Hls.Events.MANIFEST_PARSED, () => {
          setLevels(instance.levels.map((l, index) => ({index, height: l.height, bitrate: l.bitrate})));
        });
        instance.on(Hls.Events.LEVEL_SWITCHED, (_event, data) => setPlayingLevel(data.level));
        instance.on(Hls.Events.AUDIO_TRACKS_UPDATED, (_event, data) => {
          setAudioTracks(data.audioTracks.map((t, i) => ({id: t.id ?? i, name: t.name || t.lang || `音轨 ${i + 1}`})));
          setAudioTrack(instance.audioTrack);
        });
        instance.on(Hls.Events.ERROR, (_event, data) => {
          if (!data.fatal) return;
          // States 05: offline keeps the buffered part playing; resume loading on reconnect.
          if (data.type === Hls.ErrorTypes.NETWORK_ERROR && !navigator.onLine) {
            if (offlineWait) return;
            offlineWait = () => {
              offlineWait = null;
              instance.startLoad();
            };
            window.addEventListener('online', offlineWait, {once: true});
            return;
          }
          if (data.type === Hls.ErrorTypes.MEDIA_ERROR && !mediaRecovered) {
            mediaRecovered = true;
            instance.recoverMediaError();
            return;
          }
          onVideoErrorRef.current();
        });
        instance.loadSource(stream.url);
        instance.attachMedia(video);
      })
      .catch(() => {
        if (!cancelled) onVideoErrorRef.current();
      });
    return () => {
      cancelled = true;
      if (offlineWait) window.removeEventListener('online', offlineWait);
      hls?.destroy();
      hlsRef.current = null;
    };
  }, [stream]);

  // Each source renders a new <video>; the tracker follows it.
  useEffect(() => {
    const video = videoRef.current;
    if (stream && video) trackerRef.current?.attach(video);
  }, [stream]);

  useEffect(() => {
    const video = videoRef.current;
    if (!stream || !video) return;
    const enter = () => setPip(true);
    const leave = () => setPip(false);
    video.addEventListener('enterpictureinpicture', enter);
    video.addEventListener('leavepictureinpicture', leave);
    return () => {
      video.removeEventListener('enterpictureinpicture', enter);
      video.removeEventListener('leavepictureinpicture', leave);
    };
  }, [stream]);

  // Playback rate survives line switches.
  useEffect(() => {
    if (videoRef.current) videoRef.current.playbackRate = rate;
  }, [rate, stream]);

  // Fullscreen state (the shell element, so overlays stay visible).
  useEffect(() => {
    const onChange = () => {
      const active = document.fullscreenElement !== null && document.fullscreenElement === shellRef.current;
      setFullscreen(active);
      if (!active) (screen.orientation as LockableOrientation | undefined)?.unlock?.();
    };
    document.addEventListener('fullscreenchange', onChange);
    return () => document.removeEventListener('fullscreenchange', onChange);
  }, []);

  useEffect(() => {
    setPipSupported(typeof document !== 'undefined' && 'pictureInPictureEnabled' in document && document.pictureInPictureEnabled);
  }, []);

  const togglePlay = useCallback(() => {
    const video = videoRef.current;
    if (!video) return;
    if (video.paused) {
      void video.play().catch(() => undefined);
      showFlash('播放');
    } else {
      video.pause();
      showFlash('暂停');
    }
  }, [showFlash]);

  const seekBy = useCallback(
    (delta: number) => {
      const video = videoRef.current;
      if (!video) return;
      const end = Number.isFinite(video.duration) ? video.duration : Infinity;
      video.dataset.seekMethod = 'keyboard';
      video.currentTime = Math.min(end, Math.max(0, video.currentTime + delta));
      showFlash(delta < 0 ? `后退 ${-delta} 秒` : `前进 ${delta} 秒`);
    },
    [showFlash],
  );

  const toggleMute = useCallback(() => {
    const video = videoRef.current;
    if (!video) return;
    video.muted = !video.muted;
    showFlash(video.muted ? '已静音' : '取消静音');
  }, [showFlash]);

  const toggleFullscreen = useCallback(async () => {
    const shell = shellRef.current;
    const video = videoRef.current as FullscreenVideo | null;
    if (document.fullscreenElement) {
      await document.exitFullscreen().catch(() => undefined);
      return;
    }
    if (shell && document.fullscreenEnabled) {
      try {
        await shell.requestFullscreen({navigationUI: 'hide'});
        if (coarse) await (screen.orientation as LockableOrientation | undefined)?.lock?.('landscape').catch(() => undefined);
      } catch {
        /* denied; stay inline */
      }
      return;
    }
    // iPhone Safari: no element fullscreen, only the native video player.
    video?.webkitEnterFullscreen?.();
  }, [coarse]);

  const togglePip = useCallback(async () => {
    const video = videoRef.current;
    if (!video) return;
    try {
      if (document.pictureInPictureElement) await document.exitPictureInPicture();
      else await video.requestPictureInPicture();
    } catch {
      showFlash('画中画暂不可用');
    }
  }, [showFlash]);

  const captionRef = useRef(CAPTIONS_OFF);
  const chooseCaption = useCallback(
    (index: number) => {
      const video = videoRef.current;
      if (!video) return;
      const tracks = Array.from(video.textTracks);
      const langOf = (i: number) => (i === CAPTIONS_OFF ? 'off' : tracks[i]?.language || tracks[i]?.label || `track_${i}`);
      const from = captionRef.current;
      tracks.forEach((t, i) => {
        t.mode = i === index ? 'showing' : 'disabled';
      });
      captionRef.current = index;
      setCaption(index);
      if (from !== index) track('subtitle_change', {lang: langOf(index), from: langOf(from)});
    },
    [],
  );

  const toggleCaptions = useCallback(() => {
    if (textTracks.length === 0) {
      showFlash('这部作品暂无字幕');
      return;
    }
    const next = caption === CAPTIONS_OFF ? 0 : CAPTIONS_OFF;
    chooseCaption(next);
    showFlash(next === CAPTIONS_OFF ? '字幕已关闭' : `字幕：${textTracks[next]}`);
  }, [caption, chooseCaption, showFlash, textTracks]);

  const chooseLevel = (index: number) => {
    const label = (i: number) => {
      if (i === AUTO) return 'auto';
      const l = levels.find(x => x.index === i);
      return l ? levelLabel(l) : String(i);
    };
    trackerRef.current?.manualQuality(label(level), label(index));
    levelRef.current = index;
    setLevel(index);
    const hls = hlsRef.current;
    if (hls) hls.currentLevel = index; // -1 = auto (ABR)
  };

  const chooseAudio = (id: number) => {
    const name = (x: number) => audioTracks.find(t => t.id === x)?.name ?? String(x);
    if (id !== audioTrack) track('audio_change', {lang: name(id), from: name(audioTrack)});
    setAudioTrack(id);
    const hls = hlsRef.current;
    if (hls) hls.audioTrack = id;
  };

  // Keyboard (PRD 5.1): only when the player or the page body has focus, never
  // while typing, and not inside menus or dialogs.
  useEffect(() => {
    if (phase !== 'ready') return;
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey) return;
      const shell = shellRef.current;
      if (!shell || !videoRef.current) return;
      const target = event.target instanceof HTMLElement ? event.target : null;
      if (target && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName))) return;
      if (target?.closest('[role="menu"], [role="dialog"], [role="tablist"], [role="radiogroup"]')) return;
      const inPlayer = target !== null && shell.contains(target);
      const onBody = target === null || target === document.body || target === document.documentElement || target.id === 'main';
      if (!inPlayer && !onBody) return;
      const key = event.key.length === 1 ? event.key.toLowerCase() : event.key;
      // A focused control in the player keeps Space for itself.
      if (key === ' ' && inPlayer && target !== videoRef.current && target?.closest('button, a')) return;
      switch (key) {
        case ' ':
          togglePlay();
          break;
        case 'ArrowLeft':
          seekBy(-5);
          break;
        case 'ArrowRight':
          seekBy(5);
          break;
        case 'j':
          seekBy(-10);
          break;
        case 'l':
          seekBy(10);
          break;
        case 'f':
          void toggleFullscreen();
          break;
        case 'm':
          toggleMute();
          break;
        case 'c':
          toggleCaptions();
          break;
        default:
          return;
      }
      event.preventDefault();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [phase, seekBy, toggleCaptions, toggleFullscreen, toggleMute, togglePlay]);

  const resumeLabel = resumeFrom ? `从 ${formatClock(resumeFrom)} 继续` : '';

  // Menu data shared by the desktop bar and the mobile settings sheet.
  const sortedLevels = [...levels].sort((a, b) => b.height - a.height || b.bitrate - a.bitrate);
  const levelLabel = (l: Level) => (l.height ? `${l.height}p` : `${Math.round(l.bitrate / 1000)} kbps`);
  const playing = levels.find(l => l.index === playingLevel);
  const autoLabel = playing ? `自动（${levelLabel(playing)}）` : '自动';
  const qualityOptions: Choice<number>[] =
    levels.length > 1 ? [{value: AUTO, label: autoLabel}, ...sortedLevels.map(l => ({value: l.index, label: levelLabel(l)}))] : [];
  const currentLevel = levels.find(l => l.index === level);
  const qualityText = level === AUTO ? autoLabel : currentLevel ? levelLabel(currentLevel) : '自动';
  const rateOptions: Choice<number>[] = RATES.map(r => ({value: r, label: r === 1 ? '1.0×（正常）' : `${r}×`}));
  const captionOptions: Choice<number>[] = textTracks.length ? [{value: CAPTIONS_OFF, label: '关闭'}, ...textTracks.map((t, i) => ({value: i, label: t}))] : [];
  const audioOptions: Choice<number>[] = audioTracks.map(t => ({value: t.id, label: t.name}));
  const sourceOptions: Choice<number>[] = entries.map((e, i) => ({
    value: i,
    label: e.version ? `${e.name} · ${e.version}` : e.name,
    hint: [e.hint, e.render === 'external' ? '在新标签页打开' : ''].filter(Boolean).join(' · '),
  }));

  const openSettings = (tab: SettingsTab) => setSettings({open: true, tab});
  const loadingSources = mode === 'pending' || (mode === 'legacy' && info.loading);

  return (
    <div className="player-wrap">
      <div
        ref={shellRef}
        className="player-shell"
        data-bar={phase === 'ready' ? 'true' : 'false'}
        data-fullscreen={fullscreen ? 'true' : 'false'}
        data-touch={touchMode ? 'true' : 'false'}
      >
        <div className="player">
          {onBack && !fullscreen && (
            <button type="button" className="player-back show-mobile" aria-label="返回" onClick={onBack}>
              <Icon name="back" size={22} />
            </button>
          )}

          {stream ? (
            <video
              key={stream.key}
              ref={videoRef}
              className="player-video"
              controls={!touchMode}
              autoPlay
              playsInline
              aria-label={title}
              onLoadedMetadata={event => {
                const video = event.currentTarget;
                video.playbackRate = rate;
                setTextTracks(Array.from(video.textTracks).map((t, i) => t.label || t.language || `字幕 ${i + 1}`));
                const target = pendingSeek.current;
                pendingSeek.current = 0;
                if (target > 0 && (!Number.isFinite(video.duration) || target < video.duration - 10)) {
                  video.dataset.seekMethod = 'skip';
                  video.currentTime = target;
                }
              }}
              onTimeUpdate={event => {
                const t = event.currentTarget.currentTime;
                if (!stream.alt && t > 0) sharedPos.current = t;
                if (Date.now() - lastSaved.current.at >= SAVE_INTERVAL_MS) save(false);
              }}
              onPause={() => save(true)}
              onEnded={() => {
                save(true);
                onEnded?.();
              }}
              onError={onVideoError}
              onDoubleClick={event => {
                // Desktop double click: our shell fullscreen, so the bar stays.
                if (coarse) return;
                event.preventDefault();
                void toggleFullscreen();
              }}
            />
          ) : entry?.render === 'embed' ? (
            <iframe
              key={entry.key}
              className="player-embed"
              src={entry.url}
              title={`${title} · ${entry.name}`}
              sandbox="allow-scripts allow-same-origin allow-presentation"
              referrerPolicy="no-referrer"
              allow="autoplay; fullscreen; picture-in-picture; encrypted-media"
              allowFullScreen
            />
          ) : entry?.render === 'external' ? (
            <div className="player-state">
              <strong>{entry.name}需要在合作方网站观看</strong>
              {entry.hint && <span>{entry.hint}</span>}
              <a className="btn btn-on-dark" href={entry.url} target="_blank" rel="noopener noreferrer">
                {entry.provider ? `前往 ${entry.provider} 观看` : '在新标签页打开'}
                <Icon name="external" size={16} />
              </a>
            </div>
          ) : mode === 'legacy' && info.error ? (
            <div className="player-state">
              <strong>暂时无法播放</strong>
              <span>{info.error.message === '内容不存在或已下架' ? '这部作品还没有可用的片源。' : info.error.message}</span>
              <button type="button" className="btn btn-on-dark" onClick={info.reload}>
                重试
              </button>
            </div>
          ) : phase === 'failed' ? (
            <div className="player-state">
              <strong>暂时无法播放</strong>
              <span>{failure}</span>
              <button type="button" className="btn btn-on-dark" onClick={() => void start()}>
                重试
              </button>
            </div>
          ) : loadingSources || phase === 'resolving' ? (
            <div className="player-state" aria-live="polite">
              {(config.slow || info.slow || phase === 'resolving') && <span>{phase === 'resolving' ? '正在加载播放地址…' : '正在查找片源…'}</span>}
            </div>
          ) : (
            <div className="player-state">
              <button type="button" className="play-big" aria-label={resumeLabel ? `播放，${resumeLabel}` : '播放'} onClick={() => void start()}>
                <Icon name="play" size={30} filled />
              </button>
              {resumeLabel && <span>{resumeLabel}</span>}
            </div>
          )}

          {touchMode && stream && (
            <PlayerTouchLayer
              key={stream.key}
              videoRef={videoRef}
              title={title}
              rate={rate}
              qualityLabel={levels.length > 1 ? qualityText : '清晰度'}
              onExit={() => void toggleFullscreen()}
              onOpenSettings={openSettings}
              reducedMotion={reducedMotion}
            />
          )}

          {overlay}

          <div className="player-flash" role="status" aria-live="polite">
            {flash && <span>{flash}</span>}
          </div>

          {notice && (
            <div className="player-notice" role="status">
              {notice}
              <button type="button" className="icon-btn icon-btn-sm" aria-label="关闭提示" onClick={() => setNotice('')}>
                <Icon name="close" size={16} />
              </button>
            </div>
          )}
        </div>

        {phase === 'ready' && !touchMode && (
          <div className="player-bar" role="toolbar" aria-label="播放器设置">
            {/* Desktop: inline menus (video sources only; embeds bring their own controls). */}
            {stream && (
              <div className="pbar-group hide-mobile">
                {qualityOptions.length > 0 && (
                  <PlayerMenu label="清晰度" buttonText={qualityText} options={qualityOptions} value={level} onChange={chooseLevel} />
                )}
                <PlayerMenu label="倍速" buttonText={rate === 1 ? '倍速' : `${rate}×`} options={rateOptions} value={rate} onChange={setRate} />
                <button
                  type="button"
                  className="pbar-btn pbar-text"
                  aria-pressed={caption !== CAPTIONS_OFF}
                  aria-disabled={textTracks.length === 0}
                  title={textTracks.length === 0 ? '暂无字幕' : '字幕 (C)'}
                  onClick={toggleCaptions}
                >
                  <Icon name="cc" size={18} />
                  <span className="sr-only">字幕</span>
                </button>
              </div>
            )}
            {/* Mobile: one button opens MobileSources. */}
            <button type="button" className="pbar-btn pbar-text show-mobile" onClick={() => openSettings(stream ? 'quality' : 'source')}>
              <Icon name="settings" size={18} />
              设置{rate !== 1 ? ` · ${rate}×` : ''}
            </button>
            <span className="section-spacer" />
            {pipSupported && stream && (
              <button type="button" className="pbar-btn" aria-label={pip ? '退出画中画' : '画中画'} aria-pressed={pip} title="画中画" onClick={togglePip}>
                <Icon name="pip" size={20} />
              </button>
            )}
            {onToggleTheater && !fullscreen && (
              <button
                type="button"
                className="pbar-btn hide-mobile"
                aria-label={theater ? '退出影院模式' : '影院模式'}
                aria-pressed={theater}
                title="影院模式"
                onClick={onToggleTheater}
              >
                <Icon name="theater" size={20} />
              </button>
            )}
            <button type="button" className="pbar-btn" aria-label={fullscreen ? '退出全屏' : '全屏'} title="全屏 (F)" onClick={() => void toggleFullscreen()}>
              <Icon name={fullscreen ? 'exitFullscreen' : 'fullscreen'} size={20} />
            </button>
          </div>
        )}

        <PlayerSettingsSheet
          open={settings.open}
          initialTab={settings.tab}
          onClose={() => setSettings(s => ({...s, open: false}))}
          container={fullscreen ? shellRef.current : null}
          source={{options: sourceOptions, value: current, onChange: i => switchTo(i), versioned: mode === 'config'}}
          quality={{options: qualityOptions, value: level, onChange: chooseLevel}}
          rate={{options: rateOptions, value: rate, onChange: setRate}}
          captions={{options: captionOptions, value: caption, onChange: chooseCaption}}
          audio={{options: audioOptions, value: audioTrack, onChange: chooseAudio}}
        />
      </div>

      {/* Video 片源 row (PRD 5.2): desktop only; phones switch in the settings sheet. */}
      {mode === 'config' && !fullscreen && (
        <SourcesRow
          entries={configEntries}
          current={phase === 'ready' ? current : -1}
          onPick={i => (phase === 'ready' ? switchTo(i) : void start(i))}
        />
      )}

      {mode === 'legacy' && entries.length > 1 && !fullscreen && (
        <div className="stream-row" role="group" aria-label="播放线路">
          <span className="stream-label">线路</span>
          {entries.map((s, i) => (
            <button
              key={s.key}
              type="button"
              className={i === current ? 'chip chip-on' : 'chip'}
              aria-pressed={i === current}
              onClick={() => switchTo(i)}
            >
              {s.hint || s.name}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * Collapsed by default: one line with the current source; opening it lists
 * every source with its version (正片 / 高画质 / 配音 / 其他版本) and spec.
 */
function SourcesRow({entries, current, onPick}: {entries: PlayEntry[]; current: number; onPick: (index: number) => void}) {
  const [open, setOpen] = useState(false);
  const listId = useId();
  const active = entries[current];
  if (entries.length === 0) return null;
  return (
    <section className="sources hide-mobile" aria-label="片源">
      <button type="button" className="sources-toggle" aria-expanded={open} aria-controls={listId} onClick={() => setOpen(o => !o)}>
        <span className="sources-label">片源</span>
        <span className="sources-current">{active ? `${active.name}${active.version ? ` · ${active.version}` : ''}` : '自动选择'}</span>
        <span className="section-spacer" />
        <span className="sources-count">
          共 {entries.length} 个 · {open ? '收起' : '展开'}
        </span>
        <Icon name="chevronDown" size={16} />
      </button>
      {open && (
        <div id={listId} className="sources-list" role="radiogroup" aria-label="选择片源">
          <p className="sources-note">切换片源会保留播放进度；时长不同的版本从头播放。</p>
          {entries.map((e, i) => (
            <button
              key={e.key}
              type="button"
              role="radio"
              aria-checked={i === current}
              className="sources-item"
              onClick={() => onPick(i)}
            >
              <span className="sources-item-name">{e.name}</span>
              <span className="sources-item-hint">{e.hint}</span>
              <span className={e.alt ? 'sources-tag sources-tag-alt' : 'sources-tag'}>{e.version}</span>
              {e.render === 'external' && <Icon name="external" size={16} />}
            </button>
          ))}
        </div>
      )}
    </section>
  );
}
