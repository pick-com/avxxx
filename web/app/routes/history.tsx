import {Fragment} from 'react';
import {ReleaseRow} from '../components/Cards';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {getWatchHistory, type WatchItem} from '../lib/api';
import {useAuth} from '../lib/auth';
import {dayLabel, formatClock} from '../lib/format';
import {t, useT} from '../lib/i18n';
import {useAsync} from '../lib/useAsync';

export const meta = () => [{title: t('title.history')}];

// NavHistory (grouped by day) on GET /api/me/watch-history.
// TODO(tech plan 3.13): single-item delete, pause history, clear all, 未看完 / 已看完 filter.
export default function History() {
  const t = useT();
  const {status} = useAuth();
  const history = useAsync(signal => getWatchHistory(signal), [status], status === 'user');

  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="history" title={t('history.anonTitle')} body={t('history.anonBody')} actions={[{to: '/login?next=%2Fhistory', label: t('auth.login')}]} />
      </div>
    );
  }

  const groups: [string, WatchItem[]][] = [];
  for (const item of history.data ?? []) {
    const label = dayLabel(item.watched_at);
    const last = groups[groups.length - 1];
    if (last && last[0] === label) last[1].push(item);
    else groups.push([label, [item]]);
  }

  return (
    <div className="page narrow">
      <h1 className="page-title">{t('nav.history')}</h1>
      {history.error ? (
        <ErrorBanner error={history.error} onRetry={history.reload} />
      ) : history.slow ? (
        <CardGridSkeleton count={5} variant="row" />
      ) : history.data && history.data.length === 0 ? (
        <EmptyState icon="history" title={t('history.emptyTitle')} body={t('history.emptyBody')} actions={[{to: '/', label: t('common.goHome')}]} />
      ) : (
        groups.map(([label, list]) => (
          <Fragment key={label}>
            <h2 className="group-label">{label}</h2>
            <div className="row-list">
              {list.map(item => {
                const done = item.duration_seconds > 0 && item.progress_seconds >= item.duration_seconds - 10;
                return (
                  <ReleaseRow
                    key={item.slug}
                    slug={item.slug}
                    title={item.title}
                    progress={item.duration_seconds > 0 ? item.progress_seconds / item.duration_seconds : undefined}
                    meta={
                      done
                        ? t('home.finished')
                        : item.duration_seconds > 0
                          ? t('history.watchedOf', {pos: formatClock(item.progress_seconds), total: formatClock(item.duration_seconds)})
                          : t('history.watched', {pos: formatClock(item.progress_seconds)})
                    }
                  />
                );
              })}
            </div>
          </Fragment>
        ))
      )}
    </div>
  );
}
