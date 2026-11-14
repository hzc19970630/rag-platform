#!/usr/bin/env bash
# B23 acceptance: generic enterprise preset pack + validate/export/diff tooling.
# Requires a development-identity stack built from the current source.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

base_url="http://localhost:${API_PORT:-8082}"
tmp_dir="$(mktemp -d)"
kb_id=""
dev=(-H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant')
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)

fail() { printf 'B23 acceptance failed: %s\n' "$*" >&2; exit 1; }
step() { printf '\n== %s\n' "$*"; }

cleanup() {
  if [[ -n "$kb_id" ]]; then
    curl -sS -o /dev/null -X DELETE "$base_url/api/v1/knowledge-bases/$kb_id" "${dev[@]}" || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

python_bin="${PYTHON_BIN:-python}"
if ! "$python_bin" -c 'import sys' >/dev/null 2>&1; then python_bin=python3; fi

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
json() { "$python_bin" -c "import json,sys; d=json.load(open(sys.argv[1], encoding='utf-8')); print(eval(sys.argv[2], {'d': d, 'json': json}))" "$2" "$1"; }

curl -fsS "$base_url/readyz" | grep -q '"status":"ok"'

step "CLI: list, validate and diff the built-in pack"
"${compose[@]}" run --rm --no-deps --entrypoint preset api list > "$tmp_dir/presets.txt"
grep -q '^general-enterprise@1.0.0' "$tmp_dir/presets.txt"
"${compose[@]}" run --rm --no-deps --entrypoint preset api validate general-enterprise@1.0.0 \
  | grep -q 'valid general-enterprise@1.0.0'
"${compose[@]}" run --rm --no-deps --entrypoint sh api -c \
  'preset export general-enterprise@1.0.0 > /tmp/a.json; sed "s/\"top_k\": 8/\"top_k\": 10/" /tmp/a.json > /tmp/b.json; preset diff /tmp/a.json /tmp/b.json' \
  | grep -q 'retrieval.top_k'

step "API: list presets"
request GET /api/v1/presets 200 "$tmp_dir/presets.json" "${dev[@]}"
[[ "$(json 'any(p["name"] == "general-enterprise" for p in d["items"])' "$tmp_dir/presets.json")" == "True" ]]

step "create a knowledge base from the generic pack"
request POST /api/v1/knowledge-bases 201 "$tmp_dir/kb.json" "${dev[@]}" -H 'Content-Type: application/json' \
  -d "{\"name\":\"B23 $(date +%s)\",\"presetName\":\"general-enterprise\",\"presetVersion\":\"1.0.0\"}"
kb_id="$(json 'd["id"]' "$tmp_dir/kb.json")"
[[ "$(json 'd["presetName"]' "$tmp_dir/kb.json")" == "general-enterprise" ]]
request GET "/api/v1/knowledge-bases/$kb_id/configuration" 200 "$tmp_dir/config.json" "${dev[@]}"
[[ "$(json 'd["retrieval"]["effective"]["top_k"]' "$tmp_dir/config.json")" == "8" ]]
[[ "$(json 'd["retrieval"]["fields"]["top_k"]["source"]' "$tmp_dir/config.json")" == "knowledge_base" ]]

step "upload a document and verify preset chunking is applied"
request POST "/api/v1/knowledge-bases/$kb_id/documents" 202 "$tmp_dir/upload.json" "${dev[@]}" \
  -H "Idempotency-Key: b23-$(date +%s)" -F "file=@examples/demo/retrieval-handbook.md;type=text/markdown"
document_id="$(json 'd["document"]["id"]' "$tmp_dir/upload.json")"
status=""
for _ in $(seq 1 60); do
  request GET "/api/v1/documents/$document_id" 200 "$tmp_dir/document.json" "${dev[@]}"
  status="$(json 'd["document"]["status"]' "$tmp_dir/document.json")"
  [[ "$status" == "EVIDENCE_READY" ]] && break
  [[ "$status" == "FAILED" ]] && fail "document failed"
  sleep 2
done
[[ "$status" == "EVIDENCE_READY" ]] || fail "document did not become ready (last: $status)"
request GET "/api/v1/documents/$document_id/chunks?limit=50" 200 "$tmp_dir/chunks.json" "${dev[@]}"
[[ "$(json 'len(d["items"])' "$tmp_dir/chunks.json")" -ge 1 ]]
[[ "$(json 'max(len(c["content"]) for c in d["items"]) <= 800' "$tmp_dir/chunks.json")" == "True" ]]

step "negative: unknown preset is rejected"
request POST /api/v1/knowledge-bases 400 "$tmp_dir/bad.json" "${dev[@]}" -H 'Content-Type: application/json' \
  -d '{"name":"B23 bad","presetName":"missing-pack"}'
message="$(json 'd["message"]' "$tmp_dir/bad.json")"
[[ "$message" == *"unknown preset"* ]] || fail "unexpected error: $message"

printf '\nB23 acceptance passed: general-enterprise@1.0.0\n'
