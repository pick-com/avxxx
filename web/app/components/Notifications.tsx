import {useCallback, useEffect, useId, useRef, useState} from 'react';
import {Link, useLocation} from '../lib/nav';
import {getNotifications, getUnreadCount, isAbort, isMissingEndpoint, markNotificationsRead, type NotificationItem, type NotificationRelease} from '../lib/api';
import {useAuth} from '../lib/auth';
import {timeAgo} from '../lib/format';
import {t, useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {usePaged} from '../lib/usePaged';
import {Avatar} from './Cards';
import {Icon} from './Icon';
import {LoadMore} from './Paging';
import {useDismiss} from './SocialButtons';
import {CardGridSkeleton, ErrorBanner} from './States';

// Notifications panel (TopBar bell) and the /notifications page.
//
// The new-work fan-out writes at most one in-app notification per user per
// local day (kind new_work, merged_key); payload.items lists up to 20 releases
// with the reason (follow / reservation) and payload.count keeps the total. A
// merged notice is one row with its releases listed inside.
//
// Tabs map to the four PRD 7 kinds: 新作 = follow items; 上线 / 更新 =
// reservation items and favorite_update; 回复 = comment_reply. Only new_work
// is produced by the backend today; the other kinds render generically.

const CHANGED = 'reel:notifications-changed';
export const notifyNotificationsChanged = () => window.dispatchEvent(new Event(CHANGED));

type Filter = 'all' | 'new' | 'live' | 'reply';
const FILTERS: [Filter, MessageKey][] = [
  ['all', 'notif.filterAll'],
  ['new', 'notif.filterNew'],
  ['live', 'notif.filterLive'],
  ['reply', 'notif.filterReply'],
];
const PAGE_SIZE = 20;
const POLL_MS = 60_000;

function releasesOf(n: NotificationItem): NotificationRelease[] {
  return (n.payload?.items ?? []).filter(i => i && typeof i.slug === 'string');
}

function categories(n: NotificationItem): Set<Filter> {
  const out = new Set<Filter>();
  if (n.kind === 'new_work') {
    const items = releasesOf(n);
    if (items.length === 0) out.add('new');
    for (const i of items) out.add(i.reason === 'reservation' ? 'live' : 'new');
  } else if (n.kind === 'reservation' || n.kind === 'favorite_update') out.add('live');
  else if (n.kind === 'comment_reply') out.add('reply');
  return out;
}

const names = (i: NotificationRelease) => (i.performers ?? []).map(p => p.name).filter(Boolean).join(t('common.listSep'));

function headline(n: NotificationItem): {kind: string; text: string} {
  const items = releasesOf(n);
  const count = Math.max(n.payload?.count ?? items.length, items.length);
  if (n.kind === 'new_work') {
    const reserved = items.filter(i => i.reason === 'reservation').length;
    const followed = items.length - reserved;
    if (count <= 1 && items[0]) {
      const i = items[0];
      return i.reason === 'reservation'
        ? {kind: t('notif.kindReservation'), text: t('notif.reservedLive', {title: i.title})}
        : {kind: t('notif.kindFollow'), text: t('notif.followedNew', {names: names(i) || t('notif.followedFallback'), title: i.title})};
    }
    if (reserved > 0 && followed === 0) return {kind: t('notif.kindReservation'), text: t('notif.reservedCount', {count})};
    if (reserved > 0) return {kind: t('notif.kindMixed'), text: t('notif.mixedCount', {count})};
    return {kind: t('notif.kindFollow'), text: t('notif.followedCount', {count})};
  }
  if (n.kind === 'comment_reply') return {kind: t('notif.kindReply'), text: t('notif.replyText')};
  if (n.kind === 'favorite_update') return {kind: t('notif.kindFavorite'), text: items[0] ? t('notif.favoriteUpdated', {title: items[0].title}) : t('notif.favoriteUpdatedGeneric')};
  if (n.kind === 'reservation') return {kind: t('notif.kindReservation'), text: items[0] ? t('notif.reservedSoon', {title: items[0].title}) : t('notif.reservedSoonGeneric')};
  return {kind: t('notif.kindGeneric'), text: t('notif.generic')};
}

/** Unread count for the bell; null when signed out or the endpoint is missing. */
function useUnread(enabled: boolean) {
  const [count, setCount] = useState<number | null>(null);
  const [missing, setMissing] = useState(false);
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    if (!enabled || missing) {
      setCount(null);
      return;
    }
    const controller = new AbortController();
    getUnreadCount(controller.signal).then(setCount, cause => {
      if (isAbort(cause) || controller.signal.aborted) return;
      if (isMissingEndpoint(cause)) setMissing(true);
    });
    return () => controller.abort();
  }, [enabled, missing, nonce]);

  useEffect(() => {
    if (!enabled || missing) return;
    const bump = () => setNonce(n => n + 1);
    const onVisible = () => {
      if (document.visibilityState === 'visible') bump();
    };
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') bump();
    }, POLL_MS);
    window.addEventListener(CHANGED, bump);
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener(CHANGED, bump);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [enabled, missing]);

  return {count, missing, refresh: useCallback(() => setNonce(n => n + 1), [])};
}

/** TopBar bell with unread badge; opens the Notifications panel. */
export function NotificationBell() {
  const t = useT();
  const {status} = useAuth();
  const location = useLocation();
  const unread = useUnread(status === 'user');
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const panelId = useId();
  useDismiss(open, () => setOpen(false), rootRef, triggerRef);

  useEffect(() => setOpen(false), [location.pathname]);

  if (status !== 'user' || unread.missing) return null;
  const n = unread.count ?? 0;
  const label = n > 0 ? t('notif.bellUnread', {n}) : t('notif.title');
  return (
    <div className="notif-anchor" ref={rootRef}>
      <button
        ref={triggerRef}
        type="button"
        className="icon-btn notif-bell"
        aria-label={label}
        title={label}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        onClick={() => setOpen(o => !o)}
      >
        <Icon name="bell" />
        {n > 0 && (
          <span className="notif-badge" aria-hidden="true">
            {n > 99 ? '99+' : n}
          </span>
        )}
      </button>
      {open && (
        <div className="popover notif-panel" id={panelId} role="dialog" aria-label={t('notif.title')}>
          <NotificationList compact onChanged={unread.refresh} onNavigate={() => setOpen(false)} />
        </div>
      )}
    </div>
  );
}

/** Tabs, list, 全部标为已读. `compact` is the TopBar panel; the page passes false. */
export function NotificationList({compact = false, onChanged, onNavigate}: {compact?: boolean; onChanged?: () => void; onNavigate?: () => void}) {
  const t = useT();
  const [filter, setFilter] = useState<Filter>('all');
  const [readIds, setReadIds] = useState<Set<number>>(new Set());
  const [allRead, setAllRead] = useState(false);
  const [error, setError] = useState('');
  const list = usePaged((cursor, signal) => getNotifications(PAGE_SIZE, cursor, signal), []);

  const isRead = (n: NotificationItem) => allRead || n.read || readIds.has(n.id);
  const shown = list.items.filter(n => filter === 'all' || categories(n).has(filter));
  const anyUnread = list.items.some(n => !isRead(n));

  const changed = () => {
    onChanged?.();
    notifyNotificationsChanged();
  };

  const markAll = async () => {
    setError('');
    setAllRead(true);
    try {
      await markNotificationsRead('all');
      changed();
    } catch (cause) {
      setAllRead(false);
      setError(cause instanceof Error ? cause.message : t('notif.failed'));
    }
  };

  const markOne = (n: NotificationItem) => {
    if (isRead(n)) return;
    setReadIds(prev => new Set(prev).add(n.id));
    markNotificationsRead([n.id]).then(changed, () =>
      setReadIds(prev => {
        const next = new Set(prev);
        next.delete(n.id);
        return next;
      }),
    );
  };

  return (
    <div className={compact ? 'notif notif-compact' : 'notif'}>
      <div className="notif-head">
        {compact ? <h2 className="notif-title">{t('notif.title')}</h2> : <span className="section-spacer" />}
        <span className="section-spacer" />
        <button type="button" className="text-btn" onClick={markAll} disabled={!anyUnread}>
          {t('notif.markAll')}
        </button>
      </div>
      <div className="chip-row notif-filters" role="tablist" aria-label={t('notif.types')}>
        {FILTERS.map(([key, label]) => (
          <button key={key} type="button" role="tab" aria-selected={filter === key} className={filter === key ? 'chip chip-on' : 'chip'} onClick={() => setFilter(key)}>
            {t(label)}
          </button>
        ))}
      </div>
      {error && <ErrorBanner error={error} />}
      <div className="notif-body" role="tabpanel">
        {list.error ? (
          <ErrorBanner error={list.error} onRetry={list.reload} />
        ) : list.loading ? (
          list.slow && <CardGridSkeleton count={3} variant="row" />
        ) : shown.length === 0 ? (
          <p className="notif-empty">{filter === 'all' ? t('notif.empty') : t('notif.emptyFilter')}</p>
        ) : (
          <ul className="notif-list">
            {shown.map(n => (
              <NotificationRow key={n.id} item={n} unread={!isRead(n)} onOpen={() => {
                markOne(n);
                onNavigate?.();
              }} />
            ))}
          </ul>
        )}
        {!compact && <LoadMore state={list} label={t('notif.older')} />}
      </div>
      {compact && (
        <div className="notif-foot">
          <Link to="/notifications" className="text-btn" onClick={onNavigate}>
            {t('notif.viewAll')}
          </Link>
          <Link to="/settings?s=notify" className="text-btn" onClick={onNavigate}>
            {t('notif.settings')}
          </Link>
        </div>
      )}
    </div>
  );
}

function NotificationRow({item, unread, onOpen}: {item: NotificationItem; unread: boolean; onOpen: () => void}) {
  const t = useT();
  const {kind, text} = headline(item);
  const items = releasesOf(item);
  const count = Math.max(item.payload?.count ?? items.length, items.length);
  const first = items[0];
  const avatarSeed = first?.performers?.[0]?.slug ?? first?.slug ?? String(item.id);
  const avatarName = first?.performers?.[0]?.name ?? first?.title ?? t('notif.avatarFallback');
  const when = timeAgo(item.updated_at || item.created_at);
  const single = items.length === 1 && count <= 1;

  const body = (
    <>
      <Avatar seed={avatarSeed} name={avatarName} size={40} />
      <span className="notif-text">
        <span className="notif-line">{text}</span>
        <span className="notif-meta">
          {kind} · {when}
        </span>
      </span>
      {unread && <span className="new-dot" aria-label={t('notif.unread')} />}
    </>
  );

  return (
    <li className={unread ? 'notif-item is-unread' : 'notif-item'}>
      {single ? (
        <Link to={`/releases/${encodeURIComponent(first.slug)}`} className="notif-main" onClick={onOpen}>
          {body}
        </Link>
      ) : (
        <div className="notif-main">{body}</div>
      )}
      {!single && items.length > 0 && (
        <ul className="notif-releases" aria-label={t('notif.releases')}>
          {items.map(i => (
            <li key={i.slug}>
              <Link to={`/releases/${encodeURIComponent(i.slug)}`} className="notif-release" onClick={onOpen}>
                <span className="notif-release-title">{t('notif.releaseTitle', {title: i.title})}</span>
                <span className="notif-meta">{i.reason === 'reservation' ? t('notif.reserved') : names(i) || t('notif.kindFollow')}</span>
              </Link>
            </li>
          ))}
          {count > items.length && <li className="notif-meta notif-more">{t('notif.more', {n: count - items.length})}</li>}
        </ul>
      )}
    </li>
  );
}
