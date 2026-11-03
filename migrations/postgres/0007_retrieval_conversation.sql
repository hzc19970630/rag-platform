-- M3: authorized RAG retrieval and citation delivery.
--
-- Two groups of state are added here:
--   1. Retrieval configuration that can change at runtime. The knowledge-base
--      level override lives in the database because it must be auditable and
--      tenant-scoped, unlike the YAML defaults shipped with the image.
--   2. Conversation, message, citation and stream-event tables. Online answers
--      run inside the API request lifecycle, so their state is stored here
--      rather than in Redis/Asynq.

ALTER TABLE knowledge_bases
    ADD COLUMN IF NOT EXISTS retrieval_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS indexing_strategy JSONB NOT NULL DEFAULT '{"vector_enabled": true, "keyword_enabled": true, "wiki_enabled": false, "graph_enabled": false, "required_capabilities": ["evidence"]}'::jsonb;

CREATE TABLE IF NOT EXISTS conversations (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id VARCHAR(64) NOT NULL REFERENCES users(id),
    title VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS conversations_scope_idx ON conversations(tenant_id, user_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS messages (
    id VARCHAR(64) PRIMARY KEY,
    conversation_id VARCHAR(64) NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id VARCHAR(64) NOT NULL,
    request_id VARCHAR(128) NOT NULL,
    role VARCHAR(16) NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL,
    -- Answer provenance: the plan that ran, the authorized evidence set, the
    -- citation-validation outcome and the model that produced the text. Kept
    -- together so a question can be reproduced and regressed later.
    query_plan JSONB,
    evidence_set JSONB,
    citation_validation JSONB,
    authorization_scope_version VARCHAR(128) NOT NULL DEFAULT '',
    model VARCHAR(128) NOT NULL DEFAULT '',
    token_usage JSONB,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    error_code VARCHAR(128),
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (request_id, role)
);

CREATE INDEX IF NOT EXISTS messages_conversation_idx ON messages(conversation_id, created_at);

CREATE TABLE IF NOT EXISTS message_citations (
    id VARCHAR(96) PRIMARY KEY,
    message_id VARCHAR(64) NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    document_id VARCHAR(64) NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    document_version_id VARCHAR(64) NOT NULL REFERENCES document_versions(id) ON DELETE CASCADE,
    chunk_id VARCHAR(96) NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    citation_index INTEGER NOT NULL,
    rank INTEGER NOT NULL,
    score DOUBLE PRECISION NOT NULL DEFAULT 0,
    quote TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (message_id, citation_index)
);

CREATE INDEX IF NOT EXISTS message_citations_chunk_idx ON message_citations(chunk_id);

-- Stream events are persisted so a client that drops the connection can resume
-- by replaying from the last sequence it saw (Last-Event-ID).
CREATE TABLE IF NOT EXISTS stream_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id VARCHAR(128) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (request_id, sequence)
);

CREATE INDEX IF NOT EXISTS stream_events_request_idx ON stream_events(request_id, sequence);