-- Async answer generation. The API accepts a question, persists the user
-- message and the first three stream events, then enqueues one worker job. The
-- worker claims the row with a lease, continues the same event sequence and
-- records the terminal answer. A crashed worker is recovered by the stale-lease
-- sweep in the maintenance loop.
--
-- The unique key matches the idempotency lookup (tenant + user + conversation +
-- request): a client-supplied request id is only unique inside one
-- conversation, so it must never collide with another caller's request.
CREATE TABLE IF NOT EXISTS answer_jobs (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(64) NOT NULL,
    conversation_id VARCHAR(64) NOT NULL,
    request_id VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_owner VARCHAR(128) NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    next_sequence INTEGER NOT NULL DEFAULT 0,
    scope_json JSONB,
    error_code VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT answer_jobs_request_unique UNIQUE (tenant_id, user_id, conversation_id, request_id)
);

-- The stale-lease sweep scans only RUNNING rows, so the composite index keeps
-- it cheap even when completed history grows.
CREATE INDEX IF NOT EXISTS answer_jobs_status_lease_idx ON answer_jobs (status, lease_until);

-- Tenant isolation matches every other tenant-owned table (0023/0024): the
-- policy is strict, and runtime shadow mode works because the owner role
-- bypasses RLS while the application still filters by tenant_id itself.
ALTER TABLE answer_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE answer_jobs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON answer_jobs;
DROP POLICY IF EXISTS tenant_isolation ON answer_jobs;
CREATE POLICY tenant_isolation ON answer_jobs FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

-- Shadow audit stays consistent with the other tenant tables.
DROP TRIGGER IF EXISTS rls_shadow_write ON answer_jobs;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON answer_jobs
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

COMMENT ON TABLE answer_jobs IS
    'M13 async answers: one row per accepted question; owns the stream event sequence and worker lease.';
