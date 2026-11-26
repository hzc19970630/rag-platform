-- B40: declare whether a chat model can accept function schemas, and whether a
-- knowledge base routes table questions through the built-in tool layer.

ALTER TABLE model_catalog_entries
    ADD COLUMN IF NOT EXISTS supports_tools BOOLEAN NOT NULL DEFAULT true;

ALTER TABLE knowledge_bases
    ADD COLUMN IF NOT EXISTS table_qa_enabled VARCHAR(16) NOT NULL DEFAULT 'auto';

COMMENT ON COLUMN model_catalog_entries.supports_tools IS
    'Chat models only: true when the provider accepts tool/function schemas.';
COMMENT ON COLUMN knowledge_bases.table_qa_enabled IS
    'auto enables the tool layer when the KB has table assets; on/off override.';