-- M4 enterprise governance and operability.
--
-- Identity: users gain an OIDC identity link, server-side sessions make logout
-- and key rotation revocable, API keys are stored as hashes. Authorization
-- revisions make cache invalidation explicit instead of relying on a
-- per-request policy rebuild.

ALTER TABLE users ADD COLUMN IF NOT EXISTS oidc_issuer VARCHAR(512) NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS oidc_subject VARCHAR(255) NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS users_oidc_identity_idx ON users(oidc_issuer, oidc_subject) WHERE oidc_subject <> '';
CREATE UNIQUE INDEX IF NOT EXISTS users_email_idx ON users(LOWER(email));

ALTER TABLE tenants ADD COLUMN IF NOT EXISTS quota JSONB NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS auth_sessions (
    id VARCHAR(64) PRIMARY KEY,
    user_id VARCHAR(64) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    token_id VARCHAR(64) NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    user_agent VARCHAR(512) NOT NULL DEFAULT '',
    ip_address VARCHAR(128) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS auth_sessions_user_idx ON auth_sessions(user_id, tenant_id, expires_at DESC);
CREATE INDEX IF NOT EXISTS auth_sessions_expiry_idx ON auth_sessions(expires_at);

CREATE TABLE IF NOT EXISTS api_keys (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    created_by VARCHAR(64) NOT NULL REFERENCES users(id),
    name VARCHAR(255) NOT NULL,
    key_prefix VARCHAR(32) NOT NULL UNIQUE,
    key_hash VARCHAR(128) NOT NULL UNIQUE,
    capabilities JSONB NOT NULL DEFAULT '[]',
    knowledge_base_ids JSONB NOT NULL DEFAULT '[]',
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS api_keys_tenant_idx ON api_keys(tenant_id, created_at DESC);

-- One revision row per tenant. Every permission mutation increments it; readers
-- compare it before using cached Casbin results, so a missed notification cannot
-- keep serving stale authorization.
CREATE TABLE IF NOT EXISTS authorization_revisions (
    tenant_id VARCHAR(64) PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO authorization_revisions (tenant_id, revision)
SELECT id, 1 FROM tenants
ON CONFLICT (tenant_id) DO NOTHING;

-- Document lifecycle: delete becomes recoverable until purge_after. Objects stay
-- in place during retention, so restore does not depend on re-upload.
ALTER TABLE documents ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS deleted_by VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE documents ADD COLUMN IF NOT EXISTS purge_after TIMESTAMPTZ;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS restored_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS documents_deleted_idx ON documents(tenant_id, deleted_at, purge_after);

-- Reparse must be able to version the same content again. Upload-level duplicate
-- detection still deduplicates content; this constraint only blocked explicit
-- reparse of an unchanged document.
ALTER TABLE document_versions DROP CONSTRAINT IF EXISTS document_versions_document_id_content_hash_key;
CREATE INDEX IF NOT EXISTS document_versions_content_idx ON document_versions(document_id, content_hash);

-- Outbox retries need bookkeeping so a transient Redis failure does not spin or
-- silently drop an event.
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ;
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS outbox_retry_idx ON outbox_events(published_at, next_attempt_at, id);

-- Dead letters become an operator-facing queue instead of an insert-only log.
ALTER TABLE task_dead_letters ADD COLUMN IF NOT EXISTS payload JSONB NOT NULL DEFAULT '{}';
ALTER TABLE task_dead_letters ADD COLUMN IF NOT EXISTS status VARCHAR(32) NOT NULL DEFAULT 'PENDING';
ALTER TABLE task_dead_letters ADD COLUMN IF NOT EXISTS resolved_at TIMESTAMPTZ;
ALTER TABLE task_dead_letters ADD COLUMN IF NOT EXISTS resolved_by VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE task_dead_letters ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
CREATE INDEX IF NOT EXISTS task_dead_letters_status_idx ON task_dead_letters(tenant_id, status, id DESC);

-- Audit queries need request correlation and client origin. The existing
-- (tenant_id, actor_id, id DESC) index stays for the self-service view; admins
-- query by tenant and time.
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS request_id VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS ip_address VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS user_agent VARCHAR(512) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS audit_logs_tenant_idx ON audit_logs(tenant_id, id DESC);
