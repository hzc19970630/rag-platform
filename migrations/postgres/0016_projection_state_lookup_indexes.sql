-- The projection state freshness guard reads the newest EvidencePackage for a
-- knowledge base. Without a composite index that lookup degrades to a table
-- scan and cancels the benefit of materializing the expected hashes.
CREATE INDEX IF NOT EXISTS evidence_packages_scope_updated_idx
    ON evidence_packages(tenant_id, knowledge_base_id, updated_at DESC);