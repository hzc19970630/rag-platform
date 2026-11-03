CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_search;

-- This table is an explicit M0 compatibility contract for the selected
-- PostgreSQL 17 + pgvector + ParadeDB BM25 stack. M2 will extend the storage
-- model around the same index boundaries.
CREATE TABLE IF NOT EXISTS platform_index_probe (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    knowledge_base_id VARCHAR(64) NOT NULL,
    chunk_id VARCHAR(64) NOT NULL,
    content TEXT NOT NULL,
    embedding halfvec,
    embedding_dimension INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS platform_index_probe_bm25_idx
    ON platform_index_probe
    USING bm25 (id, tenant_id, knowledge_base_id, chunk_id, content)
    WITH (key_field = 'id');

CREATE INDEX IF NOT EXISTS platform_index_probe_vector_idx
    ON platform_index_probe
    USING hnsw ((embedding::halfvec(3)) halfvec_cosine_ops)
    WITH (m = 16, ef_construction = 64)
    WHERE embedding_dimension = 3;

CREATE TABLE IF NOT EXISTS platform_settings (
    key VARCHAR(128) PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS seed_runs (
    version VARCHAR(64) PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
