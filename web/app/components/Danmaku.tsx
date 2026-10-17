import {useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState, type Ref, type RefObject} from 'react';
import {isAbort} from '../lib/api';
import {track} from '../lib/analytics';
import {useAuth} from '../lib/auth';
import {getDanmaku, postDanmaku, postErrorMessage, reportDanmaku, type Danmaku, type ReportReason} from '../lib/community';
import {
  DANMAKU_COLORS,
  DANMAKU_LANGS,
  FONT_PX,
  SPEED_PX,
  keptByDensity,
  langAllowed,
  setDanmakuPref,
  useDanmakuPrefs,
  type DanmakuLang,
  type DanmakuPrefs,
} from '../lib/danmakuPrefs';
import {
  MAX_BLOCK_WORDS,
  MAX_BLOCK_WORD_LENGTH,
  addBlockWord,
  foldBlockText,
  hitsBlockWord,
  removeBlockWord,
  useDanmakuBlockWords,
  useDanmakuBlockWordsSync,
} from '../lib/danmakuBlockWords';
import {formatClock} from '../lib/format';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {usePendingAction, useRequireAuth} from './AuthPrompt';
import {Icon} from './Icon';
import {ReportPicker} from './ReportPicker';
import {Sheet} from './Sheet';
import {Switch} from './Switch';
import {TimestampText} from './Timestamps';

// 弹幕 (PRD 5.5 · DanmakuPlayer / MobileDanmaku; internal/api/community.go).
//
//   useDanmakuFeed   loads 60 s segments around the playhead (GET .../danmaku).
//   DanmakuLayer     draws them over the video: scroll / top / bottom lanes,
//                    opacity, size, speed, display area, density, block
//                    types, language filter, personal block words, 防挡字幕.
//   DanmakuBar       on/off, the send box (login, 50 characters, mute 403,
//                    429), the list and the settings button.
//   DanmakuListSheet loaded danmaku around the playhead, with 举报.
//   DanmakuSettings  the settings tab of the player sheet.
//
// Language filter: the server detects danmaku.lang on post (community.
// DetectLang, a script heuristic; migration 045 backfills older rows) and the
// filter runs here. Personal block words: danmakuBlockWords.ts (synced
// through user_settings for logged-in users, localStorage for visitors).
//
// Not built: auto-translation (PRD 5.5 语言过滤与自动翻译; out of scope, it
// needs a translation service and per-language caching of danmaku bodies),
// hiding during ads (ads are on hold).

export const DANMAKU_MAX = 50;
const SEGMENT_MS = 60_000;
const FIXED_MS = 4_000;
const REPEAT_WINDOW_MS = 10_000;
const SUBTITLE_SAFE = 0.15;

export const runeLength = (s: string) => Array.from(s).length;

// ---- Feed ----

export type DanmakuFeed = {
  /** Loaded danmaku sorted by t_ms. */
  items: RefObject<Danmaku[]>;
  /** Bumps when items change (list view). */
  version: number;
  add: (d: Danmaku) => void;
};

/** Loads the segments covering the playhead and the next minute. */
export function useDanmakuFeed(slug: string, sourceId: number | undefined, video: HTMLVideoElement | null, active: boolean): DanmakuFeed {
  const items = useRef<Danmaku[]>([]);
  const segments = useRef(new Set<number>());
  const [version, setVersion] = useState(0);

  useEffect(() => {
    items.current = [];
    segments.current = new Set();
    setVersion(v => v + 1);
  }, [slug, sourceId]);

  useEffect(() => {
    if (!active || !video) return;
    const controller = new AbortController();
    const merge = (list: Danmaku[]) => {
      if (list.length === 0) return;
      const seen = new Set(items.current.map(d => d.id));
      const next = items.current.concat(list.filter(d => !seen.has(d.id)));
      next.sort((a, b) => a.t_ms - b.t_ms || a.id - b.id);
      items.current = next;
      setVersion(v => v + 1);
    };
    const ensure = () => {
      const seg = Math.floor((video.currentTime * 1000) / SEGMENT_MS);
      const missing = [seg, seg + 1].filter(s => s >= 0 && !segments.current.has(s));
      if (missing.length === 0) return;
      missing.forEach(s => segments.current.add(s));
      const from = Math.min(...missing) * SEGMENT_MS;
      const to = (Math.max(...missing) + 1) * SEGMENT_MS;
      getDanmaku(slug, from, to, sourceId, controller.signal).then(
        page => merge(page.items),
        error => {
          // Retry these segments on the next tick (offline, 5xx); not after an abort.
          if (!isAbort(error)) missing.forEach(s => segments.current.delete(s));
        },
      );
    };
    ensure();
    video.addEventListener('timeupdate', ensure);
    video.addEventListener('seeked', ensure);
    return () => {
      controller.abort();
      // Segments that were in flight load again next time.
      video.removeEventListener('timeupdate', ensure);
      video.removeEventListener('seeked', ensure);
      segments.current = new Set(Array.from(segments.current).filter(s => items.current.some(d => Math.floor(d.t_ms / SEGMENT_MS) === s)));
    };
  }, [slug, sourceId, video, active]);

  const add = useCallback((d: Danmaku) => {
    if (items.current.some(x => x.id === d.id)) return;
    items.current = [...items.current, d].sort((a, b) => a.t_ms - b.t_ms || a.id - b.id);
    setVersion(v => v + 1);
  }, []);

  return {items, version, add};
}

// ---- Layer ----

export type DanmakuLayerHandle = {
  /** Show one danmaku now (the viewer's own, right after sending). */
  spawn: (d: Danmaku) => void;
};

/** Filtered out for this viewer by language or a personal block word (the list sheet hides these too). */
function filteredOut(d: Danmaku, p: DanmakuPrefs, foldedWords: string[]): boolean {
  if (d.mine) return false;
  return !langAllowed(d.lang, p.langs) || hitsBlockWord(d.body, foldedWords);
}

function blocked(d: Danmaku, p: DanmakuPrefs, foldedWords: string[]): boolean {
  if (d.spoiler && p.blockSpoiler) return true;
  if (d.mine) return false;
  if (filteredOut(d, p, foldedWords)) return true;
  if (!keptByDensity(d.id, p.density)) return true;
  if (d.mode === 'top' && p.blockTop) return true;
  if (d.mode === 'bottom' && p.blockBottom) return true;
  if (d.mode === 'scroll' && p.blockScroll) return true;
  if (p.blockColor && d.color.toUpperCase() !== '#FFFFFF') return true;
  return false;
}

const normalize = (s: string) => s.replace(/\s+/g, '').toLowerCase();

/**
 * Draws danmaku over the video. Purely visual (aria-hidden): the list sheet
 * is the accessible way to read them. Animations pause with the video and
 * the layer clears on seek.
 */
export function DanmakuLayer({video, feed, prefs, reducedMotion, ref}: {
  video: HTMLVideoElement | null;
  feed: DanmakuFeed;
  prefs: DanmakuPrefs;
  reducedMotion: boolean;
  ref?: Ref<DanmakuLayerHandle>;
}) {
  const layerRef = useRef<HTMLDivElement>(null);
  const prefsRef = useRef(prefs);
  prefsRef.current = prefs;
  const {user} = useAuth();
  useDanmakuBlockWordsSync(user?.id ?? null);
  const words = useDanmakuBlockWords();
  const folded = useMemo(() => words.map(foldBlockText), [words]);
  const foldedRef = useRef(folded);
  foldedRef.current = folded;
  const motionRef = useRef(reducedMotion);
  motionRef.current = reducedMotion;
  const lanes = useRef({scroll: [] as number[], top: [] as number[], bottom: [] as number[]});
  const recent = useRef(new Map<string, number>());

  const clear = useCallback(() => {
    const layer = layerRef.current;
    if (!layer) return;
    layer.getAnimations({subtree: true}).forEach(a => a.cancel());
    layer.replaceChildren();
    lanes.current = {scroll: [], top: [], bottom: []};
    recent.current.clear();
  }, []);

  const spawn = useCallback((d: Danmaku, force = false) => {
    const layer = layerRef.current;
    const p = prefsRef.current;
    if (!layer || !p.enabled) return;
    if (!force && blocked(d, p, foldedRef.current)) return;
    if (!force && p.blockRepeat) {
      const key = normalize(d.body);
      const last = recent.current.get(key);
      if (last !== undefined && Math.abs(d.t_ms - last) < REPEAT_WINDOW_MS) return;
      recent.current.set(key, d.t_ms);
      if (recent.current.size > 500) recent.current.clear();
    }
    const width = layer.clientWidth;
    const height = layer.clientHeight;
    if (!width || !height) return;
    const scale = Math.min(1.2, Math.max(0.6, width / 960));
    const font = Math.round(FONT_PX[p.size] * scale);
    const laneH = Math.round(font * 1.35);
    const bottomSafe = p.antiSubtitle ? height * SUBTITLE_SAFE : 0;
    const usable = Math.max(laneH, Math.min(height * p.area, height - bottomSafe));
    const count = Math.max(1, Math.floor(usable / laneH));
    const mode = d.mode === 'scroll' && motionRef.current ? 'top' : d.mode;
    const now = performance.now();

    const el = document.createElement('span');
    el.className = d.mine ? 'dm-item dm-mine' : 'dm-item';
    el.textContent = d.body;
    el.style.fontSize = `${font}px`;
    el.style.color = /^#[0-9a-fA-F]{6}$/.test(d.color) ? d.color : '#FFFFFF';
    layer.appendChild(el);
    const w = el.offsetWidth;

    const free = mode === 'scroll' ? lanes.current.scroll : mode === 'top' ? lanes.current.top : lanes.current.bottom;
    let lane = -1;
    for (let i = 0; i < count; i++) {
      if ((free[i] ?? 0) <= now) {
        lane = i;
        break;
      }
    }
    if (lane < 0) {
      if (!force) {
        el.remove();
        return;
      }
      lane = 0;
    }
    if (mode === 'scroll') {
      const speed = SPEED_PX[p.speed] * Math.max(0.6, width / 1280);
      const duration = ((width + w) / speed) * 1000;
      // The lane is free once this item's tail is fully in view plus a gap.
      free[lane] = now + ((w + font * 2) / speed) * 1000;
      el.style.top = `${lane * laneH}px`;
      el.style.left = `${width}px`;
      const anim = el.animate([{transform: 'translateX(0)'}, {transform: `translateX(${-(width + w)}px)`}], {duration, easing: 'linear'});
      anim.onfinish = () => el.remove();
      if (video?.paused) anim.pause();
    } else {
      free[lane] = now + FIXED_MS;
      el.style.left = `${Math.max(0, (width - w) / 2)}px`;
      if (mode === 'top') el.style.top = `${lane * laneH}px`;
      else el.style.top = `${height - bottomSafe - (lane + 1) * laneH}px`;
      const anim = el.animate([{opacity: 1}, {opacity: 1}], {duration: FIXED_MS});
      anim.onfinish = () => el.remove();
      if (video?.paused) anim.pause();
    }
  }, [video]);

  useImperativeHandle(ref, () => ({spawn: d => spawn(d, true)}), [spawn]);

  // Emit danmaku whose time passes while playing.
  useEffect(() => {
    if (!video || !prefs.enabled) {
      clear();
      return;
    }
    let last = video.currentTime * 1000;
    let frame = 0;
    // rAF for smooth timing; timeupdate as well, since rAF stops in
    // background or hidden views while the video keeps playing.
    const tick = () => {
      frame = window.requestAnimationFrame(tick);
      step();
    };
    const step = () => {
      if (video.paused || video.seeking) return;
      const now = video.currentTime * 1000;
      if (now < last || now - last > 1500) {
        last = now;
        return;
      }
      const list = feed.items.current;
      // First item after `last` (binary search), then everything up to `now`.
      let lo = 0;
      let hi = list.length;
      while (lo < hi) {
        const mid = (lo + hi) >> 1;
        if (list[mid].t_ms <= last) lo = mid + 1;
        else hi = mid;
      }
      for (let i = lo; i < list.length && list[i].t_ms <= now; i++) spawn(list[i]);
      last = now;
    };
    frame = window.requestAnimationFrame(tick);
    video.addEventListener('timeupdate', step);
    const onPause = () => layerRef.current?.getAnimations({subtree: true}).forEach(a => a.pause());
    const onPlay = () => layerRef.current?.getAnimations({subtree: true}).forEach(a => a.play());
    const onSeek = () => {
      clear();
      last = video.currentTime * 1000;
    };
    video.addEventListener('pause', onPause);
    video.addEventListener('play', onPlay);
    video.addEventListener('seeking', onSeek);
    return () => {
      window.cancelAnimationFrame(frame);
      video.removeEventListener('timeupdate', step);
      video.removeEventListener('pause', onPause);
      video.removeEventListener('play', onPlay);
      video.removeEventListener('seeking', onSeek);
    };
  }, [video, prefs.enabled, feed.items, spawn, clear]);

  // Display changes apply to what comes next; blocking a type clears the screen.
  useEffect(() => clear(), [prefs.blockTop, prefs.blockBottom, prefs.blockScroll, prefs.blockColor, prefs.blockSpoiler, prefs.area, prefs.antiSubtitle, prefs.size, clear]);
  // Narrowing languages, density or adding a block word clears the screen too.
  const langKey = prefs.langs.join(',');
  useEffect(() => clear(), [langKey, prefs.density, folded, clear]);

  return <div ref={layerRef} className="dm-layer" aria-hidden="true" style={{opacity: prefs.opacity}} />;
}

// ---- Bar: toggle, send box, list, settings ----

export function DanmakuBar({slug, releaseId, sourceId, video, feed, layer, onOpenSettings, onOpenList}: {
  slug: string;
  releaseId?: number;
  sourceId?: number;
  video: HTMLVideoElement | null;
  feed: DanmakuFeed;
  layer: RefObject<DanmakuLayerHandle | null>;
  onOpenSettings: () => void;
  onOpenList: () => void;
}) {
  const t = useT();
  const {status} = useAuth();
  const requireAuth = useRequireAuth();
  const prefs = useDanmakuPrefs();
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [sent, setSent] = useState('');
  const length = runeLength(text.trim());
  const tooLong = length > DANMAKU_MAX;
  const inputRef = useRef<HTMLInputElement>(null);

  // A guest's draft waits through the sign-in and comes back here, unsent.
  usePendingAction('danmaku', slug, payload => {
    if (typeof payload.draft === 'string') setText(payload.draft);
    inputRef.current?.focus({preventScroll: true});
    return t('authPrompt.doneDraft');
  });

  const send = async () => {
    const body = text.trim();
    if (!body || tooLong || busy || !video) return;
    if (!requireAuth('danmaku', {target: slug, payload: {draft: body}})) return;
    setBusy(true);
    setError('');
    setSent('');
    const tMs = Math.max(0, Math.round(video.currentTime * 1000));
    try {
      const d = await postDanmaku(slug, {t_ms: tMs, body, color: prefs.sendColor, mode: prefs.sendMode, source_id: sourceId});
      const mine = {...d, mine: true};
      feed.add(mine);
      if (prefs.enabled) layer.current?.spawn(mine);
      setText('');
      setSent(t('danmaku.sent'));
      if (releaseId) track('danmaku_send', {release_id: releaseId, position_ms: tMs, length: runeLength(body), color: d.color || prefs.sendColor});
    } catch (cause) {
      setError(postErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="dm-bar-wrap">
      <div className="dm-bar" role="group" aria-label={t('danmaku.label')}>
        <button
          type="button"
          className="pbar-btn dm-toggle"
          aria-pressed={prefs.enabled}
          aria-label={prefs.enabled ? t('danmaku.turnOff') : t('danmaku.turnOn')}
          title={prefs.enabled ? t('danmaku.turnOff') : t('danmaku.turnOn')}
          onClick={() => setDanmakuPref('enabled', !prefs.enabled)}
        >
          <span className="dm-toggle-glyph" aria-hidden="true">
            {t('danmaku.glyph')}
          </span>
        </button>
        {status !== 'unknown' ? (
          <form
            className="dm-form"
            onSubmit={event => {
              event.preventDefault();
              void send();
            }}
          >
            <label className="sr-only" htmlFor="dm-input">
              {t('danmaku.inputLabel')}
            </label>
            <input
              ref={inputRef}
              id="dm-input"
              className="dm-input"
              type="text"
              value={text}
              placeholder={status === 'user' ? t('danmaku.placeholder') : t('danmaku.loginToSend')}
              autoComplete="off"
              enterKeyHint="send"
              aria-invalid={tooLong || !!error || undefined}
              aria-describedby="dm-count"
              onChange={e => {
                setText(e.target.value);
                if (error) setError('');
              }}
            />
            <span id="dm-count" className={tooLong ? 'dm-count dm-count-over' : 'dm-count'} aria-live="polite">
              {length}/{DANMAKU_MAX}
            </span>
            <button type="submit" className="btn btn-primary btn-sm dm-send" disabled={busy || length === 0 || tooLong}>
              {busy ? t('danmaku.sending') : t('danmaku.send')}
            </button>
          </form>
        ) : (
          <span className="dm-form" />
        )}
        <button type="button" className="pbar-btn" aria-label={t('danmaku.list')} title={t('danmaku.list')} onClick={onOpenList}>
          <Icon name="list" size={20} />
        </button>
        <button type="button" className="pbar-btn" aria-label={t('danmaku.settings')} title={t('danmaku.settings')} onClick={onOpenSettings}>
          <Icon name="settings" size={20} />
        </button>
      </div>
      {(error || tooLong) && (
        <p className="dm-error" role="alert">
          {tooLong ? t('danmaku.tooLong', {n: DANMAKU_MAX}) : error}
        </p>
      )}
      <span className="sr-status" role="status">
        {sent}
      </span>
    </div>
  );
}

// ---- List + report ----

export function DanmakuListSheet({open, onClose, feed, video, container}: {
  open: boolean;
  onClose: () => void;
  feed: DanmakuFeed;
  video: HTMLVideoElement | null;
  container?: HTMLElement | null;
}) {
  const t = useT();
  const prefs = useDanmakuPrefs();
  const {status} = useAuth();
  const requireAuth = useRequireAuth();
  const [reporting, setReporting] = useState<number | null>(null);
  const [revealed, setRevealed] = useState<Set<number>>(new Set());
  const [done, setDone] = useState<Record<number, string>>({});
  const words = useDanmakuBlockWords();
  // Snapshot when opened (and when new items load), so the list does not jump;
  // the language filter and block words apply here too.
  const list = useMemo(() => {
    if (!open) return [];
    const folded = words.map(foldBlockText);
    return feed.items.current.filter(d => !filteredOut(d, prefs, folded));
  }, [open, feed.version, feed.items, words, prefs]);

  useEffect(() => {
    if (!open) setReporting(null);
  }, [open]);

  const report = async (d: Danmaku, reason: ReportReason) => {
    const r = await reportDanmaku(d.id, reason);
    track('danmaku_report', {danmaku_id: d.id, reason});
    setDone(m => ({...m, [d.id]: r.created ? t('report.thanks') : t('report.already')}));
    setReporting(null);
  };

  return (
    <Sheet open={open} onClose={onClose} label={t('danmaku.list')} container={container} className="sheet-danmaku" header={<h2 className="sheet-title">{t('danmaku.listTitle', {n: list.length})}</h2>}>
      {list.length === 0 ? (
        <p className="sheet-note">{t('danmaku.listEmpty')}</p>
      ) : (
        <ul className="dm-list">
          {list.map(d => {
            const hidden = d.spoiler && prefs.blockSpoiler && !revealed.has(d.id);
            return (
              <li key={d.id} className="dm-row">
                <div className="dm-row-main">
                  <button
                    type="button"
                    className="dm-time"
                    aria-label={t('danmaku.seekTo', {time: formatClock(d.t_ms / 1000)})}
                    onClick={() => {
                      if (video) {
                        video.dataset.seekMethod = 'drag';
                        video.currentTime = d.t_ms / 1000;
                      }
                    }}
                  >
                    {formatClock(d.t_ms / 1000)}
                  </button>
                  {hidden ? (
                    <button type="button" className="dm-spoiler" onClick={() => setRevealed(s => new Set(s).add(d.id))}>
                      {t('danmaku.spoilerFolded')}
                    </button>
                  ) : (
                    <span className="dm-body">
                      <TimestampText
                        text={d.body}
                        maxSeconds={video && Number.isFinite(video.duration) ? video.duration : undefined}
                        onSeek={s => {
                          if (video) {
                            video.dataset.seekMethod = 'drag';
                            video.currentTime = s;
                          }
                        }}
                      />
                      {d.mine && <span className="dm-tag">{t('danmaku.mine')}</span>}
                    </span>
                  )}
                  {!d.mine && !done[d.id] && (
                    <button
                      type="button"
                      className="icon-btn dm-report-btn"
                      aria-label={t('danmaku.report')}
                      aria-expanded={reporting === d.id}
                      onClick={() => setReporting(r => (r === d.id ? null : d.id))}
                    >
                      <Icon name="flag" size={18} />
                    </button>
                  )}
                </div>
                {done[d.id] && (
                  <p className="dm-done" role="status">
                    {done[d.id]}
                  </p>
                )}
                {reporting === d.id &&
                  (status === 'user' ? (
                    <ReportPicker onSubmit={reason => report(d, reason)} onCancel={() => setReporting(null)} />
                  ) : (
                    <p className="dm-done">
                      <button
                        type="button"
                        className="text-link"
                        onClick={() => {
                          // One dialog at a time: the sign-in sheet replaces this list.
                          onClose();
                          requireAuth('report');
                        }}
                      >
                        {t('report.loginFirst')}
                      </button>
                    </p>
                  ))}
              </li>
            );
          })}
        </ul>
      )}
    </Sheet>
  );
}

// ---- Settings tab ----

type Opt<T> = {value: T; label: MessageKey};

function Segmented<T extends string | number>({label, options, value, onChange}: {label: string; options: Opt<T>[]; value: T; onChange: (v: T) => void}) {
  const t = useT();
  return (
    <div className="dm-set-row">
      <span className="dm-set-label">{label}</span>
      <div className="segmented segmented-sm" role="radiogroup" aria-label={label}>
        {options.map(o => (
          <button key={String(o.value)} type="button" role="radio" aria-checked={value === o.value} className="segmented-item" onClick={() => onChange(o.value)}>
            {t(o.label)}
          </button>
        ))}
      </div>
    </div>
  );
}

const BLOCKS: {key: 'blockTop' | 'blockBottom' | 'blockScroll' | 'blockColor' | 'blockRepeat' | 'blockSpoiler'; label: MessageKey}[] = [
  {key: 'blockTop', label: 'danmaku.blockTop'},
  {key: 'blockBottom', label: 'danmaku.blockBottom'},
  {key: 'blockScroll', label: 'danmaku.blockScroll'},
  {key: 'blockColor', label: 'danmaku.blockColor'},
  {key: 'blockRepeat', label: 'danmaku.blockRepeat'},
  {key: 'blockSpoiler', label: 'danmaku.blockSpoiler'},
];

const LANG_NAMES: Record<DanmakuLang, MessageKey> = {
  zh: 'danmaku.langZh',
  ja: 'danmaku.langJa',
  ko: 'danmaku.langKo',
  en: 'danmaku.langEn',
  es: 'danmaku.langEs',
  vi: 'danmaku.langVi',
  th: 'danmaku.langTh',
  und: 'danmaku.langOther',
};

/** 语言过滤: 全部, or any set of languages (all of them selected again means 全部). */
function LangFilter({langs}: {langs: DanmakuLang[]}) {
  const t = useT();
  const toggle = (l: DanmakuLang) => {
    const next = langs.includes(l) ? langs.filter(x => x !== l) : DANMAKU_LANGS.filter(x => x === l || langs.includes(x));
    setDanmakuPref('langs', next.length === DANMAKU_LANGS.length ? [] : next);
  };
  return (
    <div className="dm-set-block">
      <span className="dm-set-text">
        <span className="dm-set-label" id="dm-lang-label">
          {t('danmaku.langFilter')}
        </span>
        <span className="dm-set-desc">{t('danmaku.langFilterDesc')}</span>
      </span>
      <div className="chip-row chip-row-wrap" role="group" aria-labelledby="dm-lang-label">
        <button type="button" className={langs.length === 0 ? 'chip chip-on' : 'chip'} aria-pressed={langs.length === 0} onClick={() => setDanmakuPref('langs', [])}>
          {langs.length === 0 && <Icon name="check" size={14} />}
          {t('danmaku.langAll')}
        </button>
        {DANMAKU_LANGS.map(l => {
          const on = langs.includes(l);
          return (
            <button key={l} type="button" className={on ? 'chip chip-on' : 'chip'} aria-pressed={on} onClick={() => toggle(l)}>
              {on && <Icon name="check" size={14} />}
              {t(LANG_NAMES[l])}
            </button>
          );
        })}
      </div>
    </div>
  );
}

/** 屏蔽词: the viewer's own list; danmaku containing a word are hidden. */
function BlockWords() {
  const t = useT();
  const {status} = useAuth();
  const words = useDanmakuBlockWords();
  const [draft, setDraft] = useState('');
  const [error, setError] = useState('');
  const add = () => {
    const word = draft.trim();
    if (!word) return;
    if (Array.from(word).length > MAX_BLOCK_WORD_LENGTH) setError(t('danmaku.blockWordTooLong', {n: MAX_BLOCK_WORD_LENGTH}));
    else if (words.length >= MAX_BLOCK_WORDS) setError(t('danmaku.blockWordsFull', {n: MAX_BLOCK_WORDS}));
    else if (!addBlockWord(word)) setError(t('danmaku.blockWordDuplicate'));
    else {
      setDraft('');
      setError('');
    }
  };
  return (
    <div className="dm-set-block">
      <span className="dm-set-text">
        <span className="dm-set-label">{t('danmaku.blockWords')}</span>
        <span className="dm-set-desc">{status === 'user' ? t('danmaku.blockWordsSynced') : t('danmaku.blockWordsLocal')}</span>
      </span>
      <form
        className="dm-word-form"
        onSubmit={event => {
          event.preventDefault();
          add();
        }}
      >
        <label className="sr-only" htmlFor="dm-word-input">
          {t('danmaku.blockWordInput')}
        </label>
        <input
          id="dm-word-input"
          className="dm-word-input"
          type="text"
          value={draft}
          placeholder={t('danmaku.blockWordPlaceholder')}
          autoComplete="off"
          enterKeyHint="done"
          aria-invalid={!!error || undefined}
          aria-describedby={error ? 'dm-word-error' : undefined}
          onChange={e => {
            setDraft(e.target.value);
            if (error) setError('');
          }}
        />
        <button type="submit" className="btn btn-sm" disabled={!draft.trim()}>
          {t('danmaku.blockWordAdd')}
        </button>
      </form>
      {error && (
        <p className="dm-error" id="dm-word-error" role="alert">
          {error}
        </p>
      )}
      {words.length > 0 && (
        <ul className="dm-words" aria-label={t('danmaku.blockWordsCount', {n: words.length, max: MAX_BLOCK_WORDS})}>
          {words.map(w => (
            <li key={w} className="dm-word">
              <span>{w}</span>
              <button type="button" aria-label={t('danmaku.blockWordRemove', {word: w})} onClick={() => removeBlockWord(w)}>
                <Icon name="close" size={14} />
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

const COLOR_NAMES: Record<string, MessageKey> = {
  '#FFFFFF': 'danmaku.colorWhite',
  '#FE0302': 'danmaku.colorRed',
  '#FFAA02': 'danmaku.colorOrange',
  '#FFE133': 'danmaku.colorYellow',
  '#00CD00': 'danmaku.colorGreen',
  '#00A1D6': 'danmaku.colorBlue',
  '#CC0273': 'danmaku.colorPink',
};

export function DanmakuSettings() {
  const t = useT();
  const p = useDanmakuPrefs();
  return (
    <div className="dm-settings">
      <div className="dm-set-row">
        <span className="dm-set-label">{t('danmaku.show')}</span>
        <Switch checked={p.enabled} label={t('danmaku.show')} onChange={v => setDanmakuPref('enabled', v)} />
      </div>
      <Segmented
        label={t('danmaku.opacity')}
        value={p.opacity}
        onChange={v => setDanmakuPref('opacity', v)}
        options={[
          {value: 1, label: 'danmaku.opacity100'},
          {value: 0.85, label: 'danmaku.opacity85'},
          {value: 0.6, label: 'danmaku.opacity60'},
          {value: 0.4, label: 'danmaku.opacity40'},
        ]}
      />
      <Segmented
        label={t('danmaku.size')}
        value={p.size}
        onChange={v => setDanmakuPref('size', v)}
        options={[
          {value: 'small', label: 'danmaku.sizeSmall'},
          {value: 'normal', label: 'danmaku.sizeNormal'},
          {value: 'large', label: 'danmaku.sizeLarge'},
        ]}
      />
      <Segmented
        label={t('danmaku.speed')}
        value={p.speed}
        onChange={v => setDanmakuPref('speed', v)}
        options={[
          {value: 'slow', label: 'danmaku.speedSlow'},
          {value: 'normal', label: 'danmaku.speedNormal'},
          {value: 'fast', label: 'danmaku.speedFast'},
        ]}
      />
      <Segmented
        label={t('danmaku.area')}
        value={p.area}
        onChange={v => setDanmakuPref('area', v)}
        options={[
          {value: 0.25, label: 'danmaku.areaQuarter'},
          {value: 0.5, label: 'danmaku.areaHalf'},
          {value: 1, label: 'danmaku.areaFull'},
        ]}
      />
      <Segmented
        label={t('danmaku.density')}
        value={p.density}
        onChange={v => setDanmakuPref('density', v)}
        options={[
          {value: 'low', label: 'danmaku.densityLow'},
          {value: 'medium', label: 'danmaku.densityMedium'},
          {value: 'high', label: 'danmaku.densityHigh'},
        ]}
      />
      <div className="dm-set-block">
        <span className="dm-set-label" id="dm-block-label">
          {t('danmaku.block')}
        </span>
        <div className="chip-row chip-row-wrap" role="group" aria-labelledby="dm-block-label">
          {BLOCKS.map(b => (
            <button key={b.key} type="button" className={p[b.key] ? 'chip chip-on' : 'chip'} aria-pressed={p[b.key]} onClick={() => setDanmakuPref(b.key, !p[b.key])}>
              {p[b.key] && <Icon name="check" size={14} />}
              {t(b.label)}
            </button>
          ))}
        </div>
      </div>
      <LangFilter langs={p.langs} />
      <BlockWords />
      <div className="dm-set-row">
        <span className="dm-set-text">
          <span className="dm-set-label">{t('danmaku.antiSubtitle')}</span>
          <span className="dm-set-desc">{t('danmaku.antiSubtitleDesc')}</span>
        </span>
        <Switch checked={p.antiSubtitle} label={t('danmaku.antiSubtitle')} onChange={v => setDanmakuPref('antiSubtitle', v)} />
      </div>
      <div className="dm-set-row">
        <span className="dm-set-text">
          <span className="dm-set-label">{t('danmaku.showPeaks')}</span>
          <span className="dm-set-desc">{t('danmaku.showPeaksDesc')}</span>
        </span>
        <Switch checked={p.showPeaks} label={t('danmaku.showPeaks')} onChange={v => setDanmakuPref('showPeaks', v)} />
      </div>
      <h3 className="dm-set-head">{t('danmaku.sendStyle')}</h3>
      <Segmented
        label={t('danmaku.mode')}
        value={p.sendMode}
        onChange={v => setDanmakuPref('sendMode', v)}
        options={[
          {value: 'scroll', label: 'danmaku.modeScroll'},
          {value: 'top', label: 'danmaku.modeTop'},
          {value: 'bottom', label: 'danmaku.modeBottom'},
        ]}
      />
      <div className="dm-set-block">
        <span className="dm-set-label" id="dm-color-label">
          {t('danmaku.color')}
        </span>
        <div className="dm-colors" role="radiogroup" aria-labelledby="dm-color-label">
          {DANMAKU_COLORS.map(c => (
            <button
              key={c}
              type="button"
              role="radio"
              aria-checked={p.sendColor === c}
              aria-label={t(COLOR_NAMES[c])}
              className="dm-swatch"
              onClick={() => setDanmakuPref('sendColor', c)}
            >
              <span style={{background: c}} />
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
