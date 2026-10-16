import {Fragment} from 'react';
import {ReleaseRow} from '../components/Cards';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {getWatchHistory, type WatchItem} from '../lib/api';
import {useAuth} from '../lib/auth';
import {dayLabel, formatClock} from '../lib/format';
import {useAsync} from '../lib/useAsync';

export const meta = () => [{title: 'REEL · 观看记录'}];

// NavHistory (grouped by day) on GET /api/me/watch-history.
// TODO(tech plan 3.13): single-item delete, pause history, clear all, 未看完 / 已看完 filter.
export default function History() {
  const {status} = useAuth();
  const history = useAsync(signal => getWatchHistory(signal), [status], status === 'user');

  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="history" title="登录后查看观看记录" body="登录后，看过的视频会出现在这里，换设备也能接着看。" actions={[{to: '/login?next=%2Fhistory', label: '登录'}]} />
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
      <h1 className="page-title">观看记录</h1>
      {history.error ? (
        <ErrorBanner error={history.error} onRetry={history.reload} />
      ) : history.slow ? (
        <CardGridSkeleton count={5} variant="row" />
      ) : history.data && history.data.length === 0 ? (
        <EmptyState icon="history" title="还没有观看记录" body="看过的视频会出现在这里，下次可以从上次的位置接着看。" actions={[{to: '/', label: '去首页逛逛'}]} />
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
                        ? '已看完'
                        : item.duration_seconds > 0
                          ? `看到 ${formatClock(item.progress_seconds)} / ${formatClock(item.duration_seconds)}`
                          : `看到 ${formatClock(item.progress_seconds)}`
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
