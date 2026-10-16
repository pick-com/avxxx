import {useCallback, useEffect, useRef, useState} from 'react';
import type HlsType from 'hls.js';
import {getPlayback, resolvePlayback, saveWatchProgress, type Stream} from '../lib/api';
import {useAuth} from '../lib/auth';
import {formatClock} from '../lib/format';
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

const isHlsStream = (stream: Stream) => stream.protocol === 'hls' || /\.m3u8(\?|#|$)/i.test(stream.url);

type FullscreenVideo = HTMLVideoElement & {webkitEnterFullscreen?: () => void; webkitSupportsFullscreen?: boolean};
type LockableOrientation = ScreenOrientation & {lock?: (orientation: string) => Promise<void>; unlock?: () => void};

/**
 * Player area for Video / MobileVideo / MobilePlayerFull. Always dark, also in
 * the light theme (PRD 5.1, 8). Flow: GET playback?source=javtiful, then POST
 * its resolve_path for streams on demand.
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
 * tracks (no subtitle data yet), multi-source switching (tech plan 3.3
 * play-config / playback_sources).
 */
export function Player({slug, title, resumeAt, onBack, theater = false, onToggleTheater}: {
  slug: string;
  title: string;
  resumeAt?: number;
  onBack?: () => void;
  theater?: boolean;
  onToggleTheater?: () => void;
}) {
  const {status} = useAuth();
  const info = useAsync(signal => getPlayback(slug, signal), [slug]);
  const [phase, setPhase] = useState<Phase>('idle');
  const [streams, setStreams] = useState<Stream[]>([]);
  const [current, setCurrent] = useState(0);
  const [failure, setFailure] = useState('');
  const [notice, setNotice] = useState('');
  const videoRef = useRef<HTMLVideoElement>(null);
  const shellRef = useRef<HTMLDivElement>(null);
  const hlsRef = useRef<HlsType | null>(null);
  const lastSaved = useRef({at: 0, position: -1});
  // Position to restore once the next stream's metadata loads (resume point, or
  // the current position when switching lines).
  const pendingSeek = useRef(0);

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
  const [flash, setFlash] = useState('');
  const flashTimer = useRef<number | undefined>(undefined);

  const coarse = useMediaQuery('(pointer: coarse)');
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)');
  const touchMode = fullscreen && coarse;

  useEffect(() => {
    setPhase('idle');
    setStreams([]);
    setCurrent(0);
    setFailure('');
    setNotice('');
    lastSaved.current = {at: 0, position: -1};
    pendingSeek.current = 0;
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

  const start = async () => {
    if (!info.data) return;
    setPhase('resolving');
    setFailure('');
    try {
      const result = await resolvePlayback(info.data.resolve_path);
      if (result.length === 0) throw new Error('没有找到可用的播放地址');
      pendingSeek.current = resumeAt && resumeAt > 5 ? resumeAt : 0;
      setStreams(result);
      setCurrent(0);
      setPhase('ready');
    } catch (cause) {
      setFailure(cause instanceof Error ? cause.message : String(cause));
      setPhase('failed');
    }
  };

  const onVideoError = () => {
    // States 04: on failure move to the next stream automatically and say so.
    if (current + 1 < streams.length) {
      const next = current + 1;
      setNotice(`线路 ${current + 1} 加载失败，已自动切到线路 ${next + 1}`);
      setCurrent(next);
      return;
    }
    setFailure('所有线路都加载失败');
    setPhase('failed');
  };
  const onVideoErrorRef = useRef(onVideoError);
  onVideoErrorRef.current = onVideoError;

  const stream = phase === 'ready' ? streams[current] : undefined;

  // Attach the stream: native src, or hls.js when HLS is not native.
  useEffect(() => {
    const video = videoRef.current;
    if (!stream || !video) return;
    setLevels([]);
    setLevel(AUTO);
    setPlayingLevel(-1);
    setAudioTracks([]);
    setAudioTrack(0);
    if (!isHlsStream(stream) || video.canPlayType('application/vnd.apple.mpegurl') !== '') {
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

  const chooseCaption = useCallback(
    (index: number) => {
      const video = videoRef.current;
      if (!video) return;
      Array.from(video.textTracks).forEach((track, i) => {
        track.mode = i === index ? 'showing' : 'disabled';
      });
      setCaption(index);
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
    setLevel(index);
    const hls = hlsRef.current;
    if (hls) hls.currentLevel = index; // -1 = auto (ABR)
  };

  const chooseAudio = (id: number) => {
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

  const resumeLabel = resumeAt && resumeAt > 5 ? `从 ${formatClock(resumeAt)} 继续` : '';

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

  const openSettings = (tab: SettingsTab) => setSettings({open: true, tab});

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

          {phase === 'ready' && stream ? (
            <video
              key={stream.url}
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
                  video.currentTime = target;
                }
              }}
              onTimeUpdate={() => {
                if (Date.now() - lastSaved.current.at >= SAVE_INTERVAL_MS) save(false);
              }}
              onPause={() => save(true)}
              onEnded={() => save(true)}
              onError={onVideoError}
              onDoubleClick={event => {
                // Desktop double click: our shell fullscreen, so the bar stays.
                if (coarse) return;
                event.preventDefault();
                void toggleFullscreen();
              }}
            />
          ) : info.error ? (
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
              <button type="button" className="btn btn-on-dark" onClick={start}>
                重试
              </button>
            </div>
          ) : info.loading || phase === 'resolving' ? (
            <div className="player-state" aria-live="polite">
              {(info.slow || phase === 'resolving') && <span>{phase === 'resolving' ? '正在加载播放地址…' : '正在查找片源…'}</span>}
            </div>
          ) : (
            <div className="player-state">
              <button type="button" className="play-big" aria-label={resumeLabel ? `播放，${resumeLabel}` : '播放'} onClick={start}>
                <Icon name="play" size={30} filled />
              </button>
              {resumeLabel && <span>{resumeLabel}</span>}
            </div>
          )}

          {touchMode && stream && (
            <PlayerTouchLayer
              key={stream.url}
              videoRef={videoRef}
              title={title}
              rate={rate}
              qualityLabel={levels.length > 1 ? qualityText : '清晰度'}
              onExit={() => void toggleFullscreen()}
              onOpenSettings={openSettings}
              reducedMotion={reducedMotion}
            />
          )}

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
            {/* Desktop: inline menus. */}
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
            {/* Mobile: one button opens MobileSources. */}
            <button type="button" className="pbar-btn pbar-text show-mobile" onClick={() => openSettings('quality')}>
              <Icon name="settings" size={18} />
              设置{rate !== 1 ? ` · ${rate}×` : ''}
            </button>
            <span className="section-spacer" />
            {pipSupported && (
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
          quality={{options: qualityOptions, value: level, onChange: chooseLevel}}
          rate={{options: rateOptions, value: rate, onChange: setRate}}
          captions={{options: captionOptions, value: caption, onChange: chooseCaption}}
          audio={{options: audioOptions, value: audioTrack, onChange: chooseAudio}}
        />
      </div>

      {streams.length > 1 && !fullscreen && (
        <div className="stream-row" role="group" aria-label="播放线路">
          <span className="stream-label">线路</span>
          {streams.map((s, i) => (
            <button
              key={s.url}
              type="button"
              className={i === current ? 'chip chip-on' : 'chip'}
              aria-pressed={i === current}
              onClick={() => {
                if (i === current) return;
                save(false);
                pendingSeek.current = videoRef.current?.currentTime ?? 0;
                setNotice('');
                setCurrent(i);
              }}
            >
              {s.quality || s.protocol.toUpperCase() || `线路 ${i + 1}`}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
