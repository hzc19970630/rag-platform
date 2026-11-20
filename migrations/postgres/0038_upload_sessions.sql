-- B30: resumable upload sessions. One row per file; a batch upload is N rows.
-- The bytes live in object storage, so the row only carries orchestration state:
-- the API never buffers a document and a retry resumes from the stored part
-- list instead of starting over.

CREATE TABLE IF NOT EXISTS upload_sessions (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    -- document_id/version_id are minted when the session is created so the
    -- object key stays stable before the document row exists. Neither is a
    -- foreign key: a session that never completes must not create documents.
    document_id VARCHAR(64) NOT NULL,
    version_id VARCHAR(64) NOT NULL,
    -- No FK either: a folder deleted mid-upload must fail completion instead of
    -- silently re-rooting the document.
    folder_id VARCHAR(64),
    filename VARCHAR(255) NOT NULL,
    mime_type VARCHAR(255) NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    part_size BIGINT NOT NULL CHECK (part_size > 0),
    part_count INTEGER NOT NULL CHECK (part_count > 0),
    object_key VARCHAR(1024) NOT NULL,
    -- S3 multipart upload id. Empty only for rows created before the adapter
    -- finished bootstrapping; the sweeper aborts whatever is left on expiry.
    multipart_upload_id VARCHAR(512) NOT NULL DEFAULT '',
    -- direct = browser PUTs presigned part URLs to object storage;
    -- proxy  = browser PUTs parts to the API, which streams them to storage.
    transport VARCHAR(16) NOT NULL CHECK (transport IN ('direct', 'proxy')),
    status VARCHAR(16) NOT NULL CHECK (status IN ('pending', 'uploading', 'completing', 'completed', 'aborted', 'expired')),
    -- Quota is reserved at create time and settled when the version row is
    -- written, so concurrent sessions cannot over-commit tenant storage.
    reserved_bytes BIGINT NOT NULL DEFAULT 0,
    idempotency_key VARCHAR(255) NOT NULL,
    created_by VARCHAR(64) NOT NULL,
    -- Completion result, kept so a repeated complete/status call can replay the
    -- same document instead of registering a second version.
    result_document_id VARCHAR(64) NOT NULL DEFAULT '',
    result_version_id VARCHAR(64) NOT NULL DEFAULT '',
    result_pipeline_id VARCHAR(64) NOT NULL DEFAULT '',
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT upload_sessions_part_math_check CHECK (
        size_bytes <= part_size * part_count
        AND size_bytes > part_size * (part_count - 1)
    ),
    UNIQUE (tenant_id, idempotency_key)
);

-- Active-session limits per tenant/knowledge base and the expiry sweeper.
CREATE INDEX IF NOT EXISTS upload_sessions_tenant_status_idx
    ON upload_sessions(tenant_id, status);
CREATE INDEX IF NOT EXISTS upload_sessions_kb_status_idx
    ON upload_sessions(knowledge_base_id, status);
CREATE INDEX IF NOT EXISTS upload_sessions_expiry_idx
    ON upload_sessions(status, expires_at);

ALTER TABLE upload_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE upload_sessions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON upload_sessions;
DROP POLICY IF EXISTS tenant_isolation ON upload_sessions;
CREATE POLICY tenant_isolation ON upload_sessions FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON upload_sessions;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON upload_sessions
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

COMMENT ON TABLE upload_sessions IS
    'B30 resumable upload sessions: one per file, bytes in object storage, quota reserved until completion.';
