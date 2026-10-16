import {useEffect, useState, type FormEvent} from 'react';
import {useSearchParams} from 'react-router';
import {ReleaseRow, Thumb} from '../components/Cards';
import {Icon} from '../components/Icon';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {
  createPlaylist,
  deletePlaylist,
  getPlaylist,
  getPlaylists,
  PLAYLIST_VISIBILITIES,
  removeFromPlaylist,
  rotatePlaylistToken,
  updatePlaylist,
  type Playlist,
  type PlaylistItem,
  type PlaylistVisibility,
} from '../lib/api';
import {useAuth} from '../lib/auth';
import {formatDate} from '../lib/format';
import {t, useT} from '../lib/i18n';
import {playlistName, shareUrl, visibilityHint, visibilityIcon, visibilityLabel} from '../lib/playlists';
import {useAsync} from '../lib/useAsync';
import '../styles/library.css';

export const meta = () => [{title: t('title.favorites')}];

// NavFavorites: the user's 收藏夹 (GET /api/me/playlists) as a grid, the
// selected one (?id=, default playlist otherwise) below with its videos.
// Visibility 私密 / 链接可见 / 公开; non-private playlists get a share link
// (/playlists/{share_token}) that can be reset, which revokes the old one.
// The default playlist backs the release page's quick 收藏 and cannot be
// deleted.
export default function Favorites() {
  const t = useT();
  const {status} = useAuth();
  const [params, setParams] = useSearchParams();
  const lists = useAsync(signal => getPlaylists(undefined, signal), [status], status === 'user');
  const [playlists, setPlaylists] = useState<Playlist[]>([]);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    if (lists.data) setPlaylists(lists.data);
  }, [lists.data]);

  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="favorites" title={t('favorites.anonTitle')} body={t('favorites.anonBody')} actions={[{to: '/login?next=%2Ffavorites', label: t('auth.login')}]} />
      </div>
    );
  }

  const wanted = Number(params.get('id'));
  const selected = playlists.find(p => p.id === wanted) ?? playlists.find(p => p.is_default) ?? playlists[0];
  const select = (id: number) =>
    setParams(
      prev => {
        const next = new URLSearchParams(prev);
        next.set('id', String(id));
        return next;
      },
      {replace: true, preventScrollReset: true},
    );
  const patchList = (p: Playlist) => setPlaylists(cur => cur.map(x => (x.id === p.id ? {...x, ...p} : x)));

  const create = async (event: FormEvent) => {
    event.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setError('');
    try {
      const p = await createPlaylist({name: name.trim()});
      setPlaylists(cur => [...cur, p]);
      setName('');
      setCreating(false);
      select(p.id);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('playlists.createFailed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="page narrow lib-page">
      <div className="later-head">
        <h1 className="page-title">{t('nav.favorites')}</h1>
        {!creating && playlists.length > 0 && (
          <button type="button" className="btn btn-secondary" onClick={() => setCreating(true)}>
            <Icon name="plus" size={16} />
            {t('playlists.new')}
          </button>
        )}
      </div>
      {creating && (
        <form className="pl-create" onSubmit={create}>
          <label className="sr-only" htmlFor="pl-new-name">
            {t('playlists.nameLabel')}
          </label>
          <input id="pl-new-name" className="pl-input" value={name} maxLength={60} placeholder={t('playlists.namePlaceholder')} onChange={e => setName(e.target.value)} autoFocus />
          <button type="submit" className="btn btn-primary" disabled={busy || !name.trim()}>
            {t('playlists.create')}
          </button>
          <button type="button" className="btn btn-secondary" onClick={() => setCreating(false)}>
            {t('playlists.cancel')}
          </button>
        </form>
      )}
      {error && <ErrorBanner error={error} />}
      {lists.error ? (
        <ErrorBanner error={lists.error} onRetry={lists.reload} />
      ) : lists.loading ? (
        lists.slow && <CardGridSkeleton count={4} />
      ) : (
        <>
          <ul className="pl-grid" aria-label={t('playlists.listLabel')}>
            {playlists.map(p => (
              <li key={p.id}>
                <button
                  type="button"
                  className={p.id === selected?.id ? 'pl-card is-on' : 'pl-card'}
                  aria-current={p.id === selected?.id ? 'true' : undefined}
                  onClick={() => select(p.id)}
                >
                  <span className="pl-mosaic" aria-hidden="true">
                    {[0, 1, 2, 3].map(i => (p.cover_slugs[i] ? <Thumb key={i} seed={p.cover_slugs[i]} /> : <span key={i} className="pl-mosaic-empty" />))}
                  </span>
                  <span className="card-title">{playlistName(p)}</span>
                  <span className="card-meta pl-card-meta">
                    <Icon name={visibilityIcon[p.visibility]} size={14} />
                    {t('playlists.countHint', {n: p.item_count, vis: t(visibilityLabel[p.visibility])})}
                  </span>
                </button>
              </li>
            ))}
          </ul>
          {selected && (
            <PlaylistPanel
              key={selected.id}
              playlist={selected}
              onChange={patchList}
              onDeleted={() => {
                setPlaylists(cur => cur.filter(x => x.id !== selected.id));
                setParams(new URLSearchParams(), {replace: true});
              }}
            />
          )}
        </>
      )}
    </div>
  );
}

function PlaylistPanel({playlist, onChange, onDeleted}: {playlist: Playlist; onChange: (p: Playlist) => void; onDeleted: () => void}) {
  const t = useT();
  const detail = useAsync(signal => getPlaylist(playlist.id, signal), [playlist.id]);
  const [items, setItems] = useState<PlaylistItem[]>([]);
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(playlist.name);
  const [description, setDescription] = useState(playlist.description);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [copied, setCopied] = useState('');

  useEffect(() => {
    if (detail.data) setItems(detail.data.items);
  }, [detail.data]);

  const run = async (fn: () => Promise<void>, failed: string) => {
    setBusy(true);
    setError('');
    try {
      await fn();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : failed);
    } finally {
      setBusy(false);
    }
  };

  const setVisibility = (v: PlaylistVisibility) => {
    if (v === playlist.visibility) return;
    run(async () => onChange(await updatePlaylist(playlist.id, {visibility: v})), t('playlists.saveFailed'));
  };

  const saveDetails = (event: FormEvent) => {
    event.preventDefault();
    run(async () => {
      onChange(await updatePlaylist(playlist.id, {name: name.trim(), description: description.trim()}));
      setEditing(false);
    }, t('playlists.saveFailed'));
  };

  const copyLink = async () => {
    if (!playlist.share_token) return;
    try {
      await navigator.clipboard.writeText(shareUrl(playlist.share_token));
      setCopied(t('playlists.linkCopied'));
    } catch {
      setCopied('');
    }
    window.setTimeout(() => setCopied(''), 2000);
  };

  const remove = async (item: PlaylistItem) => {
    const before = items;
    setItems(before.filter(i => i.slug !== item.slug));
    setError('');
    try {
      await removeFromPlaylist(playlist.id, item.slug);
      onChange({...playlist, item_count: Math.max(0, playlist.item_count - 1), cover_slugs: playlist.cover_slugs.filter(s => s !== item.slug)});
    } catch (cause) {
      setItems(before);
      setError(cause instanceof Error ? cause.message : t('favorites.removeFailed'));
    }
  };

  return (
    <section className="pl-panel" aria-labelledby="pl-panel-title">
      {editing ? (
        <form className="pl-edit" onSubmit={saveDetails}>
          <label className="field">
            {t('playlists.nameLabel')}
            <input value={name} maxLength={60} onChange={e => setName(e.target.value)} autoFocus />
          </label>
          <label className="field">
            {t('playlists.descriptionLabel')}
            <textarea className="pl-textarea" value={description} maxLength={300} rows={2} onChange={e => setDescription(e.target.value)} />
          </label>
          <div className="action-row">
            <button type="submit" className="btn btn-primary" disabled={busy || !name.trim()}>
              {t('playlists.save')}
            </button>
            <button type="button" className="btn btn-secondary" onClick={() => setEditing(false)}>
              {t('playlists.cancel')}
            </button>
          </div>
        </form>
      ) : (
        <div className="pl-panel-head">
          <div>
            <h2 className="section-title" id="pl-panel-title">
              {playlistName(playlist)}
            </h2>
            {playlist.description && <p className="muted pl-desc">{playlist.description}</p>}
          </div>
          <div className="action-row">
            <button type="button" className="text-btn" onClick={() => setEditing(true)}>
              {t('playlists.edit')}
            </button>
            {!playlist.is_default && !confirmDelete && (
              <button type="button" className="text-btn btn-danger" onClick={() => setConfirmDelete(true)}>
                {t('playlists.delete')}
              </button>
            )}
          </div>
        </div>
      )}
      {confirmDelete && (
        <div className="confirm-row" role="group" aria-label={t('playlists.confirmDeleteLabel')}>
          <span>{t('playlists.confirmDelete', {name: playlistName(playlist)})}</span>
          <button type="button" className="btn btn-secondary" onClick={() => setConfirmDelete(false)} disabled={busy}>
            {t('playlists.cancel')}
          </button>
          <button
            type="button"
            className="btn btn-primary btn-danger-solid"
            disabled={busy}
            onClick={() =>
              run(async () => {
                await deletePlaylist(playlist.id);
                onDeleted();
              }, t('playlists.deleteFailed'))
            }
          >
            {t('playlists.delete')}
          </button>
        </div>
      )}

      <div className="segmented segmented-sm pl-vis" role="radiogroup" aria-label={t('playlists.visibility')}>
        {PLAYLIST_VISIBILITIES.map(v => (
          <button key={v} type="button" role="radio" aria-checked={playlist.visibility === v} className="segmented-item" disabled={busy} onClick={() => setVisibility(v)}>
            <Icon name={visibilityIcon[v]} size={14} />
            {t(visibilityLabel[v])}
          </button>
        ))}
      </div>
      <p className="muted pl-vis-hint">{t(visibilityHint[playlist.visibility])}</p>
      {playlist.visibility !== 'private' && playlist.share_token && (
        <div className="pl-share">
          <label className="sr-only" htmlFor="pl-share-url">
            {t('playlists.shareLink')}
          </label>
          <input id="pl-share-url" className="pl-input" readOnly value={shareUrl(playlist.share_token)} onFocus={e => e.currentTarget.select()} />
          <button type="button" className="btn btn-secondary" onClick={copyLink}>
            <Icon name="share" size={16} />
            {t('playlists.copyLink')}
          </button>
          <button type="button" className="text-btn" disabled={busy} onClick={() => run(async () => onChange(await rotatePlaylistToken(playlist.id)), t('playlists.saveFailed'))}>
            {t('playlists.resetLink')}
          </button>
          <span className="sr-status" role="status">
            {copied}
          </span>
        </div>
      )}
      {error && <ErrorBanner error={error} />}

      {detail.error ? (
        <ErrorBanner error={detail.error} onRetry={detail.reload} />
      ) : detail.loading ? (
        detail.slow && <CardGridSkeleton count={4} variant="row" />
      ) : items.length === 0 ? (
        <EmptyState icon="favorites" title={t('favorites.emptyTitle')} body={t('favorites.emptyBody')} actions={[{to: '/', label: t('common.goHome')}]} />
      ) : (
        <div className="row-list">
          {items.map(item => (
            <ReleaseRow key={item.slug} slug={item.slug} title={item.title} meta={t('favorites.savedAt', {date: formatDate(item.added_at)})}>
              <button type="button" className="text-btn" onClick={() => remove(item)} aria-label={t('favorites.removeLabel', {title: item.title})}>
                {t('favorites.remove')}
              </button>
            </ReleaseRow>
          ))}
        </div>
      )}
    </section>
  );
}
