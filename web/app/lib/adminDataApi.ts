// 数据 clients for the back office: the AdminVideo 数据标签 tab
// (internal/api/admin_curves.go) and the AdminDashboard site KPIs
// (internal/api/admin_site_metrics.go).

import {api} from './api';

/** One timeline of GET admin/releases/{id}/curves; arrays have 200 buckets. */
export type CurveVersion = {
  version_key: string;
  valid_plays: number;
  duration_ms: number | null;
  computed_at: string;
  /** False below min_valid_plays: replay_peak is null. */
  sufficient: boolean;
  retention: number[];
  viewers: number[];
  replays: number[];
  replay_rate: number[];
  replay_peak: number[] | null;
  /** Null when the timeline's duration is unknown. */
  skips: number[] | null;
  skip_rate: number[] | null;
  intro_skip_suggested: boolean;
};

export type ReleaseCurves = {release_id: number; min_valid_plays: number; intro_skip_suggested: boolean; versions: CurveVersion[]};

export const getReleaseCurves = (id: number, signal?: AbortSignal) =>
  api<ReleaseCurves>(`admin/releases/${id}/curves`, {signal}).then(d => ({...d, versions: d.versions ?? []}));

/** One UTC day of site_daily_metrics (GET admin/site-metrics, internal/sitemetrics). */
export type SiteDay = {
  day: string;
  plays: number;
  dau: number;
  signed_in: number;
  watch_seconds: number;
  completed_plays: number;
  /** completed_plays / plays (0-1), null without plays. */
  completion_rate: number | null;
  computed_at: string;
  /** Today (UTC), still being counted. */
  partial: boolean;
};

/** The latest closed day's value with its day-over-day and 7-day comparisons. */
export type SiteKPI = {
  value: number | null;
  prev_day: number | null;
  prev_7d_avg: number | null;
  /** Relative changes (0.12 = +12 %), null when the base is missing or 0. */
  dod: number | null;
  vs_7d: number | null;
};

export type SiteMetrics = {
  days: number;
  latest_day: string;
  kpis: {plays: SiteKPI; dau: SiteKPI; watch_seconds: SiteKPI; completion_rate: SiteKPI};
  series: SiteDay[];
  retention_days: number;
  job: string;
  definitions: Record<string, string>;
};

export const getSiteMetrics = (days = 30, signal?: AbortSignal) =>
  api<SiteMetrics>(`admin/site-metrics?days=${days}`, {signal}).then(d => ({...d, series: d.series ?? []}));
