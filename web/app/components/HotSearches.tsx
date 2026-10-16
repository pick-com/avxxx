import {Link} from 'react-router';
import {useT} from '../lib/i18n';
import {getHotSearches} from '../lib/search';
import {useAsync} from '../lib/useAsync';
import {SectionHeader} from './Cards';

/**
 * 大家都在搜 (MobileSearchTyping idle state) and 热搜词 (NavTrending right
 * column) from GET /api/search/hot. Renders nothing while loading, when the
 * board is empty or when the backend has no hot-search route.
 */
export function HotSearches({variant, limit = 10, onPick}: {variant: 'list' | 'side'; limit?: number; onPick?: (q: string) => void}) {
  const t = useT();
  const board = useAsync(signal => getHotSearches(limit, signal), [limit]);
  const items = board.data?.items ?? [];
  if (!items.length) return null;

  const list = (
    <ol className={`hot-list hot-list-${variant}`}>
      {items.map(h => (
        <li key={h.q}>
          <Link to={`/search?q=${encodeURIComponent(h.q)}`} className="hot-row" onClick={() => onPick?.(h.q)}>
            <span className={h.rank <= 3 ? 'rank-num rank-top' : 'rank-num'} aria-label={t('hot.rank', {n: h.rank})}>
              {h.rank}
            </span>
            <span className="hot-q">{h.q}</span>
          </Link>
        </li>
      ))}
    </ol>
  );

  if (variant === 'side') {
    return (
      <div className="side-card hot-card">
        <SectionHeader title={t('hot.trendingTitle')} sub={t('hot.updated')} />
        {list}
      </div>
    );
  }
  return (
    <section className="shelf hot-shelf" aria-label={t('hot.title')}>
      <SectionHeader title={t('hot.title')} />
      {list}
    </section>
  );
}
