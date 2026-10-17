import {useEffect, useId, useRef, useState, type FormEvent} from 'react';
import {addToPlaylist, createPlaylist, getPlaylists, isMissingEndpoint, removeFromPlaylist, setFavorite, type Playlist} from '../lib/api';
import {useAuth} from '../lib/auth';
import {useT} from '../lib/i18n';
import {playlistName, visibilityIcon, visibilityLabel} from '../lib/playlists';
import {useMediaQuery} from '../lib/useMediaQuery';
import {useRequireAuth} from './AuthPrompt';
import {Icon} from './Icon';
import {Sheet} from './Sheet';
import {useDismiss} from './SocialButtons';
import '../styles/library.css';

// Release page 收藏 control (NavFavorites, PRD 7): opens a picker of the
// user's 收藏夹 with a checkbox per playlist and 新建收藏夹. A popover on
// desktop, a bottom sheet on phones (PRD 9 底部面板优先). The button shows 已收藏
// while the release is in any playlist. On a backend without playlists it
// falls back to the single favorites list (POST /api/me/favorites).

export function FavoritePicker({slug, title}: {slug: string; title: string}) {
  const t = useT();
  const {status} = useAuth();
  const requireAuth = useRequireAuth();
  const mobile = useMediaQuery('(max-width: 767px)');
  const [lists, setLists] = useState<Playlist[] | null>(null);
  const [legacy, setLegacy] = useState<boolean | null>(null); // saved state when playlists are not deployed
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState<number | null>(null);
  const [error, setError] = useState('');
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const panelId = useId();
  useDismiss(open && !mobile, () => setOpen(false), rootRef, triggerRef);

  useEffect(() => {
    setLists(null);
    setLegacy(null);
    if (status !== 'user') return;
    const ctrl = new AbortController();
    getPlaylists(slug, ctrl.signal).then(setLists, cause => {
      if (isMissingEndpoint(cause)) setLegacy(false);
    });
    return () => ctrl.abort();
  }, [slug, status]);

  const saved = legacy ?? lists?.some(p => p.contains) ?? false;

  const onButton = async () => {
    if (!requireAuth('favorite')) return;
    if (legacy !== null) {
      // Older backend: a plain toggle on the single list.
      const next = !legacy;
      setLegacy(next);
      setError('');
      try {
        await setFavorite(slug, next);
      } catch (cause) {
        setLegacy(!next);
        setError(cause instanceof Error ? cause.message : t('release.favoriteFailed'));
      }
      return;
    }
    setOpen(o => !o);
  };

  const toggle = async (p: Playlist) => {
    const next = !p.contains;
    const patch = (on: boolean) => setLists(cur => cur?.map(x => (x.id === p.id ? {...x, contains: on, item_count: Math.max(0, x.item_count + (on ? 1 : -1))} : x)) ?? cur);
    patch(next);
    setBusy(p.id);
    setError('');
    try {
      if (next) await addToPlaylist(p.id, slug);
      else await removeFromPlaylist(p.id, slug);
    } catch (cause) {
      patch(!next);
      setError(cause instanceof Error ? cause.message : t('release.favoriteFailed'));
    } finally {
      setBusy(null);
    }
  };

  const create = async (event: FormEvent) => {
    event.preventDefault();
    const n = name.trim();
    if (!n) return;
    setBusy(-1);
    setError('');
    try {
      const p = await createPlaylist({name: n});
      await addToPlaylist(p.id, slug);
      setLists(cur => [...(cur ?? []), {...p, contains: true, item_count: p.item_count + 1}]);
      setName('');
      setCreating(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('playlists.createFailed'));
    } finally {
      setBusy(null);
    }
  };

  const panel = (
    <>
      {lists === null ? (
        <p className="muted pl-picker-note">{t('common.loadingMore')}</p>
      ) : (
        <div className="pl-picker-list" role="group" aria-label={t('playlists.pickerLabel', {title})}>
          {lists.map(p => (
            <button key={p.id} type="button" role="checkbox" aria-checked={!!p.contains} className="menu-choice" disabled={busy !== null} onClick={() => toggle(p)}>
              <span className={p.contains ? 'pl-check is-on' : 'pl-check'} aria-hidden="true">
                {p.contains && <Icon name="check" size={14} />}
              </span>
              <span className="menu-choice-text">
                <span>{playlistName(p)}</span>
                <span className="menu-choice-hint">{t('playlists.countHint', {n: p.item_count, vis: t(visibilityLabel[p.visibility])})}</span>
              </span>
              <Icon name={visibilityIcon[p.visibility]} size={16} />
            </button>
          ))}
        </div>
      )}
      {creating ? (
        <form className="pl-create" onSubmit={create}>
          <label className="sr-only" htmlFor={`${panelId}-name`}>
            {t('playlists.nameLabel')}
          </label>
          <input
            id={`${panelId}-name`}
            className="pl-input"
            value={name}
            maxLength={60}
            placeholder={t('playlists.namePlaceholder')}
            onChange={event => setName(event.target.value)}
            autoFocus
          />
          <button type="submit" className="btn btn-primary btn-sm" disabled={!name.trim() || busy !== null}>
            {t('playlists.create')}
          </button>
        </form>
      ) : (
        lists !== null && (
          <button type="button" className="menu-choice" onClick={() => setCreating(true)}>
            <Icon name="plus" size={18} />
            <span className="menu-choice-text">{t('playlists.new')}</span>
          </button>
        )
      )}
      {error && (
        <span className="inline-error" role="alert">
          {error}
        </span>
      )}
    </>
  );

  return (
    <div className="pl-picker" ref={rootRef}>
      <button
        ref={triggerRef}
        type="button"
        className={saved ? 'pill-btn is-on' : 'pill-btn'}
        aria-pressed={legacy !== null ? saved : undefined}
        aria-haspopup={legacy === null ? 'dialog' : undefined}
        aria-expanded={legacy === null ? open : undefined}
        aria-controls={open && !mobile ? panelId : undefined}
        onClick={onButton}
      >
        <Icon name="favorites" size={18} filled={saved} />
        {saved ? t('release.favorited') : t('release.favorite')}
      </button>
      {open && !mobile && (
        <div className="popover pl-popover" id={panelId} role="dialog" aria-label={t('playlists.pickerTitle')}>
          <span className="popover-title">{t('playlists.pickerTitle')}</span>
          {panel}
        </div>
      )}
      {mobile && (
        <Sheet open={open} onClose={() => setOpen(false)} label={t('playlists.pickerTitle')} header={<h2 className="sheet-title pl-sheet-title">{t('playlists.pickerTitle')}</h2>}>
          <div className="pl-sheet">{panel}</div>
        </Sheet>
      )}
      {error && !open && (
        <span className="inline-error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}
