-- B36: table assets and selective indexing. Tables are no longer assumed to be
-- fully embedded: every table block records its sheet/header/row range, and
-- chunks carry an index role so raw rows can serve BM25/structured queries
-- without spending embedding tokens.

CREATE TABLE IF NOT EXISTS table_assets (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    document_id VARCHAR(64) NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    document_version_id VARCHAR(64) NOT NULL REFERENCES document_versions(id) ON DELETE CASCADE,
    sheet VARCHAR(255) NOT NULL DEFAULT '',
    source_path VARCHAR(512) NOT NULL DEFAULT '',
    header JSONB NOT NULL DEFAULT '[]',
    columns JSONB NOT NULL DEFAULT '[]',
    row_start INTEGER NOT NULL DEFAULT 0,
    row_end INTEGER NOT NULL DEFAULT 0,
    row_count INTEGER NOT NULL DEFAULT 0,
    column_count INTEGER NOT NULL DEFAULT 0,
    sample_rows JSONB NOT NULL DEFAULT '[]',
    stats JSONB NOT NULL DEFAULT '{}',
    index_mode VARCHAR(16) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS table_assets_document_idx
    ON table_assets(tenant_id, knowledge_base_id, document_id);
CREATE INDEX IF NOT EXISTS table_assets_version_idx
    ON table_assets(document_version_id);

ALTER TABLE chunks
    ADD COLUMN IF NOT EXISTS index_role VARCHAR(16) NOT NULL DEFAULT 'both',
    ADD COLUMN IF NOT EXISTS table_asset_id VARCHAR(64);

ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS index_mode VARCHAR(16) NOT NULL DEFAULT '';

ALTER TABLE document_versions
    ADD COLUMN IF NOT EXISTS index_mode VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS vector_rows BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS total_rows BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS vector_sheets INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS total_sheets INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS vector_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS estimated_tokens BIGINT NOT NULL DEFAULT 0;

ALTER TABLE table_assets ENABLE ROW LEVEL SECURITY;
ALTER TABLE table_assets FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON table_assets;
DROP POLICY IF EXISTS tenant_isolation ON table_assets;
CREATE POLICY tenant_isolation ON table_assets FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON table_assets;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON table_assets
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

COMMENT ON TABLE table_assets IS
    'B36 table schema/coverage per parsed table block; raw rows stay in chunks for BM25/structured retrieval.';
COMMENT ON COLUMN chunks.index_role IS
    'both = vector+keyword, keyword = BM25/structured only, vector = vector only.';
