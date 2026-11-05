-- Chat and rerank routing are runtime-changeable per knowledge base. An empty
-- model means inherit the deployment default. The version columns are copied
-- from the registry binding so the audit trail shows an in-place provider
-- upgrade as a version change rather than a silent rewrite.
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS chat_model VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS chat_model_version VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS rerank_model VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS rerank_model_version VARCHAR(64) NOT NULL DEFAULT '';
