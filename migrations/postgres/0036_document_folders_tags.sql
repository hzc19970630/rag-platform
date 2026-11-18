-- B6-a knowledge space structure: folder tree, per-knowledge-base tags, and
-- document tag assignments. Permission stays at the knowledge-base role;
-- folders and tags only organize and filter documents.

CREATE TABLE IF NOT EXISTS folders (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    parent_id VARCHAR(64) REFERENCES folders(id) ON DELETE RESTRICT,
    name VARCHAR(255) NOT NULL,
    -- Materialized path of folder ids: /<root-id>/<child-id>/<self-id>/
    path VARCHAR(4096) NOT NULL,
    depth INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT folders_depth_positive CHECK (depth >= 1)
);

-- NULL parent_id marks a root folder; the expression keeps sibling names
-- unique per parent without relying on NULL distinctness.
CREATE UNIQUE INDEX IF NOT EXISTS folders_sibling_name
    ON folders (knowledge_base_id, COALESCE(parent_id, ''), lower(name));
CREATE INDEX IF NOT EXISTS folders_scope_path_idx
    ON folders (tenant_id, knowledge_base_id, path);
CREATE INDEX IF NOT EXISTS folders_parent_idx
    ON folders (knowledge_base_id, parent_id);

ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS folder_id VARCHAR(64) REFERENCES folders(id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS documents_folder_idx
    ON documents (tenant_id, knowledge_base_id, folder_id);

CREATE TABLE IF NOT EXISTS tags (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    name VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS tags_scope_name
    ON tags (knowledge_base_id, lower(name));

CREATE TABLE IF NOT EXISTS document_tags (
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    document_id VARCHAR(64) NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    tag_id VARCHAR(64) NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (document_id, tag_id)
);

CREATE INDEX IF NOT EXISTS document_tags_tag_idx
    ON document_tags (tenant_id, knowledge_base_id, tag_id);

ALTER TABLE folders ENABLE ROW LEVEL SECURITY;
ALTER TABLE folders FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON folders;
DROP POLICY IF EXISTS tenant_isolation ON folders;
CREATE POLICY tenant_isolation ON folders FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON folders;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON folders
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

ALTER TABLE tags ENABLE ROW LEVEL SECURITY;
ALTER TABLE tags FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON tags;
DROP POLICY IF EXISTS tenant_isolation ON tags;
CREATE POLICY tenant_isolation ON tags FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON tags;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON tags
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

ALTER TABLE document_tags ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_tags FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_shadow_allow ON document_tags;
DROP POLICY IF EXISTS tenant_isolation ON document_tags;
CREATE POLICY tenant_isolation ON document_tags FOR ALL TO PUBLIC
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP TRIGGER IF EXISTS rls_shadow_write ON document_tags;
CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON document_tags
    FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation();

COMMENT ON TABLE folders IS 'B6-a knowledge space folder tree; permissions stay on the knowledge base.';
COMMENT ON TABLE tags IS 'B6-a per-knowledge-base manual tags; filtering only, never authorization.';
COMMENT ON TABLE document_tags IS 'B6-a document tag assignments.';