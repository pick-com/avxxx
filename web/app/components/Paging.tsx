import {useEffect, useRef} from 'react';
import {useT} from '../lib/i18n';
import type {PagedState} from '../lib/usePaged';

/**
 * Infinite scroll with a visible fallback: when the sentinel scrolls into view
 * the next page loads by itself; the button does the same for keyboard users
 * and browsers without IntersectionObserver. A failed page stops auto-loading
 * until the user retries.
 */
export function LoadMore({state, label}: {state: PagedState<unknown>; label?: string}) {
  const t = useT();
  const ref = useRef<HTMLDivElement>(null);
  const {hasMore, loadingMore, moreError, loadMore} = state;

  useEffect(() => {
    const node = ref.current;
    if (!node || !hasMore || moreError || typeof IntersectionObserver === 'undefined') return;
    const observer = new IntersectionObserver(
      entries => {
        if (entries.some(e => e.isIntersecting)) loadMore();
      },
      {rootMargin: '600px 0px'},
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, [hasMore, moreError, loadMore]);

  if (!hasMore) return null;
  return (
    <div className="load-more" ref={ref}>
      {moreError && (
        <span className="inline-error" role="alert">
          {moreError.message}
        </span>
      )}
      <button type="button" className="btn btn-secondary" onClick={loadMore} disabled={loadingMore} aria-busy={loadingMore}>
        {loadingMore ? t('common.loadingMore') : moreError ? t('common.retry') : (label ?? t('common.loadMore'))}
      </button>
    </div>
  );
}
