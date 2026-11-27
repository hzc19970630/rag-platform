-- B40: table-row citations are produced by the read-only tools (no chunk row
-- and no single document version), so the chunk/version columns must accept
-- NULL and the row locator lives on the citation itself.
ALTER TABLE message_citations
    ALTER COLUMN chunk_id DROP NOT NULL,
    ALTER COLUMN document_version_id DROP NOT NULL;

ALTER TABLE message_citations
    ADD COLUMN IF NOT EXISTS table_asset_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS sheet VARCHAR(255),
    ADD COLUMN IF NOT EXISTS row_start INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS row_end INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS message_citations_table_asset_idx ON message_citations(table_asset_id);