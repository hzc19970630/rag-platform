-- M5 external data sources and incremental sync.
--
-- A connector is bound to exactly one knowledge base and stores only an opaque
-- credential envelope. Sync runs are durable before they reach Redis, and the
-- partial unique index guarantees one active run per connector so a manual
-- trigger and a scheduled trigger cannot ingest the same source concurrently.

CREATE TABLE IF NOT EXISTS connectors (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    type VARCHAR(64) NOT NULL,
    config JSONB NOT NULL DEFAULT '{}',
    -- AES-GCM envelope. Named credential_ref to match the architecture model;
    -- the value is ciphertext, never a plaintext secret or a storage URL.
    credential_ref TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sync_interval_seconds INTEGER NOT NULL DEFAULT 0,
    next_sync_at TIMESTAMPTZ,
    last_sync_at TIMESTAMPTZ,
    created_by VARCHAR(64) NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS connectors_scope_idx ON connectors(tenant_id, knowledge_base_id, created_at DESC);
CREATE INDEX IF NOT EXISTS connectors_due_idx ON connectors(enabled, next_sync_at) WHERE enabled AND next_sync_at IS NOT NULL;

-- One row per remote item. document_id is a deterministic function of
-- connector_id + external_id, so a retried run reuses the same document instead
-- of creating a duplicate. TOMBSTONED rows keep the mapping after the source
-- item disappears; a later reappearance restores the same document.
CREATE TABLE IF NOT EXISTS connector_items (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    connector_id VARCHAR(64) NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    external_id VARCHAR(512) NOT NULL,
    remote_path VARCHAR(1024) NOT NULL DEFAULT '',
    remote_version VARCHAR(255) NOT NULL DEFAULT '',
    content_hash VARCHAR(64) NOT NULL DEFAULT '',
    document_id VARCHAR(64) REFERENCES documents(id) ON DELETE SET NULL,
    document_version_id VARCHAR(64) REFERENCES document_versions(id) ON DELETE SET NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'ACTIVE',
    last_synced_at TIMESTAMPTZ,
    tombstoned_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (connector_id, external_id)
);

CREATE INDEX IF NOT EXISTS connector_items_status_idx ON connector_items(connector_id, status);

CREATE TABLE IF NOT EXISTS sync_runs (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    connector_id VARCHAR(64) NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    mode VARCHAR(16) NOT NULL,
    status VARCHAR(32) NOT NULL,
    cursor JSONB NOT NULL DEFAULT '{}',
    resume_of_run_id VARCHAR(64) REFERENCES sync_runs(id) ON DELETE SET NULL,
    triggered_by VARCHAR(64) NOT NULL DEFAULT '',
    items_total INTEGER NOT NULL DEFAULT 0,
    items_created INTEGER NOT NULL DEFAULT 0,
    items_updated INTEGER NOT NULL DEFAULT 0,
    items_deleted INTEGER NOT NULL DEFAULT 0,
    items_skipped INTEGER NOT NULL DEFAULT 0,
    items_failed INTEGER NOT NULL DEFAULT 0,
    error_summary TEXT NOT NULL DEFAULT '',
    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_until TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sync_runs_connector_idx ON sync_runs(connector_id, created_at DESC);
CREATE INDEX IF NOT EXISTS sync_runs_scope_idx ON sync_runs(tenant_id, knowledge_base_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS sync_runs_one_active_idx ON sync_runs(connector_id) WHERE status IN ('PENDING', 'RUNNING');
