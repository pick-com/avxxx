// 夜间任务 client (internal/api/admin_jobs.go): the jobs queue per kind, the
// nightly_runs history, retrying a failed job and running a sourceless kind now.

import {api, query} from './api';

export type QueueJob = {
  id: number;
  kind: string;
  source_id: number | null;
  source_name: string;
  phase: string;
  status: 'pending' | 'running' | 'failed' | 'succeeded';
  attempt: number;
  max_attempts: number;
  error: string;
  error_code: string;
  worker_id: string | null;
  lease_until: string | null;
  heartbeat_at: string | null;
  available_at: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
  result_counts?: Record<string, unknown>;
};

export type QueueKind = {
  kind: string;
  pending: number;
  running: number;
  failed_7d: number;
  succeeded_7d: number;
  last_succeeded_at: string | null;
  last_failed_at: string | null;
};

export type RunnableKind = {kind: string; label: string; runs: string};

export type JobQueue = {kinds: QueueKind[]; items: QueueJob[]; runnable: RunnableKind[]};

export type RunResult = 'ok' | 'issues' | 'failed' | 'not_applicable';
export type RunPoint = {job_id: number | null; started_at: string; duration_ms: number; checked: number; fixed: number; issues: number; result: RunResult};
export type RunSeries = {job: string; runs: RunPoint[]};

export const getJobQueue = (signal?: AbortSignal) =>
  api<JobQueue>('admin/jobs/queue', {signal}).then(d => ({kinds: d.kinds ?? [], items: d.items ?? [], runnable: d.runnable ?? []}));
export const getJobRuns = (p: {days?: number; job?: string}, signal?: AbortSignal) =>
  api<{days: number; jobs: RunSeries[] | null}>(`admin/jobs/runs${query(p)}`, {signal}).then(d => d.jobs ?? []);
export const retryJob = (id: number) => api<QueueJob>(`admin/jobs/${id}/retry`, {method: 'POST'});
export const runJobNow = (kind: string) => api<{job_id: number; enqueued: boolean; kind: string}>('admin/jobs/run', {method: 'POST', body: {kind}});
