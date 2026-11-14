-- D5 retrieval feedback: explicit answer ratings and implicit citation clicks.
-- Rows are scoped to the tenant and the actor; a rating is one row per message
-- and actor, while a citation click is deduplicated per message and chunk.
CREATE TABLE IF NOT EXISTS retrieval_feedback (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    knowledge_base_id VARCHAR(96) NOT NULL,
    conversation_id VARCHAR(64) NOT NULL,
    message_id VARCHAR(64) NOT NULL,
    citation_id VARCHAR(96) NOT NULL DEFAULT '',
    chunk_id VARCHAR(96) NOT NULL DEFAULT '',
    kind VARCHAR(32) NOT NULL,
    actor_id VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT retrieval_feedback_kind_check CHECK (kind IN ('helpful', 'unhelpful', 'citation_click')),
    CONSTRAINT retrieval_feedback_message_fk FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS retrieval_feedback_rating_idx
    ON retrieval_feedback (tenant_id, actor_id, message_id)
    WHERE kind IN ('helpful', 'unhelpful');
CREATE UNIQUE INDEX IF NOT EXISTS retrieval_feedback_click_idx
    ON retrieval_feedback (tenant_id, actor_id, message_id, chunk_id)
    WHERE kind = 'citation_click';
CREATE INDEX IF NOT EXISTS retrieval_feedback_scope_idx
    ON retrieval_feedback (tenant_id, knowledge_base_id, created_at DESC);

ALTER TABLE retrieval_feedback ENABLE ROW LEVEL SECURITY;
ALTER TABLE retrieval_feedback FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON retrieval_feedback;
DROP POLICY IF EXISTS tenant_isolation ON retrieval_feedback;
CREATE POLICY tenant_isolation ON retrieval_feedback FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON retrieval_feedback;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON retrieval_feedback
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

COMMENT ON TABLE retrieval_feedback IS
    'D5 answer ratings and citation clicks used by the retrieval-quality loop.';
