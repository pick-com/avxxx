import {createContext, Fragment, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode} from 'react';
import {setUiLang as setAnalyticsLang} from './analytics';
import {setContentLang as setApiContentLang, tokens, type LangCode} from './api';
import {setFormatLocale} from './format';
import {LANGS} from './langs';
import {zhCN, type MessageKey} from './messages.zh-CN';
import {useSettings} from './settings';

// Multi-language UI (PRD 8; tech plan 3.10). Three independent layers:
//   - UI language (ui_lang): these strings, <html lang>, Intl formatting;
//   - content language (content_lang): sent as ?lang= on catalog calls, and
//     display_* fields are shown when the backend returns them;
//   - audio / subtitle languages: settings.subtitle_langs (player).
//
// Signed-in users take ui_lang / content_lang from GET /api/me/settings and
// the TopBar switcher PATCHes ui_lang. Visitors keep one choice in this
// browser (it sets both layers). The last values are cached so the first
// render already uses them.
//
// UI strings: the bundled zh-CN dictionary (messages.zh-CN.ts) is the source
// and the fallback for every key. Other languages load GET /api/i18n/ui/{lang}
// (a flat key → string map of reviewed strings), cached in localStorage with
// its ETag and revalidated with If-None-Match. A 404 (backend without the
// i18n routes, or an unsupported language) or a missing key falls back to
// zh-CN, so the page never shows an empty label.
//
// URL language prefixes: /en/... (any supported language) is accepted as an
// entry URL (routes/lang-prefix.tsx): it switches the UI language and drops
// the prefix. Keeping the prefix in every link and emitting hreflang
// alternates waits for SSR (react-router.config.ts ssr: false): in SPA mode
// every URL is served the same index.html, so crawlers would not see
// per-language <link rel="alternate" hreflang> or localized markup anyway.

export type Messages = Partial<Record<string, string>>;

const UI_KEY = 'ui_lang';
const CONTENT_KEY = 'content_lang';
const DICT_PREFIX = 'i18n_ui_v1_';

export const DEFAULT_LANG: LangCode = 'zh-CN';

export function isLang(v: unknown): v is LangCode {
  return typeof v === 'string' && (LANGS as string[]).includes(v);
}

/** Map a tag such as zh-Hant, zh-HK, en-US to a launch language (internal/i18n.Normalize). */
export function normalizeLang(tag: string | null | undefined): LangCode | null {
  const t = (tag ?? '').trim().toLowerCase().replace(/_/g, '-');
  if (!t) return null;
  const parts = t.split('-');
  if (parts[0] === 'zh') return parts.slice(1).some(p => ['hant', 'tw', 'hk', 'mo'].includes(p)) ? 'zh-TW' : 'zh-CN';
  return LANGS.find(l => l.toLowerCase() === parts[0]) ?? null;
}

function readLocal(key: string): LangCode | null {
  try {
    const v = window.localStorage.getItem(key);
    return isLang(v) ? v : null;
  } catch {
    return null;
  }
}

function writeLocal(key: string, value: LangCode) {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    /* not persisted */
  }
}

/** Language picked by a /{lang}/... entry URL before the provider mounted. */
export function rememberUiLang(lang: LangCode) {
  writeLocal(UI_KEY, lang);
  writeLocal(CONTENT_KEY, lang);
}

// ---- Module state, so non-React helpers can translate too ----

let currentDict: Messages = {};
let currentLang: LangCode = DEFAULT_LANG;

function format(template: string, vars?: Record<string, string | number>): string {
  if (!vars) return template;
  return template.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m));
}

/** Translate with the current UI language; falls back to zh-CN, then the key. */
export function t(key: MessageKey, vars?: Record<string, string | number>): string {
  const value = currentDict[key];
  return format(typeof value === 'string' && value ? value : zhCN[key] ?? key, vars);
}

export const uiLang = () => currentLang;

// ---- Remote dictionary with ETag cache ----

type Cached = {etag: string; strings: Messages};

function readDict(lang: LangCode): Cached | null {
  try {
    const raw = window.localStorage.getItem(DICT_PREFIX + lang);
    if (!raw) return null;
    const v = JSON.parse(raw) as Cached;
    return v && typeof v.strings === 'object' ? v : null;
  } catch {
    return null;
  }
}

function writeDict(lang: LangCode, value: Cached) {
  try {
    window.localStorage.setItem(DICT_PREFIX + lang, JSON.stringify(value));
  } catch {
    /* quota: the next visit fetches again */
  }
}

function cleanStrings(data: unknown): Messages {
  const out: Messages = {};
  if (data && typeof data === 'object' && !Array.isArray(data)) {
    for (const [k, v] of Object.entries(data as Record<string, unknown>)) if (typeof v === 'string' && v) out[k] = v;
  }
  return out;
}

const missing = new Set<LangCode>();

/** GET /api/i18n/ui/{lang}; null means "use what you have" (304, 404, offline). */
async function fetchDict(lang: LangCode, cached: Cached | null, signal: AbortSignal): Promise<Cached | null> {
  if (missing.has(lang)) return null;
  const headers = new Headers({Accept: 'application/json'});
  if (cached?.etag) headers.set('If-None-Match', cached.etag);
  let response: Response;
  try {
    response = await fetch(`/api/i18n/ui/${encodeURIComponent(lang)}`, {headers, signal});
  } catch {
    return null;
  }
  if (response.status === 304) return null;
  if (!response.ok) {
    if (response.status === 404 || response.status === 405 || response.status === 501) missing.add(lang);
    return null;
  }
  let data: unknown;
  try {
    data = await response.json();
  } catch {
    return null;
  }
  return {etag: response.headers.get('ETag') ?? '', strings: cleanStrings(data)};
}

// ---- Provider ----

type I18nValue = {
  uiLang: LangCode;
  contentLang: LangCode;
  /** Bumps when strings arrive, so memoized consumers can re-render. */
  version: number;
  t: typeof t;
  /** TopBar switcher: UI language (visitors: both layers). */
  setUiLang: (lang: LangCode) => void;
};

const I18nContext = createContext<I18nValue>({
  uiLang: DEFAULT_LANG,
  contentLang: DEFAULT_LANG,
  version: 0,
  t,
  setUiLang: () => {},
});

function applyGlobals(ui: LangCode, content: LangCode) {
  currentLang = ui;
  setAnalyticsLang(ui);
  setFormatLocale(ui);
  setApiContentLang(content);
  if (typeof document !== 'undefined') document.documentElement.lang = ui;
}

export function I18nProvider({children}: {children: ReactNode}) {
  const {settings, save, status} = useSettings();
  // A choice made while a signed-in user's settings are still loading (e.g. a
  // /ja/... entry URL) is PATCHed once they arrive instead of being overridden.
  const pendingUi = useRef<LangCode | null>(null);
  // The SPA never server-renders the app tree (only HydrateFallback), so the
  // stored choice can be read on the first render. With SSR this would come
  // from the URL prefix or a cookie instead.
  const [local, setLocal] = useState<{ui: LangCode; content: LangCode} | null>(() => {
    if (typeof window === 'undefined') return null;
    const ui = readLocal(UI_KEY) ?? DEFAULT_LANG;
    return {ui, content: readLocal(CONTENT_KEY) ?? ui};
  });
  const [version, setVersion] = useState(0);

  const ui: LangCode = (isLang(settings?.ui_lang) ? settings.ui_lang : null) ?? local?.ui ?? DEFAULT_LANG;
  const content: LangCode = (isLang(settings?.content_lang) ? settings.content_lang : null) ?? local?.content ?? ui;

  // Globals are set during render so children rendered in this pass already
  // use the right locale and ?lang=.
  applyGlobals(ui, content);

  // Keep the synced values for the next visit's first render.
  useEffect(() => {
    if (!settings) return;
    const pending = pendingUi.current;
    if (pending) {
      pendingUi.current = null;
      if (settings.ui_lang !== pending) save('ui_lang', {ui_lang: pending}, s => ({...s, ui_lang: pending}));
      return;
    }
    if (isLang(settings.ui_lang)) writeLocal(UI_KEY, settings.ui_lang);
    if (isLang(settings.content_lang)) writeLocal(CONTENT_KEY, settings.content_lang);
  }, [settings]);

  useEffect(() => {
    if (ui === DEFAULT_LANG) {
      currentDict = {};
      setVersion(v => v + 1);
      return;
    }
    const cached = readDict(ui);
    currentDict = cached?.strings ?? {};
    setVersion(v => v + 1);
    const controller = new AbortController();
    fetchDict(ui, cached, controller.signal).then(fresh => {
      if (!fresh || controller.signal.aborted) return;
      writeDict(ui, fresh);
      currentDict = fresh.strings;
      setVersion(v => v + 1);
    });
    return () => controller.abort();
  }, [ui]);

  const setUiLang = useCallback(
    (lang: LangCode) => {
      if (settings) {
        save('ui_lang', {ui_lang: lang}, s => ({...s, ui_lang: lang}));
        writeLocal(UI_KEY, lang);
        return;
      }
      if (status !== 'missing' && status !== 'error' && tokens.user()) {
        pendingUi.current = lang;
        writeLocal(UI_KEY, lang);
        setLocal(prev => ({ui: lang, content: prev?.content ?? lang}));
        return;
      }
      writeLocal(UI_KEY, lang);
      writeLocal(CONTENT_KEY, lang);
      setLocal({ui: lang, content: lang});
    },
    [settings, save, status],
  );

  const value = useMemo(() => ({uiLang: ui, contentLang: content, version, t, setUiLang}), [ui, content, version, setUiLang]);
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export const useI18n = () => useContext(I18nContext);

/** t() that re-renders the caller when strings arrive. */
export function useT(): typeof t {
  useContext(I18nContext);
  return t;
}

/**
 * Remounts its children when the UI or content language changes, so every
 * page refetches with the new ?lang= and re-formats dates and numbers.
 */
export function LangBoundary({children}: {children: ReactNode}) {
  const {uiLang: ui, contentLang: content} = useI18n();
  return <Fragment key={`${ui}|${content}`}>{children}</Fragment>;
}
