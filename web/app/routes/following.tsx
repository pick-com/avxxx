import {useEffect, useMemo, useRef, useState} from 'react';
import {Link} from 'react-router';
import {Avatar, ReleaseRow, Thumb} from '../components/Cards';
import {LoadMore} from '../components/Paging';
import {LaterButton, ReserveButton} from '../components/SocialButtons';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {getFollowingFeed, isMissingEndpoint, type FeedItem} from '../lib/api';
import {useAuth} from '../lib/auth';
import {formatDate, formatSchedule} from '../lib/format';
import {t, useT} from '../lib/i18n';
import {useSocial} from '../lib/social';
import {usePaged} from '../lib/usePaged';

export const meta = () => [{title: t('title.following')}];

const PAGE_SIZE = 20;

// MobileFollowing / desktop following feed: GET /api/me/following/feed
// (published and upcoming works of followed performers, newest first, cursor
// paged). Opening the first page marks every follow as seen on the server,
// which clears the SideNav new-work dots, so follows are refreshed after it.
// Upcoming works get 预约; they have no public page yet, so they are not links.
// The performer strip filters the loaded items client-side (the feed has no
// performer parameter).
type Row = FeedItem & {id: string};

export default function Following() {
  const t = useT();
  const {status} = useAuth();
  const {follows, followsReady, available, refreshFollows} = useSocial();
  const [only, setOnly] = useState<string | null>(null);
  const feed = usePaged<Row>(
    (cursor, signal) => getFollowingFeed(PAGE_SIZE, cursor, signal).then(page => ({...page, items: page.items.map(i => ({...i, id: i.slug}))})),
    [status],
    status === 'user',
  );
  // Snapshot of which performers had new works before the feed cleared the dots.
  const [newBefore, setNewBefore] = useState<Set<string>>(new Set());
  const captured = useRef(false);
  useEffect(() => {
    if (captured.current || !followsReady) return;
    captured.current = true;
    setNewBefore(new Set(follows.filter(f => f.new_count > 0).map(f => f.slug)));
  }, [follows, followsReady]);
  const refreshed = useRef(false);
  useEffect(() => {
    if (!feed.loading && !feed.error && feed.paged !== undefined && !refreshed.current) {
      refreshed.current = true;
      refreshFollows();
    }
  }, [feed.loading, feed.error, feed.paged, refreshFollows]);

  const names = useMemo(() => new Map(follows.map(f => [f.slug, f.name])), [follows]);
  const shown = only ? feed.items.filter(i => i.followed_performers?.includes(only)) : feed.items;

  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState
          icon="following"
          title={t('following.anonTitle')}
          body={t('following.anonBody')}
          actions={[{to: '/login?next=%2Ffollowing', label: t('auth.login')}, {to: '/discover?tab=actors', label: t('common.browseActors'), primary: false}]}
        />
      </div>
    );
  }
  if (available.follows === 'no' || (feed.error && isMissingEndpoint(feed.error))) {
    return (
      <div className="page">
        <EmptyState icon="following" title={t('following.soonTitle')} body={t('following.soonBody')} actions={[{to: '/discover?tab=actors', label: t('common.browseActors')}]} />
      </div>
    );
  }

  return (
    <div className="page following-page">
      <div className="section-head">
        <h1 className="page-title">{t('tabs.following')}</h1>
        <span className="section-spacer" />
        <Link to="/discover?tab=actors" className="text-btn">
          {t('following.discoverMore')}
        </Link>
      </div>

      {follows.length > 0 && (
        <div className="chip-row follow-strip" role="group" aria-label={t('following.filter')}>
          <button type="button" className={only === null ? 'follow-pick is-on' : 'follow-pick'} aria-pressed={only === null} onClick={() => setOnly(null)}>
            <span className="follow-pick-all" aria-hidden="true">
              {t('home.all')}
            </span>
            <span className="follow-pick-name">{t('home.all')}</span>
          </button>
          {follows.map(f => (
            <button
              key={f.slug}
              type="button"
              className={only === f.slug ? 'follow-pick is-on' : 'follow-pick'}
              aria-pressed={only === f.slug}
              aria-label={newBefore.has(f.slug) ? t('following.onlyNew', {name: f.name}) : t('following.only', {name: f.name})}
              onClick={() => setOnly(o => (o === f.slug ? null : f.slug))}
            >
              <span className="follow-pick-avatar">
                <Avatar seed={f.slug} name={f.name} size={52} />
                {newBefore.has(f.slug) && <span className="new-dot" aria-hidden="true" />}
              </span>
              <span className="follow-pick-name">{f.name}</span>
            </button>
          ))}
        </div>
      )}

      {feed.error ? (
        <ErrorBanner error={feed.error} onRetry={feed.reload} />
      ) : feed.loading ? (
        feed.slow && <CardGridSkeleton count={6} variant="row" />
      ) : followsReady && follows.length === 0 ? (
        <EmptyState icon="following" title={t('following.emptyTitle')} body={t('following.emptyBody')} actions={[{to: '/discover?tab=actors', label: t('common.browseActors')}]} />
      ) : shown.length === 0 ? (
        <EmptyState icon="following" title={t('following.noUpdates')} body={t('following.noUpdatesBody')} />
      ) : (
        <>
          <div className="row-list">
            {shown.map(item => (
              <FeedRow key={item.slug} item={item} names={names} />
            ))}
          </div>
          {!only && <LoadMore state={feed} label={t('following.older')} />}
          {only && feed.hasMore && (
            <button type="button" className="btn btn-secondary" onClick={feed.loadMore} disabled={feed.loadingMore}>
              {feed.loadingMore ? t('common.loadingMore') : t('following.loadOlder')}
            </button>
          )}
        </>
      )}
    </div>
  );
}

function FeedRow({item, names}: {item: Row; names: Map<string, string>}) {
  const t = useT();
  const who = (item.followed_performers ?? []).map(s => names.get(s) ?? s).join(t('common.listSep'));
  if (item.status === 'upcoming') {
    return (
      <div className="release-row">
        <div className="release-row-link">
          <Thumb seed={item.slug} badge={t('following.upcoming')} className="thumb-row" />
          <span className="release-row-text">
            <span className="card-meta">{who ? t('following.whoUpcoming', {who}) : t('following.upcoming')}</span>
            <span className="card-title">{item.title}</span>
            <span className="card-meta">{formatSchedule(item.scheduled_at, item.release_date)}</span>
          </span>
        </div>
        <ReserveButton slug={item.slug} title={item.title} initial={item.reserved} size="sm" />
      </div>
    );
  }
  return (
    <ReleaseRow
      slug={item.slug}
      title={item.title}
      meta={who ? t('following.whoNew', {who}) : t('following.newWork')}
      sub={item.release_date ? t('following.released', {date: formatDate(item.release_date)}) : undefined}
    >
      <LaterButton slug={item.slug} title={item.title} variant="icon" />
    </ReleaseRow>
  );
}
