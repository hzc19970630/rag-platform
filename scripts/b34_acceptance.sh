#!/usr/bin/env bash
# B34 acceptance: CSV/table parsing, header repetition, optional large XLSX
# streaming, and zip-bomb protection. Requires the development stack with
# dev auth and an embedding provider (local embedding is fine).
#
# Optional:
#   B34_SAMPLE_XLSX=/path/small.xlsx   upload and assert table chunks
#   B34_LARGE_XLSX=/path/119MB.xlsx    upload and assert parse/chunk progress
#   B34_REQUIRE_READY=1                fail unless the large file reaches EVIDENCE_READY
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
kb_ids=()
trap 'cleanup' EXIT

headers=(-H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant')
python_bin="${PYTHON_BIN:-python}"
if ! "$python_bin" -c 'import sys' >/dev/null 2>&1; then python_bin=python3; fi

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

cleanup() {
  for kb in "${kb_ids[@]:-}"; do
    curl -sS -o /dev/null -X DELETE "$base_url/api/v1/knowledge-bases/$kb" "${headers[@]}" || true
  done
  rm -rf "$tmp_dir"
}

create_kb() {
  local name="$1" output="$2"
  request POST /api/v1/knowledge-bases 201 "$output" "${headers[@]}" -H 'Content-Type: application/json' -d "{\"name\":\"$name\"}"
  "$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$output"
}

upload_document() {
  local kb="$1" file="$2" mime="$3" output="$4"
  request POST "/api/v1/knowledge-bases/$kb/documents" 202 "$output" "${headers[@]}" -F "file=@$file;type=$mime"
  json 'd["document"]["id"]' "$output"
}

wait_version() {
  local document="$1" timeout_seconds="$2" output="$3"
  local deadline=$((SECONDS + timeout_seconds)) status=""
  while [[ $SECONDS -lt $deadline ]]; do
    request GET "/api/v1/documents/$document/status" 200 "$output" "${headers[@]}"
    status="$(json 'd["version"]["status"]' "$output")"
    case "$status" in
      EVIDENCE_READY) printf '%s' "$status"; return 0 ;;
      FAILED|CANCELLED) printf '%s' "$status"; return 0 ;;
    esac
    sleep 3
  done
  printf '%s' "$status"
  return 0
}

curl -fsS "$base_url/readyz" | grep -q '"csv"'

kb="$(create_kb "B34 Acceptance" "$tmp_dir/kb.json")"
kb_ids+=("$kb")

echo "== CSV table block and repeated header"
printf 'customer,amount,date\nA,100,2025-01-01\nB,200,2025-01-02\nC,300,2025-01-03\n' > "$tmp_dir/table.csv"
csv_doc="$(upload_document "$kb" "$tmp_dir/table.csv" text/csv "$tmp_dir/csv-upload.json")"
csv_status="$(wait_version "$csv_doc" 180 "$tmp_dir/csv-status.json")"
[[ "$csv_status" == "EVIDENCE_READY" ]] || { echo "CSV status=$csv_status"; exit 1; }
request GET "/api/v1/documents/$csv_doc/chunks?limit=20" 200 "$tmp_dir/csv-chunks.json" "${headers[@]}"
"$python_bin" - "$tmp_dir/csv-chunks.json" <<'PY'
import json, sys
items = json.load(open(sys.argv[1], encoding="utf-8"))["items"]
assert items, "CSV produced no chunks"
for chunk in items:
    lines = chunk["content"].splitlines()
    assert lines and lines[0].startswith("customer | amount | date"), lines[0]
assert any("A | 100" in chunk["content"] for chunk in items)
PY

if [[ -n "${B34_SAMPLE_XLSX:-}" ]]; then
  echo "== XLSX table blocks"
  xlsx_doc="$(upload_document "$kb" "$B34_SAMPLE_XLSX" application/vnd.openxmlformats-officedocument.spreadsheetml.sheet "$tmp_dir/xlsx-upload.json")"
  xlsx_status="$(wait_version "$xlsx_doc" 180 "$tmp_dir/xlsx-status.json")"
  [[ "$xlsx_status" == "EVIDENCE_READY" ]] || { echo "XLSX status=$xlsx_status"; exit 1; }
  request GET "/api/v1/documents/$xlsx_doc/chunks?limit=5" 200 "$tmp_dir/xlsx-chunks.json" "${headers[@]}"
  "$python_bin" - "$tmp_dir/xlsx-chunks.json" <<'PY'
import json, sys
items = json.load(open(sys.argv[1], encoding="utf-8"))["items"]
assert items and any(" | " in chunk["content"] for chunk in items)
PY
fi

if [[ -n "${B34_LARGE_XLSX:-}" ]]; then
  echo "== large XLSX streaming parse"
  large_size="$(wc -c < "$B34_LARGE_XLSX" | tr -d ' ')"
  max_bytes=$(( ${DOCREADER_MAX_FILE_MB:-100} * 1024 * 1024 ))
  [[ "$large_size" -le "$max_bytes" ]] || { echo "large file exceeds DOCREADER_MAX_FILE_MB"; exit 1; }
  before_spool="$("${compose[@]}" exec -T docreader sh -c 'ls /spool 2>/dev/null | wc -l' | tr -d ' ')"
  large_doc="$(upload_document "$kb" "$B34_LARGE_XLSX" application/vnd.openxmlformats-officedocument.spreadsheetml.sheet "$tmp_dir/large-upload.json")"
  large_status="$(wait_version "$large_doc" 900 "$tmp_dir/large-status.json")"
  if [[ "$large_status" == "EVIDENCE_READY" ]]; then
    request GET "/api/v1/documents/$large_doc/chunks?limit=1" 200 "$tmp_dir/large-chunks.json" "${headers[@]}"
    [[ "$(json 'len(d["items"])' "$tmp_dir/large-chunks.json")" -ge 1 ]]
  else
    [[ "${B34_REQUIRE_READY:-0}" != "1" ]] || { echo "large status=$large_status"; cat "$tmp_dir/large-status.json"; exit 1; }
    echo "large status=$large_status (set B34_REQUIRE_READY=1 to require EVIDENCE_READY)"
  fi
  after_spool="$("${compose[@]}" exec -T docreader sh -c 'ls /spool 2>/dev/null | wc -l' | tr -d ' ')"
  [[ "$after_spool" == "$before_spool" ]] || { echo "docreader spool leaked"; exit 1; }
fi

echo "== zip bomb is rejected"
"$python_bin" - "$tmp_dir/bomb.docx" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w", zipfile.ZIP_DEFLATED) as archive:
    archive.writestr("word/document.xml", "<w:document xmlns:w='http://schemas.openxmlformats.org/wordprocessingml/2006/main'><w:body/></w:document>")
    archive.writestr("word/media/bomb.bin", b"0" * (5 * 1024 * 1024))
PY
bomb_doc="$(upload_document "$kb" "$tmp_dir/bomb.docx" application/vnd.openxmlformats-officedocument.wordprocessingml.document "$tmp_dir/bomb-upload.json")"
bomb_status="$(wait_version "$bomb_doc" 120 "$tmp_dir/bomb-status.json")"
[[ "$bomb_status" == "FAILED" ]]
[[ "$(json 'd["version"]["errorCode"]' "$tmp_dir/bomb-status.json")" == "document_too_complex" ]]

echo "B34 acceptance passed"
