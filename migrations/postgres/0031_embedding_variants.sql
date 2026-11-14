-- D3 embedding variants: vectors live per (chunk, model) instead of only on the
-- chunk row, so a knowledge base can be migrated to another vector space while
-- the old space stays searchable and rollback is a binding flip.
--
-- chunks.embedding remains during the transition: ingestion dual-writes both
-- locations, which keeps a rolling deployment readable by the previous binary
-- and gives backfill a source.
CREATE TABLE IF NOT EXISTS chunk_embeddings (
    chunk_id VARCHAR(96) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    knowledge_base_id VARCHAR(96) NOT NULL,
    model VARCHAR(128) NOT NULL,
    dimension INTEGER NOT NULL,
    embedding halfvec NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (chunk_id, model),
    CONSTRAINT chunk_embeddings_dimension_check CHECK (dimension > 0),
    CONSTRAINT chunk_embeddings_chunk_fk FOREIGN KEY (chunk_id) REFERENCES chunks(id) ON DELETE CASCADE
);

-- The ANN index is per dimension and lives under a versioned name, matching the
-- chunks index contract. It is created by embedctl because dimensions are data,
-- not schema. This scope index keeps backfill and cleanup bounded per tenant.
CREATE INDEX IF NOT EXISTS chunk_embeddings_scope_idx
    ON chunk_embeddings (tenant_id, knowledge_base_id, model);
CREATE INDEX IF NOT EXISTS chunk_embeddings_dimension_idx
    ON chunk_embeddings (dimension, model);

ALTER TABLE chunk_embeddings ENABLE ROW LEVEL SECURITY;
ALTER TABLE chunk_embeddings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON chunk_embeddings;
DROP POLICY IF EXISTS tenant_isolation ON chunk_embeddings;
CREATE POLICY tenant_isolation ON chunk_embeddings FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON chunk_embeddings;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON chunk_embeddings
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

-- One managed migration per knowledge base. The partial unique index is the
-- concurrency guard: a second start while one is active fails instead of
-- creating two writers for the same vector space.
CREATE TABLE IF NOT EXISTS embedding_migrations (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    knowledge_base_id VARCHAR(96) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    source_model VARCHAR(128) NOT NULL DEFAULT '',
    source_dimension INTEGER NOT NULL DEFAULT 0,
    target_model VARCHAR(128) NOT NULL,
    target_dimension INTEGER NOT NULL,
    total_chunks BIGINT NOT NULL DEFAULT 0,
    done_chunks BIGINT NOT NULL DEFAULT 0,
    cursor_chunk_id VARCHAR(96) NOT NULL DEFAULT '',
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_by VARCHAR(64) NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    ready_at TIMESTAMPTZ,
    cutover_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT embedding_migrations_status_check CHECK (status IN (
        'pending', 'building_index', 'reembedding', 'verifying', 'ready',
        'cutover', 'finalized', 'rolled_back', 'cancelled', 'failed'
    )),
    CONSTRAINT embedding_migrations_dimension_check CHECK (target_dimension > 0),
    CONSTRAINT embedding_migrations_kb_fk FOREIGN KEY (knowledge_base_id)
        REFERENCES knowledge_bases(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS embedding_migrations_active_idx
    ON embedding_migrations (tenant_id, knowledge_base_id)
    WHERE status IN ('pending', 'building_index', 'reembedding', 'verifying', 'ready');
CREATE INDEX IF NOT EXISTS embedding_migrations_kb_idx
    ON embedding_migrations (tenant_id, knowledge_base_id, created_at DESC);

ALTER TABLE embedding_migrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE embedding_migrations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON embedding_migrations;
DROP POLICY IF EXISTS tenant_isolation ON embedding_migrations;
CREATE POLICY tenant_isolation ON embedding_migrations FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON embedding_migrations;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON embedding_migrations
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

COMMENT ON TABLE chunk_embeddings IS
    'D3 vector variants per chunk and model; active space is the knowledge-base binding.';
COMMENT ON TABLE embedding_migrations IS
    'D3 managed embedding migrations: batch progress, cutover and rollback state.';
