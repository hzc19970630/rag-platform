#!/usr/bin/env bash
# B15-a acceptance: one business-configuration schema, tenant overrides,
# knowledge-base field sources, configuration health and connection tests.
#
# Runs against the development compose stack. It mutates the dev and other
# tenants' configuration and restores the empty override on exit.
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
trap 'cleanup' EXIT

dev_headers=(-H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant')
other_headers=(-H 'X-Dev-User-ID: other-user' -H 'X-Dev-Tenant-ID: other-tenant')
viewer_headers=(-H 'X-Dev-User-ID: viewer-a' -H 'X-Dev-Tenant-ID: dev-tenant')

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

python_bin="${PYTHON_BIN:-python}"
if ! "$python_bin" -c 'import sys' >/dev/null 2>&1; then
  python_bin=python3
fi

json() {
  "$python_bin" -c "import json,sys; d=json.load(open(sys.argv[1], encoding='utf-8')); print(eval(sys.argv[2], {'d': d}))" "$2" "$1"
}

psql_value() {
  "${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "$1"
}

cleanup() {
  curl -sS -o /dev/null -X PUT "$base_url/api/v1/tenant/config" \
    "${dev_headers[@]}" -H 'Content-Type: application/json' -d '{"retrieval":{},"chunking":{}}' || true
  curl -sS -o /dev/null -X PUT "$base_url/api/v1/knowledge-bases/kb-dev/retrieval-config" \
    "${dev_headers[@]}" -H 'Content-Type: application/json' -d '{}' || true
  rm -rf "$tmp_dir"
}

curl -fsS "$base_url/readyz" | grep -q '"status":"ok"'

# ---------------------------------------------------------------------------
# Schema and tenant override.
# ---------------------------------------------------------------------------
request GET /api/v1/tenant/config 200 "$tmp_dir/config-initial.json" "${dev_headers[@]}"
[[ "$(json 'len(d["retrieval"]["schema"])' "$tmp_dir/config-initial.json")" == "16" ]]
[[ "$(json 'len(d["chunking"]["schema"])' "$tmp_dir/config-initial.json")" == "4" ]]
[[ "$(json 'd["retrieval"]["fields"]["top_k"]["applyMode"]' "$tmp_dir/config-initial.json")" == "next_retrieval" ]]
[[ "$(json 'd["chunking"]["fields"]["size"]["applyMode"]' "$tmp_dir/config-initial.json")" == "next_ingest" ]]

request PUT /api/v1/tenant/config 200 "$tmp_dir/config-saved.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"retrieval":{"top_k":6},"chunking":{"size":640}}'
[[ "$(json 'd["retrieval"]["fields"]["top_k"]["source"]' "$tmp_dir/config-saved.json")" == "tenant" ]]
[[ "$(json 'd["retrieval"]["effective"]["top_k"]' "$tmp_dir/config-saved.json")" == "6" ]]
[[ "$(json 'd["chunking"]["effective"]["size"]' "$tmp_dir/config-saved.json")" == "640" ]]
[[ "$(psql_value "SELECT (retrieval_config->>'top_k') FROM tenants WHERE id = 'dev-tenant';")" == "6" ]]
[[ "$(psql_value "SELECT (chunking_config->>'size') FROM tenants WHERE id = 'dev-tenant';")" == "640" ]]
[[ "$(psql_value "SELECT count(*) FROM audit_logs WHERE resource_type = 'tenant_config' AND resource_id = 'dev-tenant' AND reason LIKE '%retrieval_after%';")" -ge 1 ]]

request PUT /api/v1/tenant/config 400 "$tmp_dir/config-bad-range.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"retrieval":{"top_k":900}}'
request PUT /api/v1/tenant/config 400 "$tmp_dir/config-unknown.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"retrieval":{"typo_key":1}}'
request PUT /api/v1/tenant/config 400 "$tmp_dir/config-bad-chunk.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"chunking":{"size":100,"overlap":100}}'

# ---------------------------------------------------------------------------
# Cross-tenant isolation and operator-only access.
# ---------------------------------------------------------------------------
request GET /api/v1/tenant/config 200 "$tmp_dir/config-other.json" "${other_headers[@]}"
[[ "$(json 'd["retrieval"]["effective"]["top_k"]' "$tmp_dir/config-other.json")" != "6" ]]
[[ "$(json 'd["retrieval"]["fields"]["top_k"]["source"]' "$tmp_dir/config-other.json")" != "tenant" ]]
[[ "$(psql_value "SELECT retrieval_config::text FROM tenants WHERE id = 'other-tenant';")" == "{}" ]]
request GET /api/v1/tenant/config 403 "$tmp_dir/config-viewer.json" "${viewer_headers[@]}"

# ---------------------------------------------------------------------------
# Knowledge-base configuration reports the tenant tier and per-field sources.
# ---------------------------------------------------------------------------
request GET /api/v1/knowledge-bases/kb-dev/configuration 200 "$tmp_dir/kb-config-tenant.json" "${dev_headers[@]}"
[[ "$(json 'd["retrieval"]["fields"]["top_k"]["source"]' "$tmp_dir/kb-config-tenant.json")" == "tenant" ]]
[[ "$(json 'd["retrieval"]["fields"]["top_k"]["value"]' "$tmp_dir/kb-config-tenant.json")" == "6" ]]
[[ "$(json 'd["retrieval"]["effective"]["top_k"]' "$tmp_dir/kb-config-tenant.json")" == "6" ]]

request PUT /api/v1/knowledge-bases/kb-dev/retrieval-config 200 "$tmp_dir/kb-config-saved.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"top_k":4}'
[[ "$(json 'd["retrieval"]["fields"]["top_k"]["source"]' "$tmp_dir/kb-config-saved.json")" == "knowledge_base" ]]
[[ "$(json 'd["retrieval"]["effective"]["top_k"]' "$tmp_dir/kb-config-saved.json")" == "4" ]]

request PUT /api/v1/knowledge-bases/kb-dev/retrieval-config 400 "$tmp_dir/kb-config-unknown.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"typo_key":1}'
request PUT /api/v1/knowledge-bases/kb-dev/retrieval-config 400 "$tmp_dir/kb-config-range.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"vector_threshold":1.5}'
request PUT /api/v1/knowledge-bases/kb-dev/retrieval-config 200 "$tmp_dir/kb-config-cleared.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{}'
[[ "$(json 'd["retrieval"]["fields"]["top_k"]["source"]' "$tmp_dir/kb-config-cleared.json")" == "tenant" ]]

# ---------------------------------------------------------------------------
# Configuration health and connection tests.
# ---------------------------------------------------------------------------
request GET /api/v1/tenant/config/health 200 "$tmp_dir/health.json" "${dev_headers[@]}"
for check in model.chat model.embedding model.rerank index.valid embedding.dimension rls.mode backup.age license.expiry drift.retrieval_file drift.stored_config; do
  status="$("$python_bin" -c "import json,sys; d=json.load(open(sys.argv[1], encoding='utf-8')); print(next((c['status'] for c in d['checks'] if c['id']==sys.argv[2]), 'missing'))" "$tmp_dir/health.json" "$check")"
  if [[ "$status" == "missing" ]]; then
    printf 'configuration health is missing check %s\n' "$check" >&2
    exit 1
  fi
  case "$check" in
    model.chat|model.embedding|model.rerank|index.valid|embedding.dimension|drift.retrieval_file|drift.stored_config)
      if [[ "$status" == "error" || "$status" == "unknown" ]]; then
        printf 'configuration health check %s is %s: ' "$check" "$status" >&2
        cat "$tmp_dir/health.json" >&2
        exit 1
      fi
      ;;
  esac
done

for capability in chat embedding rerank; do
  request POST /api/v1/tenant/config/test-connection 200 "$tmp_dir/test-$capability.json" "${dev_headers[@]}" \
    -H 'Content-Type: application/json' -d "{\"capability\":\"$capability\"}"
  [[ "$(json 'd["healthy"]' "$tmp_dir/test-$capability.json")" == "True" ]]
done
[[ "$(json 'd["dimension"]' "$tmp_dir/test-embedding.json")" -gt 0 ]]

printf 'B15-a acceptance passed\n'
