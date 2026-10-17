import {Fragment, useEffect, useState} from 'react';
import {useSearchParams} from 'react-router';
import {ReleaseRow} from '../components/Cards';
import {Icon} from '../components/Icon';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {Switch} from '../components/Switch';
import {clearWatchHistory, deleteWatchEntry, getWatchHistory, type WatchItem} from '../lib/api';
import {useAuth} from '../lib/auth';
import {clearGuestHistory, deleteGuestEntry, guestHistory} from '../lib/guestProgress';
import {dayLabel, formatClock} from '../lib/format';
import {filterHistory, normalizeHistoryQuery} from '../lib/historyFilter';
import {t, useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {useSettings} from '../lib/settings';
import {useAsync} from '../lib/useAsync';
import '../styles/library.css';

export const meta = () => [{title: t('title.history')}];

// NavHistory on GET /api/me/watch-history, grouped by day, with 全部 / 未看完 /
// 已看完, single-item delete (DELETE /api/me/watch-history/{slug}), 清除全部
// (DELETE /api/me/watch-history) and the 记录观看历史 switch, which is
// user_settings.history_paused (the same setting as 设置 → 隐私).
// Signed out: the same list from this browser's guest history
// (lib/guestProgress), with delete / clear and a sign-in hint; no pause switch.
// 搜索观看记录 (?q=, debounced): signed in, GET /api/me/watch-history?q=
// (title, performer, product code); signed out, the guest list filtered here
// (lib/historyFilter: title and slug / code; the guest store has no cast).

type Filter = 'all' | 'unfinished' | 'finished';
const FILTERS: {key: Filter; label: MessageKey}[] = [
  {key: 'all', label: 'history.filterAll'},
  {key: 'unfinished', label: 'history.filterUnfinished'},
  {key: 'finished', label: 'history.filterFinished'},
];

const SEARCH_DELAY_MS = 300;

/** 已看完: within 10 s of the end or at least 90 % (the watch-later rule). */
function isFinished(item: WatchItem): boolean {
  const d = item.duration_seconds;
  return d > 0 && (item.progress_seconds >= d - 10 || item.progress_seconds >= 0.9 * d);
}

export default function History() {
  const t = useT();
  const {status} = useAuth();
  const {status: settingsStatus, settings, save, fieldErrors, pending} = useSettings();
  const [params, setParams] = useSearchParams();
  const query = normalizeHistoryQuery(params.get('q') ?? '');
  const [text, setText] = useState(params.get('q') ?? '');
  const history = useAsync(signal => getWatchHistory(signal, query), [status, query], status === 'user');
  const [items, setItems] = useState<WatchItem[]>([]);
  const [filter, setFilter] = useState<Filter>('all');
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [announce, setAnnounce] = useState('');

  const guest = status === 'anon';
  useEffect(() => {
    if (history.data) setItems(history.data);
  }, [history.data]);
  // Guest history lives in localStorage: read after mount.
  useEffect(() => {
    if (guest) setItems(guestHistory());
  }, [guest]);

  // The URL follows the field after a pause (replace: no entry per keystroke)…
  useEffect(() => {
    if (normalizeHistoryQuery(text) === query) return;
    const timer = window.setTimeout(() => {
      setParams(
        p => {
          const next = new URLSearchParams(p);
          if (text.trim()) next.set('q', text.trim());
          else next.delete('q');
          return next;
        },
        {replace: true, preventScrollReset: true},
      );
    }, SEARCH_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [text, query, setParams]);
  // …and the field follows the URL (back / forward).
  useEffect(() => {
    setText(v => (normalizeHistoryQuery(v) === query ? v : (params.get('q') ?? '')));
  }, [query]);

  // Signed in, the server already searched; guests filter here.
  const searched = guest ? filterHistory(items, query) : items;
  const shown = searched.filter(i => filter === 'all' || (filter === 'finished') === isFinished(i));
  const groups: [string, WatchItem[]][] = [];
  for (const item of shown) {
    const label = dayLabel(item.watched_at);
    const last = groups[groups.length - 1];
    if (last && last[0] === label) last[1].push(item);
    else groups.push([label, [item]]);
  }

  const paused = settings?.history_paused ?? false;
  const setRecording = (on: boolean) => save('history_paused', {history_paused: !on}, s => ({...s, history_paused: !on}));

  const remove = async (item: WatchItem) => {
    const before = items;
    setItems(before.filter(i => i.slug !== item.slug));
    setError('');
    try {
      if (guest) deleteGuestEntry(item.slug);
      else await deleteWatchEntry(item.slug);
      setAnnounce(t('history.removed', {title: item.title}));
    } catch (cause) {
      setItems(before);
      setError(cause instanceof Error ? cause.message : t('history.removeFailed'));
    }
  };

  const clearAll = async () => {
    setBusy(true);
    setError('');
    try {
      const n = guest ? clearGuestHistory() : (await clearWatchHistory())?.deleted ?? 0;
      setItems([]);
      setConfirming(false);
      setAnnounce(t('history.cleared', {n}));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('history.clearFailed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="page narrow lib-page">
      <div className="later-head">
        <h1 className="page-title">{t('nav.history')}</h1>
        <div className="action-row">
          {!guest && settingsStatus === 'ready' && (
            <span className="lib-switch">
              <span className="lib-switch-label" aria-hidden="true">
                {t('history.record')}
              </span>
              <Switch checked={!paused} onChange={setRecording} label={t('history.record')} busy={pending.has('history_paused')} />
            </span>
          )}
          {items.length > 0 && !confirming && !query && (
            <button type="button" className="btn btn-secondary btn-danger" onClick={() => setConfirming(true)}>
              <Icon name="trash" size={16} />
              {t('history.clearAll')}
            </button>
          )}
        </div>
      </div>
      {guest && (
        <div className="banner" role="status">
          <Icon name="history" size={18} />
          <span className="banner-text">{t('history.guestNote')}</span>
          <a className="btn btn-secondary btn-sm" href="/login?next=%2Fhistory">
            {t('auth.login')}
          </a>
        </div>
      )}
      {fieldErrors.history_paused && <ErrorBanner error={fieldErrors.history_paused} />}
      {!guest && paused && (
        <div className="banner banner-warn" role="status">
          <Icon name="pause" size={18} />
          <span className="banner-text">{t('history.pausedBanner')}</span>
        </div>
      )}
      {confirming && (
        <div className="confirm-row" role="group" aria-label={t('history.confirmClearLabel')}>
          <span>{t('history.confirmClear')}</span>
          <button type="button" className="btn btn-secondary" onClick={() => setConfirming(false)} disabled={busy}>
            {t('playlists.cancel')}
          </button>
          <button type="button" className="btn btn-primary btn-danger-solid" onClick={clearAll} disabled={busy}>
            {busy ? t('history.clearing') : t('history.confirmClearButton')}
          </button>
        </div>
      )}
      {error && <ErrorBanner error={error} />}
      <span className="sr-only" role="status" aria-live="polite">
        {announce}
      </span>
      {(items.length > 0 || query) && (
        <label className="lib-search">
          <Icon name="search" size={16} />
          <input
            type="search"
            value={text}
            onChange={e => setText(e.target.value)}
            placeholder={t('history.search')}
            aria-label={t('history.search')}
            maxLength={100}
            enterKeyHint="search"
          />
        </label>
      )}
      {items.length > 0 && (
        <div className="segmented segmented-sm lib-filter" role="radiogroup" aria-label={t('history.filter')}>
          {FILTERS.map(f => (
            <button key={f.key} type="button" role="radio" aria-checked={filter === f.key} className="segmented-item" onClick={() => setFilter(f.key)}>
              {t(f.label)}
            </button>
          ))}
        </div>
      )}
      {history.error ? (
        <ErrorBanner error={history.error} onRetry={history.reload} />
      ) : history.loading ? (
        history.slow && <CardGridSkeleton count={5} variant="row" />
      ) : query && searched.length === 0 ? (
        <EmptyState icon="search" title={t('history.noMatchTitle')} body={t('history.noMatchBody', {q: params.get('q')?.trim() || query})} />
      ) : items.length === 0 ? (
        <EmptyState icon="history" title={t('history.emptyTitle')} body={t('history.emptyBody')} actions={[{to: '/', label: t('common.goHome')}]} />
      ) : shown.length === 0 ? (
        <EmptyState icon="history" title={filter === 'finished' ? t('history.noneFinished') : t('history.noneUnfinished')} />
      ) : (
        groups.map(([label, list]) => (
          <Fragment key={label}>
            <h2 className="group-label">{label}</h2>
            <div className="row-list">
              {list.map(item => (
                <ReleaseRow
                  key={item.slug}
                  slug={item.slug}
                  title={item.title}
                  progress={item.duration_seconds > 0 ? item.progress_seconds / item.duration_seconds : undefined}
                  meta={
                    isFinished(item)
                      ? t('home.finished')
                      : item.duration_seconds > 0
                        ? t('history.watchedOf', {pos: formatClock(item.progress_seconds), total: formatClock(item.duration_seconds)})
                        : t('history.watched', {pos: formatClock(item.progress_seconds)})
                  }
                >
                  <button type="button" className="icon-btn" aria-label={t('history.removeLabel', {title: item.title})} title={t('history.remove')} onClick={() => remove(item)}>
                    <Icon name="close" size={18} />
                  </button>
                </ReleaseRow>
              ))}
            </div>
          </Fragment>
        ))
      )}
    </div>
  );
}
