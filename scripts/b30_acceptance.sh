#!/usr/bin/env bash
# B30 acceptance: resumable upload sessions, both transports, idempotency,
# abort cleanup, batch (N sessions), negative authorization and the legacy
# single-shot endpoint. The transport under test is whatever the API is
# configured with; set B30_EXPECT_TRANSPORT=direct|proxy to assert it.
#
# B30_STRESS=1 additionally runs concurrent 20 MiB direct/proxy uploads and
# samples API memory; it is opt-in because it writes real objects.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

base_url="http://localhost:${API_PORT:-8082}"
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)
tmp_dir="$(cd "$(mktemp -d)" && pwd -W 2>/dev/null || pwd)"
run_id="$$-$(date +%s)"
kb_ids=()
trap 'cleanup' EXIT

headers=(-H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant')
viewer_headers=(-H 'X-Dev-User-ID: viewer-a' -H 'X-Dev-Tenant-ID: dev-tenant')
other_headers=(-H 'X-Dev-User-ID: other-user' -H 'X-Dev-Tenant-ID: other-tenant')

python_bin="${PYTHON_BIN:-python}"
if ! "$python_bin" -c 'import sys' >/dev/null 2>&1; then
  python_bin=python3
fi

json() {
  "$python_bin" -c "import json,sys; d=json.load(open(sys.argv[1], encoding='utf-8')); print(eval(sys.argv[2], {'d': d}))" "$2" "$1"
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

request_url() {
  local method="$1" url="$2" expected="$3" output="$4"
  shift 4
  local code
  code="$(curl -sS -o "$output" -w '%{http_code}' -X "$method" "$url" "$@")"
  if [[ "$code" != "$expected" ]]; then
    printf 'expected %s %s, got %s: ' "$method" "$url" "$code" >&2
    cat "$output" >&2
    exit 1
  fi
}

api_memory_mib() {
  local container="${COMPOSE_PROJECT_NAME:-docker-compose}-api-1"
  docker stats --no-stream --format '{{.MemUsage}}' "$container" | awk '{
    value = $1; unit = $1;
    gsub(/[0-9.]/, "", unit); gsub(/[A-Za-z]/, "", value);
    if (unit ~ /GiB/) value *= 1024; else if (unit ~ /KiB/) value /= 1024;
    printf "%d", value
  }'
}

incomplete_count() {
  "${compose[@]}" exec -T minio sh -c 'mc alias set local http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null; mc ls --incomplete --recursive local/rag-platform-local | wc -l' | tr -d ' '
}

cleanup() {
  for kb in "${kb_ids[@]:-}"; do
    curl -sS -o /dev/null -X DELETE "$base_url/api/v1/knowledge-bases/$kb" "${headers[@]}" || true
  done
  rm -rf "$tmp_dir"
}

create_kb() {
  local name="$1" output="$2"
  request POST /api/v1/knowledge-bases 201 "$output" "${headers[@]}" \
    -H 'Content-Type: application/json' -d "{\"name\":\"$name\"}"
  local id
  id="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$output")"
  printf '%s' "$id"
}
create_session() {
  local kb="$1" filename="$2" size="$3" key="$4" output="$5"
  request POST "/api/v1/knowledge-bases/$kb/uploads" 201 "$output" "${headers[@]}" \
    -H 'Content-Type: application/json' -H "Idempotency-Key: $key" \
    -d "{\"filename\":\"$filename\",\"sizeBytes\":$size,\"mimeType\":\"text/plain\"}"
}

put_part() {
  local session_json="$1" part="$2" file="$3" output="$4"
  local transport url
  transport="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["transport"])' "$session_json")"
  if [[ "$transport" == "direct" ]]; then
    url="$("$python_bin" -c "import json,sys; d=json.load(open(sys.argv[1])); print(next(p['url'] for p in d['parts'] if p['number'] == $part))" "$session_json")"
    request_url PUT "$url" 200 "$output" -H 'Content-Type: application/octet-stream' --data-binary "@$file"
  else
    local session_id
    session_id="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$session_json")"
    request PUT "/api/v1/uploads/$session_id/parts/$part" 200 "$output" "${headers[@]}" \
      -H 'Content-Type: application/octet-stream' --data-binary "@$file"
  fi
}

# upload_session_parts_lenient uploads every part of a stress session. It stops
# on the first 429 (proxy gate) and prints "busy"; otherwise prints "ok".
upload_session_parts_lenient() {
  local session_json="$1" file="$2" workdir="$3"
  local part_size part_count number code part_file
  part_size="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["partSize"])' "$session_json")"
  part_count="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["partCount"])' "$session_json")"
  for number in $(seq 1 "$part_count"); do
    part_file="$workdir/part-$number.bin"
    dd if="$file" of="$part_file" bs="$part_size" skip=$((number - 1)) count=1 status=none
    code="$(put_part_lenient "$session_json" "$number" "$part_file" "$workdir/put-$number.json")"
    rm -f "$part_file"
    if [[ "$code" == "429" ]]; then
      printf 'busy'
      return 0
    fi
  done
  printf 'ok'
}
put_part_lenient() {
  local session_json="$1" part="$2" file="$3" output="$4"
  local transport url session_id code
  transport="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["transport"])' "$session_json")"
  if [[ "$transport" == "direct" ]]; then
    url="$("$python_bin" -c "import json,sys; d=json.load(open(sys.argv[1])); print(next(p['url'] for p in d['parts'] if p['number'] == $part))" "$session_json")"
    code="$(curl -sS -o "$output" -w '%{http_code}' -X PUT "$url" -H 'Content-Type: application/octet-stream' --data-binary "@$file")"
  else
    session_id="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$session_json")"
    code="$(curl -sS -o "$output" -w '%{http_code}' -X PUT "$base_url/api/v1/uploads/$session_id/parts/$part" "${headers[@]}" -H 'Content-Type: application/octet-stream' --data-binary "@$file")"
  fi
  if [[ "$code" != "200" && "$code" != "429" ]]; then
    echo "stress part failed with $code: $(cat "$output")" >&2
    exit 1
  fi
  printf '%s' "$code"
}

curl -fsS "$base_url/readyz" | grep -q '"status":"ok"'

echo "== create knowledge base"
kb="$(create_kb "B30 Acceptance" "$tmp_dir/kb.json")"
kb_ids+=("$kb")

echo "== happy path: create, upload, complete, replay"
printf 'b30 acceptance content' > "$tmp_dir/small.txt"
size="$(wc -c < "$tmp_dir/small.txt" | tr -d ' ')"
create_session "$kb" small.txt "$size" "b30-happy-$run_id" "$tmp_dir/small-session.json"
session_id="$(json 'd["id"]' "$tmp_dir/small-session.json")"
transport="$(json 'd["transport"]' "$tmp_dir/small-session.json")"
if [[ -n "${B30_EXPECT_TRANSPORT:-}" && "$transport" != "$B30_EXPECT_TRANSPORT" ]]; then
  echo "transport=$transport, expected $B30_EXPECT_TRANSPORT" >&2
  exit 1
fi
create_session "$kb" small.txt "$size" "b30-happy-$run_id" "$tmp_dir/small-session-2.json"
[[ "$(json 'd["id"]' "$tmp_dir/small-session-2.json")" == "$session_id" ]]
put_part "$tmp_dir/small-session.json" 1 "$tmp_dir/small.txt" "$tmp_dir/put-small.json"
request POST "/api/v1/uploads/$session_id/complete" 202 "$tmp_dir/complete.json" "${headers[@]}"
version_id="$(json 'd["version"]["id"]' "$tmp_dir/complete.json")"
[[ "$(json 'd["version"]["sizeBytes"]' "$tmp_dir/complete.json")" == "$size" ]]
request POST "/api/v1/uploads/$session_id/complete" 202 "$tmp_dir/complete-2.json" "${headers[@]}"
[[ "$(json 'd["version"]["id"]' "$tmp_dir/complete-2.json")" == "$version_id" ]]
request GET "/api/v1/uploads/$session_id" 200 "$tmp_dir/session-done.json" "${headers[@]}"
[[ "$(json 'd["status"]' "$tmp_dir/session-done.json")" == "completed" ]]

echo "== missing part is rejected, abort cleans the multipart upload"
incomplete_before="$(incomplete_count)"
dd if=/dev/zero of="$tmp_dir/large.bin" bs=1M count=17 status=none
large_size="$(wc -c < "$tmp_dir/large.bin" | tr -d ' ')"
create_session "$kb" large.txt "$large_size" "b30-large-$run_id" "$tmp_dir/large-session.json"
large_id="$(json 'd["id"]' "$tmp_dir/large-session.json")"
"$python_bin" -c "import sys; open(sys.argv[1],'wb').write(open(sys.argv[2],'rb').read(16*1024*1024))" "$tmp_dir/large-part1.bin" "$tmp_dir/large.bin"
put_part "$tmp_dir/large-session.json" 1 "$tmp_dir/large-part1.bin" "$tmp_dir/put-large.json"
request POST "/api/v1/uploads/$large_id/complete" 400 "$tmp_dir/large-complete.json" "${headers[@]}"
grep -qi 'missing' "$tmp_dir/large-complete.json"
request DELETE "/api/v1/uploads/$large_id" 204 "$tmp_dir/large-abort.json" "${headers[@]}"
request DELETE "/api/v1/uploads/$large_id" 204 "$tmp_dir/large-abort-2.json" "${headers[@]}"
incomplete_after="$(incomplete_count)"
[[ "$incomplete_after" == "$incomplete_before" ]]

echo "== batch: three sessions complete independently"
for index in 1 2 3; do
  printf 'batch file %s' "$index" > "$tmp_dir/batch-$index.txt"
  batch_size="$(wc -c < "$tmp_dir/batch-$index.txt" | tr -d ' ')"
  create_session "$kb" "batch-$index.txt" "$batch_size" "b30-batch-$run_id-$index" "$tmp_dir/batch-$index-session.json"
  batch_id="$(json 'd["id"]' "$tmp_dir/batch-$index-session.json")"
  put_part "$tmp_dir/batch-$index-session.json" 1 "$tmp_dir/batch-$index.txt" "$tmp_dir/batch-$index-put.json"
  request POST "/api/v1/uploads/$batch_id/complete" 202 "$tmp_dir/batch-$index-complete.json" "${headers[@]}"
done
request GET "/api/v1/knowledge-bases/$kb/documents?limit=50" 200 "$tmp_dir/documents.json" "${headers[@]}"
[[ "$(json 'len(d["items"])' "$tmp_dir/documents.json")" -ge 4 ]]

echo "== negative access and forged session ids"
request POST "/api/v1/knowledge-bases/$kb/uploads" 403 "$tmp_dir/viewer.json" "${viewer_headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"filename\":\"x.txt\",\"sizeBytes\":4}"
request GET "/api/v1/uploads/$session_id" 404 "$tmp_dir/cross-tenant.json" "${other_headers[@]}"
request GET "/api/v1/uploads/upl-forged" 404 "$tmp_dir/forged.json" "${headers[@]}"

echo "== legacy single-shot endpoint still works"
printf 'legacy upload' > "$tmp_dir/legacy.txt"
request POST "/api/v1/knowledge-bases/$kb/documents" 202 "$tmp_dir/legacy.json" "${headers[@]}" \
  -F "file=@$tmp_dir/legacy.txt;type=text/plain"

if [[ "${B30_STRESS:-0}" == "1" ]]; then
  stress_mb="${B30_STRESS_MB:-12}"
  stress_count="${B30_STRESS_COUNT:-8}"
  echo "== stress: ${stress_count} concurrent ${stress_mb}MiB uploads, API memory bounded"
  dd if=/dev/zero of="$tmp_dir/stress.bin" bs=1M count="$stress_mb" status=none
  stress_size="$(wc -c < "$tmp_dir/stress.bin" | tr -d ' ')"
  stress_kbs=("$kb")
  # The per-knowledge-base active-session cap is 10; spread larger runs.
  extra_index=10
  while [[ "$extra_index" -lt "$stress_count" ]]; do
    extra_kb="$(create_kb "B30 Stress $extra_index" "$tmp_dir/stress-kb-$extra_index.json")"
    kb_ids+=("$extra_kb")
    stress_kbs+=("$extra_kb")
    extra_index=$((extra_index + 10))
  done
  before="$(api_memory_mib)"
  pids=()
  busy_file="$tmp_dir/stress-busy"
  : > "$busy_file"
  for index in $(seq 1 "$stress_count"); do
    (
      kb_index=$(((index - 1) / 10))
      target_kb="${stress_kbs[$kb_index]}"
      create_session "$target_kb" "stress-$index.txt" "$stress_size" "b30-stress-$run_id-$index" "$tmp_dir/stress-$index-session.json"
      workdir="$tmp_dir/stress-work-$index"
      mkdir -p "$workdir"
      result="$(upload_session_parts_lenient "$tmp_dir/stress-$index-session.json" "$tmp_dir/stress.bin" "$workdir")"
      [[ "$result" == "busy" ]] && echo busy >> "$busy_file"
      rm -rf "$workdir"
      stress_id="$(json 'd["id"]' "$tmp_dir/stress-$index-session.json")"
      curl -sS -o /dev/null -X DELETE "$base_url/api/v1/uploads/$stress_id" "${headers[@]}"
    ) &
    pids+=("$!")
  done
  for pid in "${pids[@]}"; do wait "$pid"; done
  after="$(api_memory_mib)"
  echo "API memory before=${before}MiB after=${after}MiB"
  [[ "$after" -lt $((before + 256)) ]]
  if [[ "$transport" == "proxy" ]]; then
    # The per-tenant proxy gate must be visible under this concurrency.
    [[ -s "$busy_file" ]]
  fi
fi

echo "B30 acceptance passed (transport=$transport)"
