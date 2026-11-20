#!/usr/bin/env bash
# D3 acceptance: per-knowledge-base embedding spaces and managed migration.
#
# Requires a development-identity stack built from the current source (new API
# and worker images, migration 0031 applied). The script starts a local
# OpenAI-compatible embedding stub reachable from the containers through
# host.docker.internal, creates a tenant provider with two dimensions, migrates
# one knowledge base through build -> verify -> cutover -> rollback -> cutover
# -> finalize, and checks retrieval before and after every binding change.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

base_url="http://localhost:${API_PORT:-8082}"
base="$base_url/api/v1"
tmp_dir="$(mktemp -d)"
stub_pid=""
run_id="$(date +%s)"
model_a="d3-a-$run_id"
model_b="d3-b-$run_id"
provider_id=""
kb_id=""
stub_port="${D3_STUB_PORT:-18090}"
migration_timeout="${D3_MIGRATION_TIMEOUT:-180}"
dev=(-H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant')
viewer=(-H 'X-Dev-User-ID: viewer-a' -H 'X-Dev-Tenant-ID: dev-tenant')

fail() { printf 'D3 acceptance failed: %s\n' "$*" >&2; exit 1; }
step() { printf '\n== %s\n' "$*"; }

cleanup() {
  if [[ -n "$kb_id" ]]; then
    curl -sS -o /dev/null -X DELETE "$base/knowledge-bases/$kb_id" "${dev[@]}" || true
  fi
  if [[ -n "$provider_id" ]]; then
    curl -sS -o /dev/null -X DELETE "$base/models/providers/$provider_id" "${dev[@]}" || true
  fi
  if [[ -n "$stub_pid" ]]; then
    kill "$stub_pid" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

python_bin="${PYTHON_BIN:-python}"
if ! "$python_bin" -c 'import sys' >/dev/null 2>&1; then
  python_bin=python3
fi
command -v curl >/dev/null 2>&1 || fail "curl is required"

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

wait_document() {
  local document_id="$1" deadline=$((SECONDS + 180)) status=""
  while (( SECONDS < deadline )); do
    request GET "/api/v1/documents/$document_id" 200 "$tmp_dir/document.json" "${dev[@]}"
    status="$(json 'd["document"]["status"]' "$tmp_dir/document.json")"
    case "$status" in
      EVIDENCE_READY) return 0 ;;
      FAILED|CANCELLED) fail "document $document_id reached $status" ;;
    esac
    sleep 2
  done
  fail "document $document_id did not become ready (last status: $status)"
}

search_hits() {
  local kb="$1" output="$2"
  request POST /api/v1/search 200 "$output" "${dev[@]}" -H 'Content-Type: application/json' \
    -d "{\"query\":\"retrieval evidence\",\"knowledgeBaseId\":\"$kb\"}"
  json 'len(d["items"])' "$output"
}

step "preflight: stack, development identity and D3 routes"
curl -fsS "$base_url/readyz" | grep -q '"status":"ok"' || fail "API is not ready; start the development stack and rebuild api/worker from this source"
curl -fsS "$base/me" "${dev[@]}" -o "$tmp_dir/me.json" || fail "development identity is not enabled"
[[ "$(json 'bool(d.get("developmentIdentity"))' "$tmp_dir/me.json")" == "True" ]] || fail "development identity is not enabled"
request POST /api/v1/knowledge-bases 201 "$tmp_dir/kb.json" "${dev[@]}" -H 'Content-Type: application/json' \
  -d "{\"name\":\"D3 Acceptance $run_id\"}"
kb_id="$(json 'd["id"]' "$tmp_dir/kb.json")"
# An unknown target model must be a validation error on D3 code and a missing
# route on a stale image; that distinguishes "not deployed" from "bad target".
code="$(curl -sS -o "$tmp_dir/route.json" -w '%{http_code}' -X POST \
  "$base/knowledge-bases/$kb_id/embedding-migration" "${dev[@]}" -H 'Content-Type: application/json' \
  -d '{"targetModel":"__d3_preflight_missing__"}')"
[[ "$code" == "400" ]] || fail "D3 routes are not deployed (unknown target returned $code); rebuild api and apply migration 0031"

step "start embedding stub on :$stub_port"
cat >"$tmp_dir/embedding_stub.py" <<'PY'
import json, sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1])

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def _json(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.rstrip("/").endswith("/models"):
            self._json(200, {"object": "list", "data": [{"id": "d3-embed-a"}, {"id": "d3-embed-b"}]})
        else:
            self._json(404, {"error": {"message": "not found"}})

    def do_POST(self):
        if not self.path.rstrip("/").endswith("/embeddings"):
            self._json(404, {"error": {"message": "not found"}})
            return
        length = int(self.headers.get("Content-Length", "0"))
        payload = json.loads(self.rfile.read(length) or b"{}")
        model = str(payload.get("model", ""))
        dimension = 8 if "-a" in model else 16 if "-b" in model else 4
        inputs = payload.get("input", [])
        if isinstance(inputs, str):
            inputs = [inputs]
        data = [{"object": "embedding", "index": i, "embedding": [1.0] * dimension} for i, _ in enumerate(inputs)]
        self._json(200, {"object": "list", "model": model, "data": data})

ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
PY
"$python_bin" -u "$tmp_dir/embedding_stub.py" "$stub_port" >"$tmp_dir/stub.log" 2>&1 &
stub_pid=$!
for _ in $(seq 1 20); do
  if curl -fsS "http://localhost:$stub_port/v1/models" >/dev/null 2>&1; then break; fi
  sleep 0.5
done
curl -fsS "http://localhost:$stub_port/v1/models" >/dev/null || fail "embedding stub did not start"

step "register a tenant embedding provider with 8 and 16 dimensions"
cat >"$tmp_dir/provider.json" <<EOF
{"name":"d3-acceptance-$run_id","kind":"openai_compatible","base_url":"http://host.docker.internal:$stub_port/v1","models":[
  {"model":"$model_a","capability":"embedding","dimension":8},
  {"model":"$model_b","capability":"embedding","dimension":16}
]}
EOF
request POST /api/v1/models/providers 201 "$tmp_dir/provider-response.json" "${dev[@]}" \
  -H 'Content-Type: application/json' --data-binary "@$tmp_dir/provider.json"
provider_id="$(json 'd["id"]' "$tmp_dir/provider-response.json")"

step "upload a document and record the source-space baseline"
request POST "/api/v1/knowledge-bases/$kb_id/documents" 202 "$tmp_dir/upload.json" "${dev[@]}" \
  -H "Idempotency-Key: d3-$run_id" -F "file=@examples/demo/retrieval-handbook.md;type=text/markdown"
document_id="$(json 'd["document"]["id"]' "$tmp_dir/upload.json")"
wait_document "$document_id"
request GET "/api/v1/knowledge-bases/$kb_id/configuration" 200 "$tmp_dir/config-before.json" "${dev[@]}"
source_model="$(json 'd["modelBinding"]["embedding"]["model"] or d["readOnly"]["embedding"]["model"]' "$tmp_dir/config-before.json")"
source_dimension="$(json 'd["modelBinding"]["embeddingDimension"] or d["readOnly"]["embedding"]["dimension"]' "$tmp_dir/config-before.json")"
[[ "$(search_hits "$kb_id" "$tmp_dir/search-before.json")" -ge 1 ]] || fail "no evidence before migration"
printf 'source space: %s/%s\n' "$source_model" "$source_dimension"

step "start migration to $model_b/16"
request POST "/api/v1/knowledge-bases/$kb_id/embedding-migration" 202 "$tmp_dir/migration.json" "${dev[@]}" \
  -H 'Content-Type: application/json' -d "{\"targetModel\":\"$model_b\"}"
migration_id="$(json 'd["id"]' "$tmp_dir/migration.json")"
[[ "$(json 'd["targetDimension"]' "$tmp_dir/migration.json")" == "16" ]] || fail "target dimension was not resolved from the catalog"

# A second start must conflict while the first migration is active.
code="$(curl -sS -o "$tmp_dir/conflict.json" -w '%{http_code}' -X POST \
  "$base/knowledge-bases/$kb_id/embedding-migration" "${dev[@]}" -H 'Content-Type: application/json' \
  -d "{\"targetModel\":\"$model_a\"}")"
[[ "$code" == "409" ]] || fail "a concurrent migration must return 409, got $code"

# Viewers cannot start a migration.
code="$(curl -sS -o "$tmp_dir/viewer.json" -w '%{http_code}' -X POST \
  "$base/knowledge-bases/$kb_id/embedding-migration" "${viewer[@]}" -H 'Content-Type: application/json' \
  -d "{\"targetModel\":\"$model_a\"}")"
[[ "$code" == "403" ]] || fail "viewer migration start must be 403, got $code"

step "wait for verification (timeout ${migration_timeout}s)"
deadline=$((SECONDS + migration_timeout))
status=""
while (( SECONDS < deadline )); do
  request GET "/api/v1/knowledge-bases/$kb_id/embedding-migration" 200 "$tmp_dir/migration-status.json" "${dev[@]}"
  status="$(json 'd["status"]' "$tmp_dir/migration-status.json")"
  case "$status" in
    ready) break ;;
    failed) fail "migration failed: $(json 'd.get("errorMessage","")' "$tmp_dir/migration-status.json")" ;;
  esac
  sleep 2
done
[[ "$status" == "ready" ]] || fail "migration did not reach ready (last status: $status)"
[[ "$(json 'd["doneChunks"]' "$tmp_dir/migration-status.json")" -ge 1 ]] || fail "migration reported no embedded chunks"

step "cut over, verify retrieval, roll back, verify again"
request POST "/api/v1/knowledge-bases/$kb_id/embedding-migration/$migration_id/cutover" 200 "$tmp_dir/cutover.json" "${dev[@]}" -H 'Content-Type: application/json' -d '{}'
[[ "$(json 'd["status"]' "$tmp_dir/cutover.json")" == "cutover" ]] || fail "cutover did not change the status"
request GET "/api/v1/knowledge-bases/$kb_id/configuration" 200 "$tmp_dir/config-cutover.json" "${dev[@]}"
[[ "$(json 'd["modelBinding"]["embedding"]["model"]' "$tmp_dir/config-cutover.json")" == "$model_b" ]] || fail "binding did not move to the target model"
[[ "$(json 'd["modelBinding"]["embeddingDimension"]' "$tmp_dir/config-cutover.json")" == "16" ]] || fail "binding dimension is not 16"
[[ "$(search_hits "$kb_id" "$tmp_dir/search-cutover.json")" -ge 1 ]] || fail "no evidence after cutover"

request POST "/api/v1/knowledge-bases/$kb_id/embedding-migration/$migration_id/rollback" 200 "$tmp_dir/rollback.json" "${dev[@]}" -H 'Content-Type: application/json' -d '{}'
[[ "$(json 'd["status"]' "$tmp_dir/rollback.json")" == "rolled_back" ]] || fail "rollback did not change the status"
request GET "/api/v1/knowledge-bases/$kb_id/configuration" 200 "$tmp_dir/config-rollback.json" "${dev[@]}"
[[ "$(json 'd["modelBinding"]["embedding"]["model"]' "$tmp_dir/config-rollback.json")" == "$source_model" ]] || fail "rollback did not restore the source model"
[[ "$(search_hits "$kb_id" "$tmp_dir/search-rollback.json")" -ge 1 ]] || fail "no evidence after rollback"

step "run a second migration, cut over and finalize the rollback window"
request POST "/api/v1/knowledge-bases/$kb_id/embedding-migration" 202 "$tmp_dir/migration2.json" "${dev[@]}" \
  -H 'Content-Type: application/json' -d "{\"targetModel\":\"$model_b\"}"
migration2_id="$(json 'd["id"]' "$tmp_dir/migration2.json")"
deadline=$((SECONDS + migration_timeout))
status=""
while (( SECONDS < deadline )); do
  request GET "/api/v1/knowledge-bases/$kb_id/embedding-migration" 200 "$tmp_dir/migration2-status.json" "${dev[@]}"
  status="$(json 'd["status"]' "$tmp_dir/migration2-status.json")"
  case "$status" in
    ready) break ;;
    failed) fail "second migration failed: $(json 'd.get("errorMessage","")' "$tmp_dir/migration2-status.json")" ;;
  esac
  sleep 2
done
[[ "$status" == "ready" ]] || fail "second migration did not reach ready (last status: $status)"
request POST "/api/v1/knowledge-bases/$kb_id/embedding-migration/$migration2_id/cutover" 200 "$tmp_dir/cutover2.json" "${dev[@]}" -H 'Content-Type: application/json' -d '{}'
request POST "/api/v1/knowledge-bases/$kb_id/embedding-migration/$migration2_id/finalize" 200 "$tmp_dir/finalize.json" "${dev[@]}" -H 'Content-Type: application/json' -d '{}'
[[ "$(json 'd["status"]' "$tmp_dir/finalize.json")" == "finalized" ]] || fail "finalize did not change the status"
[[ "$(search_hits "$kb_id" "$tmp_dir/search-final.json")" -ge 1 ]] || fail "no evidence after finalize"

printf '\nD3 acceptance passed: %s -> %s/16 -> rollback -> re-migrate -> finalize\n' "$source_model" "$model_b"
