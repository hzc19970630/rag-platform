-- M6 Wiki projection and Knowledge Index.
--
-- Projection is a separate pipeline over an immutable EvidencePackage. It never
-- writes Pipeline A rows: projection_runs/stage_runs record only projection
-- state, wiki_page_* store reviewed derived content, and knowledge_index_records
-- is the page-level index used by the Knowledge retrieval adapter.

CREATE TABLE IF NOT EXISTS projection_runs (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    evidence_package_id VARCHAR(64) NOT NULL REFERENCES evidence_packages(id) ON DELETE CASCADE,
    projection_type VARCHAR(16) NOT NULL,
    status VARCHAR(32) NOT NULL,
    source_hash VARCHAR(64) NOT NULL DEFAULT '',
    acl_version BIGINT NOT NULL,
    model VARCHAR(128) NOT NULL DEFAULT '',
    prompt_version VARCHAR(128) NOT NULL DEFAULT '',
    retry_of_run_id VARCHAR(64) REFERENCES projection_runs(id) ON DELETE SET NULL,
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS projection_runs_scope_idx ON projection_runs(tenant_id, knowledge_base_id, projection_type, created_at DESC);
CREATE INDEX IF NOT EXISTS projection_runs_package_idx ON projection_runs(evidence_package_id, projection_type);
-- At most one live projection per knowledge base and type. A projection run
-- reads every current EvidencePackage in the knowledge base (a page may span
-- documents), so per-package parallel runs would race on the same pages.
CREATE UNIQUE INDEX IF NOT EXISTS projection_runs_one_active_idx
    ON projection_runs(knowledge_base_id, projection_type)
    WHERE status IN ('WIKI_PENDING', 'WIKI_GENERATING', 'RETRY_WIKI');

CREATE TABLE IF NOT EXISTS projection_stage_runs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    projection_run_id VARCHAR(64) NOT NULL REFERENCES projection_runs(id) ON DELETE CASCADE,
    projection_type VARCHAR(16) NOT NULL,
    stage VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    idempotency_key VARCHAR(255) NOT NULL UNIQUE,
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (projection_run_id, stage)
);

CREATE INDEX IF NOT EXISTS projection_stage_runs_run_idx ON projection_stage_runs(projection_run_id, stage);

-- Logical page. published_version_id is the version readers see; a newer
-- pending version makes the page REVIEW_REQUIRED while the published version
-- stays live and indexed.
CREATE TABLE IF NOT EXISTS wiki_pages (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    document_id VARCHAR(64) REFERENCES documents(id) ON DELETE CASCADE,
    page_type VARCHAR(16) NOT NULL,
    slug VARCHAR(255) NOT NULL,
    title VARCHAR(512) NOT NULL,
    status VARCHAR(32) NOT NULL,
    current_version_id VARCHAR(64),
    published_version_id VARCHAR(64),
    acl_version BIGINT NOT NULL,
    source_hash VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (knowledge_base_id, page_type, slug)
);

CREATE INDEX IF NOT EXISTS wiki_pages_scope_idx ON wiki_pages(tenant_id, knowledge_base_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS wiki_pages_document_idx ON wiki_pages(document_id) WHERE document_id IS NOT NULL;

-- Immutable page version. blocks carries the paragraph-level traceability:
-- every generated block names the EvidencePackage chunks it came from.
CREATE TABLE IF NOT EXISTS wiki_page_versions (
    id VARCHAR(64) PRIMARY KEY,
    wiki_page_id VARCHAR(64) NOT NULL REFERENCES wiki_pages(id) ON DELETE CASCADE,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    blocks JSONB NOT NULL DEFAULT '[]',
    source_hash VARCHAR(64) NOT NULL,
    generation_model VARCHAR(128) NOT NULL DEFAULT '',
    prompt_version VARCHAR(128) NOT NULL DEFAULT '',
    review_status VARCHAR(32) NOT NULL,
    review_note TEXT NOT NULL DEFAULT '',
    reviewed_by VARCHAR(64) NOT NULL DEFAULT '',
    reviewed_at TIMESTAMPTZ,
    acl_version BIGINT NOT NULL,
    created_by VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (wiki_page_id, version)
);

CREATE INDEX IF NOT EXISTS wiki_page_versions_review_idx ON wiki_page_versions(knowledge_base_id, review_status, created_at DESC);

-- Source mapping for one page version. It always names the concrete
-- document_version and chunk, never a raw object key.
CREATE TABLE IF NOT EXISTS wiki_page_sources (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    wiki_page_version_id VARCHAR(64) NOT NULL REFERENCES wiki_page_versions(id) ON DELETE CASCADE,
    document_id VARCHAR(64) NOT NULL,
    document_version_id VARCHAR(64) NOT NULL,
    chunk_id VARCHAR(96) NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    source_path VARCHAR(512) NOT NULL DEFAULT '',
    heading_path TEXT NOT NULL DEFAULT '',
    page_number INTEGER NOT NULL DEFAULT 0,
    UNIQUE (wiki_page_version_id, chunk_id)
);

CREATE INDEX IF NOT EXISTS wiki_page_sources_version_idx ON wiki_page_sources(wiki_page_version_id);
CREATE INDEX IF NOT EXISTS wiki_page_sources_chunk_idx ON wiki_page_sources(chunk_id);

-- Page-level derived index. Only the currently published, ACL-current version
-- of a PUBLISHED page may have an ACTIVE row; invalidation deletes it so the
-- Knowledge adapter stops returning the page before a rebuild runs.
CREATE TABLE IF NOT EXISTS knowledge_index_records (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    wiki_page_id VARCHAR(64) NOT NULL REFERENCES wiki_pages(id) ON DELETE CASCADE,
    wiki_page_version_id VARCHAR(64) NOT NULL REFERENCES wiki_page_versions(id) ON DELETE CASCADE,
    title VARCHAR(512) NOT NULL,
    content TEXT NOT NULL,
    content_hash VARCHAR(64) NOT NULL,
    embedding halfvec,
    embedding_model VARCHAR(128) NOT NULL DEFAULT '',
    embedding_dimension INTEGER NOT NULL DEFAULT 0,
    acl_version BIGINT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE',
    indexed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS knowledge_index_records_active_idx
    ON knowledge_index_records(wiki_page_id) WHERE status = 'ACTIVE';
CREATE INDEX IF NOT EXISTS knowledge_index_records_scope_idx
    ON knowledge_index_records(tenant_id, knowledge_base_id, status, embedding_dimension);
CREATE INDEX IF NOT EXISTS knowledge_index_records_bm25_idx
    ON knowledge_index_records USING bm25 (id, tenant_id, knowledge_base_id, title, content)
    WITH (key_field = 'id');
