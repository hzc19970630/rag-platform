-- Per-package projection generation cache. Wiki pages and relation extraction
-- are deterministic functions of one EvidencePackage plus the prompt/model/ACL
-- configuration; caching that result lets a run regenerate only packages whose
-- inputs changed. The table is derived data: clearing it only costs another
-- model call, never correctness.
CREATE TABLE IF NOT EXISTS projection_generation_cache (
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    projection_type VARCHAR(16) NOT NULL,
    artifact VARCHAR(32) NOT NULL,
    package_id VARCHAR(64) NOT NULL,
    input_hash VARCHAR(64) NOT NULL,
    payload JSONB NOT NULL,
    prompt_version VARCHAR(128) NOT NULL DEFAULT '',
    model VARCHAR(128) NOT NULL DEFAULT '',
    acl_version BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, knowledge_base_id, projection_type, artifact, package_id)
);

CREATE INDEX IF NOT EXISTS projection_generation_cache_scope_idx
    ON projection_generation_cache(tenant_id, knowledge_base_id, projection_type);

CREATE INDEX IF NOT EXISTS projection_generation_cache_package_idx
    ON projection_generation_cache(package_id);