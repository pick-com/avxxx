// Typed client for the admin back office (PRD 11). Every path is under
// admin/*, so api() sends the admin session (cookie + X-Admin-CSRF; the
// signed-in admin is the actor). Shapes mirror internal/api + internal/store on the backend:
//   admin_audit.go   dashboard, audit issues, audit run, playback source health
//   admin.go         ingest sources, jobs, crawl policies
//   admin_release.go release editor (PR #61; may 404 on older backends)
//   moderation.go    moderation queue, actions, words, mutes
//   admin_i18n.go    translations, stats, glossary, UI strings
//   search.go        AdminSearch: stats, zero-result terms, synonyms,
//                    redirects, blocklist, term fixes (search PR; may 404)
//   admin_performer.go AdminActors: performer list / editor, requests,
//                    similarity preview (admin-performers PR; may 404)

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
    if (cause.status === 401) return '登录已过期，请重新登录后台';
    if (cause.status === 403) return '当前角色没有此操作的权限';
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

export type QueueKind = 'reports' | 'danmaku' | 'reviews' | 'rating_floods' | 'comments';
export type QueueCounts = {reports: number; reports_overdue: number; danmaku: number; reviews: number; rating_floods: number; comments?: number};
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
/** store.QueuedComment (评论; parent_id set on a reply). */
export type QueuedComment = {
  id: number;
  release_id: number;
  release_slug: string;
  parent_id: number | null;
  user_id: number;
  body: string;
  spoiler: boolean;
  status: string;
  likes: number;
  open_reports: number;
  created_at: string;
  /** Earlier versions kept from author edits (getCommentEdits). */
  edits?: number;
  edited_at?: string | null;
};
/** store.CommentHistory: the current text and every earlier version, oldest first. */
export type CommentHistory = {
  id: number;
  user_id: number;
  body: string;
  spoiler: boolean;
  status: string;
  created_at: string;
  edited_at: string | null;
  versions: {body: string; spoiler: boolean; status: string; replaced_at: string}[];
};
export const getCommentEdits = (id: number) => api<CommentHistory>(`admin/moderation/comments/${id}/edits`);
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
export type ModerationTarget = 'danmaku' | 'reviews' | 'comments' | 'ratings';
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
/** Bulk removal needs a second admin: a signed-in admin gets {approval_required} (202) and the request waits in /admin/approvals. */
export const removeRatingFlood = (f: {release_id: number; score: number; window_start: string; reason?: string}) =>
  api<{removed: number} | {approval_required: true; approval: {id: number}}>('admin/moderation/rating-floods/remove', {method: 'POST', body: f});
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

// ---------------------------------------------------------------- AdminSearch (search.go)

export type SearchTotals = {
  searches: number;
  zero_results: number;
  clicks: number;
  first_page_clicks: number;
  rescued: number;
  zero_result_rate: number;
  zero_result_target: number;
  meets_target: boolean;
  ctr: number;
  first_page_ctr: number;
  rescue_rate: number;
};
export type SearchStats = {days: number; totals: SearchTotals; daily: (SearchTotals & {day: string})[] | null; trgm: boolean};
export type NearMatch = {type: 'release' | 'performer' | string; id: number; name: string; field: string; status: string; similarity: number};
export type ZeroClass = 'not_in_library' | 'in_library_missed' | 'not_video';
export type ZeroResultTerm = {
  term: string;
  searches: number;
  zero_results: number;
  rescued: number;
  last_seen: string;
  stripped?: string;
  would_rescue: boolean;
  resolved: boolean;
  near_matches: NearMatch[] | null;
  class: ZeroClass | string;
  class_label: string;
  actions: string[] | null;
};
export type SearchSynonym = {id: number; term: string; canonical: string; created_by: string; created_at: string; updated_at: string};
export type SearchRedirectRule = {id: number; term: string; target_url: string; reason: string; created_by: string; created_at: string; updated_at: string};
export type SearchBlockedTerm = {id: number; term: string; reason: string; created_by: string; created_at: string};
export type TermFix = {entity_type: string; entity_id: number; action: string; value: string; term: string};

export const getSearchStats = (days: number) => api<SearchStats>(`admin/search/stats${qs({days})}`);
export const getZeroResultTerms = (f: {days: number; min: number; limit?: number}) =>
  api<{days: number; min: number; items: ZeroResultTerm[] | null}>(`admin/search/zero-results${qs(f)}`).then(itemsOf);
export const getSynonyms = (q = '') => api<ItemsBody<SearchSynonym>>(`admin/search/synonyms${qs({q})}`).then(itemsOf);
export const createSynonym = (term: string, canonical: string) =>
  api<SearchSynonym>('admin/search/synonyms', {method: 'POST', body: {term, canonical}});
export const updateSynonym = (id: number, term: string, canonical: string) =>
  api<SearchSynonym>(`admin/search/synonyms/${id}`, {method: 'PUT', body: {term, canonical}});
export const deleteSynonym = (id: number) => api<null>(`admin/search/synonyms/${id}`, {method: 'DELETE'});
export const getRedirects = (q = '') => api<ItemsBody<SearchRedirectRule>>(`admin/search/redirects${qs({q})}`).then(itemsOf);
export const createRedirect = (term: string, target_url: string, reason: string) =>
  api<SearchRedirectRule>('admin/search/redirects', {method: 'POST', body: {term, target_url, reason}});
export const updateRedirect = (id: number, term: string, target_url: string, reason: string) =>
  api<SearchRedirectRule>(`admin/search/redirects/${id}`, {method: 'PUT', body: {term, target_url, reason}});
export const deleteRedirect = (id: number) => api<null>(`admin/search/redirects/${id}`, {method: 'DELETE'});
export const getBlocklist = () => api<ItemsBody<SearchBlockedTerm>>('admin/search/blocklist').then(itemsOf);
export const blockTerm = (term: string, reason: string) => api<SearchBlockedTerm>('admin/search/blocklist', {method: 'POST', body: {term, reason}});
export const unblockTerm = (id: number) => api<null>(`admin/search/blocklist/${id}`, {method: 'DELETE'});
export const aliasFromTerm = (b: {term: string; performer_id: number; alias?: string; reason?: string}) =>
  api<TermFix>('admin/search/terms/alias', {method: 'POST', body: b});
export const englishTitleFromTerm = (b: {term: string; release_id: number; title?: string; reason?: string}) =>
  api<TermFix>('admin/search/terms/english-title', {method: 'POST', body: b});
/** 补片 (draft) or 建预约页 (upcoming, with scheduled_at) from a zero-result term; needs catalog:write. */
export const releaseFromTerm = (b: {term: string; title?: string; code?: string; scheduled_at?: string; reason?: string}) =>
  api<TermFix & {slug: string; status: ReleaseStatus}>('admin/search/terms/release', {method: 'POST', body: b});

// ---------------------------------------------------------------- AdminActors (admin_performer.go)

export type PerformerFilter =
  | 'incomplete'
  | 'missing_birth'
  | 'missing_photo'
  | 'upcoming'
  | 'birth_hidden'
  | 'suspected_duplicate'
  | 'hidden'
  | 'name_issue';
export const PERFORMER_FILTERS: {v: PerformerFilter; t: string}[] = [
  {v: 'incomplete', t: '信息不全'},
  {v: 'missing_birth', t: '缺生日'},
  {v: 'missing_photo', t: '缺照片'},
  {v: 'upcoming', t: '有即将上线作品'},
  {v: 'birth_hidden', t: '生日已隐藏'},
  {v: 'suspected_duplicate', t: '疑似重复'},
  {v: 'hidden', t: '已隐藏'},
  {v: 'name_issue', t: '名称可疑'},
];
export const COMPLETENESS_LABELS: Record<string, string> = {
  canonical_name_ja: '日文名',
  birth_date: '生日',
  birth_place: '出生地',
  debut_date: '出道日期',
  height_cm: '身高',
  photo: '照片',
};

export type AdminPerformerSummary = {
  id: number;
  slug: string;
  name: string;
  name_ja: string | null;
  status: 'active' | 'hidden' | string;
  hidden_reason: string | null;
  birth_date: string | null;
  birth_date_hidden: boolean;
  works: number;
  published_works: number;
  upcoming_works: number;
  has_photo: boolean;
  completeness: number;
  missing: string[] | null;
  open_issues: number;
  open_requests: number;
  duplicate_candidates: number;
  version: number;
  updated_at: string;
};
export type PerformerAlias = {id: number; name: string; language: string | null; alias_type: string};
export const ALIAS_TYPES = ['stage', 'birth', 'romanized', 'former', 'other'] as const;
export const ALIAS_TYPE_LABELS: Record<string, string> = {
  stage: '艺名',
  birth: '本名',
  romanized: '罗马字',
  former: '曾用名',
  other: '其他',
  merged: '合并带入',
  translation: '译名',
  search: '搜索词',
};
export type AdminPerformerWork = {
  release_id: number;
  slug: string;
  title: string;
  status: string;
  release_date: string | null;
  scheduled_at: string | null;
  role: 'lead' | 'guest' | string;
  is_primary: boolean;
  billing_order: number | null;
  credited_name: string | null;
  upcoming: boolean;
  age_at_filming: FilmingAge | null;
};
export type SimilarEvidence = {
  metadata: number;
  co_star: number;
  co_star_score: number;
  co_watch: number;
  shared_tags?: string[] | null;
  shared_makers?: string[] | null;
};
export type AdminSimilarPerformer = {
  id: number;
  slug: string;
  name: string;
  status: string;
  score: number;
  match_percent: number;
  reason: string;
  evidence: SimilarEvidence;
};
export type PerformerRequestKind = 'correct_field' | 'hide_birth_date' | 'remove_photo' | 'other';
export const REQUEST_KIND_LABELS: Record<string, string> = {
  correct_field: '更正资料',
  hide_birth_date: '隐藏生日',
  remove_photo: '删除照片',
  other: '其他',
};
export const REQUEST_STATUS_LABELS: Record<string, string> = {open: '待处理', approved: '已批准', rejected: '已驳回'};
export const REQUESTER_ROLE_LABELS: Record<string, string> = {self: '本人', agency: '经纪方', other: '其他'};
export type PerformerRequest = {
  id: number;
  performer_id: number;
  performer_slug: string;
  performer_name: string;
  kind: PerformerRequestKind | string;
  field_name: string | null;
  proposed_value: unknown;
  requester_contact: string;
  evidence: {role?: string; message?: string; links?: string[] | null} | null;
  status: 'open' | 'approved' | 'rejected' | string;
  due_at: string;
  overdue: boolean;
  resolved_by: string | null;
  resolution_note: string | null;
  created_at: string;
  resolved_at: string | null;
};
export type AdminPerformer = {
  id: number;
  slug: string;
  name: string;
  name_ja: string | null;
  birth_date: string | null;
  birth_date_hidden: boolean;
  birth_place: string | null;
  debut_date: string | null;
  debut_age: number | null;
  height_cm: number | null;
  bust_cm: number | null;
  waist_cm: number | null;
  hip_cm: number | null;
  cup_size: string | null;
  shoe_size: number | null;
  biography: string | null;
  status: string;
  hidden_reason: string | null;
  merged_into_id: number | null;
  version: number;
  created_at: string;
  updated_at: string;
  photos: number;
  completeness: number;
  missing: string[] | null;
  overrides: Override[] | null;
  aliases: PerformerAlias[] | null;
  works: AdminPerformerWork[] | null;
  upcoming: AdminPerformerWork[] | null;
  similar: AdminSimilarPerformer[] | null;
  duplicate_candidates: Record<string, unknown>[] | null;
  audit_issues: AuditIssue[] | null;
  requests: PerformerRequest[] | null;
  review_events: ReviewEvent[] | null;
};
export type AgeRecheck = {releases: number; flagged: number; held: number; status_changed: number; still_held: number};
export type PerformerPatchResult = {performer_id: number; version: number; changed: string[] | null; age_recheck?: AgeRecheck};
export type PerformerStatusResult = {performer_id: number; from: string; to: string; hidden_reason: string | null; version: number; resolved_issues?: number};
export type RequestDecisionResult = {request: PerformerRequest; applied?: Record<string, unknown> | null; age_recheck?: AgeRecheck};
export type Weights = {metadata: number; co_star: number; co_watch: number; visual: number};
export type WeightsView = Weights & {updated_by?: string; updated_at: string; effective_from: string};
export type PreviewMatch = {id: number; slug: string; name: string; score: number; match_percent: number; rank: number; stored_rank: number; reason: string};
export type PreviewSample = {
  performer: {id: number; slug: string; name: string};
  current: PreviewMatch[] | null;
  proposed: PreviewMatch[] | null;
  changed: boolean;
  overlap: number;
};
export type SimilarityPreview = {weights: Weights; current_weights: Weights; saved: false; effective_from: string; samples: PreviewSample[] | null; note: string};

export const getAdminPerformers = (f: {q?: string; status?: string; filter?: string; cursor?: string | null; limit?: number}) =>
  api<{items: AdminPerformerSummary[] | null; next_cursor: string | null; counts: Record<string, number> | null}>(`admin/performers${qs(f)}`).then(d => ({
    ...pageOf(d),
    counts: d?.counts ?? {},
  }));
export const getAdminPerformer = (id: number) => api<AdminPerformer>(`admin/performers/${id}`);
export const patchAdminPerformer = (id: number, body: Record<string, unknown>) =>
  api<PerformerPatchResult>(`admin/performers/${id}`, {method: 'PATCH', body});
export const clearPerformerOverride = (id: number, field: string, reason: string) =>
  api<{ok: boolean}>(`admin/performers/${id}/overrides/${enc(field)}`, {method: 'DELETE', body: reason ? {reason} : undefined});
export const addPerformerAlias = (id: number, b: {name: string; language?: string; alias_type?: string; reason?: string}) =>
  api<PerformerAlias>(`admin/performers/${id}/aliases`, {method: 'POST', body: b});
export const removePerformerAlias = (id: number, aliasId: number, reason: string) =>
  api<{ok: boolean}>(`admin/performers/${id}/aliases/${aliasId}`, {method: 'DELETE', body: reason ? {reason} : undefined});
export const setPerformerStatus = (id: number, b: {status: 'active' | 'hidden'; reason?: string; version?: number}) =>
  api<PerformerStatusResult>(`admin/performers/${id}/status`, {method: 'POST', body: b});
export const getPerformerRequests = (f: {status?: string; kind?: string; performer_id?: number; cursor?: string | null; limit?: number}) =>
  api<{items: PerformerRequest[] | null; next_cursor: string | null}>(`admin/performer-requests${qs(f)}`).then(pageOf);
export const decidePerformerRequest = (id: number, to: 'approve' | 'reject', b: {note?: string; value?: unknown}) =>
  api<RequestDecisionResult>(`admin/performer-requests/${id}/${to}`, {method: 'POST', body: b});
export const getSimilarityWeights = () => api<WeightsView>('admin/similarity-weights');
export const putSimilarityWeights = (w: Weights) => api<WeightsView>('admin/similarity-weights', {method: 'PUT', body: w});
export const previewSimilarityWeights = (b: Partial<Weights> & {performer_ids?: number[]; sample?: number; top?: number}) =>
  api<SimilarityPreview>('admin/similarity-weights/preview', {method: 'POST', body: b});

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
