-- B15-a: tenant-level business configuration. Deployment connection settings
-- (database, object storage, provider endpoints) stay in env/YAML; retrieval and
-- chunking policy move into the database so an operator can configure a
-- knowledge base without redeploying.
ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS retrieval_config JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS chunking_config JSONB NOT NULL DEFAULT '{}';

COMMENT ON COLUMN tenants.retrieval_config IS 'Tenant tier of the retrieval parameter override chain (schema defaults -> deployment -> tenant -> knowledge base -> session).';
COMMENT ON COLUMN tenants.chunking_config IS 'Tenant tier of the chunking policy; applies to documents ingested after the change.';
