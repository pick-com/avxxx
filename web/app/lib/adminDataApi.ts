// 数据 clients for the back office: the AdminVideo 数据标签 tab
// (internal/api/admin_curves.go).

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
