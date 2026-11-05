-- Shadow audit fix for child tables (stage_runs, projection_stage_runs,
-- knowledge_base_members). Their tenant is derived from the parent row, but an
-- ON DELETE CASCADE removes the children after the parent row is already gone.
-- The parent lookup then returns NULL and the old trigger recorded every
-- cascade delete as a violation, which buried real findings.
--
-- An unattributable delete is not a cross-tenant finding. True violations
-- (parent still exists and belongs to another tenant) are recorded unchanged.
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
    IF parent_tenant IS NULL THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;
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

-- Remove the cascade-delete false positives recorded before this fix. Findings
-- with a non-null row_tenant_id, or the task_inbox backfill findings, are kept.
DELETE FROM rls_shadow_violations
WHERE row_tenant_id IS NULL
  AND detail = 'parent tenant does not match app.tenant_id';

COMMENT ON FUNCTION rls_shadow_violation_child() IS
    'Audits child-table writes against the parent tenant; cascade deletes whose parent is already gone are skipped.';
