-- B23 preset packs are data: a knowledge base records the pack and version it
-- was created from, and keeps its own retrieval/chunking defaults so later pack
-- upgrades cannot silently change an existing knowledge base.
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS preset_name VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS preset_version VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS chunking_config JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN knowledge_bases.preset_name IS 'B23 preset pack this knowledge base was created from.';
COMMENT ON COLUMN knowledge_bases.chunking_config IS 'B23 knowledge-base chunking defaults (preset or explicit).';
