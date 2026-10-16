// Typed client for the admin back office (PRD 11). Every path is under
// admin/*, so api() sends only the admin token, plus X-Admin-User (操作人)
// on writes. Shapes mirror internal/api + internal/store on the backend:
//   admin_audit.go   dashboard, audit issues, audit run, playback source health
//   admin.go         ingest sources, jobs, crawl policies
//   admin_release.go release editor (PR #61; may 404 on older backends)
//   moderation.go    moderation queue, actions, words, mutes
//   admin_i18n.go    translations, stats, glossary, UI strings

import {ApiError, api, isMissingEndpoint} from './api';

const enc = encodeURIComponent;

function qs(params: Record<string, string | number | boolean | null | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== null && v !== undefined && v !== '' && v !== false) q.set(k, String(v));
  }
  const text = q.toString();
  return text ? `?${text}` : '';
}

/** Cursor page as the admin lists return it: {items, next_cursor}. */
export type CursorPage<T> = {items: T[]; next_cursor: string | null};
const pageOf = <T,>(d: {items: T[] | null; next_cursor?: string | null} | null): CursorPage<T> => ({
  items: d?.items ?? [],
  next_cursor: d?.next_cursor || null,
});
type ItemsBody<T> = {items: T[] | null};
const itemsOf = <T,>(d: ItemsBody<T> | null) => d?.items ?? [];

/** Human text for an admin failure: the raw server message when there is one. */
export function adminErrorText(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 401) return '管理员令牌无效或未设置';
    if (isMissingEndpoint(cause) && !cause.detail) return '后端未部署该接口';
    return cause.detail && cause.detail !== cause.message ? `${cause.message}：${cause.detail}` : cause.message;
  }
  return cause instanceof Error ? cause.message : String(cause);
}

// ---------------------------------------------------------------- dashboard / audit

export type HealthBand = 'severe' | 'degraded' | 'healthy' | 'excellent' | 'insufficient_data';
export const BANDS: HealthBand[] = ['severe', 'degraded', 'healthy', 'excellent', 'insufficient_data'];

export type JobCounts = {pending: number; running: number; failed_24h: number};
export type NightlyRun = {
  job: string;
  job_id?: number;
  started_at: string;
  duration_ms: number;
  checked: number;
  fixed: number;
  issues: number;
  result: 'ok' | 'issues' | 'failed' | 'not_applicable' | string;
  detail?: Record<string, unknown>;
};
export type Dashboard = {
  generated_at: string;
  releases: {total: number; held: number; by_status: Record<string, number>};
  performers: {total: number; by_status: Record<string, number>};
  issues: {
    open: number;
    by_rule: Record<string, number>;
    by_severity: Record<string, number>;
    by_rule_severity: Record<string, Record<string, number>>;
  };
  jobs: Record<string, JobCounts>;
  nightly_runs: NightlyRun[];
  identity_candidates_open: number;
  conflicts_open: number;
  catalog_health: {score: number | null; band?: HealthBand; published: number; with_open_issues: number};
  source_health: {
    score: number | null;
    band?: HealthBand;
    attempts: number;
    playable: number;
    ff_ok: number;
    sources: number;
    scored: number;
    insufficient_data: number;
    by_band: Record<string, number>;
    by_status: Record<string, number>;
    last_run_at: string | null;
  };
};
export const getDashboard = () => api<Dashboard>('admin/dashboard');

export type AuditRule =
  | 'R-AGE'
  | 'R-META'
  | 'R-TITLE'
  | 'R-STATS'
  | 'R-SOURCE'
  | 'R-WATERMARK'
  | 'R-SUBTITLE'
  | 'R-SCREENSHOT'
  | 'R-SEO';
export const AUDIT_RULES: AuditRule[] = ['R-AGE', 'R-META', 'R-TITLE', 'R-STATS', 'R-SOURCE', 'R-WATERMARK', 'R-SUBTITLE', 'R-SCREENSHOT', 'R-SEO'];
export type IssueStatus = 'open' | 'fixed' | 'dismissed';
export type Severity = 'blocking' | 'high' | 'medium' | 'low';
export type AuditIssue = {
  id: number;
  rule: AuditRule;
  entity_type: string;
  entity_id: number;
  severity: Severity;
  detail: Record<string, unknown> | null;
  priority: number;
  status: IssueStatus;
  created_at: string;
  last_seen_at: string;
  resolved_at?: string;
};
export const getAuditIssues = (f: {rule?: string; status?: string; cursor?: string | null; limit?: number}) =>
  api<{items: AuditIssue[] | null; next_cursor: string | null}>(`admin/audit/issues${qs(f)}`).then(pageOf);
export const dismissAuditIssue = (id: number, note: string) =>
  api<{ok: boolean}>(`admin/audit/issues/${id}/dismiss`, {method: 'POST', body: {note}});
export const runAudit = () => api<{job_id: number; enqueued: boolean}>('admin/audit/run', {method: 'POST'});

export type SourceHealthItem = {
  source_id: number;
  release_id: number;
  release_slug: string;
  release_title: string;
  kind: string;
  provider: string;
  label: string;
  status: string;
  score: number | null;
  band: HealthBand;
  checked_at: string | null;
  latest: Record<string, unknown> | null;
  trend: Record<string, unknown>[] | null;
};
export const getSourceHealth = (f: {band?: string; cursor?: string | null; limit?: number; trend?: number}) =>
  api<{items: SourceHealthItem[] | null; next_cursor: string | null}>(`admin/playback-sources/health${qs(f)}`).then(pageOf);

// ---------------------------------------------------------------- ingest pipeline

export type IngestSource = {
  id: number;
  name: string;
  url: string;
  enabled: boolean;
  version: number;
  adapter: string;
  source_kind: string;
  license_status: string;
  egress_policy: string;
};
export type IngestJob = {
  id: number;
  source_id: number;
  status: string;
  error: string;
  created_at: string;
  attempt: number;
  lease_until?: string;
  execution_version: number;
};
export type CrawlPolicy = {
  source_id: number;
  discovery_interval: string;
  detail_refresh_interval: string;
  min_request_interval: string;
  daily_page_budget: number;
  daily_byte_budget: number;
  enabled_at: string | null;
  baseline_completed_at: string | null;
};
export type CrawlPolicyInput = Omit<CrawlPolicy, 'source_id' | 'enabled_at' | 'baseline_completed_at'>;

export const getIngestSources = () => api<IngestSource[] | null>('admin/sources').then(d => d ?? []);
export const getIngestJobs = () => api<IngestJob[] | null>('admin/jobs').then(d => d ?? []);
export const getCrawlPolicies = () => api<CrawlPolicy[] | null>('admin/crawl-policies').then(d => d ?? []);
export const addIngestSource = (name: string, url: string) => api<{ok: boolean}>('admin/sources', {method: 'POST', body: {name, url}});
export const enqueueIngestJob = (sourceId: number) => api<{ok: boolean}>(`admin/sources/${sourceId}/jobs`, {method: 'POST'});
export const putCrawlPolicy = (sourceId: number, p: CrawlPolicyInput) =>
  api<{ok: boolean}>(`admin/sources/${sourceId}/crawl-policy`, {method: 'PUT', body: p});

// ---------------------------------------------------------------- release editor (PR #61)

export type ReleaseStatus = 'draft' | 'upcoming' | 'published' | 'unpublished' | 'held_review';
export const RELEASE_STATUSES: ReleaseStatus[] = ['draft', 'upcoming', 'published', 'unpublished', 'held_review'];
/** store.allowedTransitions; publishing is further gated by R-AGE on the server. */
export const ALLOWED_TRANSITIONS: Record<string, ReleaseStatus[]> = {
  draft: ['published', 'upcoming', 'unpublished'],
  upcoming: ['published', 'upcoming', 'draft', 'unpublished'],
  published: ['unpublished'],
  unpublished: ['published', 'draft'],
  held_review: ['published', 'unpublished'],
};
export const AGE_RATINGS = ['all', '13', '16', '18'] as const;

export type AdminReleaseSummary = {
  id: number;
  slug: string;
  title: string;
  title_ja: string | null;
  status: ReleaseStatus;
  release_date: string | null;
  scheduled_at: string | null;
  version: number;
  updated_at: string;
  identifiers: string[] | null;
  source_count: number;
  open_issues: number;
  pending_conflicts: number;
};

export type Override = {field: string; value: unknown; reason: string | null; actor: string; conflict_id: number | null; updated_at: string};
export type SourceKind = 'official_embed' | 'official_preview' | 'external_link' | 'hls_owned';
export const SOURCE_KINDS: SourceKind[] = ['official_embed', 'official_preview', 'external_link', 'hls_owned'];
export type VersionKind = 'main' | 'high_quality' | 'dub' | 'alt';
export const VERSION_KINDS: VersionKind[] = ['main', 'high_quality', 'dub', 'alt'];
/** store.RequiresLicense: everything except official previews and plain external links. */
export const requiresLicense = (kind: string) => kind !== 'official_preview' && kind !== 'external_link';

export type AdminPlaybackSource = {
  id: number;
  release_id: number;
  kind: SourceKind;
  provider: string;
  label: string;
  url: string | null;
  storage_prefix: string | null;
  max_height: number | null;
  has_hdr: boolean;
  duration_seconds: number | null;
  audio_langs: string[];
  subtitle_langs: string[];
  version_kind: VersionKind;
  is_alt_version: boolean;
  priority: number;
  region_allow: string[] | null;
  region_deny: string[] | null;
  license_ref: string | null;
  status: 'active' | 'degraded' | 'disabled' | string;
  health_score: number;
  health_band: HealthBand;
  health_checked_at: string | null;
  status_changed_by: string | null;
  created_at: string;
  updated_at: string;
};
export type AdminMediaSource = {source: string; media_id: string; media_url: string | null; confidence: string; promoted_source_id: number | null};
export type FilmingAge = {age: number; basis: 'shoot_year' | 'release_year'; approx: boolean};
export type AdminReleasePerformer = {
  id: number;
  slug: string;
  name: string;
  name_ja: string | null;
  birth_date: string | null;
  role: 'lead' | 'guest';
  is_primary: boolean;
  billing_order: number | null;
  credited_name: string | null;
  age_at_filming: FilmingAge | null;
  age_flag?: string;
};
export type TranslationStatusRow = {field: string; lang: string; status: string; updated_at: string | null};
export type ReviewEvent = {
  id: number;
  conflict_id: number | null;
  entity_type: string;
  entity_id: number;
  action: string;
  before: unknown;
  after: unknown;
  reason: string | null;
  actor: string | null;
  created_at: string;
};
export type AdminRelease = {
  id: number;
  slug: string;
  title: string;
  title_ja: string | null;
  synopsis: string | null;
  release_date: string | null;
  shoot_year: number | null;
  runtime_minutes: number | null;
  maker: string | null;
  label: string | null;
  series: string | null;
  director: string | null;
  censorship_type: string | null;
  edition_type: string;
  status: ReleaseStatus;
  scheduled_at: string | null;
  notified_at: string | null;
  age_rating: string | null;
  region_allow: string[] | null;
  region_deny: string[] | null;
  featured_note: string | null;
  version: number;
  created_at: string;
  updated_at: string;
  identifiers: {type: string; value: string; scope: string}[] | null;
  tags: {tag: string; slug: string}[] | null;
  overrides: Override[] | null;
  sources: AdminPlaybackSource[] | null;
  media_sources: AdminMediaSource[] | null;
  performers: AdminReleasePerformer[] | null;
  translations: {counts: Record<string, number>; items: TranslationStatusRow[] | null};
  audit_issues: AuditIssue[] | null;
  conflicts: Record<string, unknown>[] | null;
  review_events: ReviewEvent[] | null;
};
export type PatchResult = {release_id: number; version: number; changed: string[]; audit: unknown; status: string};
export type TransitionResult = {release_id: number; from: string; to: string; scheduled_at: string | null; version: number};

export type SourceInput = {
  kind: string;
  provider: string;
  label: string;
  url: string | null;
  storage_prefix: string | null;
  max_height: number | null;
  has_hdr: boolean;
  duration_seconds: number | null;
  audio_langs: string[];
  subtitle_langs: string[];
  version_kind: string;
  priority: number | null;
  region_allow: string[];
  region_deny: string[];
  license_ref: string | null;
};
export type PromoteInput = {
  source: string;
  kind: string;
  label: string;
  version_kind: string;
  priority: number;
  max_height: number | null;
  duration_seconds: number | null;
  license_ref: string;
};

export const getAdminReleases = (f: {q?: string; status?: string; cursor?: string | null; limit?: number}) =>
  api<{items: AdminReleaseSummary[] | null; next_cursor: string | null}>(`admin/releases${qs(f)}`).then(pageOf);
export const getAdminRelease = (id: number) => api<AdminRelease>(`admin/releases/${id}`);
export const patchAdminRelease = (id: number, body: Record<string, unknown>) =>
  api<PatchResult>(`admin/releases/${id}`, {method: 'PATCH', body});
export const transitionRelease = (
  id: number,
  body: {status: string; scheduled_at?: string; override_reason?: string; reason?: string; version?: number},
) => api<TransitionResult>(`admin/releases/${id}/status`, {method: 'POST', body});
export const clearOverride = (id: number, field: string, reason: string) =>
  api<{ok: boolean}>(`admin/releases/${id}/overrides/${enc(field)}`, {method: 'DELETE', body: reason ? {reason} : undefined});
export const getReleaseSources = (id: number) =>
  api<ItemsBody<AdminPlaybackSource>>(`admin/releases/${id}/sources`).then(itemsOf);
export const createReleaseSource = (id: number, input: SourceInput) =>
  api<AdminPlaybackSource>(`admin/releases/${id}/sources`, {method: 'POST', body: input});
export const updateReleaseSource = (id: number, sourceId: number, patch: Partial<SourceInput>) =>
  api<AdminPlaybackSource>(`admin/releases/${id}/sources/${sourceId}`, {method: 'PATCH', body: patch});
export const reorderReleaseSources = (id: number, ids: number[]) =>
  api<ItemsBody<AdminPlaybackSource>>(`admin/releases/${id}/sources/reorder`, {method: 'POST', body: {ids}}).then(itemsOf);
export const promoteMediaSource = (id: number, input: PromoteInput) =>
  api<AdminPlaybackSource>(`admin/releases/${id}/sources/promote`, {method: 'POST', body: input});
export const setReleaseSourceEnabled = (id: number, sourceId: number, enable: boolean, reason: string) =>
  api<AdminPlaybackSource>(`admin/releases/${id}/sources/${sourceId}/${enable ? 'enable' : 'disable'}`, {
    method: 'POST',
    body: reason ? {reason} : undefined,
  });
export const setReleasePerformer = (
  id: number,
  performerId: number,
  body: {role: 'lead' | 'guest'; billing_order?: number | null; credited_name?: string | null; reason?: string},
) => api<PatchResult>(`admin/releases/${id}/performers/${performerId}`, {method: 'PUT', body});
export const removeReleasePerformer = (id: number, performerId: number, reason: string) =>
  api<PatchResult>(`admin/releases/${id}/performers/${performerId}`, {method: 'DELETE', body: reason ? {reason} : undefined});

/** One performer named by a 409 AgeGateError ("Name (17, reason), ..."). */
export type AgeGateFinding = {name: string; age: number; reason: string};

/**
 * Parses the R-AGE publish block (store.AgeGateError, 409):
 * "R-AGE still fails: age at filming may be under 18: A (17, reason), B (16, reason); fix ...".
 * null when the error is something else.
 */
export function parseAgeGate(cause: unknown): AgeGateFinding[] | null {
  if (!(cause instanceof ApiError) || cause.status !== 409 || !cause.detail?.startsWith('R-AGE')) return null;
  const body = cause.detail.replace(/^.*?under 18:\s*/, '').replace(/;\s*fix .*$/, '');
  const out: AgeGateFinding[] = [];
  const re = /\s*(.+?) \((-?\d+), ([^)]*)\)(?:,|$)/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(body))) out.push({name: m[1].trim(), age: Number(m[2]), reason: m[3]});
  return out;
}

// ---------------------------------------------------------------- moderation

export type QueueKind = 'reports' | 'danmaku' | 'reviews' | 'rating_floods';
export type QueueCounts = {reports: number; reports_overdue: number; danmaku: number; reviews: number; rating_floods: number};
export type Report = {
  id: number;
  target_type: string;
  target_id: number;
  reporter_id: number | null;
  reason: string;
  note: string;
  status: string;
  due_at: string;
  overdue: boolean;
  created_at: string;
  resolved_at: string | null;
  resolution: string | null;
  target?: Record<string, unknown> | null;
};
export type QueuedDanmaku = {
  id: number;
  release_id: number;
  release_slug: string;
  version_key: string;
  t_ms: number;
  body: string;
  user_id: number;
  status: string;
  peak: number | null;
  open_reports: number;
  created_at: string;
};
export type QueuedReview = {
  id: number;
  release_id: number;
  release_slug: string;
  user_id: number;
  body: string;
  spoiler: boolean;
  status: string;
  likes: number;
  open_reports: number;
  created_at: string;
};
export type RatingFlood = {
  release_id: number;
  release_slug: string;
  score: number;
  window_start: string;
  window_end: string;
  votes: number;
  anonymous: number;
};
export type ModerationAction = 'fold' | 'delete' | 'delete_mute' | 'approve';
export type ModerationTarget = 'danmaku' | 'reviews' | 'ratings';
export type Keyword = {word: string; category: string};
export const KEYWORD_CATEGORIES = ['ad', 'abuse', 'spoiler', 'illegal', 'political', 'other'] as const;

export const getModerationOverview = (limit = 1) =>
  api<{counts: QueueCounts} & Record<QueueKind, CursorPage<unknown>>>(`admin/moderation/queue${qs({limit})}`);
export const getModerationQueue = <T,>(kind: QueueKind, f: {status?: string; cursor?: string | null; limit?: number} = {}) =>
  api<{items: T[] | null; next_cursor: string | null}>(`admin/moderation/queue${qs({kind, ...f})}`).then(pageOf);
export const moderate = (target: ModerationTarget, id: number, body: {action: ModerationAction; mute_days?: number; reason?: string}) =>
  api<{target_type: string; id: number; before: string; after: string; reports_resolved: number; muted_until?: string}>(
    `admin/moderation/${target}/${id}/action`,
    {method: 'POST', body},
  );
export const resolveReport = (id: number, status: 'resolved' | 'dismissed', resolution: string) =>
  api<Report>(`admin/moderation/reports/${id}/resolve`, {method: 'POST', body: {status, resolution}});
export const removeRatingFlood = (f: {release_id: number; score: number; window_start: string; reason?: string}) =>
  api<{removed: number}>('admin/moderation/rating-floods/remove', {method: 'POST', body: f});
export const getModerationWords = () => api<ItemsBody<Keyword>>('admin/moderation/words').then(itemsOf);
export const addModerationWord = (word: string, category: string) =>
  api<Keyword>('admin/moderation/words', {method: 'POST', body: {word, category}});
export const deleteModerationWord = (word: string) => api<{deleted: boolean}>(`admin/moderation/words/${enc(word)}`, {method: 'DELETE'});
export const unmuteUser = (userId: number, reason: string) =>
  api<{deleted: boolean}>(`admin/moderation/mutes/${userId}${qs({reason})}`, {method: 'DELETE'});

// ---------------------------------------------------------------- i18n

export const I18N_LANGS = ['zh-CN', 'zh-TW', 'en', 'ja', 'ko', 'vi', 'es', 'th'] as const;
export const LANG_NAMES: Record<string, string> = {
  'zh-CN': '简体中文',
  'zh-TW': '繁體中文',
  en: 'English',
  ja: '日本語',
  ko: '한국어',
  vi: 'Tiếng Việt',
  es: 'Español',
  th: 'ไทย',
};
export const TRANSLATION_STATUSES = ['machine', 'draft', 'reviewed', 'stale'] as const;
export const I18N_FIELDS: Record<string, string[]> = {release: ['title', 'synopsis'], performer: ['name', 'biography'], tag: ['name']};

export type TranslationTask = {
  entity_type: string;
  entity_id: string;
  field: string;
  lang: string;
  source: string | null;
  id: number | null;
  value: string | null;
  status: string;
  reviewed_value: string | null;
  submitted: boolean;
  outdated: boolean;
  submitted_at: string | null;
  reviewed_at: string | null;
  updated_by: string | null;
  updated_at: string | null;
};
export type TranslationStats = {total_fields: number; by_lang: Record<string, Record<string, number>>};
export type GlossaryEntry = {id: number; term: string; lang: string; translation: string; note: string; updated_by: string | null; updated_at: string};
export type UIString = {key: string; lang: string; value: string; status: string; updated_by: string | null; updated_at: string};

const tPath = (t: {entity_type: string; entity_id: string; field: string; lang: string}) =>
  `admin/i18n/translations/${enc(t.entity_type)}/${enc(t.entity_id)}/${enc(t.field)}/${enc(t.lang)}`;

export const getTranslationTasks = (f: {lang?: string; status?: string; entity_type?: string; missing?: boolean; cursor?: string | null; limit?: number}) =>
  api<{items: TranslationTask[] | null; next_cursor: string | null}>(`admin/i18n/translations${qs(f)}`).then(pageOf);
export const getTranslationStats = () => api<TranslationStats>('admin/i18n/stats');
export const markStale = () => api<{marked_stale: number}>('admin/i18n/translations/mark-stale', {method: 'POST'});
export const saveTranslation = (t: TranslationTask, value: string) => api<TranslationTask>(tPath(t), {method: 'PUT', body: {value}});
export const submitTranslation = (t: TranslationTask) => api<TranslationTask>(`${tPath(t)}/submit`, {method: 'POST'});
export const reviewTranslation = (t: TranslationTask) => api<TranslationTask>(`${tPath(t)}/review`, {method: 'POST'});
export const getGlossary = (f: {lang?: string; q?: string; limit?: number}) =>
  api<ItemsBody<GlossaryEntry>>(`admin/i18n/glossary${qs(f)}`).then(itemsOf);
export type GlossaryInput = {term: string; lang: string; translation: string; note: string};
export const createGlossary = (g: GlossaryInput) => api<GlossaryEntry>('admin/i18n/glossary', {method: 'POST', body: g});
export const updateGlossary = (id: number, g: GlossaryInput) => api<GlossaryEntry>(`admin/i18n/glossary/${id}`, {method: 'PUT', body: g});
export const deleteGlossary = (id: number) => api<{ok: boolean}>(`admin/i18n/glossary/${id}`, {method: 'DELETE'});
export const getUIStrings = (f: {lang?: string; status?: string; prefix?: string}) =>
  api<ItemsBody<UIString>>(`admin/i18n/ui${qs(f)}`).then(itemsOf);
export const putUIString = (lang: string, key: string, value: string, status: 'draft' | 'reviewed') =>
  api<UIString>(`admin/i18n/ui/${enc(lang)}/${enc(key)}`, {method: 'PUT', body: {value, status}});

// ---------------------------------------------------------------- labels

export const STATUS_LABELS: Record<string, string> = {
  draft: '草稿',
  upcoming: '待上线',
  published: '已上线',
  unpublished: '已下线',
  held_review: '合规暂停',
};
export const BAND_LABELS: Record<string, string> = {
  severe: '严重',
  degraded: '降级',
  healthy: '健康',
  excellent: '优秀',
  insufficient_data: '样本不足',
};
export const SEVERITY_LABELS: Record<string, string> = {blocking: '阻断', high: '高', medium: '中', low: '低'};
export const RULE_LABELS: Record<string, string> = {
  'R-AGE': '出演时年龄（合规）',
  'R-META': 'C · 元数据缺失',
  'R-TITLE': 'E · 繁简与标题规范',
  'R-STATS': 'G · 统计重建',
  'R-SOURCE': 'A · 片源健康',
  'R-WATERMARK': 'B · 水印与黑边',
  'R-SUBTITLE': 'D · 字幕问题',
  'R-SCREENSHOT': 'F · 截屏不足',
  'R-SEO': 'H · 收录检查',
};
export const KIND_LABELS: Record<string, string> = {
  official_embed: '官方嵌入',
  official_preview: '官方预告',
  external_link: '外部链接',
  hls_owned: '自有 HLS',
};
export const VERSION_LABELS: Record<string, string> = {main: '正片', high_quality: '高画质', dub: '配音', alt: '其他版本'};
export const TRANSLATION_LABELS: Record<string, string> = {
  machine: '机翻待审',
  draft: '草稿',
  reviewed: '已上线',
  stale: '需更新',
  missing: '缺失',
};
