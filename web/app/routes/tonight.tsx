import {useCallback, useEffect, useMemo, useRef, useState} from 'react';
import {useLocation, useNavigate} from '../lib/nav';
import {Thumb} from '../components/Cards';
import {Icon} from '../components/Icon';
import {BlockSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {getPlayConfig, saveWatchProgress} from '../lib/api';
import {track} from '../lib/analytics';
import {useAuth} from '../lib/auth';
import {formatRuntime, formatYear} from '../lib/format';
import {t, useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {entriesFromConfig} from '../lib/playSources';
import {getTonight, performerName, reasonText, recsMissing, recTitle, type RecItem, type TonightCompany, type TonightDuration, type TonightMood} from '../lib/recs';
import {useSocial} from '../lib/social';
import {useAsync} from '../lib/useAsync';
import {useMediaQuery} from '../lib/useMediaQuery';

export const meta = () => [{title: t('title.tonight')}];

// 今晚看什么 · Tonight / MobileTonight (PRD 18). Opened from the home button,
// the T shortcut (routes/shell.tsx) and the top of mobile 发现.
//
// GET /api/recs?slot=tonight&duration=&exclude= returns one pick (unwatched,
// rated ≥ 7.5 where rated, playable) plus alternates; every 换一个 / 看过了
// refetches with the swapped ids in `exclude`, which the backend also uses to
// demote their maker and performer. The last 3 swapped-out picks stay as
// chips and can be brought back. 看过了 writes a finished watch_history row.
//
// Filters: 时长 is sent as `duration` (a filter); 想要 and 和谁看 are sent as
// `mood` / `company`, soft preferences the backend maps conservatively to
// genre tags and a runtime window (internal/recs prefs.go) — they reorder
// the pick, never empty it.
// Preview: muted autoplay only when play-config has a direct media file the
// browser can play (no trailer field exists); otherwise the placeholder.

type Duration = TonightDuration;
type Mood = TonightMood;
type Who = TonightCompany;
type Filters = {duration: Duration; mood: Mood; who: Who};

const DURATIONS: {value: Duration; label: MessageKey}[] = [
  {value: 'any', label: 'tonight.durAny'},
  {value: '30', label: 'tonight.dur30'},
  {value: '60', label: 'tonight.dur60'},
  {value: '120', label: 'tonight.dur120'},
  {value: 'series', label: 'tonight.durSeries'},
];
const MOODS: {value: Mood; label: MessageKey}[] = [
  {value: 'any', label: 'tonight.moodAny'},
  {value: 'light', label: 'tonight.moodLight'},
  {value: 'thrill', label: 'tonight.moodThrill'},
  {value: 'touching', label: 'tonight.moodTouching'},
  {value: 'brainy', label: 'tonight.moodBrainy'},
];
const WHO: {value: Who; label: MessageKey}[] = [
  {value: 'self', label: 'tonight.whoSelf'},
  {value: 'friends', label: 'tonight.whoFriends'},
  {value: 'family', label: 'tonight.whoFamily'},
];

const KEEP_SKIPPED = 3;
const SWIPE_PX = 60;

function filterProps(f: Filters): Record<string, string> {
  const out: Record<string, string> = {};
  if (f.duration !== 'any') out.duration = f.duration;
  if (f.mood !== 'any') out.mood = f.mood;
  if (f.who !== 'self') out.who = f.who;
  return out;
}

export default function TonightPage() {
  const t = useT();
  const navigate = useNavigate();
  const location = useLocation();
  const {status} = useAuth();
  const {setLater, later, refreshLater} = useSocial();
  const mobile = useMediaQuery('(max-width: 767px)');
  const [filters, setFilters] = useState<Filters>({duration: 'any', mood: 'any', who: 'self'});
  // Ids swapped away or marked watched in this session (sent as exclude).
  const [exclude, setExclude] = useState<number[]>([]);
  // A pick brought back from the skipped chips (shown instead of the fetched one).
  const [restored, setRestored] = useState<RecItem | null>(null);
  const [skipped, setSkipped] = useState<RecItem[]>([]);
  const [shuffles, setShuffles] = useState(0);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const result = useAsync(
    signal => getTonight({duration: filters.duration, mood: filters.mood, company: filters.who, exclude}, signal),
    [filters.duration, filters.mood, filters.who, exclude.join(','), status],
  );
  // Keep showing the previous pick while the next one loads (no blank flash).
  const [last, setLast] = useState<RecItem | null>(null);
  useEffect(() => {
    if (result.data) setLast(result.data.item);
  }, [result.data]);
  const pick = restored ?? (result.loading ? last : result.data?.item ?? null);
  const n = shuffles + 1;

  const opened = useRef(false);
  useEffect(() => {
    if (opened.current) return;
    opened.current = true;
    track('tonight_open', {filters: filterProps(filters)});
  }, [filters]);

  const close = useCallback(() => {
    if ((location.key && location.key !== 'default') || window.history.length > 1) navigate(-1);
    else navigate('/');
  }, [location.key, navigate]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !event.defaultPrevented) close();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [close]);

  const flash = (msg: string) => {
    setNotice(msg);
    window.setTimeout(() => setNotice(m => (m === msg ? '' : m)), 2500);
  };

  /** Drop the current pick and ask for the next one. */
  const advance = (current: RecItem, keepAsSkipped: boolean) => {
    setRestored(null);
    if (keepAsSkipped) setSkipped(prev => [current, ...prev.filter(x => x.id !== current.id)].slice(0, KEEP_SKIPPED));
    setExclude(prev => (prev.includes(current.id) ? prev : [...prev, current.id]));
  };

  const shuffle = () => {
    if (!pick || result.loading) return;
    const attempt = shuffles + 1;
    setShuffles(attempt);
    track('tonight_shuffle', {attempt, filters: filterProps(filters)});
    advance(pick, true);
  };

  const accept = () => {
    if (!pick) return;
    track('tonight_accept', {release_id: pick.id, attempt: n, reason: pick.reason.code});
    navigate(`/releases/${encodeURIComponent(pick.slug)}`, {state: {slot: 'tonight', reason: pick.reason.code, autoplay: true}});
  };

  const needLogin = () => {
    if (status === 'user') return false;
    navigate(`/login?next=${encodeURIComponent('/tonight')}`);
    return true;
  };

  const saveLater = async () => {
    if (!pick || needLogin()) return;
    setBusy(true);
    const error = await setLater(pick.slug, true);
    setBusy(false);
    flash(error || t('tonight.laterSaved'));
  };

  const markWatched = async () => {
    if (!pick || needLogin()) return;
    setBusy(true);
    try {
      const seconds = (pick.runtime_minutes ?? 0) * 60;
      // A finished row: progress = duration (an unknown runtime still records the watch).
      const saved = await saveWatchProgress(pick.slug, seconds || 1, seconds || 1);
      if (saved?.later_removed) refreshLater();
      flash(t('tonight.watchedSaved'));
      advance(pick, false);
    } catch (cause) {
      flash(cause instanceof Error ? cause.message : t('tonight.failed'));
    } finally {
      setBusy(false);
    }
  };

  const restore = (item: RecItem) => {
    setSkipped(prev => {
      const rest = prev.filter(x => x.id !== item.id);
      return pick && pick.id !== item.id ? [pick, ...rest].slice(0, KEEP_SKIPPED) : rest;
    });
    if (pick && pick.id !== item.id) setExclude(prev => (prev.includes(pick.id) ? prev : [...prev, pick.id]));
    setRestored(item);
  };

  const reset = () => {
    setRestored(null);
    setSkipped([]);
    setExclude([]);
  };

  const setFilter = <K extends keyof Filters>(key: K, value: Filters[K]) => {
    setRestored(null);
    setFilters(prev => ({...prev, [key]: value}));
  };

  // Mobile: left swipe = 换一个.
  const touch = useRef<{x: number; y: number} | null>(null);
  const swipe = {
    onTouchStart: (e: React.TouchEvent) => {
      const p = e.touches[0];
      touch.current = p ? {x: p.clientX, y: p.clientY} : null;
    },
    onTouchEnd: (e: React.TouchEvent) => {
      const start = touch.current;
      const p = e.changedTouches[0];
      touch.current = null;
      if (!start || !p) return;
      const dx = p.clientX - start.x;
      const dy = p.clientY - start.y;
      if (dx < -SWIPE_PX && Math.abs(dy) < Math.abs(dx) / 2) shuffle();
    },
  };

  const missing = result.error && recsMissing(result.error);
  const inLater = !!pick && status === 'user' && later.has(pick.slug);

  return (
    <div className="page tonight-page">
      <section className="tonight-panel" role="dialog" aria-modal="false" aria-labelledby="tonight-title">
        <header className="tonight-head">
          <h1 id="tonight-title">{t('tonight.title')}</h1>
          <span className="section-spacer" />
          <button type="button" className="icon-btn" aria-label={t('tonight.close')} onClick={close}>
            <Icon name="close" />
          </button>
        </header>

        <div className="tonight-filters">
          <FilterRow label={t('tonight.duration')} options={DURATIONS} value={filters.duration} onPick={v => setFilter('duration', v)} />
          <FilterRow label={t('tonight.mood')} options={MOODS} value={filters.mood} onPick={v => setFilter('mood', v)} />
          <FilterRow label={t('tonight.company')} options={WHO} value={filters.who} onPick={v => setFilter('who', v)} />
        </div>

        {missing ? (
          <EmptyState icon="tonight" title={t('tonight.empty')} body={t('trending.emptyBody')} actions={[{to: '/discover?tab=new', label: t('nav.new')}]} />
        ) : result.error && !pick ? (
          <ErrorBanner error={result.error} onRetry={result.reload} />
        ) : !pick ? (
          result.loading ? (
            result.slow && <BlockSkeleton lines={4} />
          ) : (
            <EmptyState icon="tonight" title={t('tonight.empty')} body={t('tonight.emptyBody')}>
              {exclude.length > 0 && (
                <div className="empty-actions">
                  <button type="button" className="btn btn-primary" onClick={reset}>
                    {t('tonight.reset')}
                  </button>
                </div>
              )}
            </EmptyState>
          )
        ) : (
          <div className="tonight-body" aria-busy={result.loading} {...(mobile ? swipe : {})}>
            <Preview item={pick} />
            <div className="tonight-info">
              <span className="tonight-nth">{t('tonight.nth', {n})}</span>
              <h2 className="tonight-pick-title">{recTitle(pick)}</h2>
              <span className="tonight-meta">
                {[
                  formatYear(pick.release_date),
                  performerName(pick.performer),
                  pick.maker_name,
                  pick.rating !== null && pick.rating !== undefined ? t('tonight.rating', {n: pick.rating.toFixed(1)}) : '',
                ]
                  .filter(Boolean)
                  .join(' · ')}
              </span>
              {reasonText(pick.reason, pick) && (
                <p className="tonight-why">
                  <Icon name="star" size={18} />
                  <span>{reasonText(pick.reason, pick)}</span>
                </p>
              )}
              <span className="section-spacer" />
              <div className="tonight-actions">
                <button type="button" className="btn btn-primary btn-lg tonight-accept" onClick={accept}>
                  {t('tonight.accept')}
                </button>
                <button type="button" className="btn btn-outline btn-lg" onClick={shuffle} disabled={result.loading}>
                  <Icon name="tonight" size={18} />
                  {t('tonight.shuffle')}
                </button>
                <button type="button" className="btn btn-outline btn-lg" onClick={saveLater} disabled={busy || inLater} aria-pressed={inLater}>
                  {inLater ? t('tonight.laterSaved') : t('tonight.later')}
                </button>
                <button type="button" className="text-btn tonight-watched" onClick={markWatched} disabled={busy || result.loading}>
                  {t('tonight.watched')}
                </button>
              </div>
              <span className="tonight-hint show-mobile">{t('tonight.swipeHint')}</span>
              <span className="tonight-notice" role="status">
                {notice}
              </span>
            </div>
          </div>
        )}

        <div className="tonight-skipped">
          <span className="tonight-skipped-label">{t('tonight.skipped')}</span>
          {skipped.length === 0 ? (
            <span className="muted">{t('tonight.skippedNone')}</span>
          ) : (
            skipped.map(item => (
              <button key={item.id} type="button" className="chip" onClick={() => restore(item)} aria-label={t('tonight.backTo', {title: recTitle(item)})}>
                {recTitle(item)}
              </button>
            ))
          )}
        </div>
      </section>
    </div>
  );
}

function FilterRow<V extends string>({label, options, value, onPick}: {label: string; options: {value: V; label: MessageKey}[]; value: V; onPick: (v: V) => void}) {
  const t = useT();
  return (
    <div className="tonight-filter" role="group" aria-label={label}>
      <span className="tonight-filter-label">{label}</span>
      <div className="chip-row">
        {options.map(o => (
          <button key={o.value} type="button" className={o.value === value ? 'chip chip-pick is-on' : 'chip chip-pick'} aria-pressed={o.value === value} onClick={() => onPick(o.value)}>
            {t(o.label)}
          </button>
        ))}
      </div>
    </div>
  );
}

/** Muted autoplay preview when the pick has a direct media file; otherwise the placeholder. */
function Preview({item}: {item: RecItem}) {
  const t = useT();
  const reduced = useMediaQuery('(prefers-reduced-motion: reduce)');
  const config = useAsync(signal => getPlayConfig(item.slug, signal), [item.slug]);
  const src = useMemo(() => {
    const video = typeof document !== 'undefined' ? document.createElement('video') : null;
    const nativeHls = !!video?.canPlayType('application/vnd.apple.mpegurl');
    const entry = entriesFromConfig(config.data).find(e => e.render === 'video' && (e.protocol === 'file' || (e.protocol === 'hls' && nativeHls)));
    return entry?.url ?? null;
  }, [config.data]);
  const badge = item.runtime_minutes ? formatRuntime(item.runtime_minutes) : undefined;
  return (
    <div className="tonight-media">
      {src && !reduced ? (
        <video className="tonight-video" src={src} muted autoPlay loop playsInline preload="metadata" aria-label={t('tonight.previewMuted')} />
      ) : (
        <Thumb seed={item.slug} className="tonight-thumb" />
      )}
      {badge && <span className="tonight-dur">{badge}</span>}
      <span className="tonight-media-note">{src && !reduced ? t('tonight.previewMuted') : t('tonight.noPreview')}</span>
    </div>
  );
}
