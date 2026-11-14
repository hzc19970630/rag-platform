-- B14 retrieval-quality loop: persist evaluation runs so a retrieval change can
-- be compared with the last known baseline and shown next to online feedback.
CREATE TABLE IF NOT EXISTS retrieval_eval_runs (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    knowledge_base_id VARCHAR(96) NOT NULL DEFAULT '',
    kind VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL,
    config_hash VARCHAR(128) NOT NULL DEFAULT '',
    metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
    report JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT retrieval_eval_runs_kind_check CHECK (kind IN ('m3', 'rerank_compare', 'manual')),
    CONSTRAINT retrieval_eval_runs_status_check CHECK (status IN ('passed', 'failed', 'regressed'))
);

CREATE INDEX IF NOT EXISTS retrieval_eval_runs_scope_idx
    ON retrieval_eval_runs (tenant_id, knowledge_base_id, kind, created_at DESC);

ALTER TABLE retrieval_eval_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE retrieval_eval_runs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON retrieval_eval_runs;
DROP POLICY IF EXISTS tenant_isolation ON retrieval_eval_runs;
CREATE POLICY tenant_isolation ON retrieval_eval_runs FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON retrieval_eval_runs;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON retrieval_eval_runs
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

COMMENT ON TABLE retrieval_eval_runs IS
    'B14 evaluation runs and regression status for the retrieval-quality loop.';
