// Typed client for the governance / data-quality admin routes that landed
// after adminApi.ts (each may 404 on an older backend; pages then show
// 后端未部署):
//   admin_governance.go  license review, phased jobs, checkpoints, field policies
//   admin_media_links.go release -> provider media-id linking attempts
//   admin_split.go       entity split preview / split / undo
//   reports.go           completeness report (JSON + CSV)
//   sitemap.go           latest SEO check run (收录健康分)

import {ApiError, adminRequestHeaders, api, isMissingEndpoint} from './api';
import type {IngestSource} from './adminApi';

const enc = encodeURIComponent;

function qs(params: Record<string, string | number | boolean | null | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== null && v !== undefined && v !== '' && v !== false) q.set(k, String(v));
  }
  const text = q.toString();
  return text ? `?${text}` : '';
}

/** A route that is not deployed (mux 404 / 405 / 501), not an entity 404 with a message. */
export const routeMissing = (cause: unknown) =>
  isMissingEndpoint(cause) && !(cause instanceof ApiError && cause.detail && !/page not found/i.test(cause.detail));

// ---------------------------------------------------------------- sources: license / phases

/** catalog.LicenseStatuses in display order. */
export const LICENSE_STATUSES = ['review_required', 'manual_only', 'denied', 'terms_confirmed', 'licensed', 'official_api', 'internal'] as const;
export const LICENSE_LABELS: Record<string, string> = {
  review_required: '待审核条款',
  manual_only: '仅人工验证',
  denied: '禁止抓取',
  terms_confirmed: '条款允许',
  licensed: '已授权',
  official_api: '官方 API',
  internal: '自有数据',
};
/** catalog.AutomaticLicenses: the statuses that permit automatic crawls (with a recorded review). */
export const AUTOMATIC_LICENSES = new Set(['terms_confirmed', 'licensed', 'official_api', 'internal']);

/** model.Source with the migrations/024 governance fields. */
export type GovSource = IngestSource & {
  terms_url?: string;
  terms_reviewed_at?: string | null;
  terms_reviewed_by?: string;
  license_note?: string;
  baseline_requested_at?: string | null;
  automatic_allowed?: boolean;
  gate_reason?: string;
  /** Not returned by any route yet; shown when a backend adds it. */
  blocked_until?: string | null;
  blocked_reason?: string | null;
};
/** model.Job with the phase fields (result_counts is shown when a backend returns it). */
export type GovJob = {
  id: number;
  source_id: number;
  phase?: string;
  page_cap?: number | null;
  status: string;
  error: string;
  error_code?: string;
  created_at: string;
  attempt: number;
  lease_until?: string;
  execution_version: number;
  result_counts?: Record<string, unknown> | null;
};
export type LicenseReview = {license_status: string; terms_url: string | null; reviewed: boolean; note: string};
export type JobPhase = 'incremental' | 'baseline' | 'verify';
export const PHASE_LABELS: Record<string, string> = {incremental: '增量', baseline: '全量基线', verify: '验证（试跑）'};
export const VERIFY_MAX_PAGES = 3;

export type CrawlCheckpoint = {
  source_id: number;
  task_type: string;
  partition_key: string;
  cursor: string;
  parser_version: string;
  last_error_code: string;
  watermark: string | null;
  completed_pages: number;
  last_success_at: string | null;
  /** Optional, if a backend reports it. */
  failures?: number;
};

export const getGovSources = () => api<GovSource[] | null>('admin/sources').then(d => d ?? []);
export const getGovJobs = () => api<GovJob[] | null>('admin/jobs').then(d => d ?? []);
export const setSourceLicense = (id: number, review: LicenseReview) =>
  api<GovSource>(`admin/sources/${id}/license`, {method: 'PUT', body: review});
export const requestJob = (id: number, phase: JobPhase, maxPages?: number) =>
  api<{ok: boolean; job_id: number; phase: string}>(`admin/sources/${id}/jobs`, {
    method: 'POST',
    body: phase === 'verify' && maxPages ? {phase, max_pages: maxPages} : {phase},
  });
export const getCheckpoints = (id: number) => api<CrawlCheckpoint[] | null>(`admin/sources/${id}/checkpoints`).then(d => d ?? []);

// ---------------------------------------------------------------- field policies

export type FieldPolicy = {
  entity_type: 'release' | 'performer' | string;
  field_name: string;
  source_id: number;
  source_name?: string;
  priority: number;
  updated_by?: string;
  updated_at?: string;
};
export type EffectiveSource = {source_id: number; source_name: string; source_kind: string; priority: number; from_policy: boolean};
export type FieldPolicies = {
  policies: FieldPolicy[] | null;
  tier_defaults: Record<string, number>;
  lower_wins: boolean;
  effective?: EffectiveSource[] | null;
};
/** store.unknownTierPriority: any source_kind outside the tier table. */
export const UNKNOWN_TIER_PRIORITY = 60;
export const TIER_LABELS: Record<string, string> = {
  official_catalog: '官方目录',
  official_maker: '片商官网',
  structured_database: '结构化资料库',
  aggregator: '聚合站',
  third_party_api: '第三方 API',
};
/** Canonical columns an admin typically sets a policy for (store.ConflictField); free text is allowed too. */
export const POLICY_FIELDS: Record<string, string[]> = {
  release: ['canonical_title', 'title_ja', 'synopsis', 'release_date', 'shoot_year', 'runtime_minutes', 'maker_name', 'label_name', 'series_name', 'director_name'],
  performer: ['canonical_name', 'canonical_name_ja', 'birth_date', 'birth_place', 'debut_date', 'height_cm', 'cup_size', 'shoe_size', 'biography'],
};

export const getFieldPolicies = (entityType?: string, fieldName?: string) =>
  api<FieldPolicies>(`admin/field-policies${qs({entity_type: entityType, field_name: fieldName})}`);
export const putFieldPolicy = (p: {entity_type: string; field_name: string; source_id: number; priority: number; reason?: string}) =>
  api<FieldPolicy>('admin/field-policies', {method: 'PUT', body: p});
export const deleteFieldPolicy = (entityType: string, field: string, sourceId: number, reason: string) =>
  api<{ok: boolean}>(`admin/field-policies/${enc(entityType)}/${enc(field)}/${sourceId}${qs({reason})}`, {method: 'DELETE'});

// ---------------------------------------------------------------- media links

export const LINK_STATUSES = ['found', 'not_found', 'ambiguous', 'error'] as const;
export const LINK_STATUS_LABELS: Record<string, string> = {found: '已匹配', not_found: '未找到', ambiguous: '多个候选', error: '出错'};
export const LINK_PROVIDERS: Record<string, string> = {javtiful: 'https://javtiful.com/'};
export type MediaLinkRow = {
  release_id: number;
  provider: string;
  status: string;
  tried_at: string;
  next_try_at: string | null;
  attempts: number;
  matched_media_id?: string;
  candidates: string[] | null;
  last_error?: string;
  slug: string;
  title: string;
  codes: string[] | null;
  linked_media_id?: string;
};
export const getMediaLinks = (f: {provider?: string; status?: string; cursor?: string | null; limit?: number}) =>
  api<{items: MediaLinkRow[] | null; next_cursor: string | null}>(`admin/media-links${qs(f)}`).then(d => ({
    items: d?.items ?? [],
    next_cursor: d?.next_cursor || null,
  }));
export const retryMediaLink = (releaseId: number, provider: string) =>
  api<{release_id: number; provider: string; due: boolean}>(`admin/media-links/${releaseId}/retry${qs({provider})}`, {method: 'POST'});
export const chooseMediaLink = (releaseId: number, provider: string, mediaId: string) =>
  api<{linked: boolean}>(`admin/media-links/${releaseId}/choose`, {method: 'POST', body: {provider, media_id: mediaId}});
export const runMediaLinks = () => api<{job_id: number; enqueued: boolean}>('admin/media-links/run', {method: 'POST'});
export const mediaURL = (provider: string, mediaId: string) => {
  const base = LINK_PROVIDERS[provider];
  return base ? base + mediaId.replace(/^\/+/, '') : '';
};

// ---------------------------------------------------------------- entity split

export type SplitEntity = 'performer' | 'release';
export type SplitSourceRecord = {
  id: number;
  kind: 'record' | 'credit' | string;
  source_id: number;
  source_name: string;
  source_record_key: string;
  source_url: string;
  last_seen_at: string;
  name?: string;
  release_id?: number;
  credited_name?: string;
};
export type SplitField = {
  column: string;
  before?: unknown;
  value: unknown;
  origin: 'evidence' | 'override' | 'cleared' | 'kept' | 'fallback' | 'empty' | string;
  source_record_id?: number;
  conflict?: boolean;
};
export type SplitTable = {table: string; fk: string; key: string[]; move: unknown[] | null; copy: unknown[] | null; user_data?: boolean};
export type SplitPlan = {
  entity_type: string;
  original_id: number;
  new_id: number;
  new_slug: string;
  moved_records: number[] | null;
  tables: SplitTable[] | null;
  original_fields: SplitField[] | null;
  new_fields: SplitField[] | null;
};
export type SplitPreview = {
  entity_type: string;
  entity_id: number;
  slug: string;
  version: number;
  source_records: SplitSourceRecord[] | null;
  plan?: SplitPlan | null;
};
export type SplitResult = {
  review_event_id: number;
  entity_type: string;
  original_id: number;
  original_slug: string;
  new_id: number;
  new_slug: string;
  plan: SplitPlan;
};
/** identity.SplitUserTables: user-data tables an admin may move explicitly, matched on user id or row id. */
export const SPLIT_USER_TABLES: Record<SplitEntity, {table: string; label: string; key: 'user_id' | 'id'}[]> = {
  performer: [{table: 'follows', label: '关注', key: 'user_id'}],
  release: [
    {table: 'favorites', label: '收藏', key: 'user_id'},
    {table: 'watch_history', label: '观看历史', key: 'user_id'},
    {table: 'reservations', label: '预约', key: 'user_id'},
    {table: 'watch_later', label: '稍后看', key: 'user_id'},
    {table: 'ratings', label: '评分', key: 'id'},
    {table: 'reviews', label: '短评', key: 'id'},
    {table: 'playback_sources', label: '片源', key: 'id'},
  ],
};

const splitBase = (e: SplitEntity, id: number) => `admin/${e === 'performer' ? 'performers' : 'releases'}/${id}`;
export const getSplitPreview = (e: SplitEntity, id: number, records: number[], mediaLinks: string[] = [], signal?: AbortSignal) =>
  api<SplitPreview>(`${splitBase(e, id)}/split-preview${qs({source_records: records.join(','), media_links: mediaLinks.join(',')})}`, {signal});
export const splitEntity = (
  e: SplitEntity,
  id: number,
  body: {source_records: number[]; version: number; reason: string; media_links?: string[]; move_user_data?: Record<string, number[]>},
) => api<SplitResult>(`${splitBase(e, id)}/split`, {method: 'POST', body});
export const undoSplit = (eventId: number, reason: string) =>
  api<{review_event_id: number; reverted_event_id: number; entity_type: string; original_id: number; merged_id: number}>(`admin/splits/${eventId}/undo`, {
    method: 'POST',
    body: {reason},
  });

// ---------------------------------------------------------------- completeness report

export type EntityRef = {id: number; slug: string; name: string; popularity: number};
export type FieldStat = {
  field: string;
  required: boolean;
  total: number;
  present: number;
  missing: number;
  percent: number;
  missing_popularity: number;
  source_provided?: number;
  top_missing?: EntityRef[] | null;
};
export type CompletenessGroup = {
  key: string;
  source_id?: number;
  source_kind?: string;
  total: number;
  required_complete: number;
  required_complete_percent: number;
  fields: FieldStat[] | null;
};
export type SourceCoverage = {source_id: number; name: string; source_kind: string; entities: number; percent: number};
export type TrendPoint = {date: string; total: number; required_complete_percent: number; fields: Record<string, number> | null};
export type CompletenessReport = {
  entity: string;
  group_by: string;
  source?: string;
  generated_at: string;
  total: number;
  required_complete: number;
  required_complete_percent: number;
  fields: FieldStat[] | null;
  groups?: CompletenessGroup[] | null;
  sources: SourceCoverage[] | null;
  trend: TrendPoint[] | null;
};
export type CompletenessQuery = {entity: 'release' | 'performer'; group_by: 'field' | 'source' | 'maker'; source?: string; runs?: number; top?: number};
export const COMPLETENESS_FIELD_LABELS: Record<string, string> = {
  cover: '封面',
  canonical_title: '标题',
  product_code: '番号',
  title_ja: '日文标题',
  release_date: '发行日期',
  shoot_year: '拍摄年份',
  runtime_minutes: '时长',
  maker_name: '片商',
  label_name: '厂牌',
  series: '系列',
  series_name: '系列',
  director_name: '导演',
  synopsis: '简介',
  performers: '演员',
  tags: '标签',
  canonical_name: '名字',
  canonical_name_ja: '日文名',
  birth_date: '生日',
  birth_place: '出生地',
  debut_date: '出道日期',
  height_cm: '身高',
  measurements: '三围',
  cup_size: '罩杯',
  shoe_size: '鞋码',
  biography: '简介',
  photo: '照片',
  aliases: '别名',
  works: '作品',
};
export const fieldLabel = (f: string) => (COMPLETENESS_FIELD_LABELS[f] ? `${COMPLETENESS_FIELD_LABELS[f]} · ${f}` : f);

export const getCompleteness = (q: CompletenessQuery, signal?: AbortSignal) =>
  api<CompletenessReport>(`admin/reports/completeness${qs(q)}`, {signal});

/** Fetches the CSV export with the admin token and hands it to the browser as a download. */
export async function downloadCompletenessCSV(q: CompletenessQuery): Promise<void> {
  const headers = adminRequestHeaders(new Headers({Accept: 'text/csv'}));
  const res = await fetch(`/api/admin/reports/completeness${qs({...q, format: 'csv'})}`, {headers});
  if (!res.ok) {
    const text = (await res.text()).trim().slice(0, 300);
    let detail = text;
    try {
      const j = JSON.parse(text);
      if (j && typeof j.error === 'string') detail = j.error;
    } catch {
      /* plain text */
    }
    throw new ApiError(res.status === 401 ? '登录已过期，请重新登录后台' : `导出失败（${res.status}）`, res.status, detail);
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `completeness-${q.entity}-${q.group_by}.csv`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// ---------------------------------------------------------------- SEO check (收录健康分)

export type SEOFailure = {url: string; reasons: string[] | null};
export type SEODetail = {
  score?: number;
  band?: string;
  base_url?: string;
  ssr_pass_rate?: number;
  sitemap_coverage?: number;
  checked?: number;
  failed?: number;
  checked_at?: string;
  counts_source?: string;
  samples_per_type?: number;
  by_type?: Record<string, {checked: number; passed: number}>;
  sitemap?: Record<string, {listed: number; expected: number | null; files_per_lang: number}>;
  sitemap_missing_langs?: string[] | null;
  failures?: SEOFailure[] | null;
};
export type SEORun = {job: string; started_at: string; duration_ms: number; checked: number; issues: number; result: string; detail?: SEODetail};
export const getSEOReport = () => api<SEORun>('admin/seo/report');
