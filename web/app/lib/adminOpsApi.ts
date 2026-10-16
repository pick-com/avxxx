// AdminOps client (internal/api/home.go): home slot schedule, drag reorder,
// live preview and announcements. Every write sends X-Admin-User (api.ts),
// recorded as review_events.actor.

import {api, query, type ApiImage} from './api';
import type {Announcement, HomeSlot, Severity} from './home';

export type SlotType = 'hero' | 'shelf';
export type TargetType = 'release' | 'series' | 'link';

/** Per-language copy: slots use title / subtitle, announcements body / link_label. */
export type OpsText = {title?: string; subtitle?: string; body?: string; link_label?: string};
export type OpsTexts = Record<string, OpsText>;

export type AdminHomeSlot = {
  id: number;
  slot_type: SlotType;
  target_type: TargetType;
  release_id?: number;
  series_id?: number;
  link_url?: string;
  texts: OpsTexts;
  langs: string[];
  regions: string[];
  starts_at: string;
  ends_at: string | null;
  priority: number;
  enabled: boolean;
  created_by: string;
  updated_by: string;
  created_at: string;
  updated_at: string;
  release?: {id: number; slug: string; canonical_title: string; status: string; runtime_minutes?: number; cover?: ApiImage | null} | null;
  series?: {id: number; slug: string; name: string; release_count: number} | null;
  target_public: boolean;
};

export type HomeSlotInput = {
  slot_type: SlotType;
  target_type: TargetType;
  release_id?: number;
  series_slug?: string;
  link_url?: string;
  texts: OpsTexts;
  langs: string[];
  regions: string[];
  starts_at: string;
  ends_at: string | null;
  priority: number;
  enabled: boolean;
};

export type AdminAnnouncement = {
  id: number;
  severity: Severity;
  texts: OpsTexts;
  link_url?: string;
  langs: string[];
  regions: string[];
  starts_at: string;
  ends_at: string | null;
  dismissible: boolean;
  enabled: boolean;
  created_by: string;
  updated_by: string;
  created_at: string;
  updated_at: string;
};

export type AnnouncementInput = Omit<AdminAnnouncement, 'id' | 'created_by' | 'updated_by' | 'created_at' | 'updated_at'>;

export type HomePreview = {at: string; hero: HomeSlot[]; shelf: HomeSlot[]; announcements: Announcement[]; region?: string; lang?: string};

type Items<T> = {items: T[] | null; now?: string};

export const getAdminHomeSlots = (p: {from?: string; to?: string; slot_type?: SlotType | ''} = {}, signal?: AbortSignal) =>
  api<Items<AdminHomeSlot>>(`admin/home-slots${query(p)}`, {signal}).then(d => ({items: d?.items ?? [], now: d?.now ?? new Date().toISOString()}));
export const createHomeSlot = (body: HomeSlotInput) => api<AdminHomeSlot>('admin/home-slots', {method: 'POST', body});
export const updateHomeSlot = (id: number, body: HomeSlotInput) => api<AdminHomeSlot>(`admin/home-slots/${id}`, {method: 'PUT', body});
export const deleteHomeSlot = (id: number) => api<{deleted: boolean}>(`admin/home-slots/${id}`, {method: 'DELETE'});
export const reorderHomeSlots = (slotType: SlotType, ids: number[]) =>
  api<{changed: number}>('admin/home-slots/reorder', {method: 'POST', body: {slot_type: slotType, ids}});
export const previewHome = (p: {at?: string; lang?: string; region?: string}, signal?: AbortSignal) =>
  api<HomePreview>(`admin/home-slots/preview${query(p)}`, {signal}).then(d => ({...d, hero: d?.hero ?? [], shelf: d?.shelf ?? [], announcements: d?.announcements ?? []}));

export const getAdminAnnouncements = (signal?: AbortSignal) =>
  api<Items<AdminAnnouncement>>('admin/announcements', {signal}).then(d => d?.items ?? []);
export const createAnnouncement = (body: AnnouncementInput) => api<AdminAnnouncement>('admin/announcements', {method: 'POST', body});
export const updateAnnouncement = (id: number, body: AnnouncementInput) => api<AdminAnnouncement>(`admin/announcements/${id}`, {method: 'PUT', body});
export const deleteAnnouncement = (id: number) => api<{deleted: boolean}>(`admin/announcements/${id}`, {method: 'DELETE'});

export const SLOT_TYPE_LABELS: Record<SlotType, string> = {hero: '主推横幅', shelf: '编辑精选'};
export const TARGET_LABELS: Record<TargetType, string> = {release: '视频', series: '系列', link: '链接'};
export const SEVERITY_LABELS: Record<Severity, string> = {info: '通知', warning: '提醒', critical: '紧急'};
