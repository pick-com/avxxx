// Typed client for AdminUsers (PRD 11, tech plan 3.6): sign-in and
// enrollment (internal/api/admin_auth.go), admin accounts, the role matrix,
// dual approvals, the op log and site users (internal/api/admin_users.go).

import {api} from './api';

const enc = encodeURIComponent;

function qs(params: Record<string, string | number | null | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== null && v !== undefined && v !== '') q.set(k, String(v));
  }
  const text = q.toString();
  return text ? `?${text}` : '';
}

export type AdminPrincipal = {id: number; username: string; email: string; display_name: string; roles: string[]};

/** GET admin/session. auth "token" is the ADMIN_TOKEN break-glass path. */
export type AdminMe = {
  auth: 'session' | 'token';
  actor: string;
  admin: AdminPrincipal;
  permissions: string[];
  idle_timeout: number;
  max_age: number;
};

export const getAdminSession = (signal?: AbortSignal) => api<AdminMe>('admin/session', {signal});
export const adminLogout = () => api<{ok: boolean}>('admin/session/logout', {method: 'POST'});
export const regenerateRecoveryCodes = (code: string) =>
  api<{recovery_codes: string[]}>('admin/session/recovery-codes', {method: 'POST', body: {code}});

// ---- Sign-in (public, /api/admin-auth/*) ----

export const adminPasswordStep = (login: string, password: string) =>
  api<{challenge: string; expires_in: number}>('admin-auth/login', {method: 'POST', body: {login, password}});

export type AdminVerifyResult = {
  admin: AdminPrincipal;
  permissions: string[];
  expires_at: string;
  idle_timeout: number;
  method: 'totp' | 'recovery_code';
  recovery_codes_left?: number | null;
};

export const adminSecondFactor = (challenge: string, f: {code?: string; recovery_code?: string}) =>
  api<AdminVerifyResult>('admin-auth/verify', {method: 'POST', body: {challenge, ...f}});

export type AdminInvite = {username: string; email: string; otpauth_uri: string; secret: string; expires_at: string};
export const getAdminInvite = (token: string) => api<AdminInvite>('admin-auth/enroll/info', {method: 'POST', body: {token}});
export const completeAdminEnrollment = (token: string, password: string, code: string) =>
  api<{username: string; recovery_codes: string[]}>('admin-auth/enroll', {method: 'POST', body: {token, password, code}});

// ---- Roles ----

export const ROLE_LABELS: Record<string, string> = {
  super_admin: '超级管理员',
  content_editor: '内容编辑',
  ingest_ops: '入库运维',
  moderator: '审核员',
  operations: '运营',
  ad_sales: '广告销售',
  translator: '外部译者',
};
export const ROLES = Object.keys(ROLE_LABELS);
export const roleLabel = (r: string) => ROLE_LABELS[r] ?? r;

export type MatrixRow = {group: string; label: string; access: Record<string, 'none' | 'read' | 'write'>};
export type RoleMatrix = {roles: {role: string; label: string}[]; matrix: MatrixRow[]; dual_approval: string[]};
export const getRoleMatrix = (signal?: AbortSignal) => api<RoleMatrix>('admin/roles', {signal});

// ---- Admin accounts ----

export type AdminUser = {
  id: number;
  username: string;
  email: string;
  display_name: string;
  status: 'invited' | 'active' | 'disabled';
  roles: string[];
  totp_enrolled: boolean;
  recovery_codes_left: number;
  failed_attempts: number;
  locked_until: string | null;
  last_login_at: string | null;
  invite_expires_at: string | null;
  active_sessions: number;
  created_by: string;
  created_at: string;
};

export const getAdminUsers = (signal?: AbortSignal) =>
  api<{items: AdminUser[] | null}>('admin/admin-users', {signal}).then(d => d.items ?? []);

export const inviteAdmin = (input: {username: string; email: string; display_name: string; roles: string[]}) =>
  api<{admin: AdminUser; enroll_url: string}>('admin/admin-users', {method: 'POST', body: input});
export const reissueInvite = (id: number) => api<{enroll_url: string}>(`admin/admin-users/${id}/invite`, {method: 'POST'});
export const disableAdmin = (id: number, reason: string) => api<AdminUser>(`admin/admin-users/${id}/disable`, {method: 'POST', body: {reason}});

/** A dual-approval route answers 202 with the stored request instead of running. */
export type ApprovalPending = {approval_required: true; approval: Approval};
export const isApprovalPending = (v: unknown): v is ApprovalPending =>
  !!v && typeof v === 'object' && (v as {approval_required?: unknown}).approval_required === true;

export const setAdminRoles = (id: number, roles: string[], reason: string) =>
  api<AdminUser | ApprovalPending>(`admin/admin-users/${id}/roles`, {method: 'PUT', body: {roles, reason}});
export const enableAdmin = (id: number, reason: string) =>
  api<AdminUser | ApprovalPending>(`admin/admin-users/${id}/enable`, {method: 'POST', body: {reason}});
export const resetAdmin2FA = (id: number, reason: string) =>
  api<{enroll_url: string} | ApprovalPending>(`admin/admin-users/${id}/reset-2fa`, {method: 'POST', body: {reason}});

/** Absolute enrollment link (the token stays in the #fragment). */
export const absoluteLink = (path: string) => (typeof window === 'undefined' ? path : `${window.location.origin}${path}`);

// ---- Approvals ----

export type Approval = {
  id: number;
  route: string;
  method: string;
  path: string;
  query: string;
  body?: unknown;
  summary: string;
  requested_by: number;
  requested_by_name: string;
  requested_at: string;
  expires_at: string;
  status: 'pending' | 'executing' | 'executed' | 'failed' | 'rejected' | 'cancelled' | 'expired';
  decided_by: string | null;
  decided_at: string | null;
  decision_note: string;
  result_status: number | null;
  can_approve: boolean;
  mine: boolean;
};

export const getApprovals = (status: string, signal?: AbortSignal) =>
  api<{items: Approval[] | null}>(`admin/approvals${qs({status})}`, {signal}).then(d => d.items ?? []);
export const approveRequest = (id: number) =>
  api<{approval: Approval; result_status: number; result: unknown}>(`admin/approvals/${id}/approve`, {method: 'POST'});
export const rejectRequest = (id: number, note: string) =>
  api<{approval: Approval}>(`admin/approvals/${id}/reject`, {method: 'POST', body: {note}});

// ---- Op log ----

export type AuditEntry = {
  id: number;
  created_at: string;
  admin_id: number | null;
  actor: string;
  auth: 'session' | 'token' | 'anonymous';
  action: string;
  method: string;
  route: string;
  path: string;
  entity_type: string;
  entity_id: string;
  status: number;
  ip_hash: string;
  approval_id: number | null;
  detail: Record<string, unknown>;
};

export type AuditQuery = {q?: string; actor?: string; auth?: string; entity_type?: string; entity_id?: string; from?: string; to?: string; cursor?: string | null};
export const getAuditLog = (f: AuditQuery, signal?: AbortSignal) =>
  api<{items: AuditEntry[] | null; next_cursor: string | null}>(`admin/audit-log${qs({...f, limit: 50})}`, {signal}).then(d => ({
    items: d.items ?? [],
    next_cursor: d.next_cursor ?? null,
  }));

// ---- Site users ----

export type SiteUser = {
  id: number;
  email: string;
  display_name: string;
  created_at: string;
  last_seen_at: string | null;
  muted_until: string | null;
  banned: boolean;
  banned_until: string | null;
  ban_reason: string;
  deletion_requested_at: string | null;
  reports_against: number;
};

export const searchSiteUsers = (q: string, status: string, signal?: AbortSignal) =>
  api<{items: SiteUser[] | null}>(`admin/site-users${qs({q, status})}`, {signal}).then(d => d.items ?? []);
export const banSiteUser = (id: number, reason: string, days: number) =>
  api<SiteUser>(`admin/site-users/${enc(String(id))}/ban`, {method: 'POST', body: {reason, days}});
export const unbanSiteUser = (id: number, reason: string) =>
  api<SiteUser>(`admin/site-users/${enc(String(id))}/unban`, {method: 'POST', body: {reason}});
export const unmuteSiteUser = (id: number, reason: string) =>
  api<{deleted: boolean}>(`admin/moderation/mutes/${id}${qs({reason})}`, {method: 'DELETE'});
