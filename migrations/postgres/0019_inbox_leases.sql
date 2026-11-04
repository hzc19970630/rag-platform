-- Task inbox rows are claimed through a lease instead of a row lock. A worker
-- may only execute a row whose lease is absent or expired; this closes the
-- window where two workers that received the same delivery both ran the stage.
ALTER TABLE task_inbox
    ADD COLUMN IF NOT EXISTS lease_owner VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE task_inbox
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;

-- Reconcile reclaims only PROCESSING rows whose lease has expired. Backfill
-- rows left PROCESSING by the previous worker so they become reclaimable
-- instead of looking permanently live.
UPDATE task_inbox
SET lease_owner = 'legacy',
    lease_until = COALESCE(updated_at, NOW()) + INTERVAL '2 minutes'
WHERE status = 'PROCESSING' AND lease_until IS NULL;

CREATE INDEX IF NOT EXISTS task_inbox_lease_idx
    ON task_inbox(status, lease_until);
