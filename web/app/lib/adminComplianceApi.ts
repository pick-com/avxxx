// Admin client for the compliance back office (PRD 11, 15): legal
// documents (internal/api/legal.go), feedback (feedback.go) and copyright
// takedowns (takedowns.go). Every path is under admin/*, so api() sends the
// admin token and, on writes, X-Admin-User (操作人, review_events.actor).

import {api} from './api';
import type {LegalKind} from './compliance';


function qs(params: Record<string, string | number | boolean | null | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== null && v !== undefined && v !== '' && v !== false) q.set(k, String(v));
  }
  const text = q.toString();
  return text ? `?${text}` : '';
}

type Page<T> = {items: T[] | null; next_cursor: string | null};

// ---- legal documents

export type AdminLegalDoc = {
  id: number;
  kind: LegalKind;
  lang: string;
  version: number;
  title: string;
  summary: string[];
  body: string;
  status: 'draft' | 'published';
  effective_at: string | null;
  published_at: string | null;
  published_by: string | null;
  change_note: string;
  created_by: string;
  created_at: string;
  updated_at: string;
};
export type LegalDraft = {
  kind?: string;
  lang?: string;
  version?: number;
  title: string;
  summary: string[];
  body: string;
  effective_at?: string | null;
  change_note?: string;
};
export type DiffLine = {op: '=' | '-' | '+'; text: string};
export type LegalDiff = {base: AdminLegalDoc | null; target: AdminLegalDoc; lines: DiffLine[]};

export const getAdminLegal = (params: {kind?: string; lang?: string}, signal?: AbortSignal) =>
  api<{items: AdminLegalDoc[] | null; kinds: string[]}>(`admin/legal${qs(params)}`, {signal}).then(d => ({items: d?.items ?? [], kinds: d?.kinds ?? []}));
export const createLegalDraft = (body: LegalDraft) => api<AdminLegalDoc>('admin/legal', {method: 'POST', body});
export const updateLegalDraft = (id: number, body: LegalDraft) => api<AdminLegalDoc>(`admin/legal/${id}`, {method: 'PUT', body});
export const publishLegal = (id: number, body: {effective_at?: string | null; note?: string}) =>
  api<AdminLegalDoc>(`admin/legal/${id}/publish`, {method: 'POST', body});
export const getLegalDiff = (id: number, base?: number, signal?: AbortSignal) => api<LegalDiff>(`admin/legal/${id}/diff${qs({base})}`, {signal});

// ---- feedback

export const FEEDBACK_STATUSES = ['open', 'in_progress', 'resolved', 'dismissed'] as const;
export type FeedbackStatus = (typeof FEEDBACK_STATUSES)[number];
export type AdminFeedback = {
  id: number;
  category: string;
  message: string;
  email: string;
  page_url: string;
  lang: string;
  status: FeedbackStatus;
  admin_note: string;
  handled_by: string | null;
  handled_at: string | null;
  created_at: string;
};

export const getAdminFeedback = (params: {status?: string; category?: string; cursor?: string | null; limit?: number}, signal?: AbortSignal) =>
  api<Page<AdminFeedback> & {open: number}>(`admin/feedback${qs(params)}`, {signal}).then(d => ({
    items: d?.items ?? [],
    next_cursor: d?.next_cursor || null,
    open: d?.open ?? 0,
  }));
export const setFeedbackStatus = (id: number, status: FeedbackStatus, note?: string) =>
  api<AdminFeedback>(`admin/feedback/${id}/status`, {method: 'POST', body: {status, note: note ?? ''}});

// ---- takedowns

export const TAKEDOWN_STATUSES = ['received', 'reviewing', 'actioned', 'rejected', 'counter_received', 'restored', 'closed'] as const;
export type TakedownStatus = (typeof TAKEDOWN_STATUSES)[number];
export type TakedownTarget = {
  id: number;
  target_type: 'release' | 'media_asset';
  target_id: number;
  action: 'unpublish' | 'suppress';
  previous_status: string | null;
  detail: {slug?: string; title?: string; entity_type?: string; entity_id?: number; asset_type?: string; object_key?: string} | null;
  state: 'active' | 'restored';
  actor: string;
  created_at: string;
  restored_at: string | null;
  restored_by: string | null;
};
export type TakedownEvent = {id: number; action: string; before: unknown; after: unknown; reason: string; actor: string; created_at: string};
export type AdminTakedown = {
  id: number;
  ticket: string;
  kind: string;
  status: TakedownStatus;
  claimant_name: string;
  claimant_email: string;
  claimant_phone: string;
  claimant_address: string;
  rights_owner: string;
  relationship: 'owner' | 'agent';
  work_description: string;
  original_urls: string[] | null;
  infringing_urls: string[] | null;
  signature: string;
  lang: string;
  due_at: string;
  first_action_at: string | null;
  overdue: boolean;
  counter_name: string | null;
  counter_email: string | null;
  counter_address: string | null;
  counter_statement: string | null;
  counter_signature: string | null;
  counter_received_at: string | null;
  resolution_note: string;
  active_targets: number;
  created_at: string;
  updated_at: string;
  targets?: TakedownTarget[] | null;
  events?: TakedownEvent[] | null;
};
export type TakedownCounts = {open: number; overdue: number};

export const getAdminTakedowns = (params: {status?: string; open?: boolean; cursor?: string | null; limit?: number}, signal?: AbortSignal) =>
  api<Page<AdminTakedown> & {counts: TakedownCounts; sla_hours: number}>(`admin/takedowns${qs(params)}`, {signal}).then(d => ({
    items: d?.items ?? [],
    next_cursor: d?.next_cursor || null,
    counts: d?.counts ?? {open: 0, overdue: 0},
    sla_hours: d?.sla_hours ?? 24,
  }));
export const getAdminTakedown = (id: number, signal?: AbortSignal) => api<AdminTakedown>(`admin/takedowns/${id}`, {signal});
export const setTakedownStatus = (id: number, status: TakedownStatus, note: string) =>
  api<AdminTakedown>(`admin/takedowns/${id}/status`, {method: 'POST', body: {status, note}});
export const takedownAction = (
  id: number,
  body: {action: 'unpublish_release'; release_id?: number; release_slug?: string; reason?: string} | {action: 'suppress_image'; media_asset_id: number; reason?: string},
) => api<AdminTakedown>(`admin/takedowns/${id}/actions`, {method: 'POST', body});
export const restoreTakedown = (id: number, reason: string) => api<AdminTakedown>(`admin/takedowns/${id}/restore`, {method: 'POST', body: {reason}});

/** Release slugs named by infringing URLs on this site (/releases/x, /en/releases/x). */
export function releaseSlugsFromURLs(urls: string[]): string[] {
  const out: string[] = [];
  for (const u of urls) {
    try {
      const m = /\/releases\/([^/?#]+)/.exec(new URL(u).pathname);
      const slug = m ? decodeURIComponent(m[1]) : '';
      if (slug && !out.includes(slug)) out.push(slug);
    } catch {
      /* not a URL */
    }
  }
  return out;
}

/** Media of a release (GET admin/media; 404 without S3 configured). */
export type AdminMediaAsset = {id: number; asset_type: string; variant: string; derived_from?: number | null; object_key: string; suppressed_at?: string | null};
export const getReleaseMedia = (releaseId: number, signal?: AbortSignal) =>
  api<{assets: AdminMediaAsset[] | null}>(`admin/media${qs({entity_type: 'release', entity_id: releaseId})}`, {signal}).then(d => d?.assets ?? []);

