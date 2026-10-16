import type {LangCode} from './api';

// Launch languages (PRD 8; store.SettingsLangs), each in its own script.
export const LANG_LABELS: Record<LangCode, string> = {
  'zh-CN': '简体中文',
  'zh-TW': '繁體中文',
  en: 'English',
  ja: '日本語',
  ko: '한국어',
  vi: 'Tiếng Việt',
  es: 'Español',
  th: 'ไทย',
};
export const LANGS = Object.keys(LANG_LABELS) as LangCode[];

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

// ---- URL language prefixes (PRD 8 / tech plan 3.10) ----
//
// Every app page except the back office has one URL per language:
// /releases/x is 简体中文 (the default, also x-default), /en/releases/x,
// /zh-TW/releases/x, ... /zh-CN/... is not a URL of its own: it answers 301
// to the unprefixed path. The UI language of a page is always the one in its
// URL; preferences only decide where links and the unprefixed entry go.

/** Paths that never take a language prefix: back office, API, files. */
const UNPREFIXED = /^\/(admin|api|assets|sitemaps?)(\/|$|\.|-)|^\/(robots\.txt|sitemap\.xml|favicon\.ico|manifest\.webmanifest|sw\.js)$/;

export const isPrefixable = (path: string) => path.startsWith('/') && !path.startsWith('//') && !UNPREFIXED.test(path);

/** A first path segment that looks like a language tag (en, zh-tw, EN-us). */
const TAG_SHAPE = /^[a-z]{2}(-[a-z0-9]{2,4})?$/i;

export type LangParam =
  | {kind: 'none'}
  | {kind: 'ok'; lang: LangCode}
  /** A non-canonical spelling (zh-tw, EN, zh-CN): 301 to the canonical URL. */
  | {kind: 'redirect'; lang: LangCode}
  /** Not a language at all (/foo or /foo/releases/x): 404. */
  | {kind: 'bad'};

/** Classifies the :lang? route param. */
export function parseLangParam(raw: string | undefined): LangParam {
  if (raw === undefined || raw === '') return {kind: 'none'};
  if (isLang(raw)) return raw === DEFAULT_LANG ? {kind: 'redirect', lang: DEFAULT_LANG} : {kind: 'ok', lang: raw};
  if (!TAG_SHAPE.test(raw)) return {kind: 'bad'};
  const lang = normalizeLang(raw);
  return lang ? {kind: 'redirect', lang} : {kind: 'bad'};
}

/** Splits /en/releases/x into {lang: 'en', rest: '/releases/x'}; unprefixed paths give lang null. */
export function splitLangPath(pathname: string): {lang: LangCode | null; rest: string} {
  const m = /^\/([^/]+)(\/.*)?$/.exec(pathname);
  if (m && isLang(m[1])) return {lang: m[1], rest: m[2] || '/'};
  return {lang: null, rest: pathname || '/'};
}

/** Path without any language prefix. */
export const stripLang = (pathname: string) => splitLangPath(pathname).rest;

/** /releases/x in lang: the default language stays unprefixed; the home page is /en. */
export function localizePath(path: string, lang: LangCode): string {
  if (!isPrefixable(path)) return path;
  const rest = stripLang(path.replace(/[?#].*$/, ''));
  const tail = path.slice(path.replace(/[?#].*$/, '').length);
  if (lang === DEFAULT_LANG) return rest + tail;
  return `/${lang}${rest === '/' ? '' : rest}${tail}`;
}

// ---- Language cookie ----
//
// `lang` records the visitor's last language choice (the switcher, or an
// entry URL with a prefix). The SSR server reads it to send an unprefixed
// entry URL to the visitor's language (302, Vary: Cookie). Crawlers send no
// cookie and always get the URL's own language.

export const LANG_COOKIE = 'lang';

export function readLangCookie(header: string | null | undefined): LangCode | null {
  if (!header) return null;
  for (const part of header.split(';')) {
    const [k, ...v] = part.trim().split('=');
    if (k === LANG_COOKIE) {
      const value = decodeURIComponent(v.join('='));
      return isLang(value) ? value : null;
    }
  }
  return null;
}

/** The language of a route loader's request; null when the prefix is invalid (the root loader answers). */
export function routeLang(params: {lang?: string}): LangCode | null {
  const p = parseLangParam(params.lang);
  if (p.kind === 'ok') return p.lang;
  return p.kind === 'none' ? DEFAULT_LANG : null;
}

export function writeLangCookie(lang: LangCode) {
  try {
    document.cookie = `${LANG_COOKIE}=${encodeURIComponent(lang)}; Path=/; Max-Age=31536000; SameSite=Lax`;
  } catch {
    /* cookies disabled */
  }
}

/** The back office (/admin, /admin/…; also under a language prefix): staff pages without the public site chrome. */
export const isAdminPath = (pathname: string): boolean => /^\/admin(\/|$)/.test(stripLang(pathname) || '/');
