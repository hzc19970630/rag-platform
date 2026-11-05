-- Tenant row-level security, strict stage. This migration only replaces the
-- permissive shadow policies from 0023 with isolation policies; it changes no
-- table structure. Apply it together with TENANT_RLS_MODE=enforce: until the
-- API sets app.tenant_id on every request transaction, strict policies make
-- rag_app reads return zero rows and reject writes.
--
-- The GUC is read through NULLIF(..., '') because a transaction-local custom
-- setting reverts to an empty string (not NULL) after commit. Both states mean
-- "no tenant context" and must match no row.
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
        EXECUTE format('DROP POLICY IF EXISTS tenant_shadow_allow ON %I', tbl);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', tbl);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I FOR ALL TO PUBLIC USING (tenant_id = NULLIF(current_setting(''app.tenant_id'', true), '''')) WITH CHECK (tenant_id = NULLIF(current_setting(''app.tenant_id'', true), ''''))',
            tbl
        );
    END LOOP;
END
$$;

-- Child tables inherit the tenant from their parent. The EXISTS subquery is
-- itself filtered by the parent policy, so an unset GUC still matches nothing.
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
        EXECUTE format('DROP POLICY IF EXISTS tenant_shadow_allow ON %I', spec.child_table);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', spec.child_table);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I FOR ALL TO PUBLIC USING (EXISTS (SELECT 1 FROM %I parent WHERE parent.id = %I.%I AND parent.tenant_id = NULLIF(current_setting(''app.tenant_id'', true), ''''))) WITH CHECK (EXISTS (SELECT 1 FROM %I parent WHERE parent.id = %I.%I AND parent.tenant_id = NULLIF(current_setting(''app.tenant_id'', true), '''')))',
            spec.child_table, spec.parent_table, spec.child_table, spec.fk_column,
            spec.parent_table, spec.child_table, spec.fk_column
        );
    END LOOP;
END
$$;

COMMENT ON POLICY tenant_isolation ON chunks IS
    'M13 enforce: a query only sees rows of app.tenant_id; unset or empty matches nothing.';
