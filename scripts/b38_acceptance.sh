#!/usr/bin/env bash
# B38 acceptance: per-sheet fairness, A-tier full coverage, and B-tier
# metadata/sample behavior. Requires the development stack with dev auth and an
# embedding provider (local embedding is fine).
#
# Optional:
#   B38_LARGE_XLSX=/path/120MB.xlsx   upload the real multi-sheet workbook
#   B38_REQUIRE_LARGE=1               fail unless the large file reaches READY
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
  request POST /api/v1/knowledge-bases 201 "$1/kb.json" "${headers[@]}" -H 'Content-Type: application/json' -d '{"name":"B38 Acceptance"}'
  "$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$1/kb.json"
}

upload_document() {
  local kb="$1" file="$2" output="$3"
  request POST "/api/v1/knowledge-bases/$kb/documents" 202 "$output" "${headers[@]}" \
    -F "file=@$file;type=application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
  json 'd["document"]["id"]' "$output"
}

wait_version() {
  local document="$1" timeout_seconds="$2" output="$3"
  local deadline=$((SECONDS + timeout_seconds)) status=""
  while [[ $SECONDS -lt $deadline ]]; do
    request GET "/api/v1/documents/$document/status" 200 "$output" "${headers[@]}"
    status="$(json 'd["version"]["status"]' "$output")"
    case "$status" in
      EVIDENCE_READY|FAILED|CANCELLED) printf '%s' "$status"; return 0 ;;
    esac
    sleep 3
  done
  printf '%s' "$status"
}

psql_value() { "${compose[@]}" exec -T postgres psql -U "${POSTGRES_USER:-rag_platform}" -d "${POSTGRES_DB:-rag_platform}" -Atc "$1"; }

echo "== migration 0042"
[[ "$(psql_value "SELECT count(*) FROM schema_migrations WHERE version = '0042_b38_sheet_coverage'")" == "1" ]] \
  || { echo "0042_b38_sheet_coverage is not applied"; exit 1; }

echo "== 65-sheet A-tier fairness"
"$python_bin" scripts/generate_xlsx_fixture.py --out "$tmp_dir/many.xlsx" --sheets 65 --rows 3 >/dev/null
kb="$(create_kb "$tmp_dir")"; kb_ids+=("$kb")
doc="$(upload_document "$kb" "$tmp_dir/many.xlsx" "$tmp_dir/many-upload.json")"
status="$(wait_version "$doc" 300 "$tmp_dir/many-status.json")"
[[ "$status" == "EVIDENCE_READY" ]] || { echo "many.xlsx status=$status"; cat "$tmp_dir/many-status.json"; exit 1; }
request GET "/api/v1/documents/$doc/index-estimate" 200 "$tmp_dir/many-estimate.json" "${headers[@]}"
"$python_bin" - "$tmp_dir/many-estimate.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["storageMode"] == "full", d
assert d["coverageKnown"] is True, d
assert d["parsedSheets"] == 65 and d["totalSheets"] == 65, d
assert d["tableCount"] == 65, d
assert d["parsedRows"] > 0 and d["totalRows"] >= d["parsedRows"], d
PY
[[ "$(psql_value "SELECT storage_mode || ':' || parsed_sheets || '/' || total_sheets FROM document_versions WHERE document_id = '$doc' AND status = 'EVIDENCE_READY'")" == "full:65/65" ]] \
  || { echo "DB coverage does not show 65/65"; exit 1; }
request GET "/api/v1/knowledge-bases/$kb/tables" 200 "$tmp_dir/many-tables.json" "${headers[@]}"
asset="$("$python_bin" - "$tmp_dir/many-tables.json" <<'PY'
import json, sys
items = json.load(open(sys.argv[1], encoding="utf-8"))["items"]
asset = next((item for item in items if item["sheet"] == "Sheet65"), None)
assert asset, [item["sheet"] for item in items[-5:]]
print(asset["id"])
PY
)"
request POST "/api/v1/knowledge-bases/$kb/tables/query" 200 "$tmp_dir/many-query.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"assetId\":\"$asset\",\"filters\":[{\"column\":\"name\",\"op\":\"eq\",\"value\":\"Sheet65-row-3\"}],\"limit\": 5}"
"$python_bin" - "$tmp_dir/many-query.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["matchedRows"] == 1, d
assert d["rows"][0]["cells"]["name"] == "Sheet65-row-3", d
PY

echo "== sampled sheet is explicitly non-queryable"
"$python_bin" scripts/generate_xlsx_fixture.py --out "$tmp_dir/sampled.xlsx" --sheets 1 --rows 2 --large-sheet-rows 60000 >/dev/null
sampled_doc="$(upload_document "$kb" "$tmp_dir/sampled.xlsx" "$tmp_dir/sampled-upload.json")"
sampled_status="$(wait_version "$sampled_doc" 300 "$tmp_dir/sampled-status.json")"
[[ "$sampled_status" == "EVIDENCE_READY" ]] || { echo "sampled status=$sampled_status"; cat "$tmp_dir/sampled-status.json"; exit 1; }
request GET "/api/v1/documents/$sampled_doc/index-estimate" 200 "$tmp_dir/sampled-estimate.json" "${headers[@]}"
"$python_bin" - "$tmp_dir/sampled-estimate.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["storageMode"] in ("sampled", "metadata"), d
assert d["parseTruncated"] is True, d
assert d["parsedRows"] < d["totalRows"], d
PY
request GET "/api/v1/knowledge-bases/$kb/tables" 200 "$tmp_dir/sampled-tables.json" "${headers[@]}"
sampled_asset="$("$python_bin" - "$tmp_dir/sampled-tables.json" "$sampled_doc" <<'PY'
import json, sys
items = json.load(open(sys.argv[1], encoding="utf-8"))["items"]
asset = next((item for item in items if item["documentId"] == sys.argv[2]), None)
assert asset, "sampled asset missing"
print(asset["id"])
PY
)"
code="$(curl -sS -o "$tmp_dir/sampled-query.json" -w '%{http_code}' -X POST "$base_url/api/v1/knowledge-bases/$kb/tables/query" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"assetId\":\"$sampled_asset\"}")"
[[ "$code" == "400" ]] || { echo "sampled query expected 400, got $code"; cat "$tmp_dir/sampled-query.json"; exit 1; }
grep -q "table_rows_not_indexed" "$tmp_dir/sampled-query.json" || { echo "missing table_rows_not_indexed"; cat "$tmp_dir/sampled-query.json"; exit 1; }

if [[ -n "${B38_LARGE_XLSX:-}" ]]; then
  echo "== real large workbook"
  # Copy to an ASCII path so curl's multipart filename is valid UTF-8 on every
  # host; the original workbook name is not part of this acceptance.
  cp "$B38_LARGE_XLSX" "$tmp_dir/large.xlsx"
  large_doc="$(upload_document "$kb" "$tmp_dir/large.xlsx" "$tmp_dir/large-upload.json")"
  large_status="$(wait_version "$large_doc" 1800 "$tmp_dir/large-status.json")"
  if [[ "$large_status" == "EVIDENCE_READY" ]]; then
    request GET "/api/v1/documents/$large_doc/index-estimate" 200 "$tmp_dir/large-estimate.json" "${headers[@]}"
    "$python_bin" - "$tmp_dir/large-estimate.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["totalSheets"] > 0, d
assert d["parsedSheets"] > 0, d
assert d["parsedSheets"] <= d["totalSheets"], d
assert d["parsedRows"] <= d["totalRows"], d
print("large coverage", d["parsedSheets"], "/", d["totalSheets"], "storage", d["storageMode"],
      "rows", d["parsedRows"], "/", d["totalRows"], "vectorRows", d["vectorRows"],
      "vectorTokens", d["currentVectorTokens"])
PY
  else
    [[ "${B38_REQUIRE_LARGE:-0}" != "1" ]] || { echo "large status=$large_status"; cat "$tmp_dir/large-status.json"; exit 1; }
    echo "large status=$large_status (set B38_REQUIRE_LARGE=1 to require READY)"
  fi
fi

echo "B38 acceptance passed"
