#!/usr/bin/env bash
# M5 acceptance: one external http_manifest connector with encrypted bearer
# credentials, cursor-based full/incremental sync, deletion tombstones,
# cancellation/resume, permission mapping and pipeline-only ingestion.
#
# The fixture server runs on the host and is reached from the worker through
# host.docker.internal, which is how Docker Desktop exposes the host loopback.
# CONNECTOR_ALLOW_PRIVATE_NETWORKS must be true in the local .env for this.
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
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)
tmp_dir="$(mktemp -d)"
run_id="$(date +%s)-$$"
fixture_root="$tmp_dir/fixture"
fixture_token="m5-fixture-secret-$run_id"
fixture_url=""
fixture_pid=""
connector_id=""
token=""
synced_docs=()

cleanup() {
  if [[ -n "$fixture_pid" ]]; then
    kill "$fixture_pid" >/dev/null 2>&1 || true
    wait "$fixture_pid" 2>/dev/null || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'M5 acceptance failed: %s\n' "$*" >&2; exit 1; }

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

psql_value() {
  "${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "$1"
}

assert_json() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
expression = sys.argv[2]
if not eval(expression, {"__builtins__": {}}, {"data": data, "len": len, "any": any, "all": all, "str": str, "int": int}):
    raise SystemExit(f"assertion failed: {expression}\n{json.dumps(data)[:3000]}")
PY
}

json_field() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
for part in sys.argv[2].split("."):
    data = data[int(part)] if part.isdigit() else data[part]
print(data)
PY
}

write_manifest() {
  python3 - "$fixture_root/manifest.json" "$@" <<'PY'
import json
import sys

items = []
for spec in sys.argv[2:]:
    external_id, filename, version = spec.split("|", 2)
    items.append({"id": external_id, "url": "/files/" + filename, "path": filename, "version": version})
json.dump({"items": items}, open(sys.argv[1], "w"))
PY
}

write_doc() {
  local filename="$1" marker="$2"
  printf '# M5 connector fixture %s\n\nThe marker %s belongs to the M5 incremental sync acceptance fixture.\n\nA second paragraph keeps the chunker and embedding stage exercised.\n' "$filename" "$marker" > "$fixture_root/files/$filename"
}

search() {
  local bearer="$1" query="$2" expected="$3" output="$4"
  request POST /api/v1/search "$expected" "$output" \
    -H "Authorization: Bearer $bearer" -H 'Content-Type: application/json' \
    -d "{\"query\":\"$query\",\"knowledgeBaseId\":\"kb-dev\"}"
}

assert_search_has_document() {
  local output="$1" document_id="$2"
  python3 - "$output" "$document_id" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
document_id = sys.argv[2]
assert any(item["documentId"] == document_id for item in data["items"]), json.dumps(data)[:2000]
PY
}

assert_search_excludes_document() {
  local output="$1" document_id="$2"
  python3 - "$output" "$document_id" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
document_id = sys.argv[2]
assert all(item["documentId"] != document_id for item in data["items"]), json.dumps(data)[:2000]
PY
}

wait_run_status() {
  local connector="$1" run_id_value="$2" expected="$3" output="$4"
  local status=""
  for _ in $(seq 1 240); do
    request GET "/api/v1/connectors/$connector/runs/$run_id_value" 200 "$output" -H "Authorization: Bearer $token"
    status="$(json_field "$output" status)"
    [[ "$status" == "$expected" ]] && return 0
    case "$status" in
      SUCCEEDED|PARTIAL|FAILED|CANCELLED)
        cat "$output" >&2
        fail "sync run $run_id_value reached $status while waiting for $expected"
        ;;
    esac
    sleep 0.5
  done
  fail "sync run $run_id_value did not reach $expected (last: $status)"
}

wait_run_running() {
  local connector="$1" run_id_value="$2" output="$3"
  local status=""
  for _ in $(seq 1 120); do
    request GET "/api/v1/connectors/$connector/runs/$run_id_value" 200 "$output" -H "Authorization: Bearer $token"
    status="$(json_field "$output" status)"
    [[ "$status" == "RUNNING" ]] && return 0
    case "$status" in
      SUCCEEDED|PARTIAL|FAILED|CANCELLED)
        cat "$output" >&2
        fail "sync run $run_id_value finished with $status before it could be inspected mid-run"
        ;;
    esac
    sleep 0.2
  done
  fail "sync run $run_id_value never reached RUNNING (last: $status)"
}

wait_run_progress() {
  local connector="$1" run_id_value="$2" output="$3"
  for _ in $(seq 1 180); do
    request GET "/api/v1/connectors/$connector/runs/$run_id_value" 200 "$output" -H "Authorization: Bearer $token"
    if python3 - "$output" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
processed = data["itemsCreated"] + data["itemsUpdated"] + data["itemsDeleted"] + data["itemsSkipped"]
raise SystemExit(0 if processed > 0 else 1)
PY
    then
      return 0
    fi
    sleep 0.2
  done
  fail "sync run $run_id_value recorded no progress before cancellation"
}

wait_pipeline_status() {
  local document_id="$1" expected="$2" output="$3"
  local status=""
  for _ in $(seq 1 180); do
    request GET "/api/v1/documents/$document_id" 200 "$output" -H "Authorization: Bearer $token"
    status="$(json_field "$output" pipeline.status)"
    [[ "$status" == "$expected" ]] && return 0
    if [[ "$status" == "FAILED" && "$expected" != "FAILED" ]]; then
      if python3 - "$output" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
raise SystemExit(0 if any(stage.get("status") == "FAILED" for stage in data.get("stages", [])) else 1)
PY
      then
        cat "$output" >&2
        fail "document $document_id failed while waiting for $expected"
      fi
    fi
    sleep 1
  done
  fail "document $document_id did not reach $expected (last: $status)"
}

trigger_sync() {
  local output="$1"
  request POST "/api/v1/connectors/$connector_id/sync" 202 "$output" -H "Authorization: Bearer $token"
}

doc_ids_sql() {
  local quoted=()
  for document_id in "${synced_docs[@]}"; do
    quoted+=("'$document_id'")
  done
  local IFS=','
  echo "${quoted[*]}"
}

# ---------------------------------------------------------------------------
step "preflight: readiness, migrations and connector tables"
# ---------------------------------------------------------------------------
curl -fsS "$base_url/readyz" | grep -q '"status":"ok"' || fail "api is not ready"
"${compose[@]}" exec -T redis redis-cli ping | grep -q PONG || fail "redis is not ready"

migrate_output="$("${compose[@]}" run --rm --no-deps migrate -check 2>&1)"
printf '%s\n' "$migrate_output" | grep -q migrations_compatible || { printf '%s\n' "$migrate_output" >&2; fail "migration -check did not report compatibility"; }
[[ "$(psql_value "SELECT count(*) FROM schema_migrations WHERE version = '0009_datasource_connectors';")" == "1" ]] || fail "migration 0009 is not applied"
[[ "$(psql_value "SELECT count(*) FROM information_schema.tables WHERE table_name IN ('connectors', 'connector_items', 'sync_runs');")" == "3" ]] || fail "connector tables are missing"

if [[ -z "${CONNECTOR_ENCRYPTION_KEY:-}" ]]; then
  fail "CONNECTOR_ENCRYPTION_KEY is not configured"
fi

# ---------------------------------------------------------------------------
step "fixture: authenticated HTTP manifest served from the host"
# ---------------------------------------------------------------------------
mkdir -p "$fixture_root/files"
printf '0\n' > "$fixture_root/delay_ms"
: > "$fixture_root/requests.log"

cat > "$tmp_dir/fixture_server.py" <<'PY'
import http.server
import os
import socket
import sys
import time

root, token = sys.argv[1], sys.argv[2]
sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.bind(("0.0.0.0", 0))
port = sock.getsockname()[1]
sock.close()


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def _record(self, status, authorized):
        with open(os.path.join(root, "requests.log"), "a") as handle:
            handle.write("%.3f %s %s %s\n" % (time.time(), self.path, status, "ok" if authorized else "denied"))

    def _respond(self, status, body=b"", ctype="text/plain"):
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if body:
            self.wfile.write(body)

    def do_GET(self):
        authorized = self.headers.get("Authorization") == "Bearer " + token
        if not authorized:
            self._record(401, False)
            self._respond(401, b"unauthorized")
            return
        try:
            delay = int(open(os.path.join(root, "delay_ms")).read().strip() or "0")
        except Exception:
            delay = 0
        if delay > 0:
            time.sleep(delay / 1000.0)
        target = self.path.split("?", 1)[0]
        if target == "/manifest.json":
            path = os.path.join(root, "manifest.json")
        elif target.startswith("/files/"):
            path = os.path.join(root, "files", os.path.basename(target))
        else:
            self._record(404, True)
            self._respond(404)
            return
        if not os.path.isfile(path):
            self._record(404, True)
            self._respond(404)
            return
        body = open(path, "rb").read()
        self._record(200, True)
        self._respond(200, body, "application/json" if target == "/manifest.json" else "application/octet-stream")


server = http.server.ThreadingHTTPServer(("0.0.0.0", port), Handler)
server.daemon_threads = True
open(os.path.join(root, "port"), "w").write(str(port))
server.serve_forever()
PY

python3 -u "$tmp_dir/fixture_server.py" "$fixture_root" "$fixture_token" >/dev/null 2>&1 &
fixture_pid=$!

fixture_port=""
for _ in $(seq 1 50); do
  if [[ -s "$fixture_root/port" ]]; then
    fixture_port="$(cat "$fixture_root/port")"
    break
  fi
  sleep 0.1
done
[[ -n "$fixture_port" ]] || fail "fixture server did not report its port"
fixture_url="http://host.docker.internal:$fixture_port/manifest.json"

alpha_id="alpha-$run_id"
beta_id="beta-$run_id"
gamma_id="gamma-$run_id"
delta_id="delta-$run_id"
write_doc alpha.md "M5-ALPHA-$run_id aurora"
write_doc beta.txt "M5-BETA-$run_id beacon"
write_doc gamma.md "M5-GAMMA-$run_id crystal"
write_doc delta.md "M5-DELTA-$run_id delta"
write_manifest "$alpha_id|alpha.md|1" "$beta_id|beta.txt|1" "$gamma_id|gamma.md|1"

code=""
for _ in $(seq 1 50); do
  code="$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $fixture_token" "http://127.0.0.1:$fixture_port/manifest.json")"
  if [[ "$code" == "200" ]]; then
    break
  fi
  sleep 0.1
done
[[ "$code" == "200" ]] || fail "fixture manifest is not reachable on the host"
[[ "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$fixture_port/manifest.json")" == "401" ]] || fail "fixture does not require the bearer credential"

# ---------------------------------------------------------------------------
step "connector validation and role boundaries"
# ---------------------------------------------------------------------------
request POST /api/v1/auth/dev-login 200 "$tmp_dir/login.json" \
  -H 'Content-Type: application/json' -d '{"userId":"dev-user","tenantId":"dev-tenant"}'
token="$(json_field "$tmp_dir/login.json" token)"
[[ -n "$token" ]]

request POST /api/v1/auth/dev-login 200 "$tmp_dir/login-viewer.json" \
  -H 'Content-Type: application/json' -d '{"userId":"viewer-a","tenantId":"dev-tenant"}'
viewer_token="$(json_field "$tmp_dir/login-viewer.json" token)"
request POST /api/v1/auth/dev-login 200 "$tmp_dir/login-other.json" \
  -H 'Content-Type: application/json' -d '{"userId":"other-user","tenantId":"other-tenant"}'
other_token="$(json_field "$tmp_dir/login-other.json" token)"

python3 - "$tmp_dir/create.json" "$fixture_url" "$fixture_token" "$run_id" <<'PY'
import json
import sys

json.dump({
    "name": "m5-acceptance-" + sys.argv[4],
    "type": "http_manifest",
    "config": {"manifestUrl": sys.argv[2], "requestsPerSecond": 2, "maxItemsPerRun": 20, "aclMode": "knowledge_base"},
    "credential": {"type": "bearer", "token": sys.argv[3]},
    "syncIntervalSeconds": 0,
    "enabled": True,
}, open(sys.argv[1], "w"))
PY

request POST /api/v1/knowledge-bases/kb-dev/connectors 403 "$tmp_dir/viewer-create.json" \
  -H "Authorization: Bearer $viewer_token" -H 'Content-Type: application/json' --data-binary @"$tmp_dir/create.json"
request GET /api/v1/knowledge-bases/kb-dev/connectors 403 "$tmp_dir/other-list.json" -H "Authorization: Bearer $other_token"
request POST /api/v1/knowledge-bases/kb-dev/connectors 403 "$tmp_dir/other-create.json" \
  -H "Authorization: Bearer $other_token" -H 'Content-Type: application/json' --data-binary @"$tmp_dir/create.json"

request POST /api/v1/knowledge-bases/kb-dev/connectors 400 "$tmp_dir/create-bad-url.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"name":"m5 bad url","config":{"manifestUrl":"ftp://example.com/manifest.json"}}'
request POST /api/v1/knowledge-bases/kb-dev/connectors 400 "$tmp_dir/create-bad-type.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"name":"m5 bad type","type":"s3","config":{"manifestUrl":"https://example.com/manifest.json"}}'
request POST /api/v1/knowledge-bases/kb-dev/connectors 400 "$tmp_dir/create-bad-acl.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"name":"m5 bad acl","config":{"manifestUrl":"https://example.com/manifest.json","aclMode":"source_acl"}}'
request POST /api/v1/knowledge-bases/kb-dev/connectors 400 "$tmp_dir/create-bad-credential.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"name":"m5 bad credential","config":{"manifestUrl":"https://example.com/manifest.json"},"credential":{"type":"bearer"}}'

# ---------------------------------------------------------------------------
step "create connector: credential is sealed and never returned"
# ---------------------------------------------------------------------------
request POST /api/v1/knowledge-bases/kb-dev/connectors 201 "$tmp_dir/connector.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' --data-binary @"$tmp_dir/create.json"
connector_id="$(json_field "$tmp_dir/connector.json" id)"
assert_json "$tmp_dir/connector.json" 'data["id"].startswith("conn-") and data["tenantId"] == "dev-tenant" and data["knowledgeBaseId"] == "kb-dev" and data["type"] == "http_manifest" and data["hasCredential"] is True and data["config"]["aclMode"] == "knowledge_base" and "credential" not in data and "credentialRef" not in data'

credential_ref="$(psql_value "SELECT credential_ref FROM connectors WHERE id = '$connector_id';")"
[[ "$credential_ref" == v1:* ]] || fail "connector credential is not stored as a v1 envelope"
[[ "$credential_ref" != *"$fixture_token"* ]] || fail "the raw connector token was stored"
[[ "$(psql_value "SELECT count(*) FROM connectors WHERE credential_ref LIKE '%$fixture_token%';")" == "0" ]] || fail "the raw connector token is present in the database"

request GET "/api/v1/knowledge-bases/kb-dev/connectors" 200 "$tmp_dir/connectors.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/connectors.json" 'any(item["id"] == "'"$connector_id"'" and item["hasCredential"] is True and "credential" not in item for item in data["connectors"])'
request GET "/api/v1/knowledge-bases/kb-dev/connectors" 200 "$tmp_dir/connectors-viewer.json" -H "Authorization: Bearer $viewer_token"
request GET "/api/v1/connectors/$connector_id" 200 "$tmp_dir/connector-viewer.json" -H "Authorization: Bearer $viewer_token"
request GET "/api/v1/connectors/$connector_id" 404 "$tmp_dir/connector-other.json" -H "Authorization: Bearer $other_token"
request POST "/api/v1/connectors/$connector_id/sync" 403 "$tmp_dir/sync-viewer.json" -H "Authorization: Bearer $viewer_token"
request POST "/api/v1/connectors/$connector_id/cancel" 403 "$tmp_dir/cancel-viewer.json" -H "Authorization: Bearer $viewer_token"
request DELETE "/api/v1/connectors/$connector_id" 403 "$tmp_dir/delete-viewer.json" -H "Authorization: Bearer $viewer_token"
request POST "/api/v1/connectors/$connector_id/sync" 404 "$tmp_dir/sync-other.json" -H "Authorization: Bearer $other_token"
request POST /api/v1/knowledge-bases/kb-dev/sync/cancel 403 "$tmp_dir/cancel-other.json" -H "Authorization: Bearer $other_token"

# ---------------------------------------------------------------------------
step "first full sync: pipeline-ready documents, one active run, no duplicates"
# ---------------------------------------------------------------------------
printf '1500\n' > "$fixture_root/delay_ms"
trigger_sync "$tmp_dir/run1.json"
run1="$(json_field "$tmp_dir/run1.json" id)"
assert_json "$tmp_dir/run1.json" 'data["status"] == "PENDING" and data["mode"] == "manual" and data["connectorId"] == "'"$connector_id"'"'
wait_run_running "$connector_id" "$run1" "$tmp_dir/run1-running.json"
request POST "/api/v1/connectors/$connector_id/sync" 409 "$tmp_dir/run-conflict.json" -H "Authorization: Bearer $token"
wait_run_status "$connector_id" "$run1" SUCCEEDED "$tmp_dir/run1-done.json"
printf '0\n' > "$fixture_root/delay_ms"
assert_json "$tmp_dir/run1-done.json" 'data["itemsTotal"] == 3 and data["itemsCreated"] == 3 and data["itemsUpdated"] == 0 and data["itemsDeleted"] == 0 and data["itemsSkipped"] == 0 and data["itemsFailed"] == 0 and data["cursor"]["phase"] == "done"'

alpha_doc="$(psql_value "SELECT document_id FROM connector_items WHERE connector_id = '$connector_id' AND external_id = '$alpha_id';")"
beta_doc="$(psql_value "SELECT document_id FROM connector_items WHERE connector_id = '$connector_id' AND external_id = '$beta_id';")"
gamma_doc="$(psql_value "SELECT document_id FROM connector_items WHERE connector_id = '$connector_id' AND external_id = '$gamma_id';")"
for document_id in "$alpha_doc" "$beta_doc" "$gamma_doc"; do
  [[ "$document_id" =~ ^doc-[0-9a-f]{24}$ ]] || fail "connector did not create a deterministic document id: $document_id"
done
[[ "$(printf '%s\n%s\n%s\n' "$alpha_doc" "$beta_doc" "$gamma_doc" | sort -u | wc -l | tr -d ' ')" == "3" ]] || fail "different connector items share a document id"
synced_docs=("$alpha_doc" "$beta_doc" "$gamma_doc")

for document_id in "${synced_docs[@]}"; do
  wait_pipeline_status "$document_id" EVIDENCE_READY "$tmp_dir/doc-ready-$document_id.json"
done
for marker in "M5-ALPHA-$run_id" "M5-BETA-$run_id" "M5-GAMMA-$run_id"; do
  search "$token" "$marker" 200 "$tmp_dir/search-$marker.json"
done
search "$token" "M5-ALPHA-$run_id" 200 "$tmp_dir/search-alpha.json"
assert_search_has_document "$tmp_dir/search-alpha.json" "$alpha_doc"
search "$token" "M5-BETA-$run_id" 200 "$tmp_dir/search-beta.json"
assert_search_has_document "$tmp_dir/search-beta.json" "$beta_doc"
search "$token" "M5-GAMMA-$run_id" 200 "$tmp_dir/search-gamma.json"
assert_search_has_document "$tmp_dir/search-gamma.json" "$gamma_doc"

for document_id in "${synced_docs[@]}"; do
  [[ "$(psql_value "SELECT count(*) FROM document_versions dv JOIN pipeline_runs pr ON pr.document_version_id = dv.id AND pr.pipeline_type = 'evidence' JOIN stage_runs sr ON sr.pipeline_run_id = pr.id AND sr.stage = 'embed' AND sr.status = 'SUCCEEDED' JOIN evidence_packages ep ON ep.document_version_id = dv.id AND ep.status = 'EVIDENCE_READY' WHERE dv.document_id = '$document_id';")" -ge 1 ]] || fail "document $document_id was not produced by the evidence pipeline"
  [[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = (SELECT latest_version_id FROM documents WHERE id = '$document_id') AND embedding_dimension > 0 AND status = 'EVIDENCE_READY';")" -ge 1 ]] || fail "document $document_id has no pipeline-generated embeddings"
done
[[ "$(psql_value "SELECT count(*) FROM information_schema.columns WHERE table_name = 'connector_items' AND column_name IN ('embedding', 'chunk_id');")" == "0" ]] || fail "connector_items must not hold index data"

# ---------------------------------------------------------------------------
step "permission mapping: synced documents inherit the knowledge base ACL"
# ---------------------------------------------------------------------------
request GET "/api/v1/connectors/$connector_id/permissions" 200 "$tmp_dir/permissions.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/permissions.json" 'data["connectorId"] == "'"$connector_id"'" and data["knowledgeBaseId"] == "kb-dev" and data["sourceAcl"] == "none" and data["effectiveAcl"] == "knowledge_base_members" and data["aclMode"] == "knowledge_base" and data["aclVersion"] > 0 and any(member["userId"] == "dev-user" for member in data["members"]) and any(member["userId"] == "viewer-a" for member in data["members"])'
request GET "/api/v1/connectors/$connector_id/permissions" 200 "$tmp_dir/permissions-viewer.json" -H "Authorization: Bearer $viewer_token"
request GET "/api/v1/connectors/$connector_id/permissions" 404 "$tmp_dir/permissions-other.json" -H "Authorization: Bearer $other_token"

# ---------------------------------------------------------------------------
step "incremental sync: changed item gets a new version, new item is created"
# ---------------------------------------------------------------------------
printf '# M5 connector fixture alpha v2\n\nThe marker M5-ALPHA2-%s aurora belongs to the updated external item.\n\nSecond paragraph for context expansion.\n' "$run_id" > "$fixture_root/files/alpha.md"
write_manifest "$alpha_id|alpha.md|2" "$beta_id|beta.txt|1" "$gamma_id|gamma.md|1" "$delta_id|delta.md|1"
trigger_sync "$tmp_dir/run2.json"
run2="$(json_field "$tmp_dir/run2.json" id)"
wait_run_status "$connector_id" "$run2" SUCCEEDED "$tmp_dir/run2-done.json"
assert_json "$tmp_dir/run2-done.json" 'data["itemsTotal"] == 4 and data["itemsCreated"] == 1 and data["itemsUpdated"] == 1 and data["itemsSkipped"] == 2 and data["itemsDeleted"] == 0 and data["itemsFailed"] == 0'
[[ "$(psql_value "SELECT document_id FROM connector_items WHERE connector_id = '$connector_id' AND external_id = '$alpha_id';")" == "$alpha_doc" ]] || fail "incremental update created a duplicate document"
delta_doc="$(psql_value "SELECT document_id FROM connector_items WHERE connector_id = '$connector_id' AND external_id = '$delta_id';")"
[[ "$(psql_value "SELECT count(*) FROM document_versions WHERE document_id = '$alpha_doc';")" == "2" ]] || fail "incremental update did not create exactly one new version"
[[ "$(psql_value "SELECT count(*) FROM document_versions WHERE document_id = '$delta_doc';")" == "1" ]] || fail "new external item did not create one version"
synced_docs+=("$delta_doc")
wait_pipeline_status "$alpha_doc" EVIDENCE_READY "$tmp_dir/alpha-v2-ready.json"
wait_pipeline_status "$delta_doc" EVIDENCE_READY "$tmp_dir/delta-ready.json"
search "$token" "M5-ALPHA2-$run_id" 200 "$tmp_dir/search-alpha2.json"
assert_search_has_document "$tmp_dir/search-alpha2.json" "$alpha_doc"
search "$token" "M5-DELTA-$run_id" 200 "$tmp_dir/search-delta.json"
assert_search_has_document "$tmp_dir/search-delta.json" "$delta_doc"

# ---------------------------------------------------------------------------
step "deletion tombstone: removed item is soft-deleted, then restored by reappearance"
# ---------------------------------------------------------------------------
write_manifest "$alpha_id|alpha.md|2" "$beta_id|beta.txt|1" "$delta_id|delta.md|1"
trigger_sync "$tmp_dir/run3.json"
run3="$(json_field "$tmp_dir/run3.json" id)"
wait_run_status "$connector_id" "$run3" SUCCEEDED "$tmp_dir/run3-done.json"
assert_json "$tmp_dir/run3-done.json" 'data["itemsTotal"] == 3 and data["itemsDeleted"] == 1 and data["itemsSkipped"] == 3 and data["itemsCreated"] == 0 and data["itemsUpdated"] == 0'
[[ "$(psql_value "SELECT status FROM connector_items WHERE connector_id = '$connector_id' AND external_id = '$gamma_id';")" == "TOMBSTONED" ]] || fail "removed item was not tombstoned"
[[ "$(psql_value "SELECT count(*) FROM documents WHERE id = '$gamma_doc' AND deleted_at IS NOT NULL AND status = 'CANCELLED';")" == "1" ]] || fail "removed item did not soft-delete its document"
request GET "/api/v1/documents/$gamma_doc" 404 "$tmp_dir/gamma-deleted.json" -H "Authorization: Bearer $token"
search "$token" "M5-GAMMA-$run_id" 200 "$tmp_dir/search-gamma-deleted.json"
assert_search_excludes_document "$tmp_dir/search-gamma-deleted.json" "$gamma_doc"

gamma_versions_before="$(psql_value "SELECT count(*) FROM document_versions WHERE document_id = '$gamma_doc';")"
write_manifest "$alpha_id|alpha.md|2" "$beta_id|beta.txt|1" "$gamma_id|gamma.md|1" "$delta_id|delta.md|1"
trigger_sync "$tmp_dir/run4.json"
run4="$(json_field "$tmp_dir/run4.json" id)"
wait_run_status "$connector_id" "$run4" SUCCEEDED "$tmp_dir/run4-done.json"
assert_json "$tmp_dir/run4-done.json" 'data["itemsUpdated"] == 1 and data["itemsSkipped"] == 3 and data["itemsDeleted"] == 0'
[[ "$(psql_value "SELECT document_id FROM connector_items WHERE connector_id = '$connector_id' AND external_id = '$gamma_id';")" == "$gamma_doc" ]] || fail "reappearing item created a duplicate document"
[[ "$(psql_value "SELECT count(*) FROM connector_items WHERE connector_id = '$connector_id' AND external_id = '$gamma_id' AND status = 'ACTIVE';")" == "1" ]] || fail "reappearing item did not reactivate its mapping"
[[ "$(psql_value "SELECT count(*) FROM documents WHERE id = '$gamma_doc' AND deleted_at IS NULL AND status = 'EVIDENCE_READY';")" == "1" ]] || fail "reappearing item did not restore a searchable document"
[[ "$(psql_value "SELECT count(*) FROM document_versions WHERE document_id = '$gamma_doc';")" == "$gamma_versions_before" ]] || fail "reappearing item duplicated its version"
search "$token" "M5-GAMMA-$run_id" 200 "$tmp_dir/search-gamma-restored.json"
assert_search_has_document "$tmp_dir/search-gamma-restored.json" "$gamma_doc"

# ---------------------------------------------------------------------------
step "permission change: revoking a member hides synced content immediately"
# ---------------------------------------------------------------------------
search "$viewer_token" "M5-ALPHA2-$run_id" 200 "$tmp_dir/viewer-before.json"
assert_search_has_document "$tmp_dir/viewer-before.json" "$alpha_doc"
revision_before="$(psql_value "SELECT COALESCE((SELECT revision FROM authorization_revisions WHERE tenant_id = 'dev-tenant'), 0);")"
request DELETE /api/v1/knowledge-bases/kb-dev/members/viewer-a 204 "$tmp_dir/remove-viewer.json" -H "Authorization: Bearer $token"
revision_after="$(psql_value "SELECT COALESCE((SELECT revision FROM authorization_revisions WHERE tenant_id = 'dev-tenant'), 0);")"
[[ "$revision_after" -gt "$revision_before" ]] || fail "removing a member did not bump the authorization revision"
search "$viewer_token" "M5-ALPHA2-$run_id" 403 "$tmp_dir/viewer-revoked.json"
request GET "/api/v1/connectors/$connector_id" 403 "$tmp_dir/viewer-revoked-connector.json" -H "Authorization: Bearer $viewer_token"
request GET "/api/v1/connectors/$connector_id/permissions" 403 "$tmp_dir/viewer-revoked-permissions.json" -H "Authorization: Bearer $viewer_token"
request POST /api/v1/knowledge-bases/kb-dev/members 201 "$tmp_dir/readd-viewer.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"user_id":"viewer-a","role":"viewer"}'
search "$viewer_token" "M5-ALPHA2-$run_id" 200 "$tmp_dir/viewer-regranted.json"
assert_search_has_document "$tmp_dir/viewer-regranted.json" "$alpha_doc"
request GET "/api/v1/connectors/$connector_id/permissions" 200 "$tmp_dir/permissions-regranted.json" -H "Authorization: Bearer $viewer_token"
assert_json "$tmp_dir/permissions-regranted.json" 'any(member["userId"] == "viewer-a" for member in data["members"])'

# ---------------------------------------------------------------------------
step "authorization: API keys can read but cannot operate connectors"
# ---------------------------------------------------------------------------
request POST /api/v1/api-keys 201 "$tmp_dir/key.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"name":"m5 acceptance read","capabilities":["knowledge_base:read"],"knowledgeBaseIds":["kb-dev"]}'
key_id="$(json_field "$tmp_dir/key.json" apiKey.id)"
key_token="$(json_field "$tmp_dir/key.json" token)"
request GET "/api/v1/connectors/$connector_id" 200 "$tmp_dir/key-connector.json" -H "Authorization: Bearer $key_token"
request GET "/api/v1/connectors/$connector_id/runs" 200 "$tmp_dir/key-runs.json" -H "Authorization: Bearer $key_token"
request POST "/api/v1/connectors/$connector_id/sync" 403 "$tmp_dir/key-sync.json" -H "Authorization: Bearer $key_token"
request DELETE "/api/v1/api-keys/$key_id" 204 "$tmp_dir/key-delete.json" -H "Authorization: Bearer $token"

# ---------------------------------------------------------------------------
step "interruption: run-level cancel preserves the cursor and retry resumes"
# ---------------------------------------------------------------------------
printf '# M5 connector fixture alpha generation 10\n\nThe marker M5-ALPHA-G10-%s belongs to generation ten.\n\nSecond paragraph for context expansion.\n' "$run_id" > "$fixture_root/files/alpha.md"
printf '# M5 connector fixture beta generation 10\n\nThe marker M5-BETA-G10-%s belongs to generation ten.\n\nSecond paragraph for context expansion.\n' "$run_id" > "$fixture_root/files/beta.txt"
printf '# M5 connector fixture gamma generation 10\n\nThe marker M5-GAMMA-G10-%s belongs to generation ten.\n\nSecond paragraph for context expansion.\n' "$run_id" > "$fixture_root/files/gamma.md"
printf '# M5 connector fixture delta generation 10\n\nThe marker M5-DELTA-G10-%s belongs to generation ten.\n\nSecond paragraph for context expansion.\n' "$run_id" > "$fixture_root/files/delta.md"
write_manifest "$alpha_id|alpha.md|10" "$beta_id|beta.txt|10" "$gamma_id|gamma.md|10" "$delta_id|delta.md|10"
printf '1500\n' > "$fixture_root/delay_ms"
trigger_sync "$tmp_dir/run5.json"
run5="$(json_field "$tmp_dir/run5.json" id)"
wait_run_running "$connector_id" "$run5" "$tmp_dir/run5-running.json"
wait_run_progress "$connector_id" "$run5" "$tmp_dir/run5-progress.json"
request POST "/api/v1/connectors/$connector_id/runs/$run5/cancel" 202 "$tmp_dir/run5-cancel.json" -H "Authorization: Bearer $token"
wait_run_status "$connector_id" "$run5" CANCELLED "$tmp_dir/run5-cancelled.json"
assert_json "$tmp_dir/run5-cancelled.json" 'data["cancelRequested"] is True and data["cursor"]["position"] >= 1'
documents_before_resume="$(psql_value "SELECT count(*) FROM documents WHERE id IN ($(doc_ids_sql));")"
printf '0\n' > "$fixture_root/delay_ms"
request POST "/api/v1/connectors/$connector_id/runs/$run5/retry" 202 "$tmp_dir/run6.json" -H "Authorization: Bearer $token"
run6="$(json_field "$tmp_dir/run6.json" id)"
assert_json "$tmp_dir/run6.json" 'data["mode"] == "resume" and data["resumeOfRunId"] == "'"$run5"'" and data["cursor"]["position"] >= 1'
wait_run_status "$connector_id" "$run6" SUCCEEDED "$tmp_dir/run6-done.json"
[[ "$(psql_value "SELECT count(*) FROM documents WHERE id IN ($(doc_ids_sql));")" == "$documents_before_resume" ]] || fail "resumed run created duplicate documents"
for document_id in "${synced_docs[@]}"; do
  wait_pipeline_status "$document_id" EVIDENCE_READY "$tmp_dir/resumed-ready-$document_id.json"
done
request POST "/api/v1/connectors/$connector_id/runs/$run1/retry" 409 "$tmp_dir/retry-succeeded.json" -H "Authorization: Bearer $token"

# ---------------------------------------------------------------------------
step "connector-level and knowledge-base-level cancellation resume too"
# ---------------------------------------------------------------------------
bump_and_trigger() {
  local generation="$1" trigger_output="$2"
  printf '# M5 connector fixture alpha generation %s\n\nThe marker M5-ALPHA-G%s-%s belongs to a later generation.\n\nSecond paragraph for context expansion.\n' "$generation" "$generation" "$run_id" > "$fixture_root/files/alpha.md"
  printf '# M5 connector fixture beta generation %s\n\nThe marker M5-BETA-G%s-%s belongs to a later generation.\n\nSecond paragraph for context expansion.\n' "$generation" "$generation" "$run_id" > "$fixture_root/files/beta.txt"
  printf '# M5 connector fixture gamma generation %s\n\nThe marker M5-GAMMA-G%s-%s belongs to a later generation.\n\nSecond paragraph for context expansion.\n' "$generation" "$generation" "$run_id" > "$fixture_root/files/gamma.md"
  printf '# M5 connector fixture delta generation %s\n\nThe marker M5-DELTA-G%s-%s belongs to a later generation.\n\nSecond paragraph for context expansion.\n' "$generation" "$generation" "$run_id" > "$fixture_root/files/delta.md"
  write_manifest "$alpha_id|alpha.md|$generation" "$beta_id|beta.txt|$generation" "$gamma_id|gamma.md|$generation" "$delta_id|delta.md|$generation"
  printf '1500\n' > "$fixture_root/delay_ms"
  trigger_sync "$trigger_output"
}

bump_and_trigger 11 "$tmp_dir/run7.json"
run7="$(json_field "$tmp_dir/run7.json" id)"
wait_run_running "$connector_id" "$run7" "$tmp_dir/run7-running.json"
wait_run_progress "$connector_id" "$run7" "$tmp_dir/run7-progress.json"
request POST "/api/v1/connectors/$connector_id/cancel" 202 "$tmp_dir/run7-cancel.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/run7-cancel.json" 'data["cancelled"] >= 1'
wait_run_status "$connector_id" "$run7" CANCELLED "$tmp_dir/run7-cancelled.json"
printf '0\n' > "$fixture_root/delay_ms"
request POST "/api/v1/connectors/$connector_id/runs/$run7/retry" 202 "$tmp_dir/run7-retry.json" -H "Authorization: Bearer $token"
run8="$(json_field "$tmp_dir/run7-retry.json" id)"
wait_run_status "$connector_id" "$run8" SUCCEEDED "$tmp_dir/run8-done.json"

bump_and_trigger 12 "$tmp_dir/run9.json"
run9="$(json_field "$tmp_dir/run9.json" id)"
wait_run_running "$connector_id" "$run9" "$tmp_dir/run9-running.json"
wait_run_progress "$connector_id" "$run9" "$tmp_dir/run9-progress.json"
request POST /api/v1/knowledge-bases/kb-dev/sync/cancel 202 "$tmp_dir/run9-cancel.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/run9-cancel.json" 'data["cancelled"] >= 1'
wait_run_status "$connector_id" "$run9" CANCELLED "$tmp_dir/run9-cancelled.json"
printf '0\n' > "$fixture_root/delay_ms"
request POST "/api/v1/connectors/$connector_id/runs/$run9/retry" 202 "$tmp_dir/run9-retry.json" -H "Authorization: Bearer $token"
run10="$(json_field "$tmp_dir/run9-retry.json" id)"
wait_run_status "$connector_id" "$run10" SUCCEEDED "$tmp_dir/run10-done.json"

# ---------------------------------------------------------------------------
step "repeatability: unchanged re-sync skips every item and adds no documents"
# ---------------------------------------------------------------------------
versions_before="$(psql_value "SELECT count(*) FROM document_versions WHERE document_id IN ($(doc_ids_sql));")"
documents_before="$(psql_value "SELECT count(*) FROM documents WHERE id IN ($(doc_ids_sql));")"
trigger_sync "$tmp_dir/run11.json"
run11="$(json_field "$tmp_dir/run11.json" id)"
wait_run_status "$connector_id" "$run11" SUCCEEDED "$tmp_dir/run11-done.json"
assert_json "$tmp_dir/run11-done.json" 'data["itemsTotal"] == 4 and data["itemsSkipped"] == 4 and data["itemsCreated"] == 0 and data["itemsUpdated"] == 0 and data["itemsDeleted"] == 0 and data["itemsFailed"] == 0'
[[ "$(psql_value "SELECT count(*) FROM documents WHERE id IN ($(doc_ids_sql));")" == "$documents_before" ]] || fail "repeat sync created documents"
[[ "$(psql_value "SELECT count(*) FROM document_versions WHERE document_id IN ($(doc_ids_sql));")" == "$versions_before" ]] || fail "repeat sync created document versions"
[[ "$(psql_value "SELECT count(*) FROM (SELECT document_id, content_hash FROM document_versions WHERE document_id IN ($(doc_ids_sql)) GROUP BY document_id, content_hash HAVING count(*) > 1) duplicated;")" == "0" ]] || fail "duplicate document versions exist"
[[ "$(psql_value "SELECT count(DISTINCT document_id) FROM connector_items WHERE connector_id = '$connector_id' AND status = 'ACTIVE';")" == "4" ]] || fail "connector mappings do not point at four distinct documents"
[[ "$(psql_value "SELECT count(*) FROM connector_items WHERE connector_id = '$connector_id' AND status = 'ACTIVE';")" == "4" ]] || fail "connector mappings are not one per item"
[[ "$(psql_value "SELECT count(*) FROM connectors WHERE id = '$connector_id' AND last_sync_at IS NOT NULL AND next_sync_at IS NULL;")" == "1" ]] || fail "connector sync timestamps were not updated"

request GET "/api/v1/connectors/$connector_id/runs?limit=50" 200 "$tmp_dir/runs.json" -H "Authorization: Bearer $token"
python3 - "$tmp_dir/runs.json" "$run1" "$run2" "$run3" "$run4" "$run5" "$run6" "$run7" "$run8" "$run9" "$run10" "$run11" <<'PY'
import json
import sys

runs = json.load(open(sys.argv[1]))["runs"]
ids = [run["id"] for run in runs]
for expected in sys.argv[2:]:
    assert expected in ids, (expected, ids)
assert ids[0] == sys.argv[-1], ids[:3]
assert any(run["mode"] == "resume" for run in runs), runs[:3]
assert all(run["status"] in ("SUCCEEDED", "PARTIAL", "FAILED", "CANCELLED") for run in runs)
created = [run["createdAt"] for run in runs]
assert created == sorted(created, reverse=True), created
PY
assert_json "$tmp_dir/runs.json" 'any(run["id"] == "'"$run5"'" and run["status"] == "CANCELLED" and run["cancelRequested"] is True and run["cursor"]["position"] >= 1 for run in data["runs"]) and any(run["id"] == "'"$run6"'" and run["resumeOfRunId"] == "'"$run5"'" and run["mode"] == "resume" for run in data["runs"])'

outbox_count="$(psql_value "SELECT count(*) FROM outbox_events WHERE event_type = 'connector.sync' AND aggregate_id = '$connector_id';")"
published_count="$(psql_value "SELECT count(*) FROM outbox_events WHERE event_type = 'connector.sync' AND aggregate_id = '$connector_id' AND published_at IS NOT NULL;")"
[[ "$outbox_count" -ge 11 ]] || fail "durable connector.sync outbox events are missing"
[[ "$outbox_count" == "$published_count" ]] || fail "connector.sync outbox events were not published"

request GET "/api/v1/audit?resourceType=connector&limit=100" 200 "$tmp_dir/audit.json" -H "Authorization: Bearer $token"
python3 - "$tmp_dir/audit.json" "$connector_id" <<'PY'
import json
import sys

rows = json.load(open(sys.argv[1]))["items"]
connector_id = sys.argv[2]
reasons = [row["reason"] for row in rows if row["resourceId"] == connector_id and row["allowed"]]
assert any(reason.startswith("connector_created") for reason in reasons), reasons
assert any(reason.startswith("sync_triggered:") for reason in reasons), reasons
assert any(reason.startswith("sync_cancelled") for reason in reasons), reasons
assert any(reason.startswith("sync_retried:") for reason in reasons), reasons
PY

# ---------------------------------------------------------------------------
step "empty manifest: removing every item tombstones all synced documents"
# ---------------------------------------------------------------------------
write_manifest
trigger_sync "$tmp_dir/run12.json"
run12="$(json_field "$tmp_dir/run12.json" id)"
wait_run_status "$connector_id" "$run12" SUCCEEDED "$tmp_dir/run12-done.json"
assert_json "$tmp_dir/run12-done.json" 'data["itemsTotal"] == 0 and data["itemsCreated"] == 0 and data["itemsUpdated"] == 0 and data["itemsDeleted"] == 4 and data["itemsSkipped"] == 0 and data["itemsFailed"] == 0 and data["cursor"]["phase"] == "done"'
[[ "$(psql_value "SELECT count(*) FROM connector_items WHERE connector_id = '$connector_id' AND status = 'TOMBSTONED';")" == "4" ]] || fail "empty manifest did not tombstone every item"
[[ "$(psql_value "SELECT count(*) FROM connector_items WHERE connector_id = '$connector_id' AND status = 'ACTIVE';")" == "0" ]] || fail "empty manifest left active mappings"
[[ "$(psql_value "SELECT count(*) FROM documents WHERE id IN ($(doc_ids_sql)) AND deleted_at IS NOT NULL;")" == "4" ]] || fail "empty manifest did not soft-delete every document"
search "$token" "M5-ALPHA2-$run_id" 200 "$tmp_dir/search-alpha-empty.json"
assert_search_excludes_document "$tmp_dir/search-alpha-empty.json" "$alpha_doc"
request GET "/api/v1/documents/$alpha_doc" 404 "$tmp_dir/alpha-empty.json" -H "Authorization: Bearer $token"

# ---------------------------------------------------------------------------
step "full reappearance: the same snapshot restores documents without duplicates"
# ---------------------------------------------------------------------------
versions_before_restore="$(psql_value "SELECT count(*) FROM document_versions WHERE document_id IN ($(doc_ids_sql));")"
documents_before_restore="$(psql_value "SELECT count(*) FROM documents WHERE id IN ($(doc_ids_sql));")"
write_manifest "$alpha_id|alpha.md|2" "$beta_id|beta.txt|1" "$gamma_id|gamma.md|1" "$delta_id|delta.md|1"
trigger_sync "$tmp_dir/run13.json"
run13="$(json_field "$tmp_dir/run13.json" id)"
wait_run_status "$connector_id" "$run13" SUCCEEDED "$tmp_dir/run13-done.json"
assert_json "$tmp_dir/run13-done.json" 'data["itemsTotal"] == 4 and data["itemsCreated"] == 0 and data["itemsUpdated"] == 4 and data["itemsDeleted"] == 0 and data["itemsSkipped"] == 0 and data["itemsFailed"] == 0'
[[ "$(psql_value "SELECT count(*) FROM documents WHERE id IN ($(doc_ids_sql)) AND deleted_at IS NULL;")" == "4" ]] || fail "full reappearance did not restore every document"
[[ "$(psql_value "SELECT count(*) FROM documents WHERE id IN ($(doc_ids_sql));")" == "$documents_before_restore" ]] || fail "full reappearance created duplicate documents"
[[ "$(psql_value "SELECT count(*) FROM document_versions WHERE document_id IN ($(doc_ids_sql));")" == "$versions_before_restore" ]] || fail "full reappearance created duplicate versions"
[[ "$(psql_value "SELECT count(*) FROM connector_items WHERE connector_id = '$connector_id' AND status = 'ACTIVE';")" == "4" ]] || fail "full reappearance did not reactivate all mappings"
for document_id in "${synced_docs[@]}"; do
  wait_pipeline_status "$document_id" EVIDENCE_READY "$tmp_dir/restored-ready-$document_id.json"
done
search "$token" "M5-ALPHA2-$run_id" 200 "$tmp_dir/search-alpha-restored.json"
assert_search_has_document "$tmp_dir/search-alpha-restored.json" "$alpha_doc"

# ---------------------------------------------------------------------------
step "scheduling configuration: enabling an interval sets the next slot"
# ---------------------------------------------------------------------------
request PATCH "/api/v1/connectors/$connector_id" 200 "$tmp_dir/patch-interval.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"syncIntervalSeconds":60}'
assert_json "$tmp_dir/patch-interval.json" 'data["syncIntervalSeconds"] == 60 and "nextSyncAt" in data and data["nextSyncAt"]'
request PATCH "/api/v1/connectors/$connector_id" 200 "$tmp_dir/patch-disable.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"syncIntervalSeconds":0,"enabled":false}'
assert_json "$tmp_dir/patch-disable.json" 'data["syncIntervalSeconds"] == 0 and data["enabled"] is False and "nextSyncAt" not in data'
request POST "/api/v1/connectors/$connector_id/sync" 400 "$tmp_dir/sync-disabled.json" -H "Authorization: Bearer $token"
request PATCH "/api/v1/connectors/$connector_id" 200 "$tmp_dir/patch-enable.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"enabled":true}'
assert_json "$tmp_dir/patch-enable.json" 'data["enabled"] is True'

# ---------------------------------------------------------------------------
step "cleanup: purge synchronized documents and remove the connector"
# ---------------------------------------------------------------------------
for document_id in "${synced_docs[@]}"; do
  curl -sS -o /dev/null -X DELETE "$base_url/api/v1/documents/$document_id" -H "Authorization: Bearer $token" || true
  curl -sS -o /dev/null -X POST "$base_url/api/v1/documents/$document_id/purge" -H "Authorization: Bearer $token" || true
done
request DELETE "/api/v1/connectors/$connector_id" 204 "$tmp_dir/connector-delete.json" -H "Authorization: Bearer $token"
[[ "$(psql_value "SELECT count(*) FROM connectors WHERE id = '$connector_id';")" == "0" ]] || fail "connector delete left the connector row"
[[ "$(psql_value "SELECT count(*) FROM connector_items WHERE connector_id = '$connector_id';")" == "0" ]] || fail "connector delete left item mappings"
[[ "$(psql_value "SELECT count(*) FROM sync_runs WHERE connector_id = '$connector_id';")" == "0" ]] || fail "connector delete left sync runs"

printf '\nM5 acceptance passed: connector=%s runs=%s..%s documents=%s run=%s\n' \
  "$connector_id" "$run1" "$run11" "$(doc_ids_sql)" "$run_id"
