#!/usr/bin/env bash
# Original M13 A3 (delivered) acceptance: async answers survive a worker restart.
#   1. normal async answer: the stream ends with `complete` and answer_jobs is
#      COMPLETED with a contiguous event sequence;
#   2. worker-down drill: with the worker stopped, the API accepts the question,
#      the client tails; after the worker restarts the stream still reaches a
#      terminal event (answer or explicit error).
# The stack must already be up (make dev-up) and built with the async code.
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
worker_was_running=0
cleanup() {
  if [[ "$worker_was_running" == "1" ]]; then
    "${compose[@]}" start worker >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

psql_value() {
  "${compose[@]}" exec -T postgres psql -U "${POSTGRES_USER:-rag_platform}" -d "${POSTGRES_DB:-rag_platform}" -Atc "$1"
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

post_answer() {
  local conversation="$1" request_id="$2" output="$3"
  curl -sS -N --max-time 240 -o "$output" \
    -H 'Content-Type: application/json' \
    -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
    -d "{\"query\":\"What does the embedding dimension contract say?\",\"requestId\":\"$request_id\"}" \
    "$base_url/api/v1/sessions/$conversation/messages"
}

assert_terminal_stream() {
  local output="$1" label="$2"
  if ! grep -q '^event: complete$' "$output"; then
    if ! grep -q '^event: error$' "$output"; then
      printf '%s did not reach a terminal SSE event:\n' "$label" >&2
      cat "$output" >&2
      exit 1
    fi
  fi
}

curl -fsS "$base_url/readyz" | grep -q '"status":"ok"'

step() { printf '\n== %s\n' "$1"; }

step "create conversation"
request POST /api/v1/sessions 201 "$tmp_dir/conversation.json" \
  -H 'Content-Type: application/json' \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -d '{"knowledge_base_id":"kb-dev","title":"M13 async acceptance"}'
conversation_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$tmp_dir/conversation.json")"

step "normal async answer"
normal_request_id="async-$(date +%s)-a"
post_answer "$conversation_id" "$normal_request_id" "$tmp_dir/stream-a.log"
assert_terminal_stream "$tmp_dir/stream-a.log" "normal answer"
grep -q '^event: complete$' "$tmp_dir/stream-a.log" || {
  cat "$tmp_dir/stream-a.log" >&2
  echo "normal async answer ended without a completion event" >&2
  exit 1
}
status="$(psql_value "SELECT status FROM answer_jobs WHERE tenant_id = 'dev-tenant' AND request_id = '$normal_request_id';")"
[[ "$status" == "COMPLETED" ]] || {
  echo "answer_jobs status = '$status', want COMPLETED" >&2
  exit 1
}
consistent="$(psql_value "SELECT count(*) = max(sequence) FROM stream_events WHERE tenant_id = 'dev-tenant' AND request_id = '$normal_request_id';")"
[[ "$consistent" == "t" ]] || {
  echo "stream events for $normal_request_id are not contiguous" >&2
  exit 1
}

step "worker-down drill"
if "${compose[@]}" ps --format '{{.Service}} {{.State}}' worker | grep -q 'running'; then
  worker_was_running=1
  "${compose[@]}" stop worker >/dev/null
fi
request_id="async-$(date +%s)-b"
post_answer "$conversation_id" "$request_id" "$tmp_dir/stream-b.log" &
curl_pid=$!
# Wait until the API accepted and persisted the job, then bring the worker back.
for _ in $(seq 1 30); do
  queued="$("${compose[@]}" exec -T postgres psql -U "${POSTGRES_USER:-rag_platform}" -d "${POSTGRES_DB:-rag_platform}" -Atc "SELECT count(*) FROM answer_jobs WHERE tenant_id = 'dev-tenant' AND request_id = '$request_id';" 2>/dev/null || echo 0)"
  [[ "$queued" == "1" ]] && break
  sleep 1
done
"${compose[@]}" start worker >/dev/null
wait "$curl_pid"
assert_terminal_stream "$tmp_dir/stream-b.log" "worker-down answer"
if ! grep -q '^event: complete$' "$tmp_dir/stream-b.log"; then
  echo "worker-down drill ended with an explicit error (accepted by the plan), stream follows:" >&2
  cat "$tmp_dir/stream-b.log" >&2
fi
status="$(psql_value "SELECT status FROM answer_jobs WHERE tenant_id = 'dev-tenant' AND request_id = '$request_id';")"
case "$status" in
  COMPLETED | FAILED) ;;
  *)
    echo "worker-down job status = '$status', want COMPLETED or FAILED" >&2
    exit 1
    ;;
esac

printf '\nM13 async acceptance passed: conversation=%s normal=%s worker-down=%s status=%s\n' "$conversation_id" "$normal_request_id" "$request_id" "$status"
