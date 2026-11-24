#!/usr/bin/env bash
# B35 projection epochs / generations smoke acceptance.
#
# Verifies the schema, RLS and the invariants that make batch projection and the
# atomic wiki switch safe. Destructive end-to-end scenarios (20-file batch,
# delete/ACL within 1s) are documented in docs/operations.md and exercised by
# the manual acceptance run; this script is safe against a populated stack.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)
psql_value() { "${compose[@]}" exec -T postgres psql -U "${POSTGRES_USER:-rag_platform}" -d "${POSTGRES_DB:-rag_platform}" -Atc "$1"; }

step() { printf '\n== %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

step "migration 0039 applied"
[[ "$(psql_value "SELECT count(*) FROM schema_migrations WHERE version = '0039_projection_epochs'")" == "1" ]] \
  || fail "0039_projection_epochs is not applied"

step "B35 tables exist and are tenant-isolated"
for table in projection_changes wiki_generations wiki_generation_pages; do
  state="$(psql_value "SELECT relrowsecurity::int || ':' || relforcerowsecurity::int FROM pg_class WHERE relname = '$table' AND relkind = 'r'")"
  [[ "$state" == "1:1" ]] || fail "$table is not ENABLE+FORCE row level security (got '$state')"
  policy="$(psql_value "SELECT count(*) FROM pg_policies WHERE schemaname='public' AND tablename='$table' AND policyname IN ('tenant_isolation','tenant_shadow_allow')")"
  [[ "$policy" == "1" ]] || fail "$table has no tenant policy"
done

step "projection state columns"
columns="$(psql_value "SELECT count(*) FROM information_schema.columns WHERE table_name='knowledge_projection_states' AND column_name IN ('target_epoch','wiki_applied_epoch','relation_applied_epoch','active_wiki_generation_id','invalidated_at')")"
[[ "$columns" == "5" ]] || fail "knowledge_projection_states is missing B35 columns (found $columns/5)"
run_columns="$(psql_value "SELECT count(*) FROM information_schema.columns WHERE table_name='projection_runs' AND column_name IN ('base_epoch','target_epoch')")"
[[ "$run_columns" == "2" ]] || fail "projection_runs is missing epoch columns"

step "at most one ACTIVE generation per knowledge base"
dupes="$(psql_value "SELECT count(*) FROM (SELECT tenant_id, knowledge_base_id FROM wiki_generations WHERE status='ACTIVE' GROUP BY 1,2 HAVING count(*) > 1) t")"
[[ "$dupes" == "0" ]] || fail "$dupes knowledge bases have more than one ACTIVE generation"

step "epoch monotonicity"
bad="$(psql_value "SELECT count(*) FROM knowledge_projection_states WHERE wiki_applied_epoch > target_epoch OR relation_applied_epoch > target_epoch")"
[[ "$bad" == "0" ]] || fail "$bad projection states have applied_epoch > target_epoch"

step "migration backfill: generation pages reference published versions"
orphans="$(psql_value "SELECT count(*) FROM wiki_generation_pages gp LEFT JOIN wiki_pages p ON p.id = gp.wiki_page_id LEFT JOIN wiki_page_versions v ON v.id = gp.wiki_page_version_id WHERE gp.wiki_page_version_id IS NOT NULL AND v.id IS NULL")"
[[ "$orphans" == "0" ]] || fail "$orphans generation pages reference a missing version"

step "blocking changes keep the invalidation gate set"
blocked="$(psql_value "SELECT count(*) FROM projection_changes c JOIN knowledge_projection_states s ON s.tenant_id = c.tenant_id AND s.knowledge_base_id = c.knowledge_base_id WHERE c.applied_at IS NULL AND c.kind IN ('delete','acl') AND s.invalidated_at IS NULL")"
[[ "$blocked" == "0" ]] || fail "$blocked pending delete/acl changes have no invalidation gate"

printf '\nB35 acceptance smoke passed\n'
