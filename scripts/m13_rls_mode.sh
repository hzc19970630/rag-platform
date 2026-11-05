#!/usr/bin/env bash
# Switch the 36 tenant tables between the shadow and off policy layouts.
#   shadow: permissive tenant_shadow_allow policies (0023 behavior)
#   off:    no policies and row-level security disabled
# Enforce (0024) is applied by `make migrate`/cmd/migrate, not by this script.
set -euo pipefail

mode="${1:-}"
case "$mode" in
  shadow|off) ;;
  *) echo "usage: $0 shadow|off" >&2; exit 2 ;;
esac

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)

"${compose[@]}" exec -T postgres psql -U "${POSTGRES_USER:-rag_platform}" -d "${POSTGRES_DB:-rag_platform}" -v ON_ERROR_STOP=1 -v mode="$mode" <<'SQL'
SELECT set_config('app.rls_mode', :'mode', false);
DO $$
DECLARE
    tbl TEXT;
    target_mode TEXT := current_setting('app.rls_mode');
    tenant_tables TEXT[] := ARRAY[
        'answer_jobs',
        'api_keys', 'audit_logs', 'auth_sessions', 'authorization_revisions',
        'chunks', 'connector_items', 'connectors', 'conversations',
        'document_versions', 'documents', 'evidence_packages', 'graph_entities',
        'graph_entity_aliases', 'graph_relation_sources', 'graph_relations',
        'graph_snapshots', 'knowledge_bases', 'knowledge_index_records',
        'knowledge_projection_states', 'memberships', 'message_citations',
        'messages', 'outbox_events', 'pipeline_runs', 'projection_generation_cache',
        'projection_runs', 'stream_events', 'sync_runs', 'task_dead_letters',
        'task_inbox', 'wiki_page_sources', 'wiki_page_versions', 'wiki_pages',
        'stage_runs', 'projection_stage_runs', 'knowledge_base_members'
    ];
BEGIN
    FOREACH tbl IN ARRAY tenant_tables LOOP
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', tbl);
        EXECUTE format('DROP POLICY IF EXISTS tenant_shadow_allow ON %I', tbl);
        IF target_mode = 'shadow' THEN
            EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
            EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
            EXECUTE format('CREATE POLICY tenant_shadow_allow ON %I FOR ALL TO PUBLIC USING (true) WITH CHECK (true)', tbl);
        ELSE
            EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', tbl);
            EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', tbl);
        END IF;
    END LOOP;
END
$$;

SELECT 'rls_mode=' || current_setting('app.rls_mode');
SQL

echo "RLS policies switched to $mode; restart api and worker if TENANT_RLS_MODE changed"
