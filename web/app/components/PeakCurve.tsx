import {useEffect, useId, useMemo, useRef, useState} from 'react';
import {track} from '../lib/analytics';
import {formatClock} from '../lib/format';
import {useT} from '../lib/i18n';

// 高能波峰 (PRD 5.5.1): play-config danmaku_curve, 200 buckets of 0-255,
// drawn as a 16 px SVG area above the progress bar. Accent at 35 %, the
// played part brighter. Clicking the curve seeks there (seek method "peak");
// the three highest peaks get markers unless the viewer blocks spoilers on a
// first watch. peak_click reports whether the viewer then watched 30 s.
//
// kind 'playback' is the 播放波峰 (PRD 14.3 replay_curve, shown when danmaku
// is off): one marker at its highest point labelled 「最多重看」 instead of
// the top-3 高能 markers, and peak_click reports curve 'playback'.

const BUCKETS = 200;
const MIN_GAP = 10; // buckets between two top markers
const WATCH_MS = 30_000;

export function topPeaks(curve: number[], n = 3): number[] {
  const order = curve.map((v, i) => [v, i] as const).sort((a, b) => b[0] - a[0] || a[1] - b[1]);
  const picked: number[] = [];
  for (const [v, i] of order) {
    if (v <= 0 || picked.length >= n) break;
    if (picked.every(p => Math.abs(p - i) >= MIN_GAP)) picked.push(i);
  }
  return picked.sort((a, b) => a - b);
}

function areaPath(curve: number[]): string {
  const n = curve.length;
  let d = `M0 16`;
  curve.forEach((v, i) => {
    const y = 16 - (Math.max(0, Math.min(255, v)) / 255) * 15;
    d += ` L${(((i + 0.5) / n) * BUCKETS).toFixed(2)} ${y.toFixed(2)}`;
  });
  return `${d} L${BUCKETS} 16 Z`;
}

export function PeakCurve({curve, kind = 'danmaku', video, showMarkers, playId}: {
  curve: number[];
  kind?: 'danmaku' | 'playback';
  video: HTMLVideoElement | null;
  showMarkers: boolean;
  /** PlayTracker.playId (peak_click is play-scoped); no event without it. */
  playId?: string;
}) {
  const t = useT();
  const [progress, setProgress] = useState(0);
  const [duration, setDuration] = useState(0);
  const pending = useRef<{position: number; top3: boolean; watched: number} | null>(null);
  const kindRef = useRef(kind);
  kindRef.current = kind;
  const playIdRef = useRef(playId);
  playIdRef.current = playId;
  const clipId = `peak-played-${useId().replace(/[^a-zA-Z0-9_-]/g, '')}`;
  const path = useMemo(() => areaPath(curve), [curve]);
  const peaks = useMemo(() => topPeaks(curve, kind === 'playback' ? 1 : 3), [curve, kind]);

  const settle = (watched30: boolean) => {
    const p = pending.current;
    pending.current = null;
    const id = playIdRef.current;
    if (p && id) track('peak_click', {curve: kindRef.current, position_ms: p.position, top3: p.top3, watched_30s: watched30}, {playId: id});
  };
  const settleRef = useRef(settle);
  settleRef.current = settle;

  useEffect(() => {
    if (!video) return;
    let last = video.currentTime;
    const update = () => {
      const d = Number.isFinite(video.duration) ? video.duration : 0;
      setDuration(d);
      setProgress(d > 0 ? Math.min(1, video.currentTime / d) : 0);
      const now = video.currentTime;
      const delta = now - last;
      last = now;
      const p = pending.current;
      if (p && !video.paused && delta > 0 && delta < 2) {
        p.watched += delta * 1000;
        if (p.watched >= WATCH_MS) settleRef.current(true);
      }
    };
    const onSeeking = () => {
      last = video.currentTime;
      // Another seek away from the peak ends the 30 s window.
      if (pending.current && video.dataset.seekMethod !== 'peak' && Math.abs(video.currentTime * 1000 - pending.current.position) > 2000) settleRef.current(false);
    };
    update();
    video.addEventListener('timeupdate', update);
    video.addEventListener('durationchange', update);
    video.addEventListener('seeking', onSeeking);
    return () => {
      video.removeEventListener('timeupdate', update);
      video.removeEventListener('durationchange', update);
      video.removeEventListener('seeking', onSeeking);
      settleRef.current(false);
    };
  }, [video]);

  const seek = (fraction: number, top3: boolean) => {
    if (!video || !duration) return;
    if (pending.current) settle(false);
    const to = Math.max(0, Math.min(duration - 1, fraction * duration));
    video.dataset.seekMethod = 'peak';
    video.currentTime = to;
    pending.current = {position: Math.round(to * 1000), top3, watched: 0};
  };

  const bucketTime = (i: number) => ((i + 0.5) / curve.length) * duration;
  // The 最多重看 point is not a spoiler marker: it shows whenever the curve does.
  const markers = (kind === 'playback' || showMarkers) && duration > 0 ? peaks : [];
  const markerLabel = (n: number, p: number) =>
    kind === 'playback'
      ? `${t('peak.mostReplayed')}（${formatClock(bucketTime(p))}）`
      : t('danmaku.peakJump', {n: n + 1, time: formatClock(bucketTime(p))});

  return (
    <div className="peak" data-kind={kind}>
      <svg
        className="peak-svg"
        viewBox={`0 0 ${BUCKETS} 16`}
        preserveAspectRatio="none"
        aria-hidden="true"
        onClick={event => {
          const box = event.currentTarget.getBoundingClientRect();
          const fraction = (event.clientX - box.left) / box.width;
          const bucket = Math.floor(fraction * curve.length);
          seek(fraction, peaks.some(p => Math.abs(p - bucket) <= 1));
        }}
      >
        <defs>
          <clipPath id={clipId}>
            <rect x="0" y="0" width={progress * BUCKETS} height="16" />
          </clipPath>
        </defs>
        <path d={path} className="peak-area" />
        <path d={path} className="peak-area-played" clipPath={`url(#${clipId})`} />
      </svg>
      {markers.map((p, n) => (
        <button
          key={p}
          type="button"
          className="peak-marker"
          style={{left: `${((p + 0.5) / curve.length) * 100}%`}}
          aria-label={markerLabel(n, p)}
          title={markerLabel(n, p)}
          onClick={() => seek((p + 0.5) / curve.length, true)}
        >
          <span aria-hidden="true" />
          {kind === 'playback' && (
            <span className="peak-tip" aria-hidden="true">
              {t('peak.mostReplayed')}
            </span>
          )}
        </button>
      ))}
    </div>
  );
}
