import {useState} from 'react';
import {ReleaseRow} from '../components/Cards';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {getFavorites, setFavorite} from '../lib/api';
import {useAuth} from '../lib/auth';
import {formatDate} from '../lib/format';
import {t, useT} from '../lib/i18n';
import {useAsync} from '../lib/useAsync';

export const meta = () => [{title: t('title.favorites')}];

// NavFavorites on GET/POST /api/me/favorites (a single list).
// TODO(tech plan 3.13 playlists): multiple folders, visibility, sharing.
export default function Favorites() {
  const t = useT();
  const {status} = useAuth();
  const favorites = useAsync(signal => getFavorites(signal), [status], status === 'user');
  const [removed, setRemoved] = useState<Set<string>>(new Set());
  const [error, setError] = useState('');

  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="favorites" title={t('favorites.anonTitle')} body={t('favorites.anonBody')} actions={[{to: '/login?next=%2Ffavorites', label: t('auth.login')}]} />
      </div>
    );
  }

  const list = (favorites.data ?? []).filter(f => !removed.has(f.slug));

  const remove = async (slug: string) => {
    setError('');
    setRemoved(prev => new Set(prev).add(slug));
    try {
      await setFavorite(slug, false);
    } catch (cause) {
      setRemoved(prev => {
        const next = new Set(prev);
        next.delete(slug);
        return next;
      });
      setError(cause instanceof Error ? cause.message : t('favorites.removeFailed'));
    }
  };

  return (
    <div className="page narrow">
      <h1 className="page-title">{t('nav.favorites')}</h1>
      {error && <ErrorBanner error={error} />}
      {favorites.error ? (
        <ErrorBanner error={favorites.error} onRetry={favorites.reload} />
      ) : favorites.slow ? (
        <CardGridSkeleton count={5} variant="row" />
      ) : favorites.data && list.length === 0 ? (
        <EmptyState icon="favorites" title={t('favorites.emptyTitle')} body={t('favorites.emptyBody')} actions={[{to: '/', label: t('common.goHome')}]} />
      ) : (
        <div className="row-list">
          {list.map(item => (
            <ReleaseRow key={item.slug} slug={item.slug} title={item.title} meta={t('favorites.savedAt', {date: formatDate(item.created_at)})}>
              <button type="button" className="text-btn" onClick={() => remove(item.slug)} aria-label={t('favorites.removeLabel', {title: item.title})}>
                {t('favorites.remove')}
              </button>
            </ReleaseRow>
          ))}
        </div>
      )}
    </div>
  );
}
