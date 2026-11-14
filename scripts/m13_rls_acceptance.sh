#!/usr/bin/env bash
# Original M13 A1.2/A1.3 (delivered) acceptance: verify the tenant RLS schema is installed and
# consistent with either the shadow (permissive) or enforce (strict) policy
# layout. This does not switch traffic between roles; role selection is a
# deployment decision (see docs/operations.md).
# Set ENFORCE_LAYOUT=1 to additionally require the strict layout, the running
# api/worker containers to use enforce with rag_app/rag_maintenance, and live
# connections for both roles. CI uses this mode.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'M13 RLS acceptance failed: %s\n' "$*" >&2; exit 1; }

psql_value() {
  "${compose[@]}" exec -T postgres psql -U "${POSTGRES_USER:-rag_platform}" -d "${POSTGRES_DB:-rag_platform}" -Atc "$1"
}

step "migration 0023 recorded"
applied="$(psql_value "SELECT count(*) FROM schema_migrations WHERE version = '0023_tenant_rls_shadow'")"
[[ "$applied" == "1" ]] || fail "migration 0023 is not applied (count=$applied)"

tables=(answer_jobs api_keys audit_logs auth_sessions authorization_revisions chunks connector_items connectors conversations
  document_versions documents evidence_packages graph_entities graph_entity_aliases graph_relation_sources
  graph_relations graph_snapshots knowledge_bases knowledge_index_records knowledge_projection_states memberships
  message_citations messages model_catalog_entries model_providers outbox_events pipeline_runs projection_generation_cache
  projection_runs stream_events
  sync_runs task_dead_letters task_inbox wiki_page_sources wiki_page_versions wiki_pages
  stage_runs projection_stage_runs knowledge_base_members chunk_embeddings embedding_migrations)

step "RLS enabled and forced on ${#tables[@]} tables"
for table in "${tables[@]}"; do
  state="$(psql_value "SELECT relrowsecurity::int || ':' || relforcerowsecurity::int FROM pg_class WHERE relname = '$table' AND relkind = 'r'")"
  [[ "$state" == "1:1" ]] || fail "$table is not ENABLE+FORCE row level security (got '$state')"
done

step "policy layout and audit triggers"
shadow_policies="$(psql_value "SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND policyname = 'tenant_shadow_allow'")"
strict_policies="$(psql_value "SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND policyname = 'tenant_isolation'")"
if [[ "$shadow_policies" == "${#tables[@]}" ]]; then
  layout="shadow"
elif [[ "$strict_policies" == "${#tables[@]}" ]]; then
  layout="enforce"
else
  fail "expected ${#tables[@]} tenant_shadow_allow (shadow) or tenant_isolation (enforce) policies, found shadow=$shadow_policies strict=$strict_policies"
fi
printf 'policy layout: %s\n' "$layout"
triggers="$(psql_value "SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid WHERE NOT t.tgisinternal AND t.tgname = 'rls_shadow_write'")"
[[ "$triggers" == "${#tables[@]}" ]] || fail "expected ${#tables[@]} rls_shadow_write triggers, found $triggers"

step "runtime roles"
app_flags="$(psql_value "SELECT rolsuper::int || ':' || rolbypassrls::int FROM pg_roles WHERE rolname = 'rag_app'")"
[[ "$app_flags" == "0:0" ]] || fail "rag_app must be NOSUPERUSER NOBYPASSRLS (got $app_flags)"
maintenance_flags="$(psql_value "SELECT rolbypassrls::int FROM pg_roles WHERE rolname = 'rag_maintenance'")"
[[ "$maintenance_flags" == "1" ]] || fail "rag_maintenance must be BYPASSRLS (got $maintenance_flags)"

if [[ "${ENFORCE_LAYOUT:-0}" == "1" ]]; then
  step "enforce layout"
  [[ "$layout" == "enforce" ]] || fail "ENFORCE_LAYOUT=1 requires tenant_isolation on all ${#tables[@]} tables (found $layout)"
  strict_applied="$(psql_value "SELECT count(*) FROM schema_migrations WHERE version = '0024_tenant_rls_strict'")"
  [[ "$strict_applied" == "1" ]] || fail "migration 0024_tenant_rls_strict is not recorded"

  api_mode="$("${compose[@]}" exec -T api printenv TENANT_RLS_MODE | tr -d '\r')"
  worker_mode="$("${compose[@]}" exec -T worker printenv TENANT_RLS_MODE | tr -d '\r')"
  [[ "$api_mode" == "enforce" ]] || fail "api container TENANT_RLS_MODE is '$api_mode', want enforce"
  [[ "$worker_mode" == "enforce" ]] || fail "worker container TENANT_RLS_MODE is '$worker_mode', want enforce"

  url_role() {
    python3 - "$1" <<'PY'
import sys, urllib.parse
print(urllib.parse.urlparse(sys.argv[1]).username or "")
PY
  }
  api_url="$("${compose[@]}" exec -T api printenv DATABASE_URL | tr -d '\r')"
  maintenance_url="$("${compose[@]}" exec -T api printenv MAINTENANCE_DATABASE_URL | tr -d '\r')"
  api_role="$(url_role "$api_url")"
  maintenance_role="$(url_role "$maintenance_url")"
  [[ "$api_role" == "rag_app" ]] || fail "api DATABASE_URL role is '$api_role', want rag_app"
  [[ -n "$maintenance_role" && "$maintenance_role" != "$api_role" ]] || fail "api maintenance role '$maintenance_role' must differ from '$api_role'"

  curl -fsS "http://localhost:${API_PORT:-8082}/readyz" >/dev/null || fail "api /readyz did not pass while checking enforce connections"
  app_conns="$(psql_value "SELECT count(*) FROM pg_stat_activity WHERE usename = 'rag_app' AND datname = current_database()")"
  [[ "$app_conns" -gt 0 ]] || fail "no live rag_app connection found; the api is not running enforce"
  maintenance_conns="$(psql_value "SELECT count(*) FROM pg_stat_activity WHERE usename = 'rag_maintenance' AND datname = current_database()")"
  [[ "$maintenance_conns" -gt 0 ]] || fail "no live rag_maintenance connection found; api/worker role separation is not active"
fi

step "task_inbox attribution"
unattributed="$(psql_value "SELECT count(*) FROM task_inbox WHERE tenant_id IS NULL OR tenant_id = ''")"
[[ "$unattributed" == "0" ]] || fail "task_inbox has $unattributed unattributable rows; clear them before enforce"
inbox_tenant="$(psql_value "SELECT count(*) FROM information_schema.columns WHERE table_name = 'task_inbox' AND column_name = 'tenant_id'")"
[[ "$inbox_tenant" == "1" ]] || fail "task_inbox.tenant_id is missing"

step "shadow findings"
findings="$(psql_value "SELECT count(*) FROM rls_shadow_violations")"
[[ "$findings" == "0" ]] || fail "rls_shadow_violations is not empty ($findings rows); inspect before enforce"

step "strict policy drill on a temporary table"
# Mirrors tests/postgres/tenantrls_test.go: under a strict policy a role without
# BYPASSRLS sees only its own tenant and cannot insert another tenant's row.
drill="$("${compose[@]}" exec -T postgres psql -U "${POSTGRES_USER:-rag_platform}" -d "${POSTGRES_DB:-rag_platform}" -At -v ON_ERROR_STOP=1 <<'SQL'
BEGIN;
CREATE TEMP TABLE rls_acceptance_probe (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL) ON COMMIT DROP;
INSERT INTO rls_acceptance_probe VALUES ('a-1', 'tenant-a'), ('b-1', 'tenant-b');
ALTER TABLE rls_acceptance_probe ENABLE ROW LEVEL SECURITY;
ALTER TABLE rls_acceptance_probe FORCE ROW LEVEL SECURITY;
CREATE POLICY strict_probe ON rls_acceptance_probe
  USING (tenant_id = current_setting('app.tenant_id', true))
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
GRANT SELECT, INSERT ON rls_acceptance_probe TO rag_app;
SET LOCAL ROLE rag_app;
SET LOCAL app.tenant_id = 'tenant-a';
SELECT 'visible=' || count(*) FROM rls_acceptance_probe;
DO $$
BEGIN
  BEGIN
    INSERT INTO rls_acceptance_probe VALUES ('b-2', 'tenant-b');
    RAISE EXCEPTION 'strict policy accepted a cross-tenant insert';
  EXCEPTION
    WHEN insufficient_privilege THEN NULL;
  END;
END
$$;
SELECT 'strict_ok';
ROLLBACK;
SQL
)"
grep -q 'visible=1' <<<"$drill" || fail "strict drill did not see exactly one row: $drill"
grep -q 'strict_ok' <<<"$drill" || fail "strict drill did not complete: $drill"

echo "M13 RLS ${layout} acceptance passed"
