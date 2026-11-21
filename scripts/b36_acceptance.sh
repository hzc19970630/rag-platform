#!/usr/bin/env bash
# B36 table indexing smoke acceptance. Safe against a populated stack: schema,
# RLS and coverage invariants only.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)
psql_value() { "${compose[@]}" exec -T postgres psql -U "${POSTGRES_USER:-rag_platform}" -d "${POSTGRES_DB:-rag_platform}" -Atc "$1"; }
step() { printf '\n== %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

step "migrations 0040/0041 applied"
for version in 0040_table_assets 0041_table_rows; do
  [[ "$(psql_value "SELECT count(*) FROM schema_migrations WHERE version = '$version'")" == "1" ]] \
    || fail "$version is not applied"
done

step "table tables exist and are tenant-isolated"
for table in table_assets table_rows; do
  state="$(psql_value "SELECT relrowsecurity::int || ':' || relforcerowsecurity::int FROM pg_class WHERE relname = '$table' AND relkind = 'r'")"
  [[ "$state" == "1:1" ]] || fail "$table is not ENABLE+FORCE row level security"
  policy="$(psql_value "SELECT count(*) FROM pg_policies WHERE schemaname='public' AND tablename='$table' AND policyname IN ('tenant_isolation','tenant_shadow_allow')")"
  [[ "$policy" == "1" ]] || fail "$table has no tenant policy"
done

step "chunk index roles are valid and keyword chunks stay vector-free"
invalid="$(psql_value "SELECT count(*) FROM chunks WHERE index_role NOT IN ('both','keyword','vector')")"
[[ "$invalid" == "0" ]] || fail "$invalid chunks have an unknown index_role"
leaked="$(psql_value "SELECT count(*) FROM chunks WHERE index_role='keyword' AND embedding IS NOT NULL")"
[[ "$leaked" == "0" ]] || fail "$leaked keyword-only chunks carry an embedding"

step "document coverage is monotonic"
bad="$(psql_value "SELECT count(*) FROM document_versions WHERE vector_rows > total_rows OR vector_sheets > total_sheets")"
[[ "$bad" == "0" ]] || fail "$bad document versions report coverage above the total"

step "table rows reference known assets"
orphans="$(psql_value "SELECT count(*) FROM table_rows r LEFT JOIN table_assets a ON a.id = r.table_asset_id WHERE a.id IS NULL")"
[[ "$orphans" == "0" ]] || fail "$orphans table rows reference a missing asset"

step "table assets use a known mode"
bad_mode="$(psql_value "SELECT count(*) FROM table_assets WHERE index_mode NOT IN ('','standard','deep','lexical')")"
[[ "$bad_mode" == "0" ]] || fail "$bad_mode table assets have an unknown index mode"

printf '\nB36 acceptance smoke passed\n'
