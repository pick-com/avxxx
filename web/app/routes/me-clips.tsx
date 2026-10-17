import {useEffect, useState} from 'react';
import {Thumb} from '../components/Cards';
import {Icon} from '../components/Icon';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {useAuth} from '../lib/auth';
import {clipPath, deleteClip, getMyClips, type MyClip} from '../lib/clips';
import {formatClock, formatDate} from '../lib/format';
import {t, useT} from '../lib/i18n';
import {Link, useLocalizedPath} from '../lib/nav';
import {useAsync} from '../lib/useAsync';
import '../styles/library.css';

export const meta = () => [{title: t('title.meClips')}];

// 我的片段 on GET /api/me/clips (internal/api/clips.go): the signed-in
// user's clips, newest first, by cursor. Each row opens the clip page, copies
// its share link or deletes it (DELETE /api/me/clips/{id}; the shared link
// stops working). Signed-out visitors are asked to log in, like the other
// /me pages.

const span = (c: MyClip) => `${formatClock(Math.floor(c.start_ms / 1000))} – ${formatClock(Math.ceil(c.end_ms / 1000))}`;
const clipName = (c: MyClip) => c.title || t('clip.untitled', {title: c.release_title});

export default function MeClips() {
  const t = useT();
  const {status} = useAuth();
  const localized = useLocalizedPath();
  const first = useAsync(signal => getMyClips(null, signal), [status], status === 'user');
  const [items, setItems] = useState<MyClip[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [more, setMore] = useState(false);
  const [confirming, setConfirming] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');

  useEffect(() => {
    if (first.data) {
      setItems(first.data.items);
      setNext(first.data.nextCursor);
    }
  }, [first.data]);

  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="film" title={t('clip.mineAnonTitle')} body={t('clip.mineAnonBody')} actions={[{to: '/login?next=%2Fme%2Fclips', label: t('auth.login')}]} />
      </div>
    );
  }

  const loadMore = async () => {
    if (!next) return;
    setMore(true);
    setError('');
    try {
      const page = await getMyClips(next);
      setItems(cur => [...cur, ...page.items.filter(x => !cur.some(c => c.id === x.id))]);
      setNext(page.nextCursor);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setMore(false);
    }
  };

  const copy = async (c: MyClip) => {
    try {
      await navigator.clipboard.writeText(new URL(localized(clipPath(c.id)), window.location.origin).href);
      setMessage(t('release.linkCopied'));
    } catch {
      setMessage(t('share.copyFailed'));
    }
  };

  const remove = async (c: MyClip) => {
    setBusy(c.id);
    setError('');
    try {
      await deleteClip(c.id);
      setItems(cur => cur.filter(x => x.id !== c.id));
      setConfirming(null);
      setMessage(t('clip.deleted'));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(null);
    }
  };

  const loading = !first.data && !first.error;
  return (
    <div className="page narrow me-clips-page">
      <h1 className="page-title">{t('clip.mineTitle')}</h1>
      {first.error ? (
        <ErrorBanner error={first.error} onRetry={first.reload} />
      ) : loading ? (
        (status === 'unknown' || first.slow) && <CardGridSkeleton count={4} variant="row" />
      ) : items.length === 0 ? (
        <EmptyState icon="film" title={t('clip.mineEmptyTitle')} body={t('clip.mineEmptyBody')} actions={[{to: '/discover', label: t('clip.mineBrowse')}]} />
      ) : (
        <>
          <div className="row-list">
            {items.map(c => (
              <div key={c.id} className="release-row">
                <Link to={clipPath(c.id)} className="release-row-link">
                  <Thumb seed={c.release_slug} badge={span(c)} className="thumb-row" image={c.release_cover} sizes="168px" />
                  <span className="release-row-text">
                    <span className="card-title">{clipName(c)}</span>
                    <span className="card-meta">{c.release_title}</span>
                    <span className="card-meta">{t('clip.mineMeta', {date: formatDate(c.created_at), n: Math.round((c.end_ms - c.start_ms) / 1000)})}</span>
                  </span>
                </Link>
                {confirming === c.id ? (
                  <span className="confirm-row" role="group" aria-label={t('clip.deleteLabel', {title: clipName(c)})}>
                    <button type="button" className="btn btn-secondary btn-sm" onClick={() => setConfirming(null)} disabled={busy === c.id}>
                      {t('playlists.cancel')}
                    </button>
                    <button type="button" className="btn btn-primary btn-danger-solid btn-sm" disabled={busy === c.id} onClick={() => void remove(c)}>
                      {t('clip.confirmDelete')}
                    </button>
                  </span>
                ) : (
                  <span className="action-row">
                    <button type="button" className="icon-btn" aria-label={t('clip.copyLabel', {title: clipName(c)})} title={t('share.copy')} onClick={() => void copy(c)}>
                      <Icon name="share" size={18} />
                    </button>
                    <button type="button" className="icon-btn" aria-label={t('clip.deleteLabel', {title: clipName(c)})} title={t('clip.delete')} onClick={() => setConfirming(c.id)}>
                      <Icon name="trash" size={18} />
                    </button>
                  </span>
                )}
              </div>
            ))}
          </div>
          {next && (
            <button type="button" className="btn btn-secondary btn-block" disabled={more} onClick={() => void loadMore()}>
              {more ? t('common.loadingMore') : t('common.loadMore')}
            </button>
          )}
        </>
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <p className="muted" role="status">
        {message}
      </p>
    </div>
  );
}
