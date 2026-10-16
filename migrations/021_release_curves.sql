-- Playback replay peak and retention curves (PRD 14.3). Computed hourly by
-- the 'release_curves' job (internal/playcurve, run by cmd/audit, enqueued
-- by cmd/scheduler) from the last 90 days of play events. Idempotent.
--
-- One row per (release, timeline, kind). version_key is the timeline, as in
-- danmaku_curves: 'main' (main / high-quality / dub) or 'alt:<source id>'.
-- kind:
--   viewers     plays whose bucket count is >= 1          (raw counts)
--   replays     plays whose bucket count is >= 2          (raw counts)
--   skips       plays that seeked forward across the bucket and never played it
--   retention   viewers / valid_plays, in basis points (0-10000)
--   replay_peak replay rate (replays / viewers), trimmed, smoothed, 0-255;
--               data NULL when valid_plays < 1000 or no replay at all
-- A timeline without any valid play has no rows; skips is absent when the
-- timeline's duration is unknown. duration_ms is the duration the buckets
-- were mapped with (NULL when unknown).
CREATE TABLE IF NOT EXISTS release_curves (
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  version_key text NOT NULL CHECK (version_key ~ '^(main|alt:[0-9]+)$'),
  kind text NOT NULL CHECK (kind IN ('viewers','replays','skips','replay_peak','retention')),
  data integer[] CHECK (data IS NULL OR (array_length(data, 1) = 200 AND array_ndims(data) = 1)),
  valid_plays integer NOT NULL DEFAULT 0 CHECK (valid_plays >= 0),
  duration_ms integer,
  computed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (release_id, version_key, kind)
);

-- End of the last fully successful run; the next run re-reads events from
-- a little before it (single row).
CREATE TABLE IF NOT EXISTS release_curve_state (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  watermark timestamptz NOT NULL
);
