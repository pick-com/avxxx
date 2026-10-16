import {useEffect, useState} from 'react';
import {Link} from 'react-router';
import {formatClock} from '../lib/format';
import {useT} from '../lib/i18n';
import {Thumb} from './Cards';
import {Icon} from './Icon';

// Over-the-video states for <video> sources (States 04 / 05, Video):
// buffering spinner, center play / pause feedback, resume toast and the end
// screen. Always dark like the rest of the player.

const BUFFER_DELAY_MS = 300;
const RESUME_TOAST_MS = 8000;

/** True once the video has been waiting for data for 300 ms. */
export function useBuffering(video: HTMLVideoElement | null): boolean {
  const [buffering, setBuffering] = useState(false);
  useEffect(() => {
    setBuffering(false);
    if (!video) return;
    let timer: number | undefined;
    const wait = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(() => setBuffering(true), BUFFER_DELAY_MS);
    };
    const clear = () => {
      window.clearTimeout(timer);
      setBuffering(false);
    };
    const onSeeking = () => {
      if (video.readyState < 3) wait();
    };
    video.addEventListener('waiting', wait);
    video.addEventListener('seeking', onSeeking);
    ['playing', 'canplay', 'seeked', 'pause', 'ended', 'error', 'emptied'].forEach(e => video.addEventListener(e, clear));
    return () => {
      window.clearTimeout(timer);
      video.removeEventListener('waiting', wait);
      video.removeEventListener('seeking', onSeeking);
      ['playing', 'canplay', 'seeked', 'pause', 'ended', 'error', 'emptied'].forEach(e => video.removeEventListener(e, clear));
    };
  }, [video]);
  return buffering;
}

export function BufferingSpinner({offline}: {offline: boolean}) {
  const t = useT();
  return (
    <div className="pc-buffering" role="status">
      <span className="pc-spinner" aria-hidden="true" />
      <span className={offline ? 'pc-buffering-text' : 'sr-only'}>{offline ? t('pc.offlineWaiting') : t('pc.buffering')}</span>
    </div>
  );
}

/** Big center button while paused, plus a short pulse on every toggle. */
export function CenterPlay({paused, pulse, onToggle}: {paused: boolean; pulse: {kind: 'play' | 'pause'; key: number} | null; onToggle: () => void}) {
  const t = useT();
  return (
    <>
      {paused && (
        <button type="button" className="pc-center" aria-label={t('player.play')} onClick={onToggle}>
          <Icon name="play" size={34} filled />
        </button>
      )}
      {pulse && !paused && (
        <span key={pulse.key} className="pc-pulse" aria-hidden="true">
          <Icon name={pulse.kind} size={30} filled={pulse.kind === 'play'} />
        </span>
      )}
    </>
  );
}

/** 「从 mm:ss 继续播放」 with 从头播放 (States 04 artboard note). */
export function ResumeToast({at, onStartOver, onClose}: {at: number; onStartOver: () => void; onClose: () => void}) {
  const t = useT();
  useEffect(() => {
    const timer = window.setTimeout(onClose, RESUME_TOAST_MS);
    return () => window.clearTimeout(timer);
  }, [at, onClose]);
  return (
    <div className="pc-toast" role="status">
      <span>{t('pc.resumed', {time: formatClock(at)})}</span>
      <button type="button" className="pc-toast-action" onClick={onStartOver}>
        {t('pc.startOver')}
      </button>
      <button type="button" className="pc-toast-close" aria-label={t('pc.closeToast')} onClick={onClose}>
        <Icon name="close" size={16} />
      </button>
    </div>
  );
}

export type EndItem = {slug: string; title: string; reasonCode?: string; badge?: string};

/** End screen: replay plus a 2×3 grid of the page's related recs. */
export function EndScreen({items, onReplay, countdown}: {items: EndItem[]; onReplay: () => void; countdown: boolean}) {
  const t = useT();
  const shown = items.slice(0, 6);
  return (
    <div className="pc-end" data-countdown={countdown ? 'true' : 'false'}>
      <button type="button" className="pc-end-replay" onClick={onReplay}>
        <Icon name="undo" size={20} />
        {t('pc.replay')}
      </button>
      {shown.length > 0 && (
        <nav className="pc-end-grid" aria-label={t('pc.endTitle')}>
          {shown.map((it, i) => (
            <Link
              key={it.slug}
              to={`/releases/${encodeURIComponent(it.slug)}`}
              state={{slot: `watch:end:r${Math.floor(i / 3) + 1}c${(i % 3) + 1}`, reason: it.reasonCode}}
              className="pc-end-card"
            >
              <Thumb seed={it.slug} badge={it.badge} />
              <span className="pc-end-title">{it.title}</span>
            </Link>
          ))}
        </nav>
      )}
    </div>
  );
}
