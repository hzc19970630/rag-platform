ALTER TABLE document_versions
    DROP COLUMN IF EXISTS estimated_tokens,
    DROP COLUMN IF EXISTS vector_tokens,
    DROP COLUMN IF EXISTS total_sheets,
    DROP COLUMN IF EXISTS vector_sheets,
    DROP COLUMN IF EXISTS total_rows,
    DROP COLUMN IF EXISTS vector_rows,
    DROP COLUMN IF EXISTS index_mode;

ALTER TABLE documents DROP COLUMN IF EXISTS index_mode;
ALTER TABLE chunks DROP COLUMN IF EXISTS table_asset_id, DROP COLUMN IF EXISTS index_role;
DROP TABLE IF EXISTS table_assets;
