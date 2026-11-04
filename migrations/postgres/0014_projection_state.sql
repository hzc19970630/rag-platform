-- ProjectionJoin previously loaded every EvidencePackage and recomputed both
-- source hashes on each gated query. Persist the expected hashes and reuse them
-- until evidence, ACL, prompt or model changes. A missing or mismatched row
-- falls back to a one-time recompute, so correctness does not depend on every
-- write path updating this table.
CREATE TABLE IF NOT EXISTS knowledge_projection_states (
    tenant_id VARCHAR(64) NOT NULL,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    acl_version BIGINT NOT NULL DEFAULT 0,
    wiki_expected_hash VARCHAR(64) NOT NULL DEFAULT '',
    wiki_prompt_version VARCHAR(128) NOT NULL DEFAULT '',
    wiki_model VARCHAR(128) NOT NULL DEFAULT '',
    relation_expected_hash VARCHAR(64) NOT NULL DEFAULT '',
    relation_prompt_version VARCHAR(128) NOT NULL DEFAULT '',
    relation_model VARCHAR(128) NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, knowledge_base_id)
);