#!/usr/bin/env bash
# B15-c acceptance: read-only configuration change preview.
#
# Verifies that query-time changes need no rebuild, chunking changes are
# optional history rebuilds, embedding changes are blocked with a migration
# action, permissions are enforced and nothing is persisted.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

base_url="http://localhost:${API_PORT:-8082}"
tmp_dir="$(mktemp -d)"
trap 'cleanup' EXIT

dev_headers=(-H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant')
viewer_headers=(-H 'X-Dev-User-ID: viewer-a' -H 'X-Dev-Tenant-ID: dev-tenant')

python_bin="${PYTHON_BIN:-python}"
if ! "$python_bin" -c 'import sys' >/dev/null 2>&1; then
  python_bin=python3
fi

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

json() {
  "$python_bin" -c "import json,sys; d=json.load(open(sys.argv[1], encoding='utf-8')); print(eval(sys.argv[2], {'d': d, 'json': json}))" "$2" "$1"
}

kb_id=""
cleanup() {
  if [[ -n "$kb_id" ]]; then
    curl -sS -o /dev/null -X DELETE -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' "$base_url/api/v1/knowledge-bases/$kb_id" || true
  fi
  rm -rf "$tmp_dir"
}

curl -fsS "$base_url/readyz" | grep -q '"status":"ok"'

request GET /api/v1/tenant/config 200 "$tmp_dir/tenant-before.json" "${dev_headers[@]}"
before_retrieval="$(json 'json.dumps(d["retrieval"]["tenantOverride"], sort_keys=True)' "$tmp_dir/tenant-before.json")"
before_chunking="$(json 'json.dumps(d["chunking"]["tenantOverride"], sort_keys=True)' "$tmp_dir/tenant-before.json")"

# A temporary knowledge base keeps the preview counts deterministic.
request POST /api/v1/knowledge-bases 201 "$tmp_dir/kb.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"name\":\"B15C Preview $(date +%s)\"}"
kb_id="$(json 'd["id"]' "$tmp_dir/kb.json")"
request GET "/api/v1/knowledge-bases/$kb_id/configuration" 200 "$tmp_dir/kb-config.json" "${dev_headers[@]}"
current_top_k="$(json 'd["retrieval"]["effective"]["top_k"]' "$tmp_dir/kb-config.json")"
current_dimension="$(json 'd["readOnly"]["embedding"]["dimension"]' "$tmp_dir/kb-config.json")"
new_top_k=7
if [[ "$current_top_k" == "7" ]]; then new_top_k=8; fi

# Query-time change: hot, no rebuild, exactly one knowledge base affected.
request POST /api/v1/config/preview 200 "$tmp_dir/kb-preview.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' \
  -d "{\"scope\":\"knowledge_base\",\"knowledgeBaseId\":\"$kb_id\",\"retrieval\":{\"top_k\":$new_top_k}}"
[[ "$(json 'd["impact"]' "$tmp_dir/kb-preview.json")" == "hot" ]]
[[ "$(json 'd["rebuildRequired"]' "$tmp_dir/kb-preview.json")" == "False" ]]
[[ "$(json 'd["rebuildOptional"]' "$tmp_dir/kb-preview.json")" == "False" ]]
[[ "$(json 'd["affected"]["knowledgeBases"]' "$tmp_dir/kb-preview.json")" == "1" ]]
[[ "$(json 'any(c["key"] == "top_k" for c in d["changes"])' "$tmp_dir/kb-preview.json")" == "True" ]]

# Chunking change: next ingestion only, optional history rebuild, never required.
request POST /api/v1/config/preview 200 "$tmp_dir/chunk-preview.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"scope":"tenant","retrieval":{},"chunking":{"size":512}}'
[[ "$(json 'd["impact"]' "$tmp_dir/chunk-preview.json")" == "next_ingest" ]]
[[ "$(json 'd["rebuildRequired"]' "$tmp_dir/chunk-preview.json")" == "False" ]]
[[ "$(json 'd["rebuildOptional"]' "$tmp_dir/chunk-preview.json")" == "True" ]]
[[ "$(json 'd["affected"]["knowledgeBases"]' "$tmp_dir/chunk-preview.json")" -ge 1 ]]

# Embedding contract change: blocked with a migration action.
request POST /api/v1/config/preview 200 "$tmp_dir/embed-preview.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' \
  -d "{\"scope\":\"knowledge_base\",\"knowledgeBaseId\":\"$kb_id\",\"embeddingDimension\":$((current_dimension + 1))}"
[[ "$(json 'd["impact"]' "$tmp_dir/embed-preview.json")" == "rebuild" ]]
[[ "$(json 'd["rebuildRequired"]' "$tmp_dir/embed-preview.json")" == "True" ]]
[[ "$(json 'len(d["blockers"])' "$tmp_dir/embed-preview.json")" -ge 1 ]]
[[ "$(json '"open_embedding_migration" in d["actions"]' "$tmp_dir/embed-preview.json")" == "True" ]]

# Negative validation and permission checks.
request POST /api/v1/config/preview 400 "$tmp_dir/unknown.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"scope":"tenant","retrieval":{"typo_key":1}}'
request POST /api/v1/config/preview 400 "$tmp_dir/range.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"scope":"tenant","retrieval":{"top_k":900}}'
request POST /api/v1/config/preview 403 "$tmp_dir/viewer.json" "${viewer_headers[@]}" \
  -H 'Content-Type: application/json' \
  -d "{\"scope\":\"knowledge_base\",\"knowledgeBaseId\":\"$kb_id\",\"retrieval\":{\"top_k\":6}}"

# Preview is read-only.
request GET /api/v1/tenant/config 200 "$tmp_dir/tenant-after.json" "${dev_headers[@]}"
after_retrieval="$(json 'json.dumps(d["retrieval"]["tenantOverride"], sort_keys=True)' "$tmp_dir/tenant-after.json")"
after_chunking="$(json 'json.dumps(d["chunking"]["tenantOverride"], sort_keys=True)' "$tmp_dir/tenant-after.json")"
[[ "$before_retrieval" == "$after_retrieval" ]]
[[ "$before_chunking" == "$after_chunking" ]]

printf 'B15-c acceptance passed\n'
