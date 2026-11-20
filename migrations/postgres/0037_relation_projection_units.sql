-- B29: relation extraction runs as durable work units (evidence package x chunk
-- batch) instead of one whole-knowledge-base task under a single stage timeout.
-- projection_units is a child table scoped through projection_runs, matching the
-- projection_stage_runs layout: no tenant_id, RLS checks the parent row.

CREATE TABLE IF NOT EXISTS projection_units (
    id VARCHAR(255) PRIMARY KEY,
    projection_run_id VARCHAR(64) NOT NULL REFERENCES projection_runs(id) ON DELETE CASCADE,
    package_id VARCHAR(64) NOT NULL,
    batch_index INTEGER NOT NULL,
    -- Ordered child chunk ids owned by this unit; immutable packages make the
    -- stored list the authority even if the run is retried later.
    chunk_ids JSONB NOT NULL DEFAULT '[]',
    status VARCHAR(32) NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    -- Extraction result for this batch. FINALIZE merges payloads without calling
    -- the model, so a run stays replayable after a worker restart.
    payload JSONB,
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (projection_run_id, package_id, batch_index)
);

CREATE INDEX IF NOT EXISTS projection_units_run_status_idx
    ON projection_units(projection_run_id, status);
CREATE INDEX IF NOT EXISTS projection_units_package_idx
    ON projection_units(projection_run_id, package_id, batch_index);

-- Run-level progress counters. The graph page reads the run row only; unit
-- completions update these under a run row lock, so the counters stay exact.
ALTER TABLE projection_runs ADD COLUMN IF NOT EXISTS unit_total INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_runs ADD COLUMN IF NOT EXISTS unit_succeeded INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_runs ADD COLUMN IF NOT EXISTS unit_failed INTEGER NOT NULL DEFAULT 0;

ALTER TABLE projection_units ENABLE ROW LEVEL SECURITY;
ALTER TABLE projection_units FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON projection_units;
DROP POLICY IF EXISTS tenant_isolation ON projection_units;
CREATE POLICY tenant_isolation ON projection_units FOR ALL TO PUBLIC
    USING (EXISTS (
        SELECT 1 FROM projection_runs parent
        WHERE parent.id = projection_units.projection_run_id
          AND parent.tenant_id = NULLIF(current_setting('app.tenant_id', true), '')
    ))
    WITH CHECK (EXISTS (
        SELECT 1 FROM projection_runs parent
        WHERE parent.id = projection_units.projection_run_id
          AND parent.tenant_id = NULLIF(current_setting('app.tenant_id', true), '')
    ));

DROP TRIGGER IF EXISTS rls_shadow_write ON projection_units;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON projection_units
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation_child('projection_runs', 'projection_run_id');

COMMENT ON TABLE projection_units IS
    'B29 relation extraction work units: one evidence package batch per durable task.';