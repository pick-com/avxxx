-- Web Push subscriptions and the outbox sender lease (PRD 17, tech plan 3.7).
-- Re-run on every start: every statement is idempotent.

-- One row per browser push subscription (PushSubscription.toJSON()). The
-- endpoint is a capability URL issued by the browser's push service; it is
-- unique across users (re-subscribing from another account moves the row).
-- p256dh / auth are the base64url client keys used for RFC 8291 encryption.
CREATE TABLE IF NOT EXISTS push_subscriptions (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  endpoint text NOT NULL UNIQUE,
  p256dh text NOT NULL,
  auth text NOT NULL,
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  last_success_at timestamptz,
  -- consecutive non-404/410 delivery failures; reset on success
  failures integer NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS push_subscriptions_user ON push_subscriptions (user_id);

-- Sender lease: when a notifier claimed the row (status 'sending'). Rows
-- stuck in 'sending' past the lease (crashed worker) go back to 'pending'.
ALTER TABLE notification_outbox ADD COLUMN IF NOT EXISTS claimed_at timestamptz;
CREATE INDEX IF NOT EXISTS notification_outbox_sending ON notification_outbox (claimed_at)
  WHERE status = 'sending';
