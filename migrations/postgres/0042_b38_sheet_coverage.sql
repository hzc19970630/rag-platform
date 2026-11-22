-- B38: per-sheet parse coverage and storage tiers. A document can now keep a
-- sheet catalog even when some sheets are only sampled or metadata-only.

ALTER TABLE document_versions
    ADD COLUMN IF NOT EXISTS storage_mode VARCHAR(16) NOT NULL DEFAULT 'full',
    ADD COLUMN IF NOT EXISTS parse_status VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS parse_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS parsed_sheets INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS parsed_rows BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS keyword_rows BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS coverage_known BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE table_assets
    ADD COLUMN IF NOT EXISTS storage_mode VARCHAR(16) NOT NULL DEFAULT 'full',
    ADD COLUMN IF NOT EXISTS parse_status VARCHAR(16) NOT NULL DEFAULT 'parsed',
    ADD COLUMN IF NOT EXISTS truncated BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS total_rows BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS parsed_rows BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS header_row INTEGER NOT NULL DEFAULT 0;

UPDATE table_assets SET total_rows = row_count WHERE total_rows = 0;
UPDATE table_assets SET parsed_rows = row_count WHERE parsed_rows = 0;
UPDATE document_versions SET coverage_known = FALSE WHERE parse_status = '';

CREATE INDEX IF NOT EXISTS table_assets_sheet_idx
    ON table_assets(document_version_id, sheet);

COMMENT ON COLUMN document_versions.storage_mode IS
    'B38 aggregate storage tier: full, sampled, or metadata.';
COMMENT ON COLUMN document_versions.parsed_sheets IS
    'Sheets whose catalog/header/sample was processed; not the same as fully indexed.';
COMMENT ON COLUMN document_versions.parsed_rows IS
    'Rows actually represented in the index; sampled/metadata rows are not included.';
COMMENT ON COLUMN document_versions.keyword_rows IS
    'Rows available to keyword or structured retrieval.';
COMMENT ON COLUMN table_assets.storage_mode IS
    'Per-sheet storage tier: full, sampled, or metadata.';
COMMENT ON COLUMN table_assets.parse_status IS
    'parsed, truncated, sampled, metadata, skipped, or failed.';
