#!/usr/bin/env bash
# End-to-end self-check for the quickstart stack: health, create KB, upload,
# parse to EVIDENCE_READY, hybrid search, and a streamed answer with citations.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

compose=(docker compose --env-file .env \
  -f deploy/docker-compose/docker-compose.yml \
  -f deploy/docker-compose/quickstart.compose.yml)

if ! "${compose[@]}" ps api >/dev/null 2>&1; then
  echo "the quickstart stack is not running; run ./scripts/quickstart.sh first" >&2
  exit 1
fi

api_port="$("${compose[@]}" port api 8080 2>/dev/null | awk -F: '{print $NF}' || true)"
if [[ -z "$api_port" ]]; then
  echo "could not determine the API host port; is the api service running?" >&2
  exit 1
fi
base_url="http://localhost:${api_port}"

PYTHON="${PYTHON:-python3}"
if ! command -v "$PYTHON" >/dev/null 2>&1; then
  PYTHON=python
fi
command -v "$PYTHON" >/dev/null 2>&1 || {
  echo "python is required for the self-check" >&2
  exit 1
}

tmp_dir="$(mktemp -d)"
kb_id=""
trap 'if [[ -n "$kb_id" ]]; then curl -sS -o /dev/null -X DELETE "$base_url/api/v1/knowledge-bases/$kb_id" -H "X-Dev-User-ID: dev-user" -H "X-Dev-Tenant-ID: dev-tenant" || true; fi; rm -rf "$tmp_dir"' EXIT

dev_headers=(-H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant')

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
  "$PYTHON" -c "import json,sys; print(json.load(open(sys.argv[1]))$1)" "$2"
}

curl -fsS "$base_url/readyz" | grep -q '"status":"ok"'

kb_name="quickstart-check-$(date +%s)"
request POST /api/v1/knowledge-bases 201 "$tmp_dir/kb.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"$kb_name\",\"description\":\"quickstart self-check\"}"
kb_id="$(json '["id"]' "$tmp_dir/kb.json")"

upload_key="quickstart-check-$(date +%s)"
request POST "/api/v1/knowledge-bases/$kb_id/documents" 202 "$tmp_dir/upload.json" "${dev_headers[@]}" \
  -H "Idempotency-Key: $upload_key" \
  -F "file=@examples/demo/retrieval-handbook.md;type=text/markdown"
document_id="$(json '["document"]["id"]' "$tmp_dir/upload.json")"

pipeline_status=""
for _ in $(seq 1 90); do
  request GET "/api/v1/documents/$document_id" 200 "$tmp_dir/status.json" "${dev_headers[@]}"
  pipeline_status="$(json '["pipeline"]["status"]' "$tmp_dir/status.json")"
  [[ "$pipeline_status" == "EVIDENCE_READY" ]] && break
  if [[ "$pipeline_status" == "FAILED" ]]; then
    cat "$tmp_dir/status.json" >&2
    exit 1
  fi
  sleep 1
done
if [[ "$pipeline_status" != "EVIDENCE_READY" ]]; then
  echo "document did not become EVIDENCE_READY (last status: $pipeline_status)" >&2
  exit 1
fi

request POST /api/v1/search 200 "$tmp_dir/search.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' \
  -d "{\"query\":\"how are keyword and vector results combined\",\"knowledgeBaseId\":\"$kb_id\"}"
"$PYTHON" - "$tmp_dir/search.json" <<'PY'
import json
import sys

body = json.load(open(sys.argv[1], encoding="utf-8"))
assert body["schemaVersion"] == "EvidenceSetV1", body
assert body["items"], "hybrid search returned no evidence"
assert body["items"][0]["knowledgeBaseId"], body["items"][0]
assert body["plan"]["intent"] == "fact", body["plan"]
assert body["plan"]["vectorEnabled"] is True, body["plan"]
assert body["plan"]["keywordEnabled"] is True, body["plan"]
PY

request POST /api/v1/sessions 201 "$tmp_dir/session.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' \
  -d "{\"knowledge_base_id\":\"$kb_id\"}"
conversation_id="$(json '["id"]' "$tmp_dir/session.json")"
request_id="quickstart-check-$(date +%s)"

ask_code="$(curl -sS -N --max-time 180 -o "$tmp_dir/stream.txt" -w '%{http_code}' \
  -X POST "$base_url/api/v1/sessions/$conversation_id/messages" \
  "${dev_headers[@]}" -H 'Content-Type: application/json' -H 'Accept: text/event-stream' \
  -d "{\"query\":\"how does hybrid recall combine keyword and vector results\",\"requestId\":\"$request_id\"}")"
if [[ "$ask_code" != "200" ]]; then
  echo "stream request returned $ask_code" >&2
  cat "$tmp_dir/stream.txt" >&2
  exit 1
fi

"$PYTHON" - "$tmp_dir/stream.txt" <<'PY'
import json
import re
import sys

raw = open(sys.argv[1], encoding="utf-8").read()
blocks = [block for block in raw.split("\n\n") if block.strip() and not block.startswith(":")]
event_types = []
answer = None
for block in blocks:
    event_type = re.search(r"^event: (.+)$", block, re.M).group(1)
    payload = json.loads(re.search(r"^data: (.+)$", block, re.M).group(1))
    event_types.append(event_type)
    if event_type == "answer":
        answer = payload

assert "references" in event_types, event_types
assert "answer" in event_types, event_types
assert event_types[-1] == "complete", event_types
assert answer is not None
assert answer["data"]["status"] == "COMPLETED", answer
assert answer["data"]["citations"], "the answer must carry citations"
assert answer["data"]["validation"]["valid"] is True, answer["data"]["validation"]
PY

echo "quickstart self-check passed"
echo "  knowledge base: $kb_id"
echo "  document:       $document_id"
echo "  conversation:   $conversation_id"
