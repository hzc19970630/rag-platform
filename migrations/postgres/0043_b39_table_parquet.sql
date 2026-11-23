-- B39: table rows move to a Parquet columnar copy in object storage. Postgres
-- keeps only the asset catalog plus materialization status; table_rows is left
-- in place for rollback but is no longer written.

ALTER TABLE table_assets
    ADD COLUMN IF NOT EXISTS storage_uri TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS storage_format VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_json JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS materialized_rows BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS byte_size BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS materialized_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS materialize_status VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS materialize_error TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS table_assets_materialize_idx
    ON table_assets(document_version_id, materialize_status);

COMMENT ON COLUMN table_assets.storage_uri IS
    'Object-storage prefix of the Parquet copy, e.g. tablehouse/tenant=.../sheet=...';
COMMENT ON COLUMN table_assets.storage_format IS
    'Columnar format of the copy; currently parquet.';
COMMENT ON COLUMN table_assets.schema_json IS
    'Column types and materialization metadata mirrored from the sheet manifest.';
COMMENT ON COLUMN table_assets.materialized_rows IS
    'Rows written to the Parquet copy; may exceed parsed_rows for keyword blocks.';
COMMENT ON COLUMN table_assets.materialize_status IS
    'ready, partial, failed, or empty; legacy assets keep an empty value.';