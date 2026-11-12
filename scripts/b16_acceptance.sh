#!/usr/bin/env bash
# B16 acceptance: create a scoped API key, upload the demo document, run the
# live MCP client (search -> ask -> citation -> out-of-scope negative), then run
# the SDK tests. The quickstart stack must be running.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() { printf 'B16 acceptance failed: %s\n' "$*" >&2; exit 1; }
step() { printf '\n== %s\n' "$*"; }

api_port="${API_PORT:-8082}"
base="http://localhost:${api_port}/api/v1"
root_url="http://localhost:${api_port}"
dev=(-H "X-Dev-User-ID: ${DEV_USER_ID:-dev-user}" -H "X-Dev-Tenant-ID: ${DEV_TENANT_ID:-dev-tenant}")
kb="${B16_KB_ID:-}"
created_kb=0
tmp_dir="$(mktemp -d)"
key_id=""
document_id=""

cleanup() {
  if [[ -n "$key_id" ]]; then
    curl -sS -o /dev/null -X DELETE "$base/api-keys/$key_id" "${dev[@]}" || true
  fi
  if [[ -n "$document_id" ]]; then
    curl -sS -o /dev/null -X DELETE "$base/documents/$document_id" "${dev[@]}" || true
  fi
  if [[ "$created_kb" == "1" && -n "$kb" ]]; then
    curl -sS -o /dev/null -X DELETE "$base/knowledge-bases/$kb" "${dev[@]}" || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

command -v curl >/dev/null 2>&1 || fail "curl is required"
PYTHON="${PYTHON:-python3}"
command -v "$PYTHON" >/dev/null 2>&1 || fail "python3 is required"

step "preflight: quickstart stack and development identity"
curl -fsS "$root_url/readyz" | grep -q '"status":"ok"' || fail "API is not ready at $root_url/readyz (run ./scripts/quickstart.sh)"
curl -fsS "$base/me" "${dev[@]}" > "$tmp_dir/me.json" || fail "development identity is not enabled"
"$PYTHON" - "$tmp_dir/me.json" <<'PY' || fail "development identity is not enabled"
import json, sys
data = json.load(open(sys.argv[1]))
raise SystemExit(0 if data.get("developmentIdentity") else 1)
PY

step "create a temporary knowledge base (bound to the running embedding model)"
if [[ -z "$kb" ]]; then
  curl -fsS -X POST "$base/knowledge-bases" "${dev[@]}" -H 'Content-Type: application/json' \
    -d "{\"name\":\"b16-$(date +%s)\"}" > "$tmp_dir/kb.json"
  kb="$("$PYTHON" -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$tmp_dir/kb.json")"
  [[ -n "$kb" ]] || fail "knowledge base creation returned no id"
  created_kb=1
fi
printf 'knowledge base: %s\n' "$kb"

step "create an API key scoped to $kb"
payload="$("$PYTHON" - "$kb" <<'PY'
import json, sys
print(json.dumps({
    "name": "b16-acceptance",
    "capabilities": ["knowledge_base:read", "chat"],
    "knowledgeBaseIds": [sys.argv[1]],
}))
PY
)"
curl -fsS -X POST "$base/api-keys" "${dev[@]}" -H 'Content-Type: application/json' -d "$payload" > "$tmp_dir/key.json"
token="$("$PYTHON" -c 'import json,sys; print(json.load(open(sys.argv[1]))["token"])' "$tmp_dir/key.json")"
key_id="$("$PYTHON" -c 'import json,sys; print(json.load(open(sys.argv[1]))["apiKey"]["id"])' "$tmp_dir/key.json")"
[[ -n "$token" && -n "$key_id" ]] || fail "API key creation returned no token"

step "upload the demo document and wait for EVIDENCE_READY"
curl -fsS -X POST "$base/knowledge-bases/$kb/documents" "${dev[@]}" \
  -H "Idempotency-Key: b16-$(date +%s)" \
  -F "file=@examples/demo/retrieval-handbook.md;type=text/markdown" > "$tmp_dir/upload.json"
document_id="$("$PYTHON" -c 'import json,sys; print(json.load(open(sys.argv[1]))["document"]["id"])' "$tmp_dir/upload.json")"
[[ -n "$document_id" ]] || fail "upload returned no document id"
status=""
for _ in $(seq 1 60); do
  curl -fsS "$base/documents/$document_id" "${dev[@]}" > "$tmp_dir/status.json"
  status="$("$PYTHON" -c 'import json,sys; print(json.load(open(sys.argv[1]))["pipeline"]["status"])' "$tmp_dir/status.json")"
  [[ "$status" == "EVIDENCE_READY" ]] && break
  sleep 2
done
[[ "$status" == "EVIDENCE_READY" ]] || fail "document did not reach EVIDENCE_READY (last status: $status)"

step "run the live MCP client: list docs -> detail -> content -> search -> ask -> citation -> forbidden KB"
RAG_API_URL="http://host.docker.internal:${api_port}/api/v1" \
RAG_API_KEY="$token" \
RAG_KNOWLEDGE_BASE_ID="$kb" \
RAG_FORBIDDEN_KB="kb-forbidden-$(date +%s)" \
make mcp-e2e

step "run SDK tests"
[[ -d sdk/typescript/node_modules ]] || fail "run npm install in sdk/typescript first"
(cd sdk/typescript && npm test && npm run typecheck)
if "$PYTHON" -c 'import httpx, pytest' >/dev/null 2>&1; then
  (cd sdk/python && PYTHONPATH=. "$PYTHON" -m pytest -q)
else
  printf 'python SDK tests skipped: install sdk/python/requirements-dev.txt\n'
fi

if [[ "${B16_RUN_EXAMPLES:-0}" == "1" ]]; then
  step "run SDK examples"
  (
    cd sdk/typescript
    RAG_API_URL="$base" RAG_API_KEY="$token" RAG_KNOWLEDGE_BASE_ID="$kb" npm run example
  )
  if "$PYTHON" -c 'import httpx' >/dev/null 2>&1; then
    RAG_API_URL="$base" RAG_API_KEY="$token" RAG_KNOWLEDGE_BASE_ID="$kb" \
      PYTHONPATH=sdk/python "$PYTHON" sdk/python/examples/search_ask_cite.py
  fi
fi

printf '\nB16 acceptance passed: API key revoked'
if [[ "$created_kb" == "1" ]]; then
  printf ', temporary knowledge base deleted\n'
else
  printf ', existing knowledge base preserved\n'
fi