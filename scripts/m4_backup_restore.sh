#!/usr/bin/env bash
# M4 backup drill: dump the live development database, restore it into a
# throwaway ParadeDB container, and verify row counts, seed invariants and the
# BM25/vector index definitions. The target is disposable; the source stack is
# only read.
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
source_user="${POSTGRES_USER:-rag_platform}"
source_db="${POSTGRES_DB:-rag_platform}"
target_password="restore-only-$RANDOM"
container="rag-m4-restore-$RANDOM-$$"
tmp_dir="$(mktemp -d)"

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'M4 backup drill failed: %s\n' "$*" >&2; exit 1; }

source_psql() {
  "${compose[@]}" exec -T postgres psql -U "$source_user" -d "$source_db" -Atc "$1" 2>/dev/null
}

target_psql() {
  docker exec -e PGPASSWORD="$target_password" "$container" psql -U postgres -d rag_restore -Atc "$1"
}

step "preflight: source stack and migration compatibility"
curl -fsS "http://localhost:${API_PORT:-8082}/readyz" | grep -q '"status":"ok"' || fail "api is not ready"
[[ "$(source_psql "SELECT count(*) FROM schema_migrations;")" -gt 0 ]] || fail "schema_migrations is empty"
[[ "$(source_psql "SELECT count(*) FROM schema_migrations WHERE checksum IS NULL;")" == "0" ]] || fail "schema_migrations has rows without checksums"

step "backup: pg_dump custom format"
pg_dump_args=(-U "$source_user" -d "$source_db" --format=custom --no-owner --no-privileges)
"${compose[@]}" exec -T postgres pg_dump "${pg_dump_args[@]}" > "$tmp_dir/rag_platform.dump"
dump_bytes="$(wc -c < "$tmp_dir/rag_platform.dump" | tr -d ' ')"
[[ "$dump_bytes" -gt 1024 ]] || fail "dump is suspiciously small ($dump_bytes bytes)"
printf 'dump bytes: %s\n' "$dump_bytes"

step "restore: fresh ParadeDB target"
docker run -d --name "$container" \
  -e POSTGRES_PASSWORD="$target_password" \
  -e POSTGRES_DB=rag_restore \
  paradedb/paradedb:v0.22.6-pg17 >/dev/null
ready=""
init_done=0
init_logs=""
for _ in $(seq 1 90); do
  init_logs="$(docker logs "$container" 2>&1 || true)"
  if grep -q 'PostgreSQL init process complete' <<<"$init_logs"; then
    init_done=1
    break
  fi
  sleep 1
done
if [[ "$init_done" != "1" ]]; then
  printf '%s\n' "$init_logs" >&2
  fail "restore target finished initialization with an error"
fi
# The image runs initdb, stops that server, and starts the real one; wait for
# the second startup and confirm it stays up before restoring.
for _ in $(seq 1 60); do
  if docker exec "$container" pg_isready -U postgres -d rag_restore >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
[[ "$ready" == "1" ]] || { docker logs "$container" >&2; fail "restore target did not become ready"; }
sleep 2
docker exec "$container" pg_isready -U postgres -d rag_restore >/dev/null 2>&1 || {
  docker logs "$container" >&2
  fail "restore target stopped during startup"
}
docker cp "$tmp_dir/rag_platform.dump" "$container:/tmp/rag_platform.dump" >/dev/null
# The ParadeDB image bootstraps the paradedb schema plus pg_search/vector into
# every database, so skip those TOC entries instead of failing on "already
# exists". All application objects live in public and are restored normally.
docker exec "$container" sh -c "pg_restore -l /tmp/rag_platform.dump > /tmp/toc.txt && grep -v -E 'SCHEMA - paradedb|EXTENSION - pg_search|COMMENT - EXTENSION pg_search|EXTENSION - vector|COMMENT - EXTENSION vector' /tmp/toc.txt > /tmp/toc.filtered"
docker exec -e PGPASSWORD="$target_password" "$container" \
  pg_restore -U postgres -d rag_restore -L /tmp/toc.filtered --no-owner --no-privileges --exit-on-error /tmp/rag_platform.dump >/dev/null

step "verify: every public table has the same row count"
tables="$(source_psql "SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename;")"
mismatches=0
printf '%-28s %10s %10s\n' table source restore
for table in $tables; do
  source_count="$(source_psql "SELECT count(*) FROM \"$table\";")"
  restore_count="$(target_psql "SELECT count(*) FROM \"$table\";")"
  printf '%-28s %10s %10s\n' "$table" "$source_count" "$restore_count"
  if [[ "$source_count" != "$restore_count" ]]; then
    mismatches=$((mismatches + 1))
  fi
done
[[ "$mismatches" == "0" ]] || fail "$mismatches tables differ between source and restore"

step "verify: seed and authorization invariants"
[[ "$(target_psql "SELECT count(*) FROM users;")" -gt 0 ]] || fail "restore lost users"
[[ "$(target_psql "SELECT count(*) FROM knowledge_bases WHERE id = 'kb-dev';")" == "1" ]] || fail "restore lost kb-dev"
[[ "$(target_psql "SELECT role FROM knowledge_base_members WHERE knowledge_base_id = 'kb-dev' AND user_id = 'dev-user';")" == "owner" ]] || fail "restore lost the kb-dev owner membership"
source_revision="$(source_psql "SELECT tenant_id || ':' || revision FROM authorization_revisions ORDER BY tenant_id;")"
target_revision="$(target_psql "SELECT tenant_id || ':' || revision FROM authorization_revisions ORDER BY tenant_id;")"
[[ "$source_revision" == "$target_revision" ]] || fail "authorization revisions differ after restore"
source_versions="$(source_psql "SELECT version || ':' || checksum FROM schema_migrations ORDER BY version;")"
target_versions="$(target_psql "SELECT version || ':' || checksum FROM schema_migrations ORDER BY version;")"
[[ "$source_versions" == "$target_versions" ]] || fail "schema_migrations differ after restore"

step "verify: keyword and vector index definitions survive"
source_indexes="$(source_psql "SELECT indexname || '|' || indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'chunks' ORDER BY indexname;")"
target_indexes="$(target_psql "SELECT indexname || '|' || indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'chunks' ORDER BY indexname;")"
[[ "$source_indexes" == "$target_indexes" ]] || {
  printf 'source indexes:\n%s\nrestore indexes:\n%s\n' "$source_indexes" "$target_indexes" >&2
  fail "chunk index definitions differ after restore"
}
grep -q 'chunks_bm25_idx' <<<"$source_indexes" || fail "source is missing chunks_bm25_idx"
# Dimension-agnostic: the quickstart stack uses a small local embedder, while
# production uses 1024-dim vectors. The equality check above already proves the
# restored definition matches the source; here we only require a real HNSW index.
grep -q 'chunks_vector_idx' <<<"$source_indexes" || fail "source is missing chunks_vector_idx"
grep -qi 'hnsw' <<<"$source_indexes" || fail "source vector index is not HNSW"
printf '%s\n' "$target_indexes"

step "restore drill complete"
printf 'M4 backup drill passed: dump=%s bytes, tables=%s, target=%s (removed)\n' \
  "$dump_bytes" "$(wc -w <<<"$tables" | tr -d ' ')" "$container"
