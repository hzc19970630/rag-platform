-- B40: surface media the parser could not turn into searchable evidence.
-- docx/pptx images, charts and notes are counted instead of being dropped
-- silently; PDF media handling remains B7a.

ALTER TABLE document_versions
    ADD COLUMN IF NOT EXISTS skipped_images INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS skipped_charts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS skipped_notes INTEGER NOT NULL DEFAULT 0;

COMMENT ON COLUMN document_versions.skipped_images IS
    'Images skipped by the parser (docx drawings/pictures, pptx pictures).';
COMMENT ON COLUMN document_versions.skipped_charts IS
    'Charts skipped by the parser; their data is not searchable yet.';
COMMENT ON COLUMN document_versions.skipped_notes IS
    'Speaker notes or footnotes/endnotes skipped by the parser.';