-- M7 Relation Projection and PostgreSQL Relation Store.
--
-- Relation projection is a separate Pipeline B type over the same immutable
-- EvidencePackage. graph_snapshots is the reviewable, publishable unit: only an
-- ACTIVE snapshot whose ACL version still matches the knowledge base may be
-- queried. Entities and relations never carry content without a source chunk;
-- graph_relation_sources is the traceability record and the authorization join.

-- Allow relation runs to share the one-live-projection constraint.
DROP INDEX IF EXISTS projection_runs_one_active_idx;
CREATE UNIQUE INDEX IF NOT EXISTS projection_runs_one_active_idx
    ON projection_runs(knowledge_base_id, projection_type)
    WHERE status IN ('WIKI_PENDING', 'WIKI_GENERATING', 'RETRY_WIKI',
                     'RELATION_PENDING', 'RELATION_EXTRACTING', 'RETRY_RELATION');

CREATE TABLE IF NOT EXISTS graph_snapshots (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    projection_run_id VARCHAR(64) NOT NULL REFERENCES projection_runs(id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    status VARCHAR(24) NOT NULL,
    source_hash VARCHAR(64) NOT NULL DEFAULT '',
    acl_version BIGINT NOT NULL,
    model VARCHAR(128) NOT NULL DEFAULT '',
    prompt_version VARCHAR(128) NOT NULL DEFAULT '',
    stats JSONB NOT NULL DEFAULT '{}',
    review_note TEXT NOT NULL DEFAULT '',
    reviewed_by VARCHAR(64) NOT NULL DEFAULT '',
    reviewed_at TIMESTAMPTZ,
    activated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (knowledge_base_id, version),
    UNIQUE (projection_run_id)
);

CREATE INDEX IF NOT EXISTS graph_snapshots_scope_idx
    ON graph_snapshots(tenant_id, knowledge_base_id, status, created_at DESC);
-- At most one usable snapshot per knowledge base. Publication supersedes the
-- previous snapshot in the same transaction.
CREATE UNIQUE INDEX IF NOT EXISTS graph_snapshots_one_active_idx
    ON graph_snapshots(knowledge_base_id)
    WHERE status = 'ACTIVE';

CREATE TABLE IF NOT EXISTS graph_entities (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    snapshot_id VARCHAR(64) NOT NULL REFERENCES graph_snapshots(id) ON DELETE CASCADE,
    canonical_key VARCHAR(512) NOT NULL,
    name VARCHAR(512) NOT NULL,
    entity_type VARCHAR(64) NOT NULL DEFAULT '',
    properties JSONB NOT NULL DEFAULT '{}',
    acl_version BIGINT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (snapshot_id, canonical_key)
);

CREATE INDEX IF NOT EXISTS graph_entities_scope_idx
    ON graph_entities(tenant_id, knowledge_base_id, snapshot_id, canonical_key);

-- Alias rows make entity normalization and merging auditable: each row records
-- the rule that produced it and the source key it came from.
CREATE TABLE IF NOT EXISTS graph_entity_aliases (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    entity_id VARCHAR(64) NOT NULL REFERENCES graph_entities(id) ON DELETE CASCADE,
    alias VARCHAR(512) NOT NULL,
    normalized VARCHAR(512) NOT NULL,
    rule VARCHAR(64) NOT NULL,
    merged_key VARCHAR(512) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (entity_id, normalized)
);

CREATE INDEX IF NOT EXISTS graph_entity_aliases_lookup_idx
    ON graph_entity_aliases(tenant_id, knowledge_base_id, normalized);

CREATE TABLE IF NOT EXISTS graph_relations (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    snapshot_id VARCHAR(64) NOT NULL REFERENCES graph_snapshots(id) ON DELETE CASCADE,
    subject_id VARCHAR(64) NOT NULL REFERENCES graph_entities(id) ON DELETE CASCADE,
    predicate VARCHAR(256) NOT NULL,
    object_id VARCHAR(64) NOT NULL REFERENCES graph_entities(id) ON DELETE CASCADE,
    properties JSONB NOT NULL DEFAULT '{}',
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1,
    acl_version BIGINT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (snapshot_id, subject_id, predicate, object_id)
);

CREATE INDEX IF NOT EXISTS graph_relations_scope_idx
    ON graph_relations(tenant_id, knowledge_base_id, snapshot_id, status);
CREATE INDEX IF NOT EXISTS graph_relations_subject_idx
    ON graph_relations(snapshot_id, subject_id);
CREATE INDEX IF NOT EXISTS graph_relations_object_idx
    ON graph_relations(snapshot_id, object_id);

-- Every relation must have at least one source. Finalization rejects a relation
-- with no source row, so an unsourced edge can never reach a usable snapshot.
CREATE TABLE IF NOT EXISTS graph_relation_sources (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    relation_id VARCHAR(64) NOT NULL REFERENCES graph_relations(id) ON DELETE CASCADE,
    document_id VARCHAR(64) NOT NULL,
    document_version_id VARCHAR(64) NOT NULL,
    chunk_id VARCHAR(96) NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    source_path VARCHAR(512) NOT NULL DEFAULT '',
    heading_path TEXT NOT NULL DEFAULT '',
    page_number INTEGER NOT NULL DEFAULT 0,
    UNIQUE (relation_id, chunk_id)
);

CREATE INDEX IF NOT EXISTS graph_relation_sources_relation_idx
    ON graph_relation_sources(relation_id);
CREATE INDEX IF NOT EXISTS graph_relation_sources_chunk_idx
    ON graph_relation_sources(chunk_id);
