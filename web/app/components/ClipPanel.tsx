import {useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent} from 'react';
import {ApiError} from '../lib/api';
import {CLIP_TITLE_MAX, clipTitleLength, clipWindow, defaultClipRange, moveClipHandle, type ClipRange} from '../lib/clipRange';
import {clipPath, createClip, deleteClip, type Clip} from '../lib/clips';
import {formatClock} from '../lib/format';
import {useT} from '../lib/i18n';
import {useLocalizedPath} from '../lib/nav';
import {useRequireAuth} from './AuthPrompt';
import {Sheet} from './Sheet';

/** 0:12.3 — tenths matter on a 5–60 s clip. */
const clock1 = (s: number) => `${formatClock(Math.floor(s))}.${Math.floor((s % 1) * 10 + 1e-6)}`;

/**
 * 剪辑 (clips; lib/clipRange.ts, lib/clips.ts): pick a 5–60 s segment on a
 * zoomed timeline (two handles; drag or arrow keys, Shift for 5 s), preview
 * it, add an optional title (≤ 100 characters) and share the /clips/{id}
 * link. Opens on the current position + 15 s. Guests get the app's sign-in
 * sheet (AuthPrompt); the API refuses them anyway.
 */
export function ClipPanel({open, onClose, container, video, duration, slug, signedIn}: {
  open: boolean;
  onClose: () => void;
  container?: HTMLElement | null;
  video: HTMLVideoElement | null;
  duration: number;
  slug: string;
  signedIn: boolean;
}) {
  const t = useT();
  const localized = useLocalizedPath();
  const requireAuth = useRequireAuth();
  const [range, setRange] = useState<ClipRange>({start: 0, end: 15});
  const [title, setTitle] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [made, setMade] = useState<{clip: Clip; titleRemoved: boolean} | null>(null);
  const [message, setMessage] = useState('');
  const trackRef = useRef<HTMLDivElement>(null);
  const drag = useRef<{which: 'start' | 'end'; id: number; win: ClipRange} | null>(null);
  const [win, setWin] = useState<ClipRange>({start: 0, end: 180});
  const stopAt = useRef<number | null>(null);

  // Each opening starts from the current position.
  useEffect(() => {
    if (!open) return;
    const r = defaultClipRange(video?.currentTime ?? 0, duration);
    setRange(r);
    setWin(clipWindow(r, duration));
    setTitle('');
    setError('');
    setMade(null);
    setMessage('');
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  // 预览 stops at the end of the range.
  useEffect(() => {
    if (!video) return;
    const onTime = () => {
      if (stopAt.current !== null && video.currentTime >= stopAt.current) {
        stopAt.current = null;
        video.pause();
      }
    };
    video.addEventListener('timeupdate', onTime);
    return () => video.removeEventListener('timeupdate', onTime);
  }, [video]);
  useEffect(() => {
    if (!open) stopAt.current = null;
  }, [open]);

  const span = win.end - win.start || 1;
  const pct = (s: number) => `${Math.min(100, Math.max(0, ((s - win.start) / span) * 100))}%`;
  const move = (which: 'start' | 'end', to: number) => setRange(r => moveClipHandle(r, which, to, duration));

  const timeAt = (clientX: number, w: ClipRange) => {
    const box = trackRef.current?.getBoundingClientRect();
    if (!box || box.width === 0) return null;
    return w.start + Math.min(1, Math.max(0, (clientX - box.left) / box.width)) * (w.end - w.start);
  };
  const onDown = (which: 'start' | 'end') => (event: ReactPointerEvent<HTMLElement>) => {
    if (!event.isPrimary || event.button !== 0) return;
    event.preventDefault();
    event.stopPropagation();
    try {
      event.currentTarget.setPointerCapture(event.pointerId);
    } catch {
      /* pointer gone */
    }
    drag.current = {which, id: event.pointerId, win};
  };
  const onMove = (event: ReactPointerEvent<HTMLElement>) => {
    const d = drag.current;
    if (!d || d.id !== event.pointerId) return;
    const at = timeAt(event.clientX, d.win);
    if (at !== null) move(d.which, at);
  };
  const onUp = (event: ReactPointerEvent<HTMLElement>) => {
    if (drag.current?.id !== event.pointerId) return;
    drag.current = null;
    setRange(r => {
      setWin(clipWindow(r, duration));
      return r;
    });
  };
  // A tap on the track moves the nearer handle there.
  const onTrackDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (!event.isPrimary || event.button !== 0) return;
    const at = timeAt(event.clientX, win);
    if (at === null) return;
    move(Math.abs(at - range.start) <= Math.abs(at - range.end) ? 'start' : 'end', at);
  };
  const onKey = (which: 'start' | 'end') => (event: ReactKeyboardEvent<HTMLElement>) => {
    const step = event.shiftKey ? 5 : 0.5;
    const now = range[which];
    let to: number | null = null;
    if (event.key === 'ArrowLeft' || event.key === 'ArrowDown') to = now - step;
    else if (event.key === 'ArrowRight' || event.key === 'ArrowUp') to = now + step;
    if (to === null) return;
    event.preventDefault();
    const next = moveClipHandle(range, which, to, duration);
    setRange(next);
    setWin(clipWindow(next, duration));
  };

  const preview = () => {
    if (!video) return;
    video.dataset.seekMethod = 'skip';
    video.currentTime = range.start;
    stopAt.current = range.end;
    void video.play().catch(() => undefined);
  };

  const create = async () => {
    setBusy(true);
    setError('');
    try {
      const out = await createClip(slug, range, title);
      setMade({clip: out.clip, titleRemoved: out.title_removed});
    } catch (e) {
      setError(e instanceof ApiError && e.status === 429 ? t('clip.rateLimited') : e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const url = made && typeof window !== 'undefined' ? new URL(localized(clipPath(made.clip.id)), window.location.origin).href : '';
  const canShare = typeof navigator !== 'undefined' && typeof navigator.share === 'function';
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      setMessage(t('release.linkCopied'));
    } catch {
      setMessage(t('share.copyFailed'));
    }
  };
  const native = async () => {
    try {
      await navigator.share({title: made?.clip.title || made?.clip.release_title, url});
    } catch {
      /* dismissed */
    }
  };
  const remove = async () => {
    if (!made) return;
    setBusy(true);
    try {
      await deleteClip(made.clip.id);
      setMade(null);
      setMessage(t('clip.deleted'));
    } catch (e) {
      setMessage(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const length = Math.round((range.end - range.start) * 10) / 10;
  const titleLen = clipTitleLength(title);

  return (
    <Sheet open={open} onClose={onClose} label={t('clip.title')} container={container} className="sheet-clip" header={<h2 className="sheet-title">{t('clip.title')}</h2>}>
      <div className="clip-panel">
        {made ? (
          <div className="share-sheet">
            <p className="clip-done">{t('clip.created', {from: clock1(made.clip.start_ms / 1000), to: clock1(made.clip.end_ms / 1000)})}</p>
            {made.titleRemoved && <p className="field-hint">{t('clip.titleRemoved')}</p>}
            <label className="sr-only" htmlFor="clip-url">
              {t('share.link')}
            </label>
            <input id="clip-url" className="share-url" readOnly value={url} onFocus={e => e.currentTarget.select()} />
            <div className="share-actions">
              <button type="button" className="btn btn-primary" onClick={() => void copy()}>
                {t('share.copy')}
              </button>
              {canShare && (
                <button type="button" className="btn btn-secondary" onClick={() => void native()}>
                  {t('share.more')}
                </button>
              )}
              <button type="button" className="btn btn-outline" disabled={busy} onClick={() => void remove()}>
                {t('clip.delete')}
              </button>
            </div>
          </div>
        ) : (
          <>
            <p className="clip-range" aria-live="polite">
              {t('clip.range', {from: clock1(range.start), to: clock1(range.end), n: length})}
            </p>
            <div className="clip-timeline">
              <div ref={trackRef} className="clip-track" onPointerDown={onTrackDown}>
                <div className="clip-sel" style={{left: pct(range.start), width: `calc(${pct(range.end)} - ${pct(range.start)})`}} />
                {(['start', 'end'] as const).map(which => (
                  <span
                    key={which}
                    className="clip-handle"
                    data-handle={which}
                    role="slider"
                    tabIndex={0}
                    aria-label={which === 'start' ? t('clip.start') : t('clip.end')}
                    aria-valuemin={0}
                    aria-valuemax={Math.round(Number.isFinite(duration) ? duration : range.end)}
                    aria-valuenow={Math.round(range[which])}
                    aria-valuetext={clock1(range[which])}
                    style={{left: pct(range[which])}}
                    onPointerDown={onDown(which)}
                    onPointerMove={onMove}
                    onPointerUp={onUp}
                    onPointerCancel={onUp}
                    onKeyDown={onKey(which)}
                  />
                ))}
              </div>
              <div className="clip-scale" aria-hidden="true">
                <span>{formatClock(win.start)}</span>
                <span>{formatClock(win.end)}</span>
              </div>
            </div>
            <p className="field-hint">{t('clip.hint')}</p>
            <div className="share-actions">
              <button type="button" className="btn btn-secondary" disabled={!video} onClick={preview}>
                {t('clip.preview')}
              </button>
            </div>
            <label className="field clip-title">
              <span>
                {t('clip.titleLabel')} · {t('clip.titleCount', {n: titleLen, max: CLIP_TITLE_MAX})}
              </span>
              <input value={title} maxLength={CLIP_TITLE_MAX * 2} placeholder={t('clip.titlePlaceholder')} onChange={e => setTitle(e.target.value)} />
            </label>
            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}
            <div className="share-actions">
              <button
                type="button"
                className="btn btn-primary"
                disabled={busy || titleLen > CLIP_TITLE_MAX}
                onClick={() => {
                  // Guests: the app's sign-in sheet; the player keeps playing underneath.
                  if (requireAuth('other')) void create();
                }}
              >
                {signedIn ? t('clip.create') : t('clip.loginToCreate')}
              </button>
            </div>
          </>
        )}
        <p className="muted share-status" role="status">
          {message}
        </p>
      </div>
    </Sheet>
  );
}
