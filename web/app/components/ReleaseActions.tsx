import {useEffect, useState} from 'react';
import {track} from '../lib/analytics';
import {useAuth} from '../lib/auth';
import {postErrorMessage} from '../lib/community';
import {formatClock, formatCount} from '../lib/format';
import {useT} from '../lib/i18n';
import {getReaction, setReaction, shareUrl, type ReactionSummary} from '../lib/interactions';
import {useAsync} from '../lib/useAsync';
import {Icon} from './Icon';
import {Sheet} from './Sheet';

// 赞 / 踩 and 分享 on the release page (PRD 5.4 · Video / MobileVideo).
//
// 赞 and 踩 are two buttons; one vote per user (or per device for visitors,
// like ratings); pressing the active one again undoes it. Only the 赞 count
// is public. 分享 opens a sheet with the link, 复制链接, the system share
// sheet (Web Share API) when the browser has one, and 从当前位置开始 (?t=),
// which the player honours when the page opens.

/** 赞 / 踩 buttons. */
export function VoteButtons({slug, releaseId}: {slug: string; releaseId: number}) {
  const t = useT();
  const {status} = useAuth();
  const loaded = useAsync(signal => getReaction(slug, signal), [slug, status]);
  const [state, setState] = useState<ReactionSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => setState(loaded.data ?? null), [loaded.data]);
  if (!state) return null;

  const vote = async (value: 1 | -1) => {
    if (busy) return;
    const next = state.my_vote === value ? 0 : value;
    const before = state;
    // Optimistic: the 赞 count follows the vote at once.
    const likes = before.likes - (before.my_vote === 1 ? 1 : 0) + (next === 1 ? 1 : 0);
    setState({likes, my_vote: next});
    setBusy(true);
    setError('');
    try {
      setState(await setReaction(slug, next));
      track('release_vote', {release_id: releaseId, vote: next === 1 ? 'like' : next === -1 ? 'dislike' : 'none'});
    } catch (cause) {
      setState(before);
      setError(postErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div className="vote-group" role="group" aria-label={t('vote.label')}>
        <button
          type="button"
          className={state.my_vote === 1 ? 'pill-btn vote-btn is-on' : 'pill-btn vote-btn'}
          aria-pressed={state.my_vote === 1}
          aria-label={t(state.my_vote === 1 ? 'vote.unlike' : 'vote.like', {n: state.likes})}
          disabled={busy}
          onClick={() => void vote(1)}
        >
          <Icon name="thumb" size={18} filled={state.my_vote === 1} />
          <span aria-hidden="true">{state.likes > 0 ? formatCount(state.likes) : t('vote.likeShort')}</span>
        </button>
        <button
          type="button"
          className={state.my_vote === -1 ? 'pill-btn vote-btn vote-down is-on' : 'pill-btn vote-btn vote-down'}
          aria-pressed={state.my_vote === -1}
          aria-label={t(state.my_vote === -1 ? 'vote.undislike' : 'vote.dislike')}
          title={t('vote.dislikeHint')}
          disabled={busy}
          onClick={() => void vote(-1)}
        >
          <span className="icon-flip">
            <Icon name="thumb" size={18} filled={state.my_vote === -1} />
          </span>
        </button>
      </div>
      {error && (
        <span className="field-error" role="alert">
          {error}
        </span>
      )}
    </>
  );
}

/** The playing <video>'s position in seconds (0 when nothing plays). */
function playerTime(): number {
  if (typeof document === 'undefined') return 0;
  const video = document.querySelector<HTMLVideoElement>('.player-host video, .video-main video');
  return video && Number.isFinite(video.currentTime) ? Math.floor(video.currentTime) : 0;
}

/** 分享: copy link, system share sheet, optional start time. */
export function ShareButton({title, releaseId}: {title: string; releaseId: number}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [at, setAt] = useState(0);
  const [withTime, setWithTime] = useState(false);
  const [message, setMessage] = useState('');
  const canShare = typeof navigator !== 'undefined' && typeof navigator.share === 'function';
  const url = open ? shareUrl(window.location.href, withTime ? at : 0) : '';

  const openSheet = () => {
    const now = playerTime();
    setAt(now);
    setWithTime(false);
    setMessage('');
    setOpen(true);
  };

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      setMessage(t('release.linkCopied'));
      track('share', {release_id: releaseId, method: 'copy', with_time: withTime && at > 0});
    } catch {
      setMessage(t('share.copyFailed'));
    }
  };

  const native = async () => {
    try {
      await navigator.share({title, url});
      track('share', {release_id: releaseId, method: 'native', with_time: withTime && at > 0});
      setOpen(false);
    } catch {
      /* dismissed */
    }
  };

  return (
    <>
      <button type="button" className="pill-btn" aria-haspopup="dialog" onClick={openSheet}>
        <Icon name="share" size={18} />
        {t('release.share')}
      </button>
      <Sheet open={open} onClose={() => setOpen(false)} label={t('share.title')} header={<h2 className="sheet-title">{t('share.title')}</h2>}>
        <div className="share-sheet">
          <label className="sr-only" htmlFor="share-url">
            {t('share.link')}
          </label>
          <input id="share-url" className="share-url" readOnly value={url} onFocus={e => e.currentTarget.select()} />
          {at > 0 && (
            <label className="review-check">
              <input type="checkbox" checked={withTime} onChange={e => setWithTime(e.target.checked)} />
              <span>{t('share.atTime', {time: formatClock(at)})}</span>
            </label>
          )}
          <div className="share-actions">
            <button type="button" className="btn btn-primary" onClick={() => void copy()}>
              {t('share.copy')}
            </button>
            {canShare && (
              <button type="button" className="btn btn-secondary" onClick={() => void native()}>
                {t('share.more')}
              </button>
            )}
          </div>
          <p className="muted share-status" role="status">
            {message}
          </p>
        </div>
      </Sheet>
    </>
  );
}
