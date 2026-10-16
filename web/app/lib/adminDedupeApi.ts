// AdminDedupe compare panel and 其他版本 (internal/api/admin_dedupe.go). 新片源
// (merge) and 不是重复 stay in lib/api.ts (adminMergeCandidate,
// adminNotDuplicate).

import {api, type ApiImage, type DecisionResult, type ReleaseVersionRef} from './api';

export type SummarySource = {source: string; url: string};
export type SummaryPlayback = {kind: 'playback_source' | 'media_source'; provider: string; label: string; url?: string; status: string};

/** One side of the compare panel (store.EntitySummary). */
export type EntitySummary = {
  id: number;
  entity_type: 'release' | 'performer';
  slug: string;
  title: string;
  title_ja?: string | null;
  status: string;
  merged_into_id?: number | null;
  release_date?: string | null;
  codes?: string[];
  runtime_minutes?: number | null;
  maker?: string | null;
  label?: string | null;
  series?: string | null;
  edition_type?: string;
  censorship_type?: string | null;
  performers?: string[];
  playback?: SummaryPlayback[];
  versions?: ReleaseVersionRef[];
  birth_date?: string | null;
  aliases?: string[];
  works?: number | null;
  sources: SummarySource[];
  cover?: ApiImage | null;
};

export type CandidateCompare = {
  candidate_id: number;
  entity_type: string;
  status: string;
  score: number;
  evidence: unknown;
  left: EntitySummary;
  right: EntitySummary;
};

export const getCandidateCompare = (id: number, signal?: AbortSignal) => api<CandidateCompare>(`admin/identity-candidates/${id}/compare`, {signal});

/** 其他版本: link the two releases of a video candidate without merging. */
export const linkCandidateVersions = (id: number, reason?: string) =>
  api<DecisionResult>(`admin/identity-candidates/${id}/versions`, {method: 'POST', body: {reason}});

export const unlinkReleaseVersion = (id: number, otherId: number, reason?: string) =>
  api<{review_event_id: number}>(`admin/releases/${id}/versions/${otherId}`, {method: 'DELETE', body: {reason}});
