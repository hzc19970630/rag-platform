DROP INDEX IF EXISTS table_assets_sheet_idx;

ALTER TABLE table_assets
    DROP COLUMN IF EXISTS header_row,
    DROP COLUMN IF EXISTS parsed_rows,
    DROP COLUMN IF EXISTS total_rows,
    DROP COLUMN IF EXISTS truncated,
    DROP COLUMN IF EXISTS parse_status,
    DROP COLUMN IF EXISTS storage_mode;

ALTER TABLE document_versions
    DROP COLUMN IF EXISTS coverage_known,
    DROP COLUMN IF EXISTS keyword_rows,
    DROP COLUMN IF EXISTS parsed_rows,
    DROP COLUMN IF EXISTS parsed_sheets,
    DROP COLUMN IF EXISTS parse_truncated,
    DROP COLUMN IF EXISTS parse_status,
    DROP COLUMN IF EXISTS storage_mode;
