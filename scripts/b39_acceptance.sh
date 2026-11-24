#!/usr/bin/env bash
# B39 P0 acceptance: full rows are materialized to Parquet, /tables/query reads
# the columnar copy, sampled sheets stay queryable, Postgres stops writing
# table_rows, and purge removes the prefix. Requires the development stack with
# dev auth and an embedding provider (local embedding is fine).
#
# Optional:
#   B39_LARGE_XLSX=/path/120MB.xlsx   upload the real multi-sheet workbook
#   B39_REQUIRE_LARGE=1               fail unless the large file reaches READY
#   B39_EXPECT_ROWS=975065            assert the materialized row total
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

base_url="http://localhost:${API_PORT:-8082}"
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)
bucket="${S3_BUCKET:-rag-platform-local}"
tmp_dir="$(cd "$(mktemp -d)" && pwd -W 2>/dev/null || pwd)"
kb_ids=()
doc_ids=()
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
  if [[ "${B39_KEEP:-0}" == "1" ]]; then
    echo "B39_KEEP=1: kept knowledge bases ${kb_ids[*]:-} and documents ${doc_ids[*]:-}"
    rm -rf "$tmp_dir"
    return
  fi
  for doc in "${doc_ids[@]:-}"; do
    curl -sS -o /dev/null -X DELETE "$base_url/api/v1/documents/$doc" "${headers[@]}" || true
  done
  for kb in "${kb_ids[@]:-}"; do
    curl -sS -o /dev/null -X DELETE "$base_url/api/v1/knowledge-bases/$kb" "${headers[@]}" || true
  done
  rm -rf "$tmp_dir"
}

create_kb() {
  request POST /api/v1/knowledge-bases 201 "$1/kb.json" "${headers[@]}" -H 'Content-Type: application/json' -d '{"name":"B39 Acceptance"}'
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

wait_pipeline_terminal() {
  local doc="$1" timeout_seconds="$2" active
  local deadline=$((SECONDS + timeout_seconds))
  while [[ $SECONDS -lt $deadline ]]; do
    active="$(psql_value "SELECT count(*) FROM pipeline_runs WHERE document_id = '$doc' AND status NOT IN ('EVIDENCE_READY','FAILED','CANCELLED')")"
    [[ "$active" == "0" ]] && return 0
    sleep 2
  done
  echo "pipelines for $doc did not settle" >&2
  return 1
}

coverage_ready() {
  local file="$1" want_total="$2"
  "$python_bin" - "$file" "$want_total" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
want = int(sys.argv[2])
assert d["materializeReadySheets"] == want, d
assert d["materializeFailedSheets"] == 0, d
assert d["materializeMissingSheets"] == 0, d
assert d["materializeRows"] > 0 and d["materializeBytes"] > 0, d
for sheet in d["sheets"]:
    assert sheet["materializeStatus"] in ("ready", "partial"), sheet
    assert sheet["materializedRows"] > 0 and sheet["byteSize"] > 0, sheet
PY
}

object_count() {
  local kb="$1" doc="$2"
  # Count data files only. pyarrow leaves zero-byte directory markers on S3 that
  # are not duplicated rows; the rebuild invariant is about parts/manifests.
  "${compose[@]}" exec -T -e "TH_PATH=$bucket/tablehouse/tenant=dev-tenant/kb=$kb/doc=$doc/" minio sh -c \
    'mc alias set th http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null || exit 1; mc ls --recursive "th/$TH_PATH" 2>/dev/null' \
    | awk '{print $NF}' \
    | awk -F/ '$NF == "_manifest.json" || $NF ~ /^part-.*[.]parquet$/ { count++ } END { print count + 0 }'
}

assert_materialized() {
  local doc="$1" output="$2"
  local version
  version="$(psql_value "SELECT id FROM document_versions WHERE document_id = '$doc' AND status = 'EVIDENCE_READY'")"
  [[ -n "$version" ]] || { echo "no ready version for $doc"; exit 1; }
  local rows
  rows="$(psql_value "SELECT count(*) FROM table_rows WHERE document_version_id = '$version'")"
  [[ "$rows" == "0" ]] || { echo "table_rows still written ($rows rows)"; exit 1; }
  local mismatch
  mismatch="$(psql_value "SELECT count(*) FROM table_assets WHERE document_version_id = '$version' AND materialize_status IN ('ready','partial') AND (storage_uri = '' OR materialized_rows = 0)")"
  [[ "$mismatch" == "0" ]] || { echo "$mismatch materialized assets missing uri/rows"; exit 1; }
  local total asset_rows
  total="$(psql_value "SELECT parsed_rows FROM document_versions WHERE id = '$version'")"
  asset_rows="$(psql_value "SELECT coalesce(sum(materialized_rows),0) FROM table_assets WHERE document_version_id = '$version'")"
  [[ "$asset_rows" == "$total" ]] || { echo "materialized rows $asset_rows != parsed rows $total"; exit 1; }
  request GET "/api/v1/documents/$doc/index-estimate" 200 "$output" "${headers[@]}"
}

echo "== migration 0043"
[[ "$(psql_value "SELECT count(*) FROM schema_migrations WHERE version = '0043_b39_table_parquet'")" == "1" ]] \
  || { echo "0043_b39_table_parquet is not applied"; exit 1; }

echo "== 65 sheets materialize and stay queryable"
"$python_bin" scripts/generate_xlsx_fixture.py --out "$tmp_dir/many.xlsx" --sheets 65 --rows 3 >/dev/null
kb="$(create_kb "$tmp_dir")"; kb_ids+=("$kb")
doc="$(upload_document "$kb" "$tmp_dir/many.xlsx" "$tmp_dir/many-upload.json")"; doc_ids+=("$doc")
status="$(wait_version "$doc" 300 "$tmp_dir/many-status.json")"
[[ "$status" == "EVIDENCE_READY" ]] || { echo "many.xlsx status=$status"; cat "$tmp_dir/many-status.json"; exit 1; }
assert_materialized "$doc" "$tmp_dir/many-estimate.json"
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

echo "== formerly sampled sheet is queryable"
"$python_bin" scripts/generate_xlsx_fixture.py --out "$tmp_dir/sampled.xlsx" --sheets 1 --rows 2 --large-sheet-rows 60000 >/dev/null
sampled_doc="$(upload_document "$kb" "$tmp_dir/sampled.xlsx" "$tmp_dir/sampled-upload.json")"; doc_ids+=("$sampled_doc")
sampled_status="$(wait_version "$sampled_doc" 600 "$tmp_dir/sampled-status.json")"
[[ "$sampled_status" == "EVIDENCE_READY" ]] || { echo "sampled status=$sampled_status"; cat "$tmp_dir/sampled-status.json"; exit 1; }
assert_materialized "$sampled_doc" "$tmp_dir/sampled-estimate.json"
request GET "/api/v1/knowledge-bases/$kb/tables" 200 "$tmp_dir/sampled-tables.json" "${headers[@]}"
sampled_asset="$("$python_bin" - "$tmp_dir/sampled-tables.json" "$sampled_doc" <<'PY'
import json, sys
items = json.load(open(sys.argv[1], encoding="utf-8"))["items"]
asset = next((item for item in items if item["documentId"] == sys.argv[2]), None)
assert asset, "sampled asset missing"
print(asset["id"])
PY
)"
request POST "/api/v1/knowledge-bases/$kb/tables/query" 200 "$tmp_dir/sampled-query.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"assetId\":\"$sampled_asset\",\"filters\":[{\"column\":\"name\",\"op\":\"eq\",\"value\":\"Huge-row-50000\"}],\"limit\": 5}"
"$python_bin" - "$tmp_dir/sampled-query.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["matchedRows"] == 1, d
assert d["rows"][0]["cells"]["name"] == "Huge-row-50000", d
PY

echo "== read-only SQL"
sql_payload() {
  local file="$1" sql="$2" preview="$3" limit="$4"
  "$python_bin" - "$file" "$asset" "$sql" "$preview" "$limit" <<'PY'
import json, sys
payload = {"assetId": sys.argv[2], "sql": sys.argv[3]}
if sys.argv[4] == "1":
    payload["preview"] = True
if sys.argv[5] != "0":
    payload["limit"] = int(sys.argv[5])
json.dump(payload, open(sys.argv[1], "w", encoding="utf-8"))
PY
}

"$python_bin" - "$tmp_dir/many-tables.json" <<'PY'
import json, sys
items = json.load(open(sys.argv[1], encoding="utf-8"))["items"]
asset = next(item for item in items if item["sheet"] == "Sheet65")
assert asset["sqlColumns"] == ["name", "value"], asset
assert len(asset["columnTypes"]) == 2, asset
PY

sql_payload "$tmp_dir/sql-preview.json" 'SELECT count(*) AS n, sum(TRY_CAST("value" AS BIGINT)) AS total FROM t' 1 0
request POST "/api/v1/knowledge-bases/$kb/tables/sql" 200 "$tmp_dir/sql-preview-result.json" "${headers[@]}" \
  -H 'Content-Type: application/json' --data-binary "@$tmp_dir/sql-preview.json"
"$python_bin" - "$tmp_dir/sql-preview-result.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["preview"] is True and d["rows"] == [], d
assert [column["name"] for column in d["columns"]] == ["n", "total"], d
assert d["rowCount"] == 1, d
PY

sql_payload "$tmp_dir/sql-exec.json" 'SELECT "name", "value", "__row_number" FROM t ORDER BY "__row_number" DESC' 0 2
request POST "/api/v1/knowledge-bases/$kb/tables/sql" 200 "$tmp_dir/sql-exec-result.json" "${headers[@]}" \
  -H 'Content-Type: application/json' --data-binary "@$tmp_dir/sql-exec.json"
"$python_bin" - "$tmp_dir/sql-exec-result.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["preview"] is False and d["rowCount"] == 3, d
assert d["returnedRows"] == 2 and d["truncated"] is True, d
assert d["rows"][0][0] == "Sheet65-row-3" and d["rows"][0][2] == 4, d
PY

"$python_bin" - "$tmp_dir/sql-params.json" "$asset" <<'PY'
import json, sys
payload = {"assetId": sys.argv[2], "sql": 'SELECT count(*) AS n FROM t WHERE "name" = ?', "params": ["Sheet65-row-2"]}
json.dump(payload, open(sys.argv[1], "w", encoding="utf-8"))
PY
request POST "/api/v1/knowledge-bases/$kb/tables/sql" 200 "$tmp_dir/sql-params-result.json" "${headers[@]}" \
  -H 'Content-Type: application/json' --data-binary "@$tmp_dir/sql-params.json"
"$python_bin" - "$tmp_dir/sql-params-result.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["rows"] == [[1]], d
PY

for unsafe_sql in 'DELETE FROM t' 'SELECT * FROM t; DROP TABLE t' "SELECT * FROM read_parquet('s3://x')" 'SELECT nope FROM t' "PRAGMA table_info('t')"; do
  sql_payload "$tmp_dir/sql-reject.json" "$unsafe_sql" 0 5
  request POST "/api/v1/knowledge-bases/$kb/tables/sql" 400 "$tmp_dir/sql-reject-result.json" "${headers[@]}" \
    -H 'Content-Type: application/json' --data-binary "@$tmp_dir/sql-reject.json"
  grep -q "table_sql_rejected" "$tmp_dir/sql-reject-result.json" \
    || { echo "unsafe sql was not rejected: $unsafe_sql"; cat "$tmp_dir/sql-reject-result.json"; exit 1; }
done

allowed_audits="$(psql_value "SELECT count(*) FROM audit_logs WHERE action = 'table_query' AND resource_type = 'table_asset' AND resource_id = '$asset' AND allowed = true")"
rejected_audits="$(psql_value "SELECT count(*) FROM audit_logs WHERE action = 'table_query' AND resource_type = 'table_asset' AND resource_id = '$asset' AND allowed = false")"
[[ "$allowed_audits" -ge 3 ]] || { echo "allowed SQL audits = $allowed_audits"; exit 1; }
[[ "$rejected_audits" -ge 5 ]] || { echo "rejected SQL audits = $rejected_audits"; exit 1; }

echo "== coverage manifest, failed-sheet retry and rebuild"
"$python_bin" scripts/generate_xlsx_fixture.py --out "$tmp_dir/retry.xlsx" --sheets 2 --rows 3 >/dev/null
retry_doc="$(upload_document "$kb" "$tmp_dir/retry.xlsx" "$tmp_dir/retry-upload.json")"; doc_ids+=("$retry_doc")
retry_status="$(wait_version "$retry_doc" 300 "$tmp_dir/retry-status.json")"
[[ "$retry_status" == "EVIDENCE_READY" ]] || { echo "retry.xlsx status=$retry_status"; cat "$tmp_dir/retry-status.json"; exit 1; }
wait_pipeline_terminal "$retry_doc" 60
assert_materialized "$retry_doc" "$tmp_dir/retry-estimate.json"
coverage_ready "$tmp_dir/retry-estimate.json" 2

retry_version="$(psql_value "SELECT latest_version_id FROM documents WHERE id = '$retry_doc'")"
retry_asset="$(psql_value "SELECT id FROM table_assets WHERE document_version_id = '$retry_version' ORDER BY sheet LIMIT 1")"
retry_sheet="$(psql_value "SELECT sheet FROM table_assets WHERE id = '$retry_asset'")"
[[ -n "$retry_asset" ]] || { echo "retry asset missing"; exit 1; }

psql_value "UPDATE table_assets SET materialize_status = 'failed', materialize_error = 'simulated sheet failure', materialized_rows = 0, byte_size = 0, storage_uri = '' WHERE id = '$retry_asset'" >/dev/null
request GET "/api/v1/documents/$retry_doc/index-estimate" 200 "$tmp_dir/retry-failed.json" "${headers[@]}"
"$python_bin" - "$tmp_dir/retry-failed.json" "$retry_sheet" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["materializeFailedSheets"] == 1, d
sheet = next(item for item in d["sheets"] if item["sheet"] == sys.argv[2])
assert sheet["materializeStatus"] == "failed", sheet
assert sheet["materializeError"] == "simulated sheet failure", sheet
PY
request POST "/api/v1/knowledge-bases/$kb/tables/query" 400 "$tmp_dir/retry-failed-query.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"assetId\":\"$retry_asset\",\"limit\": 5}"
grep -q "table_materialize_failed" "$tmp_dir/retry-failed-query.json" \
  || { echo "failed sheet query did not report table_materialize_failed"; cat "$tmp_dir/retry-failed-query.json"; exit 1; }

request POST "/api/v1/documents/$retry_doc/tables/rebuild" 202 "$tmp_dir/retry-rebuild.json" "${headers[@]}"
retry_status="$(wait_version "$retry_doc" 300 "$tmp_dir/retry-after.json")"
[[ "$retry_status" == "EVIDENCE_READY" ]] || { echo "retry rebuild status=$retry_status"; cat "$tmp_dir/retry-after.json"; exit 1; }
wait_pipeline_terminal "$retry_doc" 60
assert_materialized "$retry_doc" "$tmp_dir/retry-after-estimate.json"
coverage_ready "$tmp_dir/retry-after-estimate.json" 2

retry_version="$(psql_value "SELECT latest_version_id FROM documents WHERE id = '$retry_doc'")"
retry_asset="$(psql_value "SELECT id FROM table_assets WHERE document_version_id = '$retry_version' ORDER BY sheet LIMIT 1")"
request POST "/api/v1/knowledge-bases/$kb/tables/query" 200 "$tmp_dir/retry-after-query.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"assetId\":\"$retry_asset\",\"limit\": 5}"
"$python_bin" - "$tmp_dir/retry-after-query.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["matchedRows"] > 0 and d["rows"], d
PY

echo "== pre-B39 assets rebuild without duplicating parts"
psql_value "UPDATE table_assets SET storage_uri = '', storage_format = '', materialize_status = '', materialize_error = '', materialized_rows = 0, byte_size = 0, materialized_at = NULL WHERE document_version_id = '$retry_version'" >/dev/null
request GET "/api/v1/documents/$retry_doc/index-estimate" 200 "$tmp_dir/retry-missing.json" "${headers[@]}"
"$python_bin" - "$tmp_dir/retry-missing.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["materializeMissingSheets"] == d["totalSheets"], d
assert d["materializeReadySheets"] == 0, d
assert all(item["materializeStatus"] == "" for item in d["sheets"]), d
PY
request POST "/api/v1/knowledge-bases/$kb/tables/query" 400 "$tmp_dir/retry-missing-query.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"assetId\":\"$retry_asset\",\"limit\": 5}"
grep -q "table_needs_reparse" "$tmp_dir/retry-missing-query.json" \
  || { echo "missing copy query did not report table_needs_reparse"; cat "$tmp_dir/retry-missing-query.json"; exit 1; }

objects_before="$(object_count "$kb" "$retry_doc")"
request POST "/api/v1/documents/$retry_doc/tables/rebuild" 202 "$tmp_dir/retry-rebuild-2.json" "${headers[@]}"
retry_status="$(wait_version "$retry_doc" 300 "$tmp_dir/retry-after-2.json")"
[[ "$retry_status" == "EVIDENCE_READY" ]] || { echo "second rebuild status=$retry_status"; cat "$tmp_dir/retry-after-2.json"; exit 1; }
wait_pipeline_terminal "$retry_doc" 60
assert_materialized "$retry_doc" "$tmp_dir/retry-after-estimate-2.json"
coverage_ready "$tmp_dir/retry-after-estimate-2.json" 2
objects_after="$(object_count "$kb" "$retry_doc")"
[[ "$objects_before" == "$objects_after" ]] \
  || { echo "rebuild duplicated parquet objects: before=$objects_before after=$objects_after"; exit 1; }
request DELETE "/api/v1/documents/$retry_doc" 204 "$tmp_dir/retry-delete.json" "${headers[@]}"
request POST "/api/v1/documents/$retry_doc/purge" 204 "$tmp_dir/retry-purge.json" "${headers[@]}"

echo "== purge removes the columnar prefix"
request DELETE "/api/v1/documents/$sampled_doc" 204 "$tmp_dir/sampled-delete.json" "${headers[@]}"
request POST "/api/v1/documents/$sampled_doc/purge" 204 "$tmp_dir/sampled-purge.json" "${headers[@]}"
listing="$("${compose[@]}" exec -T -e "TH_PATH=$bucket/tablehouse/tenant=dev-tenant/kb=$kb/doc=$sampled_doc/" minio sh -c 'mc alias set th http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null || exit 1; mc ls --recursive "th/$TH_PATH" 2>/dev/null || true')" \
  || { echo "minio listing failed"; exit 1; }
leftover="$(printf '%s' "$listing" | grep -c . || true)"
[[ "$leftover" == "0" ]] || { echo "parquet leftovers after purge: $leftover objects"; exit 1; }

if [[ -n "${B39_LARGE_XLSX:-}" ]]; then
  echo "== real large workbook"
  cp "$B39_LARGE_XLSX" "$tmp_dir/large.xlsx"
  large_doc="$(upload_document "$kb" "$tmp_dir/large.xlsx" "$tmp_dir/large-upload.json")"; doc_ids+=("$large_doc")
  large_status="$(wait_version "$large_doc" 3600 "$tmp_dir/large-status.json")"
  if [[ "$large_status" == "EVIDENCE_READY" ]]; then
    assert_materialized "$large_doc" "$tmp_dir/large-estimate.json"
    "$python_bin" - "$tmp_dir/large-estimate.json" "${B39_EXPECT_ROWS:-}" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["totalSheets"] >= 65, d
assert d["parsedSheets"] == d["totalSheets"], d
# Declared totals include header rows; parsed counts only data rows.
assert d["parsedRows"] <= d["totalRows"], d
assert d["parsedRows"] >= d["totalRows"] - d["totalSheets"], d
if sys.argv[2]:
    assert d["parsedRows"] == int(sys.argv[2]), d
print("large coverage", d["parsedSheets"], "/", d["totalSheets"],
      "rows", d["parsedRows"], "/", d["totalRows"],
      "vectorRows", d["vectorRows"], "vectorTokens", d["currentVectorTokens"])
PY
    request GET "/api/v1/knowledge-bases/$kb/tables" 200 "$tmp_dir/large-tables.json" "${headers[@]}"
    sampled_ready="$("$python_bin" - "$tmp_dir/large-tables.json" "$large_doc" <<'PY'
import json, sys
items = json.load(open(sys.argv[1], encoding="utf-8"))["items"]
asset = next((item for item in items if item["documentId"] == sys.argv[2] and item["storageMode"] != "full"), None)
print(asset["id"] if asset else "")
PY
)"
    if [[ -n "$sampled_ready" ]]; then
      request POST "/api/v1/knowledge-bases/$kb/tables/query" 200 "$tmp_dir/large-query.json" "${headers[@]}" \
        -H 'Content-Type: application/json' -d "{\"assetId\":\"$sampled_ready\",\"limit\": 2}"
      "$python_bin" - "$tmp_dir/large-query.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["matchedRows"] > 0 and d["rows"], d
PY
    fi
  else
    [[ "${B39_REQUIRE_LARGE:-0}" != "1" ]] || { echo "large status=$large_status"; cat "$tmp_dir/large-status.json"; exit 1; }
    echo "large status=$large_status (set B39_REQUIRE_LARGE=1 to require READY)"
  fi
fi

echo "B39 acceptance passed"