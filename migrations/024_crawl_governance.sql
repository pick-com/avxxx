-- Crawl governance (FINAL_DATA_DESIGN 2.4, 7, 10.3): license/terms gate,
-- baseline vs incremental vs verify job phases with separate checkpoints and
-- budgets, and field-priority policy bookkeeping. Re-run on every start:
-- every statement is idempotent. (022/023 are reserved for other work.)

-- ---- License / terms review on sources.
ALTER TABLE sources ADD COLUMN IF NOT EXISTS terms_reviewed_at timestamptz;
ALTER TABLE sources ADD COLUMN IF NOT EXISTS terms_reviewed_by text;
ALTER TABLE sources ADD COLUMN IF NOT EXISTS license_note text;
-- An explicit admin request for a baseline pass. The scheduler keeps
-- enqueueing baseline jobs while it is set; a completed pass clears it.
ALTER TABLE sources ADD COLUMN IF NOT EXISTS baseline_requested_at timestamptz;
ALTER TABLE sources ADD COLUMN IF NOT EXISTS baseline_requested_by text;

-- Conservative migration: any status outside the catalog.LicenseStatuses
-- enum becomes review_required (manual verify only). Statuses already in the
-- enum are kept, but without terms_reviewed_at they still cannot run
-- automatic crawls until an admin records a review.
UPDATE sources SET license_status='review_required'
WHERE license_status NOT IN ('review_required','manual_only','denied','terms_confirmed','licensed','official_api','internal');
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'sources_license_status_valid') THEN
    ALTER TABLE sources ADD CONSTRAINT sources_license_status_valid
      CHECK (license_status IN ('review_required','manual_only','denied','terms_confirmed','licensed','official_api','internal'));
  END IF;
END
$$;

-- ---- Job phases. Non-crawl jobs (kind <> 'crawl') ignore phase.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS phase text NOT NULL DEFAULT 'incremental';
-- Discovery page cap for this job (verify jobs; NULL = the phase default).
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS page_cap integer;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS requested_by text;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'jobs_phase_valid') THEN
    ALTER TABLE jobs ADD CONSTRAINT jobs_phase_valid CHECK (phase IN ('baseline','incremental','verify'));
  END IF;
END
$$;
-- One active job per (source, phase): a long baseline no longer blocks the
-- daily incremental job from being queued. Claim still runs one job per
-- source at a time and prefers verify, then incremental, then baseline.
DROP INDEX IF EXISTS jobs_active_source;
CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_source_phase ON jobs(source_id, phase)
  WHERE source_id IS NOT NULL AND status IN ('pending','running');

-- ---- Checkpoints: discovery checkpoints are partitioned by phase. The
-- pre-phase discovery checkpoint was a resumable full pass: it becomes the
-- baseline checkpoint; incremental starts fresh.
UPDATE crawl_checkpoints c SET partition_key='baseline'
WHERE c.task_type='catalog_discovery' AND c.partition_key=''
  AND NOT EXISTS (SELECT 1 FROM crawl_checkpoints b WHERE b.source_id=c.source_id AND b.task_type='catalog_discovery' AND b.partition_key='baseline');

-- ---- Separate budgets: baseline spends its own daily budget so it cannot
-- starve the incremental pass, and yields after baseline_pages_per_run pages.
ALTER TABLE source_crawl_policies ADD COLUMN IF NOT EXISTS baseline_daily_page_budget integer NOT NULL DEFAULT 0;
ALTER TABLE source_crawl_policies ADD COLUMN IF NOT EXISTS baseline_daily_byte_budget bigint NOT NULL DEFAULT 0;
ALTER TABLE source_crawl_policies ADD COLUMN IF NOT EXISTS baseline_pages_per_run integer NOT NULL DEFAULT 20;
ALTER TABLE source_crawl_usage ADD COLUMN IF NOT EXISTS phase text NOT NULL DEFAULT 'incremental';
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'source_crawl_usage_pkey'
             AND pg_get_constraintdef(oid) NOT LIKE '%phase%') THEN
    ALTER TABLE source_crawl_usage DROP CONSTRAINT source_crawl_usage_pkey;
    ALTER TABLE source_crawl_usage ADD CONSTRAINT source_crawl_usage_pkey PRIMARY KEY (source_id, usage_date, phase);
  END IF;
END
$$;

-- ---- Field priority policies (store.SaveCatalogRecord): lower priority
-- number wins. Rows override the source_kind tier defaults per field.
ALTER TABLE source_field_policies ADD COLUMN IF NOT EXISTS updated_by text;
ALTER TABLE source_field_policies ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();
