ALTER TABLE knowledge_bases
    DROP COLUMN IF EXISTS table_qa_enabled;

ALTER TABLE model_catalog_entries
    DROP COLUMN IF EXISTS supports_tools;