ALTER TABLE document_versions
    DROP COLUMN IF EXISTS skipped_notes,
    DROP COLUMN IF EXISTS skipped_charts,
    DROP COLUMN IF EXISTS skipped_images;