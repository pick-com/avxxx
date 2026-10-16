import {useState} from 'react';
import {ReleaseRow} from '../components/Cards';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {getFavorites, setFavorite} from '../lib/api';
import {useAuth} from '../lib/auth';
import {formatDate} from '../lib/format';
import {useAsync} from '../lib/useAsync';

export const meta = () => [{title: 'REEL · 收藏夹'}];

// NavFavorites on GET/POST /api/me/favorites (a single list).
// TODO(tech plan 3.13 playlists): multiple folders, visibility, sharing.
export default function Favorites() {
  const {status} = useAuth();
  const favorites = useAsync(signal => getFavorites(signal), [status], status === 'user');
  const [removed, setRemoved] = useState<Set<string>>(new Set());
  const [error, setError] = useState('');

  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="favorites" title="登录后查看收藏夹" body="收藏喜欢的作品，换设备也能找到。" actions={[{to: '/login?next=%2Ffavorites', label: '登录'}]} />
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
      setError(cause instanceof Error ? cause.message : '取消收藏失败');
    }
  };

  return (
    <div className="page narrow">
      <h1 className="page-title">收藏夹</h1>
      {error && <ErrorBanner error={error} />}
      {favorites.error ? (
        <ErrorBanner error={favorites.error} onRetry={favorites.reload} />
      ) : favorites.slow ? (
        <CardGridSkeleton count={5} variant="row" />
      ) : favorites.data && list.length === 0 ? (
        <EmptyState icon="favorites" title="收藏夹还是空的" body="在视频页点「收藏」，作品会出现在这里。" actions={[{to: '/', label: '去首页逛逛'}]} />
      ) : (
        <div className="row-list">
          {list.map(item => (
            <ReleaseRow key={item.slug} slug={item.slug} title={item.title} meta={`收藏于 ${formatDate(item.created_at)}`}>
              <button type="button" className="text-btn" onClick={() => remove(item.slug)} aria-label={`取消收藏 ${item.title}`}>
                取消收藏
              </button>
            </ReleaseRow>
          ))}
        </div>
      )}
    </div>
  );
}
