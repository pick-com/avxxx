import {Link} from '../lib/nav';
import {ReleaseRow, Thumb} from '../components/Cards';
import {ReserveButton} from '../components/SocialButtons';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {getReservations, isMissingEndpoint, type ReservationItem} from '../lib/api';
import {useAuth} from '../lib/auth';
import {formatDate, formatSchedule} from '../lib/format';
import {t, useT} from '../lib/i18n';
import {useAsync} from '../lib/useAsync';

export const meta = () => [{title: t('title.reservations')}];

// GET /api/me/reservations: upcoming first (soonest first), then works that
// have gone live. Upcoming works have no public page yet (GET /api/releases/
// {slug} only serves published works), so they are listed without a link.
export default function Reservations() {
  const t = useT();
  const {status} = useAuth();
  const list = useAsync(signal => getReservations(signal), [status], status === 'user');

  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="calendar" title={t('reservations.anonTitle')} body={t('reservations.anonBody')} actions={[{to: '/login?next=%2Freservations', label: t('auth.login')}]} />
      </div>
    );
  }

  const items = list.data ?? [];
  const upcoming = items.filter(i => i.status === 'upcoming');
  const live = items.filter(i => i.status !== 'upcoming');

  return (
    <div className="page narrow">
      <h1 className="page-title">{t('me.reservations')}</h1>
      {list.error ? (
        isMissingEndpoint(list.error) ? (
          <EmptyState icon="calendar" title={t('reservations.soonTitle')} body={t('reservations.soonBody')} />
        ) : (
          <ErrorBanner error={list.error} onRetry={list.reload} />
        )
      ) : list.loading ? (
        list.slow && <CardGridSkeleton count={4} variant="row" />
      ) : items.length === 0 ? (
        <EmptyState icon="calendar" title={t('reservations.emptyTitle')} body={t('reservations.emptyBody')} actions={[{to: '/following', label: t('reservations.seeFollowing')}]} />
      ) : (
        <>
          {upcoming.length > 0 && (
            <section className="shelf">
              <h2 className="section-title">{t('reservations.upcomingCount', {n: upcoming.length})}</h2>
              <div className="row-list">
                {upcoming.map(item => (
                  <UpcomingRow key={item.slug} item={item} />
                ))}
              </div>
            </section>
          )}
          {live.length > 0 && (
            <section className="shelf">
              <h2 className="section-title">{t('reservations.live')}</h2>
              <div className="row-list">
                {live.map(item => (
                  <ReleaseRow key={item.slug} slug={item.slug} title={item.title} meta={item.release_date ? t('reservations.liveOn', {date: formatDate(item.release_date)}) : t('reservations.live')} />
                ))}
              </div>
            </section>
          )}
        </>
      )}
      <Link to="/me" className="text-btn">
        {t('reservations.backToMe')}
      </Link>
    </div>
  );
}

function UpcomingRow({item}: {item: ReservationItem}) {
  const t = useT();
  return (
    <div className="release-row">
      <div className="release-row-link">
        <Thumb seed={item.slug} badge={t('following.upcoming')} className="thumb-row" />
        <span className="release-row-text">
          <span className="card-title">{item.title}</span>
          <span className="card-meta">{formatSchedule(item.scheduled_at, item.release_date)}</span>
        </span>
      </div>
      <ReserveButton slug={item.slug} title={item.title} initial size="sm" />
    </div>
  );
}
