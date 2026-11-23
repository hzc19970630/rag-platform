DROP INDEX IF EXISTS table_assets_materialize_idx;

ALTER TABLE table_assets
    DROP COLUMN IF EXISTS materialize_error,
    DROP COLUMN IF EXISTS materialize_status,
    DROP COLUMN IF EXISTS materialized_at,
    DROP COLUMN IF EXISTS byte_size,
    DROP COLUMN IF EXISTS materialized_rows,
    DROP COLUMN IF EXISTS schema_json,
    DROP COLUMN IF EXISTS storage_format,
    DROP COLUMN IF EXISTS storage_uri;