-- Tenant row-level security, shadow stage. The application still filters by
-- tenant_id itself; this migration makes PostgreSQL aware of the tenant so the
-- write path can be audited before enforcement (0024 replaces the permissive
-- policies with strict ones).
--
-- Runtime roles:
--   rag_app          application role, NOBYPASSRLS (password set by the setup script)
--   rag_maintenance  cross-tenant maintenance, BYPASSRLS (retention, reconcile)
-- Migrations and admin tooling keep using the owner role.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'rag_app') THEN
        CREATE ROLE rag_app NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'rag_maintenance') THEN
        CREATE ROLE rag_maintenance NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE BYPASSRLS;
    END IF;
END
$$;

-- Shadow findings. A row is written when a statement on the runtime role
-- carries a row tenant that differs from app.tenant_id, or when the GUC is not
-- set at all. Admin and owner sessions (seed, migrate, embedctl) are exempt:
-- they are expected to operate across tenants.
CREATE TABLE IF NOT EXISTS rls_shadow_violations (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    table_name TEXT NOT NULL,
    operation TEXT NOT NULL,
    row_tenant_id TEXT,
    session_tenant_id TEXT,
    db_user NAME NOT NULL DEFAULT current_user,
    detail TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS rls_shadow_violations_observed_idx
    ON rls_shadow_violations(observed_at);

CREATE OR REPLACE FUNCTION rls_shadow_violation() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    row_tenant TEXT;
    session_tenant TEXT := current_setting('app.tenant_id', true);
BEGIN
    -- Only the runtime role is audited. Owner sessions run migrations, seeds
    -- and tooling by design, and an unset GUC there is not a finding. A custom
    -- GUC reverts to an empty string (not NULL) after its transaction commits,
    -- so both states mean "no tenant context".
    IF (session_tenant IS NULL OR session_tenant = '') AND current_user <> 'rag_app' THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        row_tenant := OLD.tenant_id;
    ELSE
        row_tenant := NEW.tenant_id;
    END IF;
    IF row_tenant IS DISTINCT FROM session_tenant THEN
        INSERT INTO rls_shadow_violations
            (table_name, operation, row_tenant_id, session_tenant_id, detail)
        VALUES
            (TG_TABLE_NAME, TG_OP, row_tenant, session_tenant,
             'row tenant does not match app.tenant_id');
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

-- Child tables have no tenant_id of their own; the finding is derived from the
-- parent row. Arguments are parent table and foreign-key column.
CREATE OR REPLACE FUNCTION rls_shadow_violation_child() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    session_tenant TEXT := current_setting('app.tenant_id', true);
    row_json JSONB;
    fk_value TEXT;
    parent_tenant TEXT;
BEGIN
    IF (session_tenant IS NULL OR session_tenant = '') AND current_user <> 'rag_app' THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        row_json := to_jsonb(OLD);
    ELSE
        row_json := to_jsonb(NEW);
    END IF;
    fk_value := row_json ->> TG_ARGV[1];
    EXECUTE format('SELECT tenant_id FROM %I WHERE id = $1', TG_ARGV[0])
        INTO parent_tenant USING fk_value;
    IF parent_tenant IS DISTINCT FROM session_tenant THEN
        INSERT INTO rls_shadow_violations
            (table_name, operation, row_tenant_id, session_tenant_id, detail)
        VALUES
            (TG_TABLE_NAME, TG_OP, parent_tenant, session_tenant,
             'parent tenant does not match app.tenant_id');
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

-- task_inbox is keyed by idempotency_key and has no tenant of its own. The
-- shadow stage gives it a nullable tenant_id so the enforce stage can apply a
-- simple policy. Rows whose key does not resolve to a pipeline or projection
-- run cannot be attributed and are recorded for the operator to clear.
ALTER TABLE task_inbox ADD COLUMN IF NOT EXISTS tenant_id VARCHAR(64);

UPDATE task_inbox ti
SET tenant_id = pr.tenant_id, updated_at = NOW()
FROM pipeline_runs pr
WHERE ti.tenant_id IS NULL AND pr.id = split_part(ti.idempotency_key, ':', 1);

UPDATE task_inbox ti
SET tenant_id = pr.tenant_id, updated_at = NOW()
FROM projection_runs pr
WHERE ti.tenant_id IS NULL AND pr.id = split_part(ti.idempotency_key, ':', 1);

INSERT INTO rls_shadow_violations
    (table_name, operation, row_tenant_id, session_tenant_id, detail)
SELECT 'task_inbox', 'BACKFILL', NULL, NULL,
       'unattributable idempotency_key=' || idempotency_key
FROM task_inbox
WHERE tenant_id IS NULL;

CREATE INDEX IF NOT EXISTS task_inbox_tenant_idx ON task_inbox(tenant_id);

-- Shadow policies allow every row so the stage never blocks traffic. 0024
-- drops them and installs tenant_isolation policies with the same names on
-- every table.
DO $$
DECLARE
    tbl TEXT;
    tenant_tables TEXT[] := ARRAY[
        'api_keys', 'audit_logs', 'auth_sessions', 'authorization_revisions',
        'chunks', 'connector_items', 'connectors', 'conversations',
        'document_versions', 'documents', 'evidence_packages', 'graph_entities',
        'graph_entity_aliases', 'graph_relation_sources', 'graph_relations',
        'graph_snapshots', 'knowledge_bases', 'knowledge_index_records',
        'knowledge_projection_states', 'memberships', 'message_citations',
        'messages', 'outbox_events', 'pipeline_runs', 'projection_generation_cache',
        'projection_runs', 'stream_events', 'sync_runs', 'task_dead_letters',
        'task_inbox', 'wiki_page_sources', 'wiki_page_versions', 'wiki_pages'
    ];
BEGIN
    FOREACH tbl IN ARRAY tenant_tables LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
        EXECUTE format('DROP POLICY IF EXISTS tenant_shadow_allow ON %I', tbl);
        EXECUTE format('CREATE POLICY tenant_shadow_allow ON %I FOR ALL TO PUBLIC USING (true) WITH CHECK (true)', tbl);
        EXECUTE format('DROP TRIGGER IF EXISTS rls_shadow_write ON %I', tbl);
        EXECUTE format('CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation()', tbl);
    END LOOP;
END
$$;

DO $$
DECLARE
    spec RECORD;
BEGIN
    FOR spec IN
        SELECT * FROM (VALUES
            ('stage_runs', 'pipeline_runs', 'pipeline_run_id'),
            ('projection_stage_runs', 'projection_runs', 'projection_run_id'),
            ('knowledge_base_members', 'knowledge_bases', 'knowledge_base_id')
        ) AS v(child_table, parent_table, fk_column)
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', spec.child_table);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', spec.child_table);
        EXECUTE format('DROP POLICY IF EXISTS tenant_shadow_allow ON %I', spec.child_table);
        EXECUTE format('CREATE POLICY tenant_shadow_allow ON %I FOR ALL TO PUBLIC USING (true) WITH CHECK (true)', spec.child_table);
        EXECUTE format('DROP TRIGGER IF EXISTS rls_shadow_write ON %I', spec.child_table);
        EXECUTE format(
            'CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation_child(%L, %L)',
            spec.child_table, spec.parent_table, spec.fk_column
        );
    END LOOP;
END
$$;

COMMENT ON TABLE rls_shadow_violations IS
    'M13 shadow findings: tenant writes whose row/parent tenant differs from app.tenant_id. Empty means the app context agrees with the data.';
COMMENT ON FUNCTION rls_shadow_violation() IS
    'Audits writes on tenant-owned tables against app.tenant_id while RLS is in shadow mode.';
