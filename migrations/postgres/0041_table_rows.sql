-- B36 P3: raw table rows for deterministic filtering, TopN and aggregation.
-- Rows are a separate store so vector indexing never has to embed them.
CREATE TABLE IF NOT EXISTS table_rows (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    document_id VARCHAR(64) NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    document_version_id VARCHAR(64) NOT NULL REFERENCES document_versions(id) ON DELETE CASCADE,
    table_asset_id VARCHAR(64) NOT NULL REFERENCES table_assets(id) ON DELETE CASCADE,
    row_number INTEGER NOT NULL,
    cells JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS table_rows_asset_idx ON table_rows(table_asset_id, row_number);
CREATE INDEX IF NOT EXISTS table_rows_version_idx ON table_rows(document_version_id);

ALTER TABLE table_rows ENABLE ROW LEVEL SECURITY;
ALTER TABLE table_rows FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON table_rows;
DROP POLICY IF EXISTS tenant_isolation ON table_rows;
CREATE POLICY tenant_isolation ON table_rows FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON table_rows;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON table_rows
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

COMMENT ON TABLE table_rows IS
    'B36 raw table cells for deterministic filter/TopN queries; vector retrieval uses summaries/keyword chunks.';
