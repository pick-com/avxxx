import {Link} from 'react-router';
import {ReleaseRow, Thumb} from '../components/Cards';
import {ReserveButton} from '../components/SocialButtons';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {getReservations, isMissingEndpoint, type ReservationItem} from '../lib/api';
import {useAuth} from '../lib/auth';
import {formatDate, formatSchedule} from '../lib/format';
import {useAsync} from '../lib/useAsync';

export const meta = () => [{title: 'REEL · 我的预约'}];

// GET /api/me/reservations: upcoming first (soonest first), then works that
// have gone live. Upcoming works have no public page yet (GET /api/releases/
// {slug} only serves published works), so they are listed without a link.
export default function Reservations() {
  const {status} = useAuth();
  const list = useAsync(signal => getReservations(signal), [status], status === 'user');

  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="calendar" title="登录后查看预约" body="预约即将上线的作品，上线时提醒你。" actions={[{to: '/login?next=%2Freservations', label: '登录'}]} />
      </div>
    );
  }

  const items = list.data ?? [];
  const upcoming = items.filter(i => i.status === 'upcoming');
  const live = items.filter(i => i.status !== 'upcoming');

  return (
    <div className="page narrow">
      <h1 className="page-title">我的预约</h1>
      {list.error ? (
        isMissingEndpoint(list.error) ? (
          <EmptyState icon="calendar" title="预约功能即将上线" body="上线后，你可以预约演员即将上线的作品。" />
        ) : (
          <ErrorBanner error={list.error} onRetry={list.reload} />
        )
      ) : list.loading ? (
        list.slow && <CardGridSkeleton count={4} variant="row" />
      ) : items.length === 0 ? (
        <EmptyState icon="calendar" title="还没有预约" body="在「关注」里看到即将上线的作品时，点「预约」就会出现在这里。" actions={[{to: '/following', label: '看看关注动态'}]} />
      ) : (
        <>
          {upcoming.length > 0 && (
            <section className="shelf">
              <h2 className="section-title">即将上线 · {upcoming.length}</h2>
              <div className="row-list">
                {upcoming.map(item => (
                  <UpcomingRow key={item.slug} item={item} />
                ))}
              </div>
            </section>
          )}
          {live.length > 0 && (
            <section className="shelf">
              <h2 className="section-title">已上线</h2>
              <div className="row-list">
                {live.map(item => (
                  <ReleaseRow key={item.slug} slug={item.slug} title={item.title} meta={item.release_date ? `${formatDate(item.release_date)} 上线` : '已上线'} />
                ))}
              </div>
            </section>
          )}
        </>
      )}
      <Link to="/me" className="text-btn">
        ← 返回我的
      </Link>
    </div>
  );
}

function UpcomingRow({item}: {item: ReservationItem}) {
  return (
    <div className="release-row">
      <div className="release-row-link">
        <Thumb seed={item.slug} badge="即将上线" className="thumb-row" />
        <span className="release-row-text">
          <span className="card-title">{item.title}</span>
          <span className="card-meta">{formatSchedule(item.scheduled_at, item.release_date)}</span>
        </span>
      </div>
      <ReserveButton slug={item.slug} title={item.title} initial size="sm" />
    </div>
  );
}
