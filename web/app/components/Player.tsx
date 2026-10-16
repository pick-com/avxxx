import {useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode} from 'react';
import type HlsType from 'hls.js';
import {ApiError, getPlayConfig, getPlayback, resolvePlayback, saveWatchProgress} from '../lib/api';
import {currentPage, entryFrom, track} from '../lib/analytics';
import {useAuth} from '../lib/auth';
import {useSocial} from '../lib/social';
import {useT} from '../lib/i18n';
import {formatClock} from '../lib/format';
import {PlayTracker, takeEndReason, takeEntryOverride} from '../lib/playAnalytics';
import {heightCap, prefHeight, qualityParam, rememberRate, rememberedRate, usePlayerPrefs} from '../lib/playerPrefs';
import {guestResume, saveGuestProgress} from '../lib/guestProgress';
import {entriesFromConfig, entriesFromStreams, isHlsEntry, type PlayEntry, type SourceDetailKey} from '../lib/playSources';
import type {ApiImage} from '../lib/api';
import {useAsync} from '../lib/useAsync';
import {useMediaQuery} from '../lib/useMediaQuery';
import {useMediaSession} from '../lib/useMediaSession';
import {castableSource} from '../lib/remotePlayback';
import {useRemotePlayback} from '../lib/useRemotePlayback';
import {useDanmakuPrefs} from '../lib/danmakuPrefs';
import {useOnline} from '../lib/useOnline';
import {applyVolume, writeVolume} from '../lib/volumePrefs';
import {DanmakuBar, DanmakuLayer, DanmakuListSheet, DanmakuSettings, useDanmakuFeed, type DanmakuLayerHandle} from './Danmaku';
import {EpisodesSheet, type PlayerEpisodes} from './Episodes';
import {Icon} from './Icon';
import {PeakCurve} from './PeakCurve';
import {PlayerControls, useVideoClock, type Chapter} from './PlayerControls';
import {PlayerMenu} from './PlayerMenu';
import {PlayerSettingsSheet, type Choice, type SettingsTab} from './PlayerSettingsSheet';
import {BufferingSpinner, CenterPlay, EndScreen, ResumeToast, useBuffering, type EndItem} from './PlayerStates';
import {PlayerTouchLayer} from './PlayerTouchLayer';

const SAVE_INTERVAL_MS = 15_000;
const FLASH_MS = 700;
const FLASH_LONG_MS = 4000;
const HIDE_CONTROLS_MS = 3000;
const FRAME_S = 1 / 30;
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
 * <video> sources use our own controls (PlayerControls: scrim, progress
 * slider with buffered ranges / chapters / hover frame preview, volume, time,
 * 清晰度 / 倍速 menus, settings, 字幕 / 画中画 / 影院 / 全屏). They hide after
 * 3 s of inactivity while playing (cursor too) and stay while paused or
 * keyboard-focused. On touch devices in fullscreen PlayerTouchLayer takes
 * over (gestures, lock); iPhone fullscreen is the native player. Embeds keep
 * the small bar under the frame (settings / fullscreen).
 *
 * States over the video (PlayerStates): buffering spinner after 300 ms,
 * 「从 mm:ss 继续播放」 toast with 从头播放, end screen (replay + related
 * grid) under the page's autoplay countdown, and an error card with 重试 /
 * 换片源. Offline: a native source that errors while offline reloads at the
 * same position on reconnect instead of failing over.
 *
 * Hover previews come from a hidden CORS video (lib/useFramePreview.ts) on
 * progressive sources with a fine pointer; otherwise time + chapter only.
 * Not built yet: sprite sheets (tech plan 2.4), caption tracks (play-config
 * subtitles are still empty), region / quality hints to play-config.
 *
 * `mini`: rendered by PlayerHost in the mobile mini bar; only the <video>
 * stays (no chrome, no keyboard).
 *
 * Progress: only shared-progress video sources save watch progress; an
 * alternate version would otherwise overwrite the main version's position
 * (watch_history has one row per release).
 *
 * Analytics (PRD 14.2): a PlayTracker per playback of a play-config <video>
 * source, when the release id is known (lib/playAnalytics.ts).
 *
 * Settings (lib/playerPrefs.ts): quality_pref goes to play-config as
 * ?quality= and picks the starting hls.js level; on cellular (or Save-Data)
 * hls.js ABR is capped at 720p, 480p with data_saver; remember_rate restores
 * the last rate used for this release. autoplay is handled by the page.
 *
 * 选集 (MobilePlayerFull): with `episodes` (the release's series, 2+ works)
 * the fullscreen touch controls get a 选集 button that opens EpisodesSheet;
 * picking one is the page's navigation (Episodes.tsx).
 *
 * Media Session (lib/useMediaSession.ts): a playing <video> source publishes
 * title / `artist` / `album` / the cover (`posterImage` sizes, else `poster`)
 * to the OS media controls, lock screen, headset and media keys, which can
 * play / pause / stop / skip ±10 s / seek; previous / next track call
 * `onPreviousTrack` / `onNextTrack` (the page's queue, series or autoplay
 * next) and are hidden without them.
 *
 * Casting (lib/useRemotePlayback.ts): a 投屏 button in the control bar and
 * the fullscreen touch controls while the browser reports a device (Remote
 * Playback API: Chrome / Android cast menu, Safari AirPlay). Connecting /
 * connected show on the button and in a small 「正在投屏到设备」 overlay;
 * the controls stay up so the progress remains visible. hls.js (MSE)
 * sources cannot be cast and set disableRemotePlayback (castableSource).
 * Limit: the receiver fetches the stream itself, so a source bound to our
 * Referer, cookies or a viewer-bound signature may fail there. The Google
 * Cast SDK (custom receiver) is a follow-up, not built.
 */
export type PlayerProps = Parameters<typeof Player>[0];

export function Player({slug, releaseId, title, resumeAt, startAt, onBack, theater = false, onToggleTheater, onEnded, autoStart = false, overlay, entryReason, related = [], onReplay, mini = false, episodes, poster, posterImage, artist, album, onPreviousTrack, onNextTrack, resumePending = false}: {
  slug: string;
  /** releases.id for analytics (play_request / progress); no tracking without it. */
  releaseId?: number;
  title: string;
  resumeAt?: number;
  /** A shared link's start time (?t=, seconds); wins over the saved resume point. */
  startAt?: number;
  onBack?: () => void;
  theater?: boolean;
  onToggleTheater?: () => void;
  /** Called when a <video> source plays to the end (play queues, autoplay). */
  onEnded?: () => void;
  /** Start playing as soon as sources are known (autoplay next, PRD 5.4). */
  autoStart?: boolean;
  /** Rendered over the video area (the autoplay countdown). */
  overlay?: ReactNode;
  /** Recommendation reason code of the card that opened this page (play_request `reason`). */
  entryReason?: string;
  /** Related works for the end screen (first six). */
  related?: EndItem[];
  /** The viewer replays from the end screen (the page cancels its countdown). */
  onReplay?: () => void;
  /** Mobile mini player: only the video, no chrome or keyboard. */
  mini?: boolean;
  /** The release's series in episode order: 选集 in the fullscreen touch controls. */
  episodes?: PlayerEpisodes;
  /** The release's stored cover: shown behind the start / status states. */
  poster?: string;
  /** The same cover with its stored sizes / srcset: Media Session artwork with `sizes`. */
  posterImage?: ApiImage | null;
  /** Lead performers (Media Session artist), already joined by the page. */
  artist?: string;
  /** Maker or series (Media Session album). */
  album?: string;
  /** previoustrack / nexttrack media keys; unset (hidden by the OS) without them. */
  onPreviousTrack?: () => void;
  onNextTrack?: () => void;
  /** The saved position (watch history) is still loading: autoStart waits so playback resumes there. */
  resumePending?: boolean;
}) {
  const t = useT();
  const {status} = useAuth();
  const {refreshLater} = useSocial();
  const prefs = usePlayerPrefs();
  const prefsRef = useRef(prefs);
  prefsRef.current = prefs;
  // The quality hint is fixed once playback starts so a late settings load
  // does not reload play-config under a playing video.
  const [phase, setPhase] = useState<Phase>('idle');
  const configQuality = useRef<number | undefined>(undefined);
  if (phase === 'idle') configQuality.current = qualityParam(prefs);
  const quality = configQuality.current;
  // play-config first; null (404 / not deployed / any failure) means legacy.
  const config = useAsync(signal => getPlayConfig(slug, signal, quality), [slug, quality]);
  const configEntries = useMemo(() => entriesFromConfig(config.data), [config.data]);
  const mode: 'pending' | 'config' | 'legacy' = config.loading ? 'pending' : configEntries.length > 0 ? 'config' : 'legacy';
  // Sources exist but none is licensed for the viewer's region (PRD 15): no legacy fallback.
  const regionBlocked = config.data?.region_blocked === true;
  const info = useAsync(signal => getPlayback(slug, signal), [slug], mode === 'legacy' && !regionBlocked);
  const [entries, setEntries] = useState<PlayEntry[]>([]);
  const [current, setCurrent] = useState(0);
  const [failure, setFailure] = useState('');
  const [notice, setNotice] = useState('');
  const videoRef = useRef<HTMLVideoElement>(null);
  // The same element as state, for the danmaku hooks that subscribe to it.
  const [videoEl, setVideoEl] = useState<HTMLVideoElement | null>(null);
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
  // Signed-out viewers resume from this browser's saved position, read after
  // mount (the server render has no localStorage); auto-start waits for it.
  const guestPos = useRef(0);
  const [guestReady, setGuestReady] = useState(false);
  useEffect(() => {
    guestPos.current = guestResume(slug);
    setGuestReady(true);
  }, [slug]);
  const resumeFrom = (() => {
    if (startAt && startAt > 0) return startAt;
    const at = config.data?.resume?.position_seconds ?? resumeAt ?? (status === 'anon' && guestPos.current > 0 ? guestPos.current : undefined);
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
  const [episodesOpen, setEpisodesOpen] = useState(false);
  const hasEpisodes = !!episodes && episodes.items.length > 1;
  useEffect(() => setEpisodesOpen(false), [slug]);
  const rateRef = useRef(rate);
  rateRef.current = rate;
  fullscreenRef.current = fullscreen;
  pipRef.current = pip;
  levelRef.current = level;
  const [flash, setFlash] = useState<{text: string; long: boolean; key: number} | null>(null);
  const flashTimer = useRef<number | undefined>(undefined);
  const [pulse, setPulse] = useState<{kind: 'play' | 'pause'; key: number} | null>(null);
  const [resumeToast, setResumeToast] = useState<number | null>(null);
  // The next loadedmetadata restores the resume point (not a source switch): say so.
  const resumeToastArmed = useRef(false);
  const closeResumeToast = useCallback(() => setResumeToast(null), []);

  const dmPrefs = useDanmakuPrefs();
  const [dmListOpen, setDmListOpen] = useState(false);
  const dmLayer = useRef<DanmakuLayerHandle>(null);
  // Alternate versions have their own danmaku timeline (version_key alt:<source id>).
  const dmSourceId = stream?.alt ? stream.sourceId : undefined;
  const dmFeed = useDanmakuFeed(slug, dmSourceId, videoEl, dmPrefs.enabled && !!stream && !!videoEl);
  // PRD 14.3 「前台只显示一条曲线」: with danmaku on, the 高能波峰 of the
  // timeline being played (peak_curve = 70 % danmaku + 30 % replay rate on a
  // replay-curves backend, else danmaku_curve; null under 50 valid danmaku);
  // with danmaku off or without a danmaku curve, the 播放波峰 replay_curve
  // (main timeline only, null under 1000 valid plays). 显示高能波峰 governs both.
  // peak_curve falls back to the replay curve alone on the server, so it is
  // used only when the timeline actually has a danmaku curve.
  const mainDanmaku = config.data?.danmaku_curve;
  const danmakuCurve = stream
    ? stream.alt
      ? config.data?.danmaku?.alt_curves?.[String(stream.sourceId)] ?? null
      : mainDanmaku && mainDanmaku.length > 0
        ? config.data?.peak_curve ?? mainDanmaku
        : null
    : null;
  const replayCurve = stream && !stream.alt ? config.data?.replay_curve ?? null : null;
  const shownCurve: {curve: number[]; kind: 'danmaku' | 'playback'} | null =
    dmPrefs.enabled && danmakuCurve && danmakuCurve.length > 0
      ? {curve: danmakuCurve, kind: 'danmaku'}
      : replayCurve && replayCurve.length > 0
        ? {curve: replayCurve, kind: 'playback'}
        : null;

  const coarse = useMediaQuery('(pointer: coarse)');
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)');
  const touchMode = fullscreen && coarse;
  const finePointer = useMediaQuery('(hover: hover) and (pointer: fine)');
  const online = useOnline();
  const clock = useVideoClock(stream ? videoEl : null);
  // Native HLS (Safari, Android / recent desktop Chrome) plays the .m3u8 directly; otherwise
  // hls.js feeds a MediaSource, which cannot be cast (castableSource).
  const [nativeHls, setNativeHls] = useState(false);
  useEffect(() => setNativeHls(document.createElement('video').canPlayType('application/vnd.apple.mpegurl') !== ''), []);
  const castable = castableSource(stream ? {hls: isHlsEntry(stream), nativeHls} : null);
  const cast = useRemotePlayback(stream ? videoEl : null, castable);
  const casting = cast.state === 'connecting' || cast.state === 'connected';
  const castControl = cast.state !== 'hidden' ? {state: cast.state, prompt: cast.prompt} : undefined;
  useMediaSession(
    stream ? videoEl : null,
    {title, artist, album, artwork: posterImage ?? (poster ? {url: poster} : null)},
    {onPrevious: onPreviousTrack, onNext: onNextTrack},
  );
  const buffering = useBuffering(stream ? videoEl : null);
  // play-config chapters belong to the main timeline only.
  const chapters = useMemo<Chapter[]>(() => {
    if (!stream || stream.alt) return [];
    return (config.data?.chapters ?? [])
      .map(c => ({
        start: typeof c?.start_ms === 'number' ? c.start_ms / 1000 : Number(c?.start_seconds ?? NaN),
        title: typeof c?.title === 'string' ? c.title.trim() : '',
      }))
      .filter(c => Number.isFinite(c.start) && c.start >= 0 && c.title)
      .sort((a, b) => a.start - b.start);
  }, [config.data, stream]);

  // Controls auto-hide (3 s without activity while playing). `hold`: pointer
  // over the bar or dragging the slider; `focused`: keyboard focus inside.
  const [chromeOn, setChromeOn] = useState(true);
  const hold = useRef(false);
  const focused = useRef(false);
  const hideTimer = useRef<number | undefined>(undefined);
  const poke = useCallback(() => {
    setChromeOn(true);
    window.clearTimeout(hideTimer.current);
    hideTimer.current = window.setTimeout(() => {
      const v = videoRef.current;
      if (v && !v.paused && !hold.current && !focused.current) setChromeOn(false);
    }, HIDE_CONTROLS_MS);
  }, []);
  useEffect(() => () => window.clearTimeout(hideTimer.current), []);
  useEffect(() => {
    if (!clock.paused) poke();
  }, [clock.paused, poke]);
  // Casting keeps the controls (and the progress) up: the picture is on the device.
  const chromeVisible = chromeOn || clock.paused || clock.ended || settings.open || casting;

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
    // remember_rate: this release's last rate; otherwise every release starts at 1×.
    setRate((prefsRef.current.rememberRate && rememberedRate(slug)) || 1);
  }, [slug]);

  const chooseRate = useCallback(
    (next: number) => {
      rateRef.current = next;
      setRate(next);
      if (prefsRef.current.rememberRate) rememberRate(slug, next);
    },
    [slug],
  );

  const showFlash = useCallback((text: string, long = false) => {
    window.clearTimeout(flashTimer.current);
    setFlash({text, long, key: Date.now()});
    flashTimer.current = window.setTimeout(() => setFlash(null), long ? FLASH_LONG_MS : FLASH_MS);
  }, []);
  useEffect(() => () => window.clearTimeout(flashTimer.current), []);

  // Watch progress: at most every 15 s while playing, plus pause / ended /
  // page hidden / leaving the page (keepalive so the request survives unload).
  const save = useCallback(
    (keepalive: boolean) => {
      if (status !== 'user' && status !== 'anon') return;
      // Alternate versions keep their own progress; never overwrite the shared one.
      const playing = entryRef.current;
      if (!playing || playing.alt || playing.render !== 'video') return;
      const video = videoRef.current;
      if (!video || !video.currentTime) return;
      const position = Math.floor(video.currentTime);
      if (position === lastSaved.current.position) return;
      lastSaved.current = {at: Date.now(), position};
      const duration = Number.isFinite(video.duration) ? video.duration : 0;
      if (status === 'anon') {
        saveGuestProgress(slug, position, duration, title);
        return;
      }
      saveWatchProgress(slug, position, duration, keepalive).then(
        // 看完自动移出: the server dropped it from 稍后观看; refresh the
        // release page's button (and any list) from the server.
        r => {
          if (r?.later_removed) refreshLater();
        },
        () => {
          /* progress is best effort */
        },
      );
    },
    [slug, status, title, refreshLater],
  );

  // Leaving the page (other tab, minimised window, app in the background)
  // pauses playback, picture-in-picture included; it does not resume on
  // return. Only casting keeps playing: the picture is on the other device.
  const keepPlayingHidden = useRef(false);
  keepPlayingHidden.current = casting;
  useEffect(() => {
    const onVisibility = () => {
      if (document.visibilityState !== 'hidden') return;
      save(true);
      const video = videoRef.current;
      if (video && !video.paused && !keepPlayingHidden.current) video.pause();
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
        reason: entryReason,
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
      resumeToastArmed.current = pendingSeek.current > 0;
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
      if (result.length === 0) throw new Error(t('player.noStream'));
      pendingSeek.current = resumeFrom;
      resumeToastArmed.current = resumeFrom > 0;
      setEntries(entriesFromStreams(result, info.data.source === 'javtiful' ? 'Javtiful' : info.data.source));
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
    if (!autoStart || phase !== 'idle' || autoStarted.current === slug || resumePending || !guestReady) return;
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
    resumeToastArmed.current = false;
    setResumeToast(null);
    if (videoRef.current && pendingSeek.current > 0) videoRef.current.dataset.seekMethod = 'skip';
    setCurrent(index);
  };

  // 片源 picks from the row, the settings sheet and the error card's 换片源.
  const pickSource = (index: number) => {
    if (phase === 'ready') {
      switchTo(index);
      return;
    }
    if (mode === 'config') {
      void start(index);
      return;
    }
    if (!entries[index]) return;
    setFailure('');
    setNotice('');
    pendingSeek.current = sharedPos.current;
    setCurrent(index);
    setPhase('ready');
  };

  // States 05: a native source that fails while offline waits for the network
  // and reloads at the same position instead of failing over.
  const offlineRetry = useRef<(() => void) | null>(null);
  useEffect(
    () => () => {
      if (offlineRetry.current) window.removeEventListener('online', offlineRetry.current);
      offlineRetry.current = null;
    },
    [stream],
  );

  const onVideoError = () => {
    const failed = videoRef.current;
    if (typeof navigator !== 'undefined' && !navigator.onLine && failed && stream && !isHlsEntry(stream)) {
      if (offlineRetry.current) return;
      const at = failed.currentTime || sharedPos.current;
      const retry = () => {
        offlineRetry.current = null;
        const v = videoRef.current;
        if (!v) return;
        pendingSeek.current = at;
        v.load();
        void v.play().catch(() => undefined);
      };
      offlineRetry.current = retry;
      window.addEventListener('online', retry, {once: true});
      return;
    }
    // States 04: on failure move to the next playable source and say so.
    // External links need the user, so the automatic chain skips them.
    const from = entries[current];
    const nextIndex = entries.findIndex((e, i) => i > current && e.render !== 'external');
    if (from && nextIndex >= 0) {
      const next = entries[nextIndex];
      setNotice(t(next.alt ? 'player.sourceFailedAlt' : 'player.sourceFailed', {from: from.name, to: next.name}));
      switchTo(nextIndex, true);
      return;
    }
    const code = videoRef.current?.error?.code;
    trackerRef.current?.end('error', code ? `media_err_${code}` : 'all_sources_failed');
    trackerRef.current = null;
    setFailure(mode === 'config' ? t('player.allSourcesFailed') : t('player.allLinesFailed'));
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
    applyVolume(video);
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
          const list = instance.levels.map((l, index) => ({index, height: l.height, bitrate: l.bitrate}));
          setLevels(list);
          const p = prefsRef.current;
          // Cellular / data saver: cap ABR (hls.js orders levels by bitrate).
          const cap = heightCap(p);
          if (Number.isFinite(cap)) {
            let capIndex = -1;
            list.forEach(l => {
              if (l.height && l.height <= cap) capIndex = Math.max(capIndex, l.index);
            });
            if (capIndex >= 0) instance.autoLevelCapping = capIndex;
          }
          // quality_pref: start on the best level at or under the preference.
          const want = Math.min(prefHeight(p.qualityPref), cap);
          if (p.qualityPref !== 'auto' && Number.isFinite(want)) {
            const pick = list.filter(l => l.height && l.height <= want).sort((a, b) => b.height - a.height || b.bitrate - a.bitrate)[0];
            if (pick) {
              instance.currentLevel = pick.index;
              levelRef.current = pick.index;
              setLevel(pick.index);
            }
          }
        });
        instance.on(Hls.Events.LEVEL_SWITCHED, (_event, data) => setPlayingLevel(data.level));
        instance.on(Hls.Events.AUDIO_TRACKS_UPDATED, (_event, data) => {
          setAudioTracks(data.audioTracks.map((tr, i) => ({id: tr.id ?? i, name: tr.name || tr.lang || t('player.audioTrack', {n: i + 1})})));
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

  // Volume / mute are remembered in this browser, whatever changed them.
  useEffect(() => {
    if (!videoEl) return;
    const onVolume = () => writeVolume({volume: videoEl.volume, muted: videoEl.muted});
    videoEl.addEventListener('volumechange', onVolume);
    return () => videoEl.removeEventListener('volumechange', onVolume);
  }, [videoEl]);

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

  const onReplayRef = useRef(onReplay);
  onReplayRef.current = onReplay;
  const theaterRef = useRef({theater, onToggleTheater});
  theaterRef.current = {theater, onToggleTheater};

  const togglePlay = useCallback(() => {
    const video = videoRef.current;
    if (!video) return;
    if (video.paused) {
      if (video.ended) {
        video.dataset.seekMethod = 'skip';
        video.currentTime = 0;
        onReplayRef.current?.();
      }
      void video.play().catch(() => undefined);
      setPulse({kind: 'play', key: Date.now()});
    } else {
      video.pause();
    }
  }, []);

  const seekBy = useCallback(
    (delta: number) => {
      const video = videoRef.current;
      if (!video) return;
      const end = Number.isFinite(video.duration) ? video.duration : Infinity;
      video.dataset.seekMethod = 'keyboard';
      video.currentTime = Math.min(end, Math.max(0, video.currentTime + delta));
      showFlash(delta < 0 ? t('player.rewind', {s: -delta}) : t('player.forward', {s: delta}));
    },
    [showFlash],
  );

  const toggleMute = useCallback(() => {
    const video = videoRef.current;
    if (!video) return;
    video.muted = !video.muted;
    showFlash(video.muted ? t('player.muted') : t('player.unmuted'));
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
      showFlash(t('player.pipUnavailable'));
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
      showFlash(t('player.noCaptions'));
      return;
    }
    const next = caption === CAPTIONS_OFF ? 0 : CAPTIONS_OFF;
    chooseCaption(next);
    showFlash(next === CAPTIONS_OFF ? t('player.captionsOff') : t('player.captionsOn', {name: textTracks[next]}));
  }, [caption, chooseCaption, showFlash, textTracks, t]);

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

  const stepRate = useCallback(
    (dir: 1 | -1) => {
      const i = RATES.indexOf(rateRef.current);
      const next = RATES[Math.min(RATES.length - 1, Math.max(0, (i < 0 ? RATES.indexOf(1) : i) + dir))];
      chooseRate(next);
      showFlash(t('pc.rateTo', {rate: `${next}×`}));
    },
    [chooseRate, showFlash, t],
  );

  const seekToFraction = useCallback((fraction: number) => {
    const video = videoRef.current;
    if (!video || !Number.isFinite(video.duration)) return;
    video.dataset.seekMethod = 'keyboard';
    video.currentTime = Math.min(video.duration, Math.max(0, fraction * video.duration));
  }, []);

  // Keyboard (PRD 5.1): only when the player or the page body has focus, never
  // while typing, and not inside menus or dialogs. Never in the mini player.
  useEffect(() => {
    if (phase !== 'ready' || mini) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey) return;
      const shell = shellRef.current;
      const video = videoRef.current;
      if (!shell || !video) return;
      const target = event.target instanceof HTMLElement ? event.target : null;
      if (target && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName))) return;
      if (target?.closest('[role="menu"], [role="dialog"], [role="tablist"], [role="radiogroup"]')) return;
      const inPlayer = target !== null && shell.contains(target);
      const onBody = target === null || target === document.body || target === document.documentElement || target.id === 'main';
      if (!inPlayer && !onBody) return;
      const key = event.key.length === 1 ? event.key.toLowerCase() : event.key;
      // A focused control in the player keeps Space / Enter for itself.
      if (key === ' ' && inPlayer && target !== video && target?.closest('button, a')) return;
      // The progress slider handles its own arrows / Home / End / Page keys.
      if (target?.getAttribute('role') === 'slider' && /^(Arrow|Home|End|Page)/.test(key)) return;
      const {theater: inTheater, onToggleTheater: toggleTheater} = theaterRef.current;
      switch (key) {
        case ' ':
        case 'k':
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
        case '0':
        case '1':
        case '2':
        case '3':
        case '4':
        case '5':
        case '6':
        case '7':
        case '8':
        case '9':
          seekToFraction(Number(key) / 10);
          showFlash(t('pc.jumpTo', {n: Number(key) * 10}));
          break;
        case ',':
        case '.':
          if (!video.paused) return;
          video.dataset.seekMethod = 'keyboard';
          video.currentTime = Math.max(0, video.currentTime + (key === '.' ? FRAME_S : -FRAME_S));
          showFlash(key === '.' ? t('pc.frameForward') : t('pc.frameBack'));
          break;
        case '<':
          stepRate(-1);
          break;
        case '>':
          stepRate(1);
          break;
        case 'Home':
          // On the page body Home / End keep scrolling the page.
          if (!inPlayer) return;
          seekToFraction(0);
          showFlash(t('pc.toStart'));
          break;
        case 'End':
          if (!inPlayer) return;
          seekToFraction(1);
          showFlash(t('pc.toEnd'));
          break;
        case 'Escape':
          if (!inTheater || !toggleTheater || document.fullscreenElement) return;
          toggleTheater();
          break;
        case '?':
          showFlash(t('pc.keysHint'), true);
          break;
        default:
          return;
      }
      event.preventDefault();
      poke();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [phase, mini, poke, seekBy, seekToFraction, showFlash, stepRate, t, toggleCaptions, toggleFullscreen, toggleMute, togglePlay]);

  const replay = useCallback(() => {
    const video = videoRef.current;
    if (!video) return;
    video.dataset.seekMethod = 'skip';
    video.currentTime = 0;
    void video.play().catch(() => undefined);
    onReplayRef.current?.();
  }, []);

  const resumeLabel = resumeFrom ? t('player.resumeFrom', {time: formatClock(resumeFrom)}) : '';

  // Menu data shared by the desktop bar and the mobile settings sheet.
  const sortedLevels = [...levels].sort((a, b) => b.height - a.height || b.bitrate - a.bitrate);
  const levelLabel = (l: Level) => (l.height ? `${l.height}p` : `${Math.round(l.bitrate / 1000)} kbps`);
  const playing = levels.find(l => l.index === playingLevel);
  const autoLabel = playing ? t('player.autoWith', {q: levelLabel(playing)}) : t('player.auto');
  const qualityOptions: Choice<number>[] =
    levels.length > 1 ? [{value: AUTO, label: autoLabel}, ...sortedLevels.map(l => ({value: l.index, label: levelLabel(l)}))] : [];
  const currentLevel = levels.find(l => l.index === level);
  const qualityText = level === AUTO ? autoLabel : currentLevel ? levelLabel(currentLevel) : t('player.auto');
  const rateOptions: Choice<number>[] = RATES.map(r => ({value: r, label: r === 1 ? t('player.rateNormal') : `${r}×`}));
  const captionOptions: Choice<number>[] = textTracks.length ? [{value: CAPTIONS_OFF, label: t('player.off')}, ...textTracks.map((name, i) => ({value: i, label: name}))] : [];
  const audioOptions: Choice<number>[] = audioTracks.map(t => ({value: t.id, label: t.name}));
  const toChoice = (e: PlayEntry, i: number): Choice<number> => ({
    value: i,
    label: e.version ? `${e.name} · ${e.version}` : e.name,
    hint: [e.hint, e.render === 'external' ? t('player.openInNewTab') : ''].filter(Boolean).join(' · '),
  });
  const sourceOptions: Choice<number>[] = entries.map(toChoice);
  // 片源 details card: the source's declared metadata, then what the stream
  // actually exposes once loaded (playing height, caption / audio tracks).
  const currentEntry = entries[current] ?? configEntries[current];
  const metaLabel: Record<SourceDetailKey | 'playing', string> = {
    provider: t('player.meta.provider'),
    version: t('player.meta.version'),
    resolution: t('player.meta.resolution'),
    duration: t('player.meta.duration'),
    audio: t('player.meta.audio'),
    subtitles: t('player.meta.subtitles'),
    format: t('player.meta.format'),
    playing: t('player.meta.playing'),
  };
  const sourceDetails: {label: string; value: string}[] = (currentEntry?.details ?? []).map(d => ({label: metaLabel[d.key], value: d.value}));
  if (playing?.height) sourceDetails.push({label: metaLabel.playing, value: levelLabel(playing)});
  if (textTracks.length && !currentEntry?.details.some(d => d.key === 'subtitles')) {
    sourceDetails.push({label: metaLabel.subtitles, value: textTracks.join('、')});
  }
  if (audioTracks.length > 1 && !currentEntry?.details.some(d => d.key === 'audio')) {
    sourceDetails.push({label: metaLabel.audio, value: audioTracks.map(a => a.name).join('、')});
  }
  // Before the first start (or once every source failed in config mode) the
  // sheet still lists play-config's sources.
  const configSourceOptions: Choice<number>[] = configEntries.map(toChoice);

  const openSettings = (tab: SettingsTab) => setSettings({open: true, tab});
  const loadingSources = mode === 'pending' || (mode === 'legacy' && info.loading);

  const canSwitchSource = sourceOptions.length > 1 || (mode === 'config' && configEntries.length > 1);
  const showControls = !!stream && !touchMode && !mini;
  const idle = showControls && !chromeVisible;
  const peakNode =
    stream && !touchMode && !mini && dmPrefs.showPeaks && shownCurve ? (
      <PeakCurve
        key={shownCurve.kind}
        curve={shownCurve.curve}
        kind={shownCurve.kind}
        video={videoEl}
        // PRD 5.5.1: no top-3 markers on a first watch with 屏蔽剧透 on.
        showMarkers={!(dmPrefs.blockSpoiler && !resumeFrom)}
        playId={trackerRef.current?.playId}
      />
    ) : null;

  return (
    <div className="player-wrap" data-mini={mini ? 'true' : undefined}>
      <div
        ref={shellRef}
        className="player-shell"
        data-bar={phase === 'ready' && !stream ? 'true' : 'false'}
        data-fullscreen={fullscreen ? 'true' : 'false'}
        data-touch={touchMode ? 'true' : 'false'}
        onPointerMove={event => {
          // Touch screens toggle the controls by tapping instead.
          if (event.pointerType === 'mouse' && !coarse) poke();
        }}
        onFocus={event => {
          if (event.target instanceof HTMLElement && event.target.matches(':focus-visible')) {
            focused.current = true;
            poke();
          }
        }}
        onBlur={event => {
          if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
            focused.current = false;
            poke();
          }
        }}
      >
        <div
          className="player"
          data-idle={idle ? 'true' : 'false'}
          onPointerLeave={event => {
            // Leaving the video hides the controls at once while playing.
            if (event.pointerType === 'mouse' && !coarse && !focused.current && videoRef.current && !videoRef.current.paused) setChromeOn(false);
          }}
        >
          {onBack && !fullscreen && !mini && (
            <button type="button" className="player-back show-mobile" aria-label={t('player.back')} onClick={onBack}>
              <Icon name="back" size={22} />
            </button>
          )}

          {/* Cover behind every non-video state (start, loading, 暂无视频资源). */}
          {poster && !stream && entry?.render !== 'embed' && <img className="player-poster" src={poster} alt="" aria-hidden="true" decoding="async" />}

          {stream ? (
            <video
              key={stream.key}
              ref={el => {
                videoRef.current = el;
                setVideoEl(el);
              }}
              className="player-video"
              // Our controls everywhere; iPhone fullscreen uses the native player.
              controls={false}
              autoPlay
              playsInline
              // AirPlay (Safari) only for castable sources; see castableSource.
              x-webkit-airplay={castable ? 'allow' : 'deny'}
              aria-label={title}
              onClick={() => {
                if (mini) return;
                if (coarse) {
                  // Touch: a tap shows / hides the controls; the buttons do the rest.
                  if (chromeVisible && videoRef.current && !videoRef.current.paused) setChromeOn(false);
                  else poke();
                  return;
                }
                togglePlay();
                poke();
              }}
              onLoadedMetadata={event => {
                const video = event.currentTarget;
                video.playbackRate = rate;
                setTextTracks(Array.from(video.textTracks).map((tr, i) => tr.label || tr.language || t('player.captionTrack', {n: i + 1})));
                const target = pendingSeek.current;
                pendingSeek.current = 0;
                const armed = resumeToastArmed.current;
                resumeToastArmed.current = false;
                if (target > 0 && (!Number.isFinite(video.duration) || target < video.duration - 10)) {
                  video.dataset.seekMethod = 'skip';
                  video.currentTime = target;
                  if (armed) setResumeToast(target);
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
                if (coarse || mini) return;
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
              <strong>{t('player.partnerOnly', {name: entry.name})}</strong>
              {entry.hint && <span>{entry.hint}</span>}
              <a className="btn btn-on-dark" href={entry.url} target="_blank" rel="noopener noreferrer">
                {entry.provider ? t('player.goPartner', {provider: entry.provider}) : t('player.openInNewTab')}
                <Icon name="external" size={16} />
              </a>
            </div>
          ) : regionBlocked ? (
            <div className="player-state" role="status">
              <Icon name="lock" size={28} />
              <strong>{t('player.unavailable')}</strong>
              <span>{t('player.regionBlocked')}</span>
            </div>
          ) : mode === 'legacy' && info.error instanceof ApiError && info.error.status === 404 ? (
            // No playable source at all: say so on the cover; retrying cannot help.
            <div className="player-state player-state-nosource" role="status">
              <Icon name="film" size={30} />
              <strong>{t('player.noSourceTitle')}</strong>
              <span>{t('player.noSource')}</span>
            </div>
          ) : mode === 'legacy' && info.error ? (
            <div className="player-state">
              <Icon name="alert" size={28} />
              <strong>{t('player.unavailable')}</strong>
              <span>{info.error.message}</span>
              <button type="button" className="btn btn-on-dark" onClick={info.reload}>
                {t('player.retry')}
              </button>
            </div>
          ) : phase === 'failed' ? (
            <div className="player-state" role="alert">
              <Icon name="alert" size={28} />
              <strong>{t('player.unavailable')}</strong>
              <span>{failure}</span>
              <div className="player-state-actions">
                <button type="button" className="btn btn-primary" onClick={() => void start()}>
                  {t('player.retry')}
                </button>
                {canSwitchSource && (
                  <button type="button" className="btn btn-on-dark" onClick={() => openSettings('source')}>
                    {t('pc.switchSource')}
                  </button>
                )}
              </div>
            </div>
          ) : loadingSources || phase === 'resolving' ? (
            <div className="player-state" aria-live="polite">
              <span className="pc-spinner" aria-hidden="true" />
              {(config.slow || info.slow || phase === 'resolving') && <span>{phase === 'resolving' ? t('player.resolving') : t('player.finding')}</span>}
            </div>
          ) : (
            <div className="player-state">
              <button type="button" className="play-big" aria-label={resumeLabel ? t('player.playResume', {label: resumeLabel}) : t('player.play')} onClick={() => void start()}>
                <Icon name="play" size={30} filled />
              </button>
              {resumeLabel && <span>{resumeLabel}</span>}
            </div>
          )}

          {touchMode && stream && (
            <PlayerTouchLayer
              // Not stream.key: the <video> sibling uses it, and duplicate
              // sibling keys remount the video (render loop once a sheet opens).
              key={`touch-${stream.key}`}
              videoRef={videoRef}
              title={title}
              rate={rate}
              qualityLabel={levels.length > 1 ? qualityText : t('player.quality')}
              onExit={() => void toggleFullscreen()}
              onOpenSettings={openSettings}
              reducedMotion={reducedMotion}
              onOpenEpisodes={hasEpisodes ? () => setEpisodesOpen(true) : undefined}
              cast={castControl}
            />
          )}

          {stream && !mini && <DanmakuLayer ref={dmLayer} video={videoEl} feed={dmFeed} prefs={dmPrefs} reducedMotion={reducedMotion} />}

          {showControls && buffering && !clock.paused && <BufferingSpinner offline={!online} />}
          {stream && !mini && casting && (
            <div className="pc-cast" role="status" data-state={cast.state}>
              <Icon name="cast" size={18} />
              {cast.state === 'connecting' ? t('player.castConnecting') : t('player.casting')}
            </div>
          )}
          {showControls && !online && !buffering && !clock.paused && (
            <div className="pc-offline" role="status">
              <Icon name="offline" size={16} />
              {t('pc.offlineBuffered')}
            </div>
          )}
          {showControls && !clock.ended && <CenterPlay paused={clock.paused && !buffering} pulse={reducedMotion ? null : pulse} onToggle={togglePlay} />}
          {showControls && clock.ended && <EndScreen items={related} onReplay={replay} countdown={!!overlay} />}

          {!mini && overlay}

          {showControls && resumeToast !== null && !clock.ended && (
            <ResumeToast
              at={resumeToast}
              onClose={closeResumeToast}
              onStartOver={() => {
                const video = videoRef.current;
                if (video) {
                  video.currentTime = 0;
                  void video.play().catch(() => undefined);
                }
                setResumeToast(null);
              }}
            />
          )}

          {showControls && (
            <PlayerControls
              video={videoEl}
              clock={clock}
              visible={chromeVisible}
              chapters={chapters}
              previewSrc={stream && !isHlsEntry(stream) ? stream.url : undefined}
              previewEnabled={finePointer}
              peak={peakNode}
              menus={
                <>
                  {qualityOptions.length > 0 && (
                    <PlayerMenu label={t('player.quality')} buttonText={qualityText} options={qualityOptions} value={level} onChange={chooseLevel} />
                  )}
                  <PlayerMenu label={t('player.rate')} buttonText={rate === 1 ? t('player.rate') : `${rate}×`} options={rateOptions} value={rate} onChange={chooseRate} />
                </>
              }
              captions={{available: textTracks.length > 0, on: caption !== CAPTIONS_OFF, toggle: toggleCaptions}}
              pip={pipSupported ? {active: pip, toggle: () => void togglePip()} : undefined}
              cast={castControl}
              theater={onToggleTheater ? {active: theater, toggle: onToggleTheater} : undefined}
              fullscreen={{active: fullscreen, toggle: () => void toggleFullscreen()}}
              onTogglePlay={() => {
                togglePlay();
                poke();
              }}
              onOpenSettings={() => openSettings(stream ? 'quality' : 'source')}
              onHoldChange={on => {
                hold.current = on;
                if (on) setChromeOn(true);
                else poke();
              }}
              showVolumeSlider={!coarse}
            />
          )}

          <div className="player-flash" role="status" aria-live="polite">
            {flash && !mini && (
              <span key={flash.key} data-long={flash.long ? 'true' : undefined}>
                {flash.text}
              </span>
            )}
          </div>

          {notice && !mini && (
            <div className="player-notice" role="status">
              {notice}
              <button type="button" className="icon-btn icon-btn-sm" aria-label={t('player.closeNotice')} onClick={() => setNotice('')}>
                <Icon name="close" size={16} />
              </button>
            </div>
          )}
        </div>

        {/* Embeds bring their own controls: a small bar for settings / fullscreen. */}
        {phase === 'ready' && !stream && !touchMode && !mini && (
          <div className="player-bar" role="toolbar" aria-label={t('player.toolbar')}>
            <button type="button" className="pbar-btn pbar-text" onClick={() => openSettings('source')}>
              <Icon name="settings" size={18} />
              {t('player.settings')}
            </button>
            <span className="section-spacer" />
            {onToggleTheater && !fullscreen && (
              <button
                type="button"
                className="pbar-btn hide-mobile"
                aria-label={theater ? t('player.exitTheater') : t('player.theater')}
                aria-pressed={theater}
                title={t('player.theater')}
                onClick={onToggleTheater}
              >
                <Icon name="theater" size={20} />
              </button>
            )}
            <button type="button" className="pbar-btn" aria-label={fullscreen ? t('player.exitFullscreen') : t('player.fullscreen')} title={t('player.fullscreenKey')} onClick={() => void toggleFullscreen()}>
              <Icon name={fullscreen ? 'exitFullscreen' : 'fullscreen'} size={20} />
            </button>
          </div>
        )}

        {/* Fullscreen is the picture only: no 弹幕 input row under the video
            (danmaku settings and the list stay in the settings sheet). */}
        {stream && !touchMode && !mini && !fullscreen && (
          <DanmakuBar
            slug={slug}
            releaseId={releaseId}
            sourceId={dmSourceId}
            video={videoEl}
            feed={dmFeed}
            layer={dmLayer}
            onOpenSettings={() => openSettings('danmaku')}
            onOpenList={() => setDmListOpen(true)}
          />
        )}

        {!mini && (
          <>
            <DanmakuListSheet open={dmListOpen} onClose={() => setDmListOpen(false)} feed={dmFeed} video={videoEl} container={fullscreen ? shellRef.current : null} />

            <PlayerSettingsSheet
              open={settings.open}
              initialTab={settings.tab}
              onClose={() => setSettings(s => ({...s, open: false}))}
              container={fullscreen ? shellRef.current : null}
              source={{
                options: sourceOptions.length > 0 ? sourceOptions : configSourceOptions,
                value: current,
                onChange: i => {
                  const wasReady = phase === 'ready';
                  pickSource(i);
                  if (!wasReady) setSettings(s => ({...s, open: false}));
                },
                versioned: mode === 'config',
                details: sourceDetails,
              }}
              quality={{options: qualityOptions, value: level, onChange: chooseLevel}}
              rate={{options: rateOptions, value: rate, onChange: chooseRate}}
              captions={{options: captionOptions, value: caption, onChange: chooseCaption}}
              audio={{options: audioOptions, value: audioTrack, onChange: chooseAudio}}
              danmaku={stream ? <DanmakuSettings /> : undefined}
            />

            {episodes && hasEpisodes && (
              <EpisodesSheet open={episodesOpen} onClose={() => setEpisodesOpen(false)} episodes={episodes} container={fullscreen ? shellRef.current : null} />
            )}
          </>
        )}
      </div>

      {/* Video 片源 row (PRD 5.2): desktop only; phones switch in the settings sheet. */}
      {mode === 'config' && !fullscreen && !mini && <SourcesRow entries={configEntries} current={phase === 'ready' ? current : -1} onPick={pickSource} />}

      {mode === 'legacy' && entries.length > 1 && !fullscreen && !mini && (
        <div className="stream-row" role="group" aria-label={t('player.lines')}>
          <span className="stream-label">{t('player.line')}</span>
          {entries.map((s, i) => (
            <button
              key={s.key}
              type="button"
              className={i === current ? 'chip chip-on' : 'chip'}
              aria-pressed={i === current}
              onClick={() => pickSource(i)}
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
  const t = useT();
  const [open, setOpen] = useState(false);
  const listId = useId();
  const active = entries[current];
  if (entries.length === 0) return null;
  return (
    <section className="sources hide-mobile" aria-label={t('player.source')}>
      <button type="button" className="sources-toggle" aria-expanded={open} aria-controls={listId} onClick={() => setOpen(o => !o)}>
        <span className="sources-label">{t('player.source')}</span>
        <span className="sources-current">{active ? `${active.name}${active.version ? ` · ${active.version}` : ''}` : t('player.autoPick')}</span>
        <span className="section-spacer" />
        <span className="sources-count">
          {t('player.sourcesCount', {count: entries.length, state: open ? t('player.collapse') : t('player.expand')})}
        </span>
        <Icon name="chevronDown" size={16} />
      </button>
      {open && (
        <div id={listId} className="sources-list" role="radiogroup" aria-label={t('player.pickSource')}>
          <p className="sources-note">{t('player.sourcesNote')}</p>
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
