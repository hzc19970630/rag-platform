-- The worker purges audit rows older than AUDIT_RETENTION_DAYS in id order.
-- Without a created_at index the batched DELETE degrades into a table scan as
-- the audit trail grows.
CREATE INDEX IF NOT EXISTS audit_logs_created_at_idx
    ON audit_logs(created_at, id);