import {createContext, Fragment, useCallback, useContext, useEffect, useMemo, type ReactNode} from 'react';
import {useLocation, useNavigate} from 'react-router';
import {setUiLang as setAnalyticsLang} from './analytics';
import {setContentLang as setApiContentLang, type LangCode} from './api';
import {setFormatLocale} from './format';
import {expandPlurals} from './plural';
import {withBundled} from './locales';
import {DEFAULT_LANG, isLang, isPrefixable, localizePath, normalizeLang, splitLangPath, stripLang, writeLangCookie} from './langs';
import {zhCN, type MessageKey} from './messages.zh-CN';
import {isServer, renderScope} from './scope';
import {useSettings} from './settings';

// Multi-language UI (PRD 8; tech plan 3.10). Three independent layers:
//   - UI language (ui_lang): these strings, <html lang>, Intl formatting;
//   - content language (content_lang): sent as ?lang= on catalog calls, and
//     display_* fields are shown when the backend returns them;
//   - audio / subtitle languages: settings.subtitle_langs (player).
//
// URL language prefixes (server-rendered since PRD 13 SEO): every app page
// has one URL per language (lib/langs.ts: /releases/x is 简体中文, /en/...,
// /zh-TW/...). The UI language of a page is the language in its URL, so the
// server, the crawler and the hydrating browser all render the same strings.
// The root route's loader resolves the language and its UI dictionary and
// passes both to I18nProvider; links keep the current prefix (lib/nav.tsx).
//
// Preferences decide where you land, not what a URL shows:
//   - the switcher (TopBar, settings) navigates to the same page in the
//     chosen language, stores it in the `lang` cookie and, signed in,
//     PATCHes ui_lang;
//   - an entry URL with a prefix stores its language in the cookie;
//   - an unprefixed URL with a non-default cookie answers 302 to the
//     visitor's language on the server (root loader); a signed-in user
//     without the cookie is sent to settings.ui_lang once settings load.
//
// Content language follows the URL as well, except for a signed-in user who
// picked a content language different from their UI language in settings.
//
// UI strings: the bundled zh-CN dictionary (messages.zh-CN.ts) is the source
// and the fallback for every key. Other languages start from their bundled
// translations (app/locales/{lang}.json, lib/locales.ts; zh-TW has none yet)
// overlaid with reviewed strings from GET /api/i18n/ui/{lang}
// (a flat key → string map of reviewed strings): on the server through a
// short in-memory cache (lib/api.server.ts), in the browser through
// localStorage with its ETag, revalidated with If-None-Match. A 404 or a
// missing key falls back to zh-CN, so the page never shows an empty label.

export type Messages = Partial<Record<string, string>>;

const UI_KEY = 'ui_lang';
const DICT_PREFIX = 'i18n_ui_v1_';

export {DEFAULT_LANG, isLang, normalizeLang};

function writeLocal(key: string, value: LangCode) {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    /* not persisted */
  }
}

// ---- Module state (browser), so non-React helpers can translate too ----

let currentDict: Messages = {};
let currentLang: LangCode = DEFAULT_LANG;

function format(template: string, vars: Record<string, string | number> | undefined, lang: LangCode): string {
  // Plural blocks first ({n, plural, one {...} other {...}}, lib/plural.ts),
  // then the plain {placeholders}, including those inside the chosen branch.
  template = expandPlurals(template, vars, lang);
  if (!vars) return template;
  return template.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m));
}

/** Translate with the current UI language; falls back to zh-CN, then the key. */
export function t(key: MessageKey, vars?: Record<string, string | number>): string {
  const dict = renderScope()?.dict ?? currentDict;
  const value = dict[key];
  return format(typeof value === 'string' && value ? value : zhCN[key] ?? key, vars, renderScope()?.lang ?? currentLang);
}

export const uiLang = (): LangCode => renderScope()?.lang ?? currentLang;

// ---- Remote dictionary with ETag cache (browser) ----

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

export function cleanStrings(data: unknown): Messages {
  const out: Messages = {};
  if (data && typeof data === 'object' && !Array.isArray(data)) {
    for (const [k, v] of Object.entries(data as Record<string, unknown>)) if (typeof v === 'string' && v) out[k] = v;
  }
  return out;
}

const missing = new Set<LangCode>();

/** GET /api/i18n/ui/{lang}; null means "use what you have" (304, 404, offline). */
async function fetchDict(lang: LangCode, cached: Cached | null, signal?: AbortSignal): Promise<Cached | null> {
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

/**
 * The UI dictionary for a client-side navigation into `lang` (root
 * clientLoader). A cached copy answers at once and is revalidated in the
 * background for the next visit; otherwise the request is awaited.
 */
export async function loadUiDict(lang: LangCode): Promise<Messages> {
  if (lang === DEFAULT_LANG) return {};
  if (lang === currentLang) return currentDict;
  const cached = readDict(lang);
  if (cached) {
    void fetchDict(lang, cached).then(fresh => fresh && writeDict(lang, fresh));
    return withBundled(lang, cached.strings);
  }
  const fresh = await fetchDict(lang, null);
  if (fresh) writeDict(lang, fresh);
  return withBundled(lang, fresh?.strings ?? {});
}

/** Keeps the server-rendered dictionary for the next client-side visit. */
function rememberDict(lang: LangCode, strings: Messages) {
  if (lang === DEFAULT_LANG || Object.keys(strings).length === 0) return;
  const cached = readDict(lang);
  if (!cached) writeDict(lang, {etag: '', strings});
}

// ---- Provider ----

type I18nValue = {
  uiLang: LangCode;
  contentLang: LangCode;
  /** Bumps when strings change, so memoized consumers can re-render. */
  version: number;
  t: typeof t;
  /** Switcher (TopBar, settings): same page in `lang`, remembered as the preference. */
  setUiLang: (lang: LangCode) => void;
};

const I18nContext = createContext<I18nValue>({
  uiLang: DEFAULT_LANG,
  contentLang: DEFAULT_LANG,
  version: 0,
  t,
  setUiLang: () => {},
});

function applyGlobals(ui: LangCode, content: LangCode, dict: Messages) {
  if (isServer) return; // the server reads the render scope instead
  currentLang = ui;
  currentDict = dict;
  setAnalyticsLang(ui);
  setFormatLocale(ui);
  setApiContentLang(content);
  document.documentElement.lang = ui;
}

let dictVersion = 0;
let lastDict: Messages | null = null;

/**
 * Browser: switch the strings and formatting locale before anything renders
 * with them: the root Layout calls it (its <Meta> runs before I18nProvider),
 * and so does the root clientLoader before a client-side navigation.
 */
export function primeLang(lang: LangCode, dict: Messages) {
  if (isServer) return;
  currentLang = lang;
  currentDict = dict;
  setFormatLocale(lang);
}

export type I18nRouteData = {
  /** The page's language (from the URL; the default for unprefixed and back-office pages). */
  lang: LangCode;
  dict: Messages;
  /** A `lang` cookie was present when the page loaded. */
  hadChoice: boolean;
};

export function I18nProvider({route: data, children}: {route: I18nRouteData; children: ReactNode}) {
  const {settings, save} = useSettings();
  const navigate = useNavigate();
  const location = useLocation();
  const route = {
    ...data,
    // The page takes language prefixes at all (false in the back office).
    localized: isPrefixable(location.pathname),
    prefixed: splitLangPath(location.pathname).lang !== null,
  };
  const ui = route.lang;
  // A signed-in user's separate content language (settings) wins over the URL.
  const content: LangCode =
    settings && isLang(settings.content_lang) && settings.content_lang !== settings.ui_lang ? settings.content_lang : ui;

  if (route.dict !== lastDict) {
    lastDict = route.dict;
    dictVersion += 1;
  }
  // Globals are set during render so children rendered in this pass already
  // use the right strings, locale and ?lang=.
  applyGlobals(ui, content, route.dict);

  // An entry URL with a prefix is a language choice.
  useEffect(() => {
    if (!route.localized || !route.prefixed) return;
    writeLangCookie(ui);
    writeLocal(UI_KEY, ui);
  }, [ui, route.localized, route.prefixed]);

  useEffect(() => rememberDict(ui, route.dict), [ui, route.dict]);

  // Signed in on an unprefixed URL with no cookie yet: follow settings.ui_lang once.
  const settingsUi = settings?.ui_lang;
  useEffect(() => {
    if (!route.localized || route.prefixed || route.hadChoice || !isLang(settingsUi) || settingsUi === ui) return;
    navigate(localizePath(stripLang(location.pathname), settingsUi) + location.search + location.hash, {replace: true, state: location.state});
    // Only when settings arrive.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [settingsUi]);

  const setUiLang = useCallback(
    (lang: LangCode) => {
      writeLangCookie(lang);
      writeLocal(UI_KEY, lang);
      if (settings && settings.ui_lang !== lang) save('ui_lang', {ui_lang: lang}, s => ({...s, ui_lang: lang}));
      if (lang === ui) return;
      const target = route.localized ? localizePath(stripLang(location.pathname), lang) : location.pathname;
      navigate(target + location.search + location.hash, {state: location.state});
    },
    [settings, save, ui, route.localized, location, navigate],
  );

  const version = dictVersion;
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
