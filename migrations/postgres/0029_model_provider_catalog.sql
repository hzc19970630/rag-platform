-- Tenant-managed model provider catalog. Until now the deployment catalog was
-- built from environment variables, so adding a provider required a redeploy.
-- These two tables make the catalog data: a provider is an endpoint and its
-- encrypted credential, and one row per routable model records the capability,
-- version and (for embeddings) the vector dimension a knowledge base pins.
--
-- API keys are stored encrypted with the same keyring the connector
-- credentials use. The column never leaves the service layer in plaintext.
CREATE TABLE IF NOT EXISTS model_providers (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    name VARCHAR(128) NOT NULL,
    kind VARCHAR(32) NOT NULL,
    base_url VARCHAR(512) NOT NULL,
    api_key_cipher TEXT NOT NULL DEFAULT '',
    health_url VARCHAR(512) NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT model_providers_name_unique UNIQUE (tenant_id, name),
    CONSTRAINT model_providers_kind_check CHECK (kind IN ('openai_compatible', 'ollama'))
);

-- A model name is unique per tenant across capabilities, matching the registry
-- lookup: a binding stores only the name, so two rows with the same name would
-- make resolution ambiguous.
CREATE TABLE IF NOT EXISTS model_catalog_entries (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    provider_id VARCHAR(64) NOT NULL,
    model VARCHAR(128) NOT NULL,
    display_name VARCHAR(128) NOT NULL DEFAULT '',
    capability VARCHAR(16) NOT NULL,
    version VARCHAR(64) NOT NULL DEFAULT 'deployment',
    dimension INTEGER NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT model_catalog_entries_model_unique UNIQUE (tenant_id, model),
    CONSTRAINT model_catalog_entries_capability_check CHECK (capability IN ('chat', 'embedding', 'rerank')),
    CONSTRAINT model_catalog_entries_dimension_check CHECK (
        (capability = 'embedding' AND dimension > 0) OR (capability <> 'embedding' AND dimension = 0)
    ),
    CONSTRAINT model_catalog_entries_provider_fk FOREIGN KEY (provider_id) REFERENCES model_providers(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS model_catalog_entries_provider_idx ON model_catalog_entries (tenant_id, provider_id);

-- Tenant isolation matches every other tenant-owned table (0023/0024/0026).
ALTER TABLE model_providers ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_providers FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON model_providers;
DROP POLICY IF EXISTS tenant_isolation ON model_providers;
CREATE POLICY tenant_isolation ON model_providers FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON model_providers;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON model_providers
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

ALTER TABLE model_catalog_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_catalog_entries FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON model_catalog_entries;
DROP POLICY IF EXISTS tenant_isolation ON model_catalog_entries;
CREATE POLICY tenant_isolation ON model_catalog_entries FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON model_catalog_entries;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON model_catalog_entries
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

COMMENT ON TABLE model_providers IS
    'B13 tenant-managed model providers: endpoint, kind and encrypted API key.';
COMMENT ON TABLE model_catalog_entries IS
    'B13 routable models per provider; chat/rerank entries route live, embedding entries define the D3 catalog.';