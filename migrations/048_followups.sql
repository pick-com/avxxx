-- Small follow-ups to #99–#103. Re-run on every start: every statement is idempotent.

-- 收藏夹 manual order (NavFavorites 拖动排序). position orders a playlist's
-- items ascending; new items get min(position) - 1 so they appear first,
-- which matches the previous newest-first order. Rows without a position
-- (all rows on the first run, or rows written by an older binary during a
-- rolling deploy) are numbered newest-first ahead of the ordered ones on the
-- next start. Reordering rewrites 1..n.
ALTER TABLE playlist_items ADD COLUMN IF NOT EXISTS position integer;
WITH unordered AS (
  SELECT playlist_id, release_id,
         row_number() OVER (PARTITION BY playlist_id ORDER BY created_at DESC, release_id DESC)::int AS rn,
         count(*) OVER (PARTITION BY playlist_id)::int AS k
  FROM playlist_items WHERE position IS NULL
), base AS (
  SELECT playlist_id, min(position) AS lo FROM playlist_items WHERE position IS NOT NULL GROUP BY playlist_id
)
UPDATE playlist_items i SET position = u.rn - u.k - 1 + COALESCE(b.lo, u.k + 1)
FROM unordered u LEFT JOIN base b ON b.playlist_id = u.playlist_id
WHERE i.playlist_id = u.playlist_id AND i.release_id = u.release_id;
CREATE INDEX IF NOT EXISTS playlist_items_order ON playlist_items (playlist_id, position);

-- Takedown mail (#103; docs/COMPLIANCE.md 4): receipts and status mail for
-- claimants and counter-notifiers, who are not users. notification_outbox is
-- keyed by user_id (NOT NULL, cascades with the account, merged per user and
-- day, filtered by user_settings notify prefs), so a nullable user_id there
-- would touch every claim and fan-out query. This separate table keeps that
-- path unchanged and stores no address: the recipient is a role on the
-- ticket, resolved from takedown_requests when the mail is sent, so the
-- contact details exist only in the ticket row and follow its retention
-- (rows cascade with the ticket; a blanked address cancels the mail).
-- cmd/notifier delivers it with the email channel (internal/notify/claimant.go).
ALTER TABLE takedown_requests ADD COLUMN IF NOT EXISTS counter_lang text;
CREATE TABLE IF NOT EXISTS mail_outbox (
  id bigserial PRIMARY KEY,
  takedown_id bigint REFERENCES takedown_requests(id) ON DELETE CASCADE,
  recipient text NOT NULL CHECK (recipient IN ('claimant', 'counter')),
  kind text NOT NULL CHECK (kind IN ('takedown_received', 'takedown_status', 'takedown_counter')),
  payload jsonb NOT NULL DEFAULT '{}'::jsonb, -- {"status": ...}: the status the mail reports
  send_after timestamptz NOT NULL DEFAULT now(),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sending','sent','failed','cancelled')),
  attempt integer NOT NULL DEFAULT 0,
  last_error text NOT NULL DEFAULT '',
  claimed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz,
  CONSTRAINT mail_outbox_source CHECK (takedown_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS mail_outbox_due ON mail_outbox (send_after, id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS mail_outbox_takedown ON mail_outbox (takedown_id);
