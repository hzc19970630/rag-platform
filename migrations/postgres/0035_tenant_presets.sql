-- B23 tenant custom presets. Built-in packs stay immutable; a tenant can clone
-- one or import a manifest, then edit its own copy. Knowledge bases record the
-- name/version they were created from.
CREATE TABLE IF NOT EXISTS tenant_presets (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    name VARCHAR(64) NOT NULL,
    version VARCHAR(32) NOT NULL,
    manifest JSONB NOT NULL,
    created_by VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT tenant_presets_unique UNIQUE (tenant_id, name, version)
);

CREATE INDEX IF NOT EXISTS tenant_presets_scope_idx ON tenant_presets (tenant_id, name, version);

ALTER TABLE tenant_presets ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_presets FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON tenant_presets;
DROP POLICY IF EXISTS tenant_isolation ON tenant_presets;
CREATE POLICY tenant_isolation ON tenant_presets FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON tenant_presets;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON tenant_presets
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

COMMENT ON TABLE tenant_presets IS 'B23 tenant custom preset packs; built-in packs stay in code.';
