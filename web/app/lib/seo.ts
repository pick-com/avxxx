import type {MetaDescriptor} from 'react-router';
import type {LangCode} from './api';
import {t} from './i18n';
import {DEFAULT_LANG, LANGS, localizePath} from './langs';

// Route `meta` helpers for the server-rendered public pages (PRD 13 SEO):
// title, description, canonical, hreflang alternates (+ x-default), Open
// Graph / Twitter cards, robots and JSON-LD.
//
// Robots: routes whose handle sets `seo: true` (home, release, performer,
// discover, legal) control robots themselves through seoMeta(); every other
// page (search, settings, library pages, login, admin, 404) gets
// `noindex` from the root Layout (root.tsx RobotsMeta).

export type SeoHandle = {seo?: boolean};

/** The root loader's data, as seen from a route's meta function. */
export type RootMetaData = {lang: LangCode; origin: string; notFound: boolean};

type MatchLike = {id: string; data?: unknown; loaderData?: unknown} | undefined;

export function rootMeta(matches: readonly MatchLike[]): RootMetaData {
  const root = matches.find(m => m?.id === 'root');
  const d = (root?.loaderData ?? root?.data) as Partial<RootMetaData> | undefined;
  return {lang: d?.lang ?? DEFAULT_LANG, origin: d?.origin ?? '', notFound: !!d?.notFound};
}

const abs = (origin: string, path: string) => `${origin}${path}`;

/** Absolute URL of `path` (unprefixed, may carry a query) in `lang`. */
export const pageUrl = (origin: string, path: string, lang: LangCode) => abs(origin, localizePath(path, lang));

/** hreflang alternates for every launch language plus x-default (the unprefixed zh-CN URL). */
export function alternates(origin: string, path: string): MetaDescriptor[] {
  return [
    ...LANGS.map(l => ({tagName: 'link', rel: 'alternate', hrefLang: l, href: pageUrl(origin, path, l)})),
    {tagName: 'link', rel: 'alternate', hrefLang: 'x-default', href: pageUrl(origin, path, DEFAULT_LANG)},
  ];
}

/** Open Graph locale (zh_CN, en_US style is not needed; language_TERRITORY when known). */
const OG_LOCALE: Record<LangCode, string> = {
  'zh-CN': 'zh_CN',
  'zh-TW': 'zh_TW',
  en: 'en_US',
  ja: 'ja_JP',
  ko: 'ko_KR',
  vi: 'vi_VN',
  es: 'es_ES',
  th: 'th_TH',
};

export function clip(text: string | null | undefined, max = 160): string {
  const s = (text ?? '').replace(/\s+/g, ' ').trim();
  if (s.length <= max) return s;
  return `${s.slice(0, max - 1).trimEnd()}…`;
}

export type SeoInput = {
  matches: readonly MatchLike[];
  /** Unprefixed path, with the query when it identifies the page (/discover?tag=x). */
  path: string;
  /** Full document title. */
  title: string;
  description?: string;
  /** Absolute image URL (cover / portrait) when one exists. */
  image?: string | null;
  ogType?: 'website' | 'video.movie' | 'profile' | 'article';
  noindex?: boolean;
  jsonLd?: Record<string, unknown>[];
};

export function seoMeta(input: SeoInput): MetaDescriptor[] {
  const {lang, origin, notFound} = rootMeta(input.matches);
  if (notFound) return [{title: t('title.notFound')}, {name: 'robots', content: 'noindex, follow'}];
  const out: MetaDescriptor[] = [{title: input.title}];
  const description = clip(input.description);
  if (description) out.push({name: 'description', content: description});
  if (input.noindex) {
    out.push({name: 'robots', content: 'noindex, follow'});
    return out;
  }
  const url = pageUrl(origin, input.path, lang);
  out.push({tagName: 'link', rel: 'canonical', href: url});
  out.push(...alternates(origin, input.path));
  out.push(
    {property: 'og:site_name', content: t('seo.siteName')},
    {property: 'og:type', content: input.ogType ?? 'website'},
    {property: 'og:title', content: input.title},
    {property: 'og:url', content: url},
    {property: 'og:locale', content: OG_LOCALE[lang]},
    {name: 'twitter:card', content: input.image ? 'summary_large_image' : 'summary'},
    {name: 'twitter:title', content: input.title},
  );
  if (description) out.push({property: 'og:description', content: description}, {name: 'twitter:description', content: description});
  if (input.image) out.push({property: 'og:image', content: input.image}, {name: 'twitter:image', content: input.image});
  for (const ld of input.jsonLd ?? []) out.push({'script:ld+json': ld});  return out;
}

/** schema.org BreadcrumbList from [name, unprefixed path] pairs. */
export function breadcrumbs(matches: readonly MatchLike[], items: [string, string][]): Record<string, unknown> {
  const {lang, origin} = rootMeta(matches);
  return {
    '@context': 'https://schema.org',
    '@type': 'BreadcrumbList',
    itemListElement: items.map(([name, path], i) => ({'@type': 'ListItem', position: i + 1, name, item: pageUrl(origin, path, lang)})),
  };
}

/** ISO 8601 duration for whole minutes (PT125M). */
export const isoMinutes = (minutes: number | null | undefined) => (minutes && minutes > 0 ? `PT${Math.round(minutes)}M` : undefined);

/** YYYY-MM-DD from an API date (2024-05-24 or 2024-05-24T00:00:00Z). */
export const isoDate = (value: string | null | undefined) => (value && /^\d{4}-\d{2}-\d{2}/.test(value) ? value.slice(0, 10) : undefined);

/** Drops undefined / null / empty values so JSON-LD only states what is known. */
export function compact<T extends Record<string, unknown>>(o: T): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(o)) {
    if (v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)) continue;
    out[k] = v;
  }
  return out;
}
