// 片段 (clips) API (internal/api/clips.go): create / read / delete / report.
import {api} from './api';
import {clipRequest, type ClipRange} from './clipRange';
import type {ReportReason} from './community';

export type Clip = {
  /** Short public id: /clips/{id}. */
  id: string;
  release_slug: string;
  release_title: string;
  start_ms: number;
  end_ms: number;
  title: string;
  status: string;
  author_name?: string;
  created_at: string;
  /** The viewer made it (may delete it). */
  mine: boolean;
};

export const createClip = (slug: string, range: ClipRange, title: string) =>
  api<{clip: Clip; title_removed: boolean}>('me/clips', {method: 'POST', body: clipRequest(slug, range, title)});

export const getClip = (id: string, signal?: AbortSignal) => api<Clip>(`clips/${encodeURIComponent(id)}`, {signal});

export const deleteClip = (id: string) => api<{deleted: boolean}>(`me/clips/${encodeURIComponent(id)}`, {method: 'DELETE'});

/** The public page of a clip (unprefixed; the router adds the language). */
export const clipPath = (id: string) => `/clips/${encodeURIComponent(id)}`;

/** 举报 into the shared moderation inbox (reasons as for danmaku / reviews). */
export const reportClip = (id: string, reason: ReportReason) =>
  api<{id: number; status: string; created: boolean}>(`clips/${encodeURIComponent(id)}/report`, {method: 'POST', body: {reason}});
