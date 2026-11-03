#!/usr/bin/env sh
set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root_dir"

compose="docker compose --env-file .env.example -f deploy/docker-compose/docker-compose.yml"

$compose config --quiet
$compose up --build -d
make migrate
make seed

curl -fsS http://localhost:"${API_PORT:-8082}"/healthz >/dev/null
curl -fsS http://localhost:"${API_PORT:-8082}"/readyz | grep -q '"status":"ok"'

sql='SELECT extname FROM pg_extension WHERE extname IN ('"'"'vector'"'"', '"'"'pg_search'"'"') ORDER BY extname; SELECT indexname FROM pg_indexes WHERE indexname IN ('"'"'platform_index_probe_bm25_idx'"'"', '"'"'platform_index_probe_vector_idx'"'"') ORDER BY indexname; SELECT value FROM platform_settings WHERE key = '"'"'environment'"'"';'
$compose exec -T postgres psql -U rag_platform -d rag_platform -Atc "$sql" | grep -q 'pg_search'
$compose exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT extname FROM pg_extension WHERE extname = 'vector';" | grep -q '^vector$'
$compose exec -T postgres psql -U rag_platform -d rag_platform -Atc "$sql" | grep -q 'platform_index_probe_bm25_idx'
$compose exec -T postgres psql -U rag_platform -d rag_platform -Atc "$sql" | grep -q 'platform_index_probe_vector_idx'
$compose exec -T postgres psql -U rag_platform -d rag_platform -Atc "$sql" | grep -q 'development'

$compose exec -T postgres psql -U rag_platform -d rag_platform -v ON_ERROR_STOP=1 -c "TRUNCATE platform_index_probe; INSERT INTO platform_index_probe (tenant_id, knowledge_base_id, chunk_id, content, embedding, embedding_dimension) VALUES ('tenant_m0', 'kb_m0', 'chunk_m0', 'M0 BM25 compatibility probe', '[1,2,3]', 3);" >/dev/null
$compose exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT id FROM platform_index_probe WHERE content @@@ 'BM25 compatibility' ORDER BY paradedb.score(id) DESC LIMIT 1;" | grep -Eq '^[0-9]+$'
$compose exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT id FROM platform_index_probe ORDER BY embedding <=> '[1,2,3]' LIMIT 1;" | grep -Eq '^[0-9]+$'

$compose exec -T postgres psql -U rag_platform -d postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE IF EXISTS m0_empty_acceptance WITH (FORCE);" >/dev/null
$compose exec -T postgres psql -U rag_platform -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE m0_empty_acceptance TEMPLATE template0;" >/dev/null
empty_url='postgres://rag_platform:change-me-local-only@postgres:5432/m0_empty_acceptance?sslmode=disable'
$compose run --rm --no-deps -e DATABASE_URL="$empty_url" --entrypoint /usr/local/bin/migrate api >/dev/null
$compose run --rm --no-deps -e DATABASE_URL="$empty_url" --entrypoint /usr/local/bin/seed api >/dev/null
$compose run --rm --no-deps -e DATABASE_URL="$empty_url" --entrypoint /usr/local/bin/migrate api >/dev/null
$compose run --rm --no-deps -e DATABASE_URL="$empty_url" --entrypoint /usr/local/bin/seed api >/dev/null
$compose exec -T postgres psql -U rag_platform -d m0_empty_acceptance -Atc "SELECT value FROM platform_settings WHERE key = 'environment';" | grep -q '^development$'

echo "M0 acceptance passed"
