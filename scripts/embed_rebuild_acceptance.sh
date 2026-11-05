#!/usr/bin/env bash
# Exercises the managed embedding dimension switch end to end:
#   create halfvec(N) index -> re-embed every active chunk -> verify no
#   old-dimension chunk remains -> retrieval index matches the new vectors.
# The stack must already be up (make dev-up).
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

base_url="http://localhost:${API_PORT:-8082}"
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

psql() {
  "${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "$1"
}

run_embedctl() {
  local command="$1"
  shift
  "${compose[@]}" run --rm --no-deps "$@" --entrypoint /usr/local/bin/embedctl api "$command"
}

request() {
  local method="$1" path="$2" expected="$3" output="$4"
  shift 4
  local code
  code="$(curl -sS -o "$output" -w '%{http_code}' -X "$method" "$base_url$path" "$@")"
  if [[ "$code" != "$expected" ]]; then
    printf 'expected %s %s, got %s: ' "$method" "$path" "$code" >&2
    cat "$output" >&2
    exit 1
  fi
}

index_definition() {
  psql "SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND starts_with(indexname, 'chunks_vector_idx') AND indexdef LIKE '%USING hnsw%' ORDER BY indexname DESC LIMIT 1;"
}

index_count_matching() {
  psql "SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND starts_with(indexname, 'chunks_vector_idx') AND indexdef LIKE '%halfvec($1))%';"
}

wait_for_evidence_ready() {
  local document_id="$1" output="$2"
  for _ in $(seq 1 30); do
    request GET "/api/v1/documents/$document_id" 200 "$output" \
      -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant'
    local status
    status="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pipeline"]["status"])' "$output")"
    [[ "$status" == "EVIDENCE_READY" ]] && return 0
    if [[ "$status" == "FAILED" ]]; then
      cat "$output" >&2
      exit 1
    fi
    sleep 1
  done
  echo "document $document_id never reached EVIDENCE_READY" >&2
  exit 1
}

curl -fsS "$base_url/readyz" | grep -q '"status":"ok"'

# Baseline: the default local adapter produces 3-dimensional vectors and the
# managed command creates the matching index. The index is built CONCURRENTLY
# under a versioned name, so the assertion accepts both the legacy name and
# chunks_vector_idx_<dim>_v<N>.
run_embedctl check >/dev/null
run_embedctl create-index --confirm >/dev/null
[[ "$(index_definition)" == *"halfvec(3)"* ]]

printf '# Embedding Rebuild Acceptance\n\nEvery chunk must be re-embedded when the dimension changes.\n' > "$tmp_dir/switch.md"
key="embed-acceptance-$(date +%s)"
request POST /api/v1/knowledge-bases/kb-dev/documents 202 "$tmp_dir/upload.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H "Idempotency-Key: $key" -F "file=@$tmp_dir/switch.md;type=text/markdown"
document_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["document"]["id"])' "$tmp_dir/upload.json")"
version_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"]["id"])' "$tmp_dir/upload.json")"
wait_for_evidence_ready "$document_id" "$tmp_dir/status.json"

children="$(psql "SELECT count(*) FROM chunks WHERE document_version_id = '$version_id' AND parent_id IS NOT NULL;")"
[[ "$children" -gt 0 ]]
at_three="$(psql "SELECT count(*) FROM chunks WHERE document_version_id = '$version_id' AND parent_id IS NOT NULL AND embedding_dimension = 3 AND embedding_model = 'local-deterministic';")"
[[ "$at_three" == "$children" ]]

# Managed switch: build halfvec(4), re-embed, then verify no 3-dimensional
# chunk is left behind. The rebuild drops the superseded 3-dimension index only
# after the 4-dimension index is valid.
run_embedctl create-index --confirm -e EMBEDDING_DIMENSION=4 >/dev/null
[[ "$(index_definition)" == *"halfvec(4)"* ]]
[[ "$(index_count_matching 3)" == "0" ]]
run_embedctl reembed -e EMBEDDING_DIMENSION=4 >/dev/null

leftover_three="$(psql "SELECT count(*) FROM chunks WHERE parent_id IS NOT NULL AND status IN ('EMBEDDED', 'EVIDENCE_READY') AND embedding_dimension = 3;")"
[[ "$leftover_three" == "0" ]]
at_four="$(psql "SELECT count(*) FROM chunks WHERE document_version_id = '$version_id' AND parent_id IS NOT NULL AND embedding_dimension = 4 AND embedding_model = 'local-deterministic';")"
[[ "$at_four" == "$children" ]]
package_dimension="$(psql "SELECT max((e ->> 'embedding_dimension')::int) FROM evidence_packages, jsonb_array_elements(package_json -> 'chunks') e WHERE document_version_id = '$version_id';")"
[[ "$package_dimension" == "4" ]]

run_embedctl verify -e EMBEDDING_DIMENSION=4 >/dev/null

# Negative case: verification must catch a dimension that no chunk satisfies.
if run_embedctl verify -e EMBEDDING_DIMENSION=8 >/dev/null 2>&1; then
  echo "verify must fail when stale-dimension chunks remain" >&2
  exit 1
fi

# The rebuilt index must serve the new dimension.
psql "SELECT id FROM chunks WHERE embedding_dimension = 4 AND parent_id IS NOT NULL ORDER BY embedding <=> '[1,2,3,4]' LIMIT 1;" | grep -Eq '^chunk-'

# Restore the default dimension so the running stack keeps matching .env.
run_embedctl create-index --confirm >/dev/null
run_embedctl reembed >/dev/null
run_embedctl verify >/dev/null
[[ "$(index_count_matching 4)" == "0" ]]

printf 'Embedding dimension switch acceptance passed: document=%s version=%s chunks=%s\n' "$document_id" "$version_id" "$children"