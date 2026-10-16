import {useCallback, useEffect, useRef, useState} from 'react';
import {getPlayback, resolvePlayback, saveWatchProgress, type Stream} from '../lib/api';
import {useAuth} from '../lib/auth';
import {formatClock} from '../lib/format';
import {useAsync} from '../lib/useAsync';
import {Icon} from './Icon';

const SAVE_INTERVAL_MS = 15_000;

type Phase = 'idle' | 'resolving' | 'ready' | 'failed';

/**
 * Player area for Video / MobileVideo. Always dark, also in the light theme (PRD 5.1, 8).
 * Flow (same as the previous ReleasePlayback): GET playback?source=javtiful, then
 * POST its resolve_path for streams on demand.
 *
 * Not built yet: HLS/DASH adaptive playback via MSE, sprite previews, chapters,
 * captions, multi-source switching (TODO tech plan 3.3 play-config / playback_sources).
 */
export function Player({slug, title, resumeAt, onBack}: {slug: string; title: string; resumeAt?: number; onBack?: () => void}) {
  const {status} = useAuth();
  const info = useAsync(signal => getPlayback(slug, signal), [slug]);
  const [phase, setPhase] = useState<Phase>('idle');
  const [streams, setStreams] = useState<Stream[]>([]);
  const [current, setCurrent] = useState(0);
  const [failure, setFailure] = useState('');
  const [notice, setNotice] = useState('');
  const videoRef = useRef<HTMLVideoElement>(null);
  const lastSaved = useRef({at: 0, position: -1});
  // Position to restore once the next stream's metadata loads (resume point, or
  // the current position when switching lines).
  const pendingSeek = useRef(0);

  useEffect(() => {
    setPhase('idle');
    setStreams([]);
    setCurrent(0);
    setFailure('');
    setNotice('');
    lastSaved.current = {at: 0, position: -1};
    pendingSeek.current = 0;
  }, [slug]);

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

  const stream = streams[current];
  const resumeLabel = resumeAt && resumeAt > 5 ? `从 ${formatClock(resumeAt)} 继续` : '';

  return (
    <div className="player-wrap">
      <div className="player">
        {onBack && (
          <button type="button" className="player-back show-mobile" aria-label="返回" onClick={onBack}>
            <Icon name="back" size={22} />
          </button>
        )}

        {phase === 'ready' && stream ? (
          <video
            key={stream.url}
            ref={videoRef}
            className="player-video"
            src={stream.url}
            controls
            autoPlay
            playsInline
            aria-label={title}
            onLoadedMetadata={event => {
              const video = event.currentTarget;
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

        {notice && (
          <div className="player-notice" role="status">
            {notice}
            <button type="button" className="icon-btn icon-btn-sm" aria-label="关闭提示" onClick={() => setNotice('')}>
              <Icon name="close" size={16} />
            </button>
          </div>
        )}
      </div>

      {streams.length > 1 && (
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
