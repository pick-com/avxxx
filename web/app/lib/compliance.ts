// Public compliance endpoints (PRD 15; internal/api consent.go, legal.go,
// feedback.go, takedowns.go): consent records, legal documents, feedback and
// copyright notices. None of these paths carries a token by default
// (lib/api tokenFor); POST consent adds the user token explicitly so a
// logged-in choice follows the account across devices.

import {api, tokens, type LangCode} from './api';

const enc = encodeURIComponent;

// ---- consent

export type ConsentSource = 'banner' | 'preferences' | 'sync' | 'gpc';
export type ServerConsent = {
  analytics: boolean;
  personalized_ads: boolean;
  personalized_recs: boolean;
  gpc: boolean;
  categories: string[];
  policy_version: number;
  source: ConsentSource;
  saved_at: string;
};

export function postConsent(body: {
  analytics: boolean;
  personalized_ads: boolean;
  personalized_recs: boolean;
  gpc: boolean;
  source: ConsentSource;
  device_id: string;
}): Promise<ServerConsent> {
  const token = tokens.user();
  return api<ServerConsent>('consent', {method: 'POST', body, headers: token ? {Authorization: `Bearer ${token}`} : undefined, keepalive: true});
}

/** The logged-in user's latest choice on any device; 404 before the first. */
export const getMyConsent = (signal?: AbortSignal) => api<ServerConsent>('me/consent', {signal});

// ---- legal documents

export const LEGAL_KINDS = ['privacy', 'terms', 'cookies', 'copyright', 'community', 'ads', 'performer-info'] as const;
export type LegalKind = (typeof LEGAL_KINDS)[number];
export const isLegalKind = (s: string): s is LegalKind => (LEGAL_KINDS as readonly string[]).includes(s);

export type LegalDocument = {
  kind: LegalKind;
  lang: LangCode;
  requested_lang: LangCode;
  fallback: boolean;
  version: number;
  current_version: number;
  is_current: boolean;
  title: string;
  summary: string[];
  /** Markdown: `## Heading {#anchor}`, paragraphs, `- ` lists. */
  body: string;
  effective_at: string;
  published_at: string;
};

export type LegalVersionInfo = {
  version: number;
  title: string;
  lang: LangCode;
  langs: LangCode[];
  effective_at: string;
  published_at: string;
  is_current: boolean;
};

export const legalDocumentPath = (kind: string, lang: LangCode, version?: number) =>
  `legal/${enc(kind)}?lang=${enc(lang)}${version ? `&version=${version}` : ''}`;
export const legalVersionsPath = (kind: string, lang: LangCode) => `legal/${enc(kind)}/versions?lang=${enc(lang)}`;

export const getLegalDocument = (kind: string, lang: LangCode, version?: number, signal?: AbortSignal) =>
  api<LegalDocument>(legalDocumentPath(kind, lang, version), {signal});
export const getLegalVersions = (kind: string, lang: LangCode, signal?: AbortSignal) =>
  api<{items: LegalVersionInfo[] | null}>(legalVersionsPath(kind, lang), {signal}).then(d => d?.items ?? []);

// ---- feedback

export const FEEDBACK_CATEGORIES = ['bug', 'playback', 'content', 'account', 'suggestion', 'partnership', 'other'] as const;
export type FeedbackCategory = (typeof FEEDBACK_CATEGORIES)[number];

export const postFeedback = (body: {category: FeedbackCategory; message: string; email?: string; page_url?: string; lang?: string}) =>
  api<{id: number; status: string; created_at: string}>('feedback', {method: 'POST', body});

// ---- copyright notices

export type TakedownNotice = {
  claimant_name: string;
  claimant_email: string;
  claimant_phone?: string;
  claimant_address?: string;
  rights_owner: string;
  relationship: 'owner' | 'agent';
  work_description: string;
  original_urls: string[];
  infringing_urls: string[];
  good_faith: boolean;
  accuracy: boolean;
  signature: string;
  lang?: string;
};
export type TakedownReceipt = {ticket: string; status: string; due_at: string; created_at: string};
export type TakedownPublicStatus = {ticket: string; status: string; received_at: string; updated_at: string; counter_allowed: boolean};
export type CounterNotice = {name: string; email: string; address: string; statement: string; consent: boolean; signature: string};

export const postTakedown = (body: TakedownNotice) => api<TakedownReceipt>('takedowns', {method: 'POST', body});
/** The email travels in a header so it never lands in URLs or access logs. */
export const getTakedownStatus = (ticket: string, email: string) =>
  api<TakedownPublicStatus>(`takedowns/${enc(ticket.trim().toUpperCase())}`, {headers: {'X-Takedown-Email': email.trim()}});
export const postCounterNotice = (ticket: string, body: CounterNotice) =>
  api<TakedownPublicStatus>(`takedowns/${enc(ticket.trim().toUpperCase())}/counter`, {method: 'POST', body});

/** Partnership contact (Footer 内容合作): VITE_PARTNERSHIP_EMAIL at build time. */
export const partnershipEmail = (): string => {
  const v = (import.meta.env.VITE_PARTNERSHIP_EMAIL as string | undefined)?.trim() ?? '';
  return /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/.test(v) ? v : '';
};
