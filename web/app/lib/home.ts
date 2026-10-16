// Home curation and announcements (internal/api/home.go):
//   GET /api/home/slots?lang=&region=         主推横幅 + curated shelf
//   GET /api/home/top-rated-short?lang=       visitor 高分短片 shelf
//   GET /api/announcements?lang=&region=      shell announcement bar
//   GET|PUT /api/me/announcement-dismissals   signed-in dismissals
// Titles come resolved for the language (operator override, else the
// target's localized title), so these paths are not in api.ts LOCALIZED_PATH
// and pass ?lang= themselves.

import {api, query, tokens, type ApiImage, type LangCode} from './api';

export type HomeRelease = {
  id: number;
  slug: string;
  canonical_title: string;
  display_title?: string;
  release_date?: string;
  runtime_minutes?: number;
  maker_name?: string;
  cover?: ApiImage | null;
};

export type HomeSlot = {
  id: number;
  slot_type: 'hero' | 'shelf';
  target_type: 'release' | 'series' | 'link';
  title: string;
  subtitle?: string;
  href: string;
  external?: boolean;
  release?: HomeRelease | null;
  series?: {id: number; slug: string; name: string; display_name?: string; release_count: number} | null;
  cover?: ApiImage | null;
  starts_at: string;
  ends_at: string | null;
  priority: number;
};

export type HomeSlots = {hero: HomeSlot[]; shelf: HomeSlot[]; region?: string; lang?: string};

export type RatedRelease = HomeRelease & {score: number | null; votes: number};
/** rated: every item has 50+ votes; few_votes: some have fewer (score hidden); fallback: newest short releases fill the rest. */
export type ShortBasis = 'rated' | 'few_votes' | 'fallback';
export type TopRatedShort = {items: RatedRelease[]; basis: ShortBasis; max_minutes: number};

export type Severity = 'info' | 'warning' | 'critical';
export type Announcement = {
  id: number;
  severity: Severity;
  body: string;
  link_url?: string;
  link_label?: string;
  external?: boolean;
  dismissible: boolean;
  starts_at: string;
  ends_at: string | null;
  /** Changes on every edit: a stored visitor dismissal is id:version. */
  version: number;
};

export const releaseTitle = (r: HomeRelease) => r.display_title || r.canonical_title;

export const getHomeSlots = (lang: LangCode, signal?: AbortSignal) =>
  api<HomeSlots>(`home/slots${query({lang})}`, {signal}).then(d => ({...d, hero: d?.hero ?? [], shelf: d?.shelf ?? []}));

export const getTopRatedShort = (lang: LangCode, limit: number, signal?: AbortSignal) =>
  api<TopRatedShort>(`home/top-rated-short${query({lang, limit})}`, {signal}).then(d => ({...d, items: d?.items ?? []}));

export const getAnnouncements = (lang: LangCode, signal?: AbortSignal) =>
  api<{items: Announcement[] | null}>(`announcements${query({lang})}`, {signal}).then(d => d?.items ?? []);

export const getDismissedAnnouncements = (signal?: AbortSignal) =>
  api<{ids: number[] | null}>('me/announcement-dismissals', {signal}).then(d => d?.ids ?? []);

export const dismissAnnouncementRemote = (id: number) =>
  api<{dismissed: boolean}>(`me/announcement-dismissals/${id}`, {method: 'PUT'});

// ---- visitor dismissals (localStorage) ----

const DISMISS_KEY = 'announcements_dismissed';
const MAX_DISMISSALS = 50;

/** Stored visitor dismissals as "id:version" keys. */
export function readDismissals(): string[] {
  try {
    const v = JSON.parse(window.localStorage.getItem(DISMISS_KEY) ?? '[]');
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

export const dismissalKey = (a: Pick<Announcement, 'id' | 'version'>) => `${a.id}:${a.version}`;

export function storeDismissal(a: Pick<Announcement, 'id' | 'version'>) {
  try {
    const keys = [dismissalKey(a), ...readDismissals().filter(k => k !== dismissalKey(a))].slice(0, MAX_DISMISSALS);
    window.localStorage.setItem(DISMISS_KEY, JSON.stringify(keys));
  } catch {
    /* storage unavailable: the bar comes back on the next page load */
  }
}

/** Signed in (a user token is stored): dismissals also go to the server. */
export const signedIn = () => !!tokens.user();
