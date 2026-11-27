-- Table-row citations have no chunk/version to restore, so drop them before
-- the NOT NULL constraints come back.
DELETE FROM message_citations WHERE chunk_id IS NULL OR document_version_id IS NULL;

DROP INDEX IF EXISTS message_citations_table_asset_idx;

ALTER TABLE message_citations
    DROP COLUMN IF EXISTS table_asset_id,
    DROP COLUMN IF EXISTS sheet,
    DROP COLUMN IF EXISTS row_start,
    DROP COLUMN IF EXISTS row_end;

ALTER TABLE message_citations
    ALTER COLUMN chunk_id SET NOT NULL,
    ALTER COLUMN document_version_id SET NOT NULL;