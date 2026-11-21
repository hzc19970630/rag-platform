-- B35: projection change log, epochs and atomic wiki generations.
--
-- Epochs give the projection pipeline a single monotonic order per knowledge
-- base: every evidence add/update/delete/ACL change appends one row and bumps
-- target_epoch. A run records the epoch it was built for; publishing is only
-- allowed while its target is still current. Wiki generations turn the
-- per-page publish path into one atomic swap, so readers see either the old
-- complete knowledge base or the new complete one.

CREATE TABLE IF NOT EXISTS projection_changes (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    kind VARCHAR(16) NOT NULL CHECK (kind IN ('add', 'update', 'delete', 'acl')),
    document_id VARCHAR(64) NOT NULL DEFAULT '',
    document_version_id VARCHAR(64) NOT NULL DEFAULT '',
    evidence_package_id VARCHAR(64) NOT NULL DEFAULT '',
    content_hash VARCHAR(64) NOT NULL DEFAULT '',
    -- Monotonic per knowledge base. Changes are applied in epoch order.
    epoch BIGINT NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    applied_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, knowledge_base_id, idempotency_key),
    UNIQUE (tenant_id, knowledge_base_id, epoch)
);

CREATE INDEX IF NOT EXISTS projection_changes_pending_idx
    ON projection_changes(tenant_id, knowledge_base_id, applied_at, epoch);
CREATE INDEX IF NOT EXISTS projection_changes_epoch_idx
    ON projection_changes(tenant_id, knowledge_base_id, epoch);

ALTER TABLE knowledge_projection_states
    ADD COLUMN IF NOT EXISTS target_epoch BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS wiki_applied_epoch BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS relation_applied_epoch BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS active_wiki_generation_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS last_change_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS invalidated_at TIMESTAMPTZ;

ALTER TABLE projection_runs
    ADD COLUMN IF NOT EXISTS base_epoch BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS target_epoch BIGINT NOT NULL DEFAULT 0;

ALTER TABLE graph_snapshots
    ADD COLUMN IF NOT EXISTS target_epoch BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS wiki_generations (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    generation_no INTEGER NOT NULL,
    -- BUILDING -> REVIEW -> READY -> ACTIVE; stale targets end SUPERSEDED.
    status VARCHAR(16) NOT NULL CHECK (status IN ('BUILDING', 'REVIEW', 'READY', 'ACTIVE', 'SUPERSEDED', 'FAILED')),
    base_generation_id VARCHAR(64),
    projection_run_id VARCHAR(64) NOT NULL DEFAULT '',
    target_epoch BIGINT NOT NULL DEFAULT 0,
    source_hash VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, knowledge_base_id, generation_no)
);

-- At most one generation is visible per knowledge base.
CREATE UNIQUE INDEX IF NOT EXISTS wiki_generations_active_idx
    ON wiki_generations(tenant_id, knowledge_base_id)
    WHERE status = 'ACTIVE';
CREATE INDEX IF NOT EXISTS wiki_generations_epoch_idx
    ON wiki_generations(tenant_id, knowledge_base_id, target_epoch);

CREATE TABLE IF NOT EXISTS wiki_generation_pages (
    tenant_id VARCHAR(64) NOT NULL,
    knowledge_base_id VARCHAR(64) NOT NULL,
    generation_id VARCHAR(64) NOT NULL REFERENCES wiki_generations(id) ON DELETE CASCADE,
    wiki_page_id VARCHAR(64) NOT NULL REFERENCES wiki_pages(id) ON DELETE CASCADE,
    wiki_page_version_id VARCHAR(64),
    -- GENERATED = fresh version in this generation, CARRIED = reused from the
    -- previous ACTIVE generation, EXCEPTION = reviewer rejected the new version
    -- so the old one is carried forward explicitly.
    status VARCHAR(16) NOT NULL CHECK (status IN ('GENERATED', 'CARRIED', 'EXCEPTION')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (generation_id, wiki_page_id)
);

CREATE INDEX IF NOT EXISTS wiki_generation_pages_lookup_idx
    ON wiki_generation_pages(tenant_id, knowledge_base_id, wiki_page_id);

-- Seed generation 0 from the currently published pages. This preserves exactly
-- what readers see today; no page or knowledge-index row is rewritten.
INSERT INTO wiki_generations (id, tenant_id, knowledge_base_id, generation_no, status, target_epoch, source_hash, created_at, updated_at)
SELECT 'gen-' || substr(md5(kb.tenant_id || ':' || kb.id), 1, 40), kb.tenant_id, kb.id, 0, 'ACTIVE',
       COALESCE(s.target_epoch, 0), '', NOW(), NOW()
FROM knowledge_bases kb
LEFT JOIN knowledge_projection_states s
       ON s.tenant_id = kb.tenant_id AND s.knowledge_base_id = kb.id
WHERE EXISTS (
    SELECT 1 FROM wiki_pages p
    WHERE p.tenant_id = kb.tenant_id AND p.knowledge_base_id = kb.id AND p.published_version_id IS NOT NULL
)
ON CONFLICT DO NOTHING;

INSERT INTO wiki_generation_pages (tenant_id, knowledge_base_id, generation_id, wiki_page_id, wiki_page_version_id, status, created_at, updated_at)
SELECT g.tenant_id, g.knowledge_base_id, g.id, p.id, p.published_version_id, 'CARRIED', NOW(), NOW()
FROM wiki_generations g
JOIN wiki_pages p
  ON p.tenant_id = g.tenant_id AND p.knowledge_base_id = g.knowledge_base_id
WHERE g.generation_no = 0 AND p.published_version_id IS NOT NULL
ON CONFLICT DO NOTHING;

INSERT INTO knowledge_projection_states (tenant_id, knowledge_base_id, active_wiki_generation_id, updated_at)
SELECT g.tenant_id, g.knowledge_base_id, g.id, NOW()
FROM wiki_generations g
WHERE g.generation_no = 0
ON CONFLICT (tenant_id, knowledge_base_id) DO UPDATE
SET active_wiki_generation_id = COALESCE(knowledge_projection_states.active_wiki_generation_id, EXCLUDED.active_wiki_generation_id);

ALTER TABLE projection_changes ENABLE ROW LEVEL SECURITY;
ALTER TABLE projection_changes FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON projection_changes;
DROP POLICY IF EXISTS tenant_isolation ON projection_changes;
CREATE POLICY tenant_isolation ON projection_changes FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON projection_changes;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON projection_changes
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

ALTER TABLE wiki_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE wiki_generations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON wiki_generations;
DROP POLICY IF EXISTS tenant_isolation ON wiki_generations;
CREATE POLICY tenant_isolation ON wiki_generations FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON wiki_generations;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON wiki_generations
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

ALTER TABLE wiki_generation_pages ENABLE ROW LEVEL SECURITY;
ALTER TABLE wiki_generation_pages FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON wiki_generation_pages;
DROP POLICY IF EXISTS tenant_isolation ON wiki_generation_pages;
CREATE POLICY tenant_isolation ON wiki_generation_pages FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON wiki_generation_pages;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON wiki_generation_pages
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

COMMENT ON TABLE projection_changes IS
    'B35 ordered projection intent per knowledge base; tombstones stay until applied_epoch covers them.';
COMMENT ON TABLE wiki_generations IS
    'B35 atomic knowledge-base generation; exactly one ACTIVE row is visible to readers.';
COMMENT ON TABLE wiki_generation_pages IS
    'B35 generation to page version mapping, including carried-forward and rejected exceptions.';
