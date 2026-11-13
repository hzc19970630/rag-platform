ALTER TABLE tenants
    DROP COLUMN IF EXISTS chunking_config,
    DROP COLUMN IF EXISTS retrieval_config;
