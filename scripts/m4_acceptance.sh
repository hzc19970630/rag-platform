#!/usr/bin/env bash
# M4 acceptance: authentication (OIDC/API key/session), authorization
# revocation, document lifecycle, outbox/inbox reliability and operator
# observability. It mutates local stack state (restarts redis/worker, inserts
# simulated failures) and must run against the development compose stack.
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
bucket="${S3_BUCKET:-rag-platform-local}"
worker_stopped=0

cleanup() {
  rm -rf "$tmp_dir"
  if [[ "$worker_stopped" == "1" ]]; then
    "${compose[@]}" start worker >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'M4 acceptance failed: %s\n' "$*" >&2; exit 1; }

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
if not eval(expression, {"__builtins__": {}}, {"data": data, "len": len, "any": any, "all": all, "str": str}):
    raise SystemExit(f"assertion failed: {expression}\n{json.dumps(data)[:2000]}")
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

search() {
  local token="$1" query="$2" knowledge_base="$3" expected="$4" output="$5"
  request POST /api/v1/search "$expected" "$output" \
    -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -d "{\"query\":\"$query\",\"knowledgeBaseId\":\"$knowledge_base\"}"
}

wait_pipeline_status() {
  local document_id="$1" token="$2" expected="$3" output="$4"
  local status=""
  for _ in $(seq 1 60); do
    request GET "/api/v1/documents/$document_id" 200 "$output" -H "Authorization: Bearer $token"
    status="$(json_field "$output" pipeline.status)"
    [[ "$status" == "$expected" ]] && return 0
    if [[ "$status" == "FAILED" && "$expected" != "FAILED" ]]; then
      # A replayed task leaves the pipeline FAILED until the worker claims the
      # stage, so only fail fast once a stage itself reports FAILED.
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

# ---------------------------------------------------------------------------
step "preflight: readiness and migration compatibility"
# ---------------------------------------------------------------------------
curl -fsS "$base_url/readyz" | grep -q '"status":"ok"' || fail "api is not ready"
"${compose[@]}" exec -T redis redis-cli ping | grep -q PONG || fail "redis is not ready"
"${compose[@]}" exec -T minio mc alias set m4accept http://127.0.0.1:9000 "${MINIO_ROOT_USER:-rag-local}" "${MINIO_ROOT_PASSWORD:-change-me-local-only}" >/dev/null

migrate_output="$("${compose[@]}" run --rm --no-deps migrate -check 2>&1)"
printf '%s\n' "$migrate_output" | grep -q migrations_compatible || { printf '%s\n' "$migrate_output" >&2; fail "migration -check did not report compatibility"; }
migration_count="$(psql_value "SELECT count(*) FROM schema_migrations;")"
checksum_count="$(psql_value "SELECT count(*) FROM schema_migrations WHERE checksum IS NOT NULL;")"
[[ "$migration_count" -gt 0 && "$migration_count" == "$checksum_count" ]] || fail "schema_migrations checksums are incomplete"

# ---------------------------------------------------------------------------
step "authentication: config, invalid credentials, OIDC-disabled surface"
# ---------------------------------------------------------------------------
request GET /api/v1/auth/config 200 "$tmp_dir/auth-config.json"
assert_json "$tmp_dir/auth-config.json" 'data["oidcEnabled"] is False and data["devAuthEnabled"] is True and data["sessionCookie"] != "" and data["sessionTTLHours"] > 0'

request GET /api/v1/me 401 "$tmp_dir/invalid-bearer.json" -H 'Authorization: Bearer definitely-not-a-token'
request GET /api/v1/auth/oidc/login 404 "$tmp_dir/oidc-disabled.json"

callback_code="$(curl -sS -o /dev/null -w '%{http_code}' "$base_url/api/v1/auth/callback?code=fake&state=missing")"
[[ "$callback_code" == "302" ]] || fail "callback without state returned $callback_code"
curl -sS -D "$tmp_dir/callback.headers" -o /dev/null "$base_url/api/v1/auth/callback?code=fake&state=missing"
grep -i '^location:.*error=state_mismatch' "$tmp_dir/callback.headers" >/dev/null || fail "callback did not reject a missing OIDC state"

# ---------------------------------------------------------------------------
step "session lifecycle: login, bearer/cookie authentication, refresh, logout"
# ---------------------------------------------------------------------------
request POST /api/v1/auth/dev-login 200 "$tmp_dir/login.json" \
  -H 'Content-Type: application/json' -c "$tmp_dir/cookies.txt" \
  -d '{"userId":"dev-user","tenantId":"dev-tenant"}'
token="$(json_field "$tmp_dir/login.json" token)"
[[ -n "$token" ]]
assert_json "$tmp_dir/login.json" 'data["principal"]["credentialType"] == "session" and data["principal"]["tenantId"] == "dev-tenant"'

request GET /api/v1/me 200 "$tmp_dir/me-bearer.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/me-bearer.json" 'data["principal"]["credentialType"] == "session" and data["principal"]["subjectId"] == "dev-user" and "kb-dev" in data["scope"]["knowledgeBaseIds"]'

request GET /api/v1/me 200 "$tmp_dir/me-cookie.json" -b "$tmp_dir/cookies.txt"
assert_json "$tmp_dir/me-cookie.json" 'data["principal"]["credentialType"] == "session"'

request POST /api/v1/auth/refresh 200 "$tmp_dir/refresh.json" -H "Authorization: Bearer $token" -c "$tmp_dir/cookies.txt"
refreshed_token="$(json_field "$tmp_dir/refresh.json" token)"
[[ -n "$refreshed_token" ]]
request GET /api/v1/me 200 "$tmp_dir/me-refreshed.json" -H "Authorization: Bearer $refreshed_token"
token="$refreshed_token"

# ---------------------------------------------------------------------------
step "API keys: scoped capabilities, hashing, audit and negative paths"
# ---------------------------------------------------------------------------
key_request_id="m4-key-create-$run_id"
request POST /api/v1/api-keys 201 "$tmp_dir/api-key.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -H "X-Request-ID: $key_request_id" \
  -d '{"name":"m4 acceptance read","capabilities":["knowledge_base:read","chat"],"knowledgeBaseIds":["kb-dev"]}'
api_key_id="$(json_field "$tmp_dir/api-key.json" apiKey.id)"
api_key_token="$(json_field "$tmp_dir/api-key.json" token)"
assert_json "$tmp_dir/api-key.json" 'data["token"].startswith("rk_") and data["apiKey"]["capabilities"] == ["knowledge_base:read", "chat"] and data["apiKey"]["knowledgeBaseIds"] == ["kb-dev"] and "keyHash" not in data["apiKey"] and "token" not in data["apiKey"]'

[[ "$(psql_value "SELECT length(key_hash) FROM api_keys WHERE id = '$api_key_id';")" == "64" ]] || fail "api key hash is not a sha256 hex digest"
[[ "$(psql_value "SELECT count(*) FROM api_keys WHERE key_hash = '$api_key_token';")" == "0" ]] || fail "the raw api key was stored in the database"

request GET /api/v1/api-keys 200 "$tmp_dir/api-keys.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/api-keys.json" 'any(item["id"] == "'"$api_key_id"'" for item in data["items"]) and all("token" not in item and "keyHash" not in item for item in data["items"])'

request GET /api/v1/me 200 "$tmp_dir/key-me.json" -H "Authorization: Bearer $api_key_token"
assert_json "$tmp_dir/key-me.json" 'data["principal"]["credentialType"] == "api_key" and data["principal"]["subjectId"] == "dev-user"'

search "$api_key_token" "m4 acceptance probe" "kb-dev" 200 "$tmp_dir/key-search.json"
request POST /api/v1/search 403 "$tmp_dir/key-cross-kb.json" \
  -H "Authorization: Bearer $api_key_token" -H 'Content-Type: application/json' \
  -d '{"query":"probe","knowledgeBaseId":"kb-other"}'
denied_request_id="m4-key-denied-$run_id"
request POST /api/v1/knowledge-bases 403 "$tmp_dir/key-create-kb.json" \
  -H "Authorization: Bearer $api_key_token" -H 'Content-Type: application/json' -H "X-Request-ID: $denied_request_id" \
  -d '{"name":"m4 should not exist"}'
request POST /api/v1/api-keys 403 "$tmp_dir/key-mint.json" \
  -H "Authorization: Bearer $api_key_token" -H 'Content-Type: application/json' \
  -d '{"name":"nested key"}'
request GET /api/v1/ops/metrics 403 "$tmp_dir/key-ops.json" -H "Authorization: Bearer $api_key_token"
request POST /api/v1/api-keys 400 "$tmp_dir/key-bad-capability.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"name":"bad","capabilities":["knowledge_base:own_everything"]}'
request POST /api/v1/api-keys 400 "$tmp_dir/key-bad-kb.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"name":"bad","knowledgeBaseIds":["kb-does-not-exist"]}'
request POST /api/v1/api-keys 400 "$tmp_dir/key-past-expiry.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"name":"bad","expiresAt":"2020-01-01T00:00:00Z"}'

request GET "/api/v1/audit?action=create&resourceType=api_key&limit=50" 200 "$tmp_dir/audit-key.json" -H "Authorization: Bearer $token"
python3 - "$tmp_dir/audit-key.json" "$key_request_id" <<'PY'
import json
import sys

rows = json.load(open(sys.argv[1]))["items"]
request_id = sys.argv[2]
assert any(row["requestId"] == request_id and row["allowed"] and row["resourceId"] for row in rows), rows[:5]
PY
request GET "/api/v1/audit?actorId=dev-user&action=write&resourceType=path&resourceId=/knowledge-bases&allowed=false&limit=50" 200 "$tmp_dir/audit-denied.json" -H "Authorization: Bearer $token"
python3 - "$tmp_dir/audit-denied.json" "$denied_request_id" <<'PY'
import json
import sys

rows = json.load(open(sys.argv[1]))["items"]
request_id = sys.argv[2]
assert any(row["requestId"] == request_id and not row["allowed"] and "api_key_capability_missing" in row["reason"] for row in rows), rows[:5]
PY

request DELETE "/api/v1/api-keys/$api_key_id" 204 "$tmp_dir/key-revoke.json" -H "Authorization: Bearer $token"
request GET /api/v1/me 401 "$tmp_dir/key-revoked.json" -H "Authorization: Bearer $api_key_token"

request POST /api/v1/api-keys 201 "$tmp_dir/api-key-expired.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"name":"m4 acceptance expired","capabilities":["knowledge_base:read"],"knowledgeBaseIds":["kb-dev"]}'
expired_id="$(json_field "$tmp_dir/api-key-expired.json" apiKey.id)"
expired_token="$(json_field "$tmp_dir/api-key-expired.json" token)"
"${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "UPDATE api_keys SET expires_at = NOW() - INTERVAL '1 hour' WHERE id = '$expired_id';" >/dev/null
request GET /api/v1/me 401 "$tmp_dir/key-expired.json" -H "Authorization: Bearer $expired_token"

# ---------------------------------------------------------------------------
step "knowledge base ingestion fixture for authorization and lifecycle"
# ---------------------------------------------------------------------------
printf '# M4 lifecycle\n\nM4 lifecycle marker %s zephyr quartz.\n\nSecond paragraph for context expansion.\n' "$run_id" > "$tmp_dir/lifecycle.md"
request POST /api/v1/knowledge-bases/kb-dev/documents 202 "$tmp_dir/upload.json" \
  -H "Authorization: Bearer $token" -H "Idempotency-Key: m4-lifecycle-$run_id" \
  -F "file=@$tmp_dir/lifecycle.md;type=text/markdown"
document_id="$(json_field "$tmp_dir/upload.json" document.id)"
version1="$(json_field "$tmp_dir/upload.json" version.id)"
pipeline1="$(json_field "$tmp_dir/upload.json" pipeline.id)"
wait_pipeline_status "$document_id" "$token" EVIDENCE_READY "$tmp_dir/status-ready.json"
marker="zephyr quartz $run_id"
search "$token" "$marker" "kb-dev" 200 "$tmp_dir/dev-search.json"
assert_json "$tmp_dir/dev-search.json" 'len(data["items"]) > 0 and all(item["knowledgeBaseId"] == "kb-dev" for item in data["items"])'

# ---------------------------------------------------------------------------
step "authorization: role revocation invalidates cached scope immediately"
# ---------------------------------------------------------------------------
request POST /api/v1/auth/dev-login 200 "$tmp_dir/login-viewer.json" \
  -H 'Content-Type: application/json' -d '{"userId":"viewer-a","tenantId":"dev-tenant"}'
viewer_token="$(json_field "$tmp_dir/login-viewer.json" token)"
request GET /api/v1/me 200 "$tmp_dir/viewer-me.json" -H "Authorization: Bearer $viewer_token"
assert_json "$tmp_dir/viewer-me.json" '"kb-dev" in data["scope"]["knowledgeBaseIds"]'
search "$viewer_token" "$marker" "kb-dev" 200 "$tmp_dir/viewer-before.json"
assert_json "$tmp_dir/viewer-before.json" 'len(data["items"]) > 0'

revision_before="$(psql_value "SELECT COALESCE((SELECT revision FROM authorization_revisions WHERE tenant_id = 'dev-tenant'), 0);")"
request DELETE /api/v1/knowledge-bases/kb-dev/members/viewer-a 204 "$tmp_dir/remove-viewer.json" -H "Authorization: Bearer $token"
revision_after="$(psql_value "SELECT COALESCE((SELECT revision FROM authorization_revisions WHERE tenant_id = 'dev-tenant'), 0);")"
[[ "$revision_after" -gt "$revision_before" ]] || fail "revocation did not bump the authorization revision"

search "$viewer_token" "$marker" "kb-dev" 403 "$tmp_dir/viewer-revoked.json"
request GET /api/v1/me 200 "$tmp_dir/viewer-me-revoked.json" -H "Authorization: Bearer $viewer_token"
assert_json "$tmp_dir/viewer-me-revoked.json" '"kb-dev" not in data["scope"]["knowledgeBaseIds"]'

request POST /api/v1/knowledge-bases/kb-dev/members 201 "$tmp_dir/readd-viewer.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"user_id":"viewer-a","role":"viewer"}'
search "$viewer_token" "$marker" "kb-dev" 200 "$tmp_dir/viewer-regranted.json"
assert_json "$tmp_dir/viewer-regranted.json" 'len(data["items"]) > 0'

# ---------------------------------------------------------------------------
step "document lifecycle: versions, reparse, rollback to the superseded version"
# ---------------------------------------------------------------------------
request GET "/api/v1/documents/$document_id/versions" 200 "$tmp_dir/versions-1.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/versions-1.json" 'len(data["items"]) == 1 and data["items"][0]["id"] == "'"$version1"'" and data["items"][0]["current"] is True'

request POST "/api/v1/documents/$document_id/reparse" 202 "$tmp_dir/reparse.json" -H "Authorization: Bearer $token"
version2="$(json_field "$tmp_dir/reparse.json" version.id)"
pipeline2="$(json_field "$tmp_dir/reparse.json" pipeline.id)"
[[ "$version2" != "$version1" ]]
wait_pipeline_status "$document_id" "$token" EVIDENCE_READY "$tmp_dir/status-reparsed.json"
[[ "$(json_field "$tmp_dir/status-reparsed.json" document.latestVersionId)" == "$version2" ]] || fail "reparse did not advance the current version"

[[ "$(psql_value "SELECT status FROM document_versions WHERE id = '$version1';")" == "CANCELLED" ]] || fail "superseded version is not CANCELLED"
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$version1' AND status = 'STALE';")" -gt 0 ]] || fail "superseded chunks were not marked STALE"
[[ "$(psql_value "SELECT status FROM evidence_packages WHERE document_version_id = '$version1';")" == "STALE" ]] || fail "superseded evidence package was not marked STALE"

request GET "/api/v1/documents/$document_id/versions" 200 "$tmp_dir/versions-2.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/versions-2.json" 'len(data["items"]) == 2 and data["currentVersionId"] == "'"$version2"'" and any(item["id"] == "'"$version1"'" and item["current"] is False and item["restorable"] is True for item in data["items"])'

request POST "/api/v1/documents/$document_id/versions/$version1/activate" 200 "$tmp_dir/activate.json" -H "Authorization: Bearer $token"
[[ "$(json_field "$tmp_dir/activate.json" document.latestVersionId)" == "$version1" ]] || fail "rollback did not restore the previous version"
[[ "$(psql_value "SELECT status FROM document_versions WHERE id = '$version1';")" == "EVIDENCE_READY" ]] || fail "reactivated version is not EVIDENCE_READY"
[[ "$(psql_value "SELECT status FROM evidence_packages WHERE document_version_id = '$version1';")" == "EVIDENCE_READY" ]] || fail "reactivated package is not EVIDENCE_READY"
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$version1' AND status = 'EVIDENCE_READY';")" -gt 0 ]] || fail "reactivated chunks are not searchable"
[[ "$(psql_value "SELECT status FROM document_versions WHERE id = '$version2';")" == "CANCELLED" ]] || fail "superseded rollback target was not cancelled"
search "$token" "$marker" "kb-dev" 200 "$tmp_dir/search-after-rollback.json"
assert_json "$tmp_dir/search-after-rollback.json" 'any(item["documentVersionId"] == "'"$version1"'" for item in data["items"])'

# ---------------------------------------------------------------------------
step "document lifecycle: soft delete, restore and hard purge"
# ---------------------------------------------------------------------------
source_key="$(psql_value "SELECT source_object_key FROM document_versions WHERE id = '$version1';")"
request DELETE "/api/v1/documents/$document_id" 204 "$tmp_dir/delete.json" -H "Authorization: Bearer $token"
[[ "$(psql_value "SELECT count(*) FROM documents WHERE id = '$document_id' AND deleted_at IS NOT NULL AND purge_after IS NOT NULL;")" == "1" ]] || fail "delete did not set the retention window"
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$version1';")" -gt 0 ]] || fail "soft delete removed chunks"
"${compose[@]}" exec -T minio mc stat "m4accept/$bucket/$source_key" >/dev/null || fail "soft delete removed the source object"
search "$token" "$marker" "kb-dev" 200 "$tmp_dir/search-deleted.json"
# The knowledge base holds fixtures from earlier runs, so assert the deleted
# document is absent rather than requiring an empty result set.
assert_json "$tmp_dir/search-deleted.json" 'all(item["documentId"] != "'"$document_id"'" for item in data["items"])'
request GET "/api/v1/documents/$document_id" 404 "$tmp_dir/deleted-read.json" -H "Authorization: Bearer $token"
request GET "/api/v1/documents/$document_id/versions" 404 "$tmp_dir/deleted-versions.json" -H "Authorization: Bearer $token"

request POST "/api/v1/documents/$document_id/restore" 200 "$tmp_dir/restore.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/restore.json" '"deletedAt" not in data["document"] and data["document"]["status"] == "EVIDENCE_READY"'
search "$token" "$marker" "kb-dev" 200 "$tmp_dir/search-restored.json"
assert_json "$tmp_dir/search-restored.json" 'any(item["documentId"] == "'"$document_id"'" for item in data["items"])'

# ---------------------------------------------------------------------------
step "reliability: duplicate outbox publication stays idempotent"
# ---------------------------------------------------------------------------
parse_key="$(psql_value "SELECT s.idempotency_key FROM stage_runs s JOIN pipeline_runs p ON p.id = s.pipeline_run_id WHERE p.document_version_id = '$version1' AND s.stage = 'parse' ORDER BY s.id DESC LIMIT 1;")"
[[ -n "$parse_key" ]]
chunks_before="$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$version1';")"
inbox_before="$(psql_value "SELECT count(*) FROM task_inbox WHERE idempotency_key LIKE '$(psql_value "SELECT id FROM pipeline_runs WHERE document_version_id = '$version1' ORDER BY created_at DESC LIMIT 1;")%';")"
"${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "UPDATE outbox_events SET published_at = NULL, next_attempt_at = NULL, attempts = 0, last_error = '' WHERE idempotency_key = '$parse_key';" >/dev/null
republished=""
for _ in $(seq 1 30); do
  republished="$(psql_value "SELECT count(*) FROM outbox_events WHERE idempotency_key = '$parse_key' AND published_at IS NOT NULL;")"
  [[ "$republished" == "1" ]] && break
  sleep 1
done
[[ "$republished" == "1" ]] || fail "the duplicate outbox event was not republished"
sleep 1
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$version1';")" == "$chunks_before" ]] || fail "duplicate publication duplicated chunks"
[[ "$(psql_value "SELECT count(*) FROM task_inbox WHERE idempotency_key LIKE '$(psql_value "SELECT id FROM pipeline_runs WHERE document_version_id = '$version1' ORDER BY created_at DESC LIMIT 1;")%';")" == "$inbox_before" ]] || fail "duplicate publication duplicated inbox rows"
[[ "$(psql_value "SELECT status FROM task_inbox WHERE idempotency_key = '$parse_key';")" == "SUCCEEDED" ]] || fail "duplicate publication changed the inbox result"

request GET "/api/v1/ops/tasks/$parse_key" 200 "$tmp_dir/task-inspection.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/task-inspection.json" 'data["inbox"]["status"] == "SUCCEEDED" and data["stage"]["status"] == "SUCCEEDED"'

# ---------------------------------------------------------------------------
step "reliability: worker restart, dead-letter replay and reconciliation"
# ---------------------------------------------------------------------------
"${compose[@]}" stop worker >/dev/null
worker_stopped=1
printf '# M4 retry\n\nM4 retry marker %s nebula onyx.\n' "$run_id" > "$tmp_dir/retry.md"
request POST /api/v1/knowledge-bases/kb-dev/documents 202 "$tmp_dir/retry-upload.json" \
  -H "Authorization: Bearer $token" -H "Idempotency-Key: m4-retry-$run_id" \
  -F "file=@$tmp_dir/retry.md;type=text/markdown"
retry_document_id="$(json_field "$tmp_dir/retry-upload.json" document.id)"
retry_version_id="$(json_field "$tmp_dir/retry-upload.json" version.id)"
retry_pipeline_id="$(json_field "$tmp_dir/retry-upload.json" pipeline.id)"
retry_key="$retry_pipeline_id:parse"
[[ "$(psql_value "SELECT count(*) FROM outbox_events WHERE idempotency_key = '$retry_key' AND published_at IS NULL;")" == "1" ]] || fail "the stopped worker still published the outbox event"

# Simulate an exhausted retry budget and the dead letter the error handler
# would have recorded after five failed deliveries.
"${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "UPDATE outbox_events SET published_at = NOW(), attempts = 5, last_error = 'simulated: delivery exhausted' WHERE idempotency_key = '$retry_key'; UPDATE stage_runs SET status = 'FAILED', attempts = 5, error_code = 'simulated', error_message = 'simulated: delivery exhausted' WHERE idempotency_key = '$retry_key'; UPDATE pipeline_runs SET status = 'FAILED', error_code = 'simulated', error_message = 'simulated: delivery exhausted' WHERE id = '$retry_pipeline_id'; UPDATE document_versions SET status = 'FAILED', error_code = 'simulated' WHERE id = '$retry_version_id';" >/dev/null
"${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "INSERT INTO task_dead_letters (idempotency_key, tenant_id, knowledge_base_id, document_version_id, task_type, stage, attempts, last_error, payload, status, created_at, updated_at) SELECT idempotency_key, tenant_id, 'kb-dev', '$retry_version_id', event_type, 'parse', 5, 'simulated: delivery exhausted', payload, 'PENDING', NOW(), NOW() FROM outbox_events WHERE idempotency_key = '$retry_key';" >/dev/null
dead_letter_id="$(psql_value "SELECT id FROM task_dead_letters WHERE idempotency_key = '$retry_key';")"
[[ -n "$dead_letter_id" ]] || fail "simulated dead letter was not recorded"

worker_log_lines="$("${compose[@]}" logs --no-color worker 2>/dev/null | wc -l | tr -d ' ')"
"${compose[@]}" start worker >/dev/null
worker_stopped=0
worker_ready=""
for _ in $(seq 1 30); do
  current_lines="$("${compose[@]}" logs --no-color worker 2>/dev/null | wc -l | tr -d ' ')"
  if [[ "$current_lines" -gt "$worker_log_lines" ]]; then
    recent_logs="$("${compose[@]}" logs --no-color worker 2>/dev/null | tail -n "+$((worker_log_lines + 1))")"
    if grep -q worker_bootstrap_ready <<<"$recent_logs"; then
      worker_ready=1
      break
    fi
  fi
  sleep 1
done
[[ "$worker_ready" == "1" ]] || fail "worker did not become ready after restart"

request POST "/api/v1/ops/dead-letters/$dead_letter_id/retry" 204 "$tmp_dir/retry-dead-letter.json" -H "Authorization: Bearer $token"
wait_pipeline_status "$retry_document_id" "$token" EVIDENCE_READY "$tmp_dir/retry-status.json"
[[ "$(psql_value "SELECT status FROM task_inbox WHERE idempotency_key = '$retry_key';")" == "SUCCEEDED" ]] || fail "replayed task did not succeed"
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$retry_version_id' AND status = 'EVIDENCE_READY';")" -gt 0 ]] || fail "replayed task produced no searchable chunks"
[[ "$(psql_value "SELECT count(*) FROM evidence_packages WHERE document_version_id = '$retry_version_id' AND status = 'EVIDENCE_READY';")" == "1" ]] || fail "replayed task produced no evidence package"

request POST "/api/v1/ops/reconcile?staleAfterSeconds=1" 200 "$tmp_dir/reconcile.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/reconcile.json" 'data["deadLettersResolved"] >= 1'
[[ "$(psql_value "SELECT status FROM task_dead_letters WHERE id = '$dead_letter_id';")" == "RESOLVED" ]] || fail "reconcile did not resolve the recovered dead letter"
request GET "/api/v1/audit?action=retry&resourceType=dead_letter&limit=20" 200 "$tmp_dir/audit-retry.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/audit-retry.json" 'any(row["resourceId"] == "'"$dead_letter_id"'" for row in data["items"])'

discard_key="m4-discard-$run_id"
"${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "INSERT INTO task_dead_letters (idempotency_key, tenant_id, knowledge_base_id, document_version_id, task_type, stage, attempts, last_error, payload, status, created_at, updated_at) VALUES ('$discard_key', 'dev-tenant', 'kb-dev', '$retry_version_id', 'document.ingestion.v1', 'parse', 1, 'simulated: discard', '{}', 'PENDING', NOW(), NOW());" >/dev/null
discard_id="$(psql_value "SELECT id FROM task_dead_letters WHERE idempotency_key = '$discard_key';")"
request POST "/api/v1/ops/dead-letters/$discard_id/discard" 204 "$tmp_dir/discard.json" -H "Authorization: Bearer $token"
[[ "$(psql_value "SELECT status FROM task_dead_letters WHERE id = '$discard_id';")" == "DISCARDED" ]] || fail "discard did not close the dead letter"
request GET "/api/v1/audit?action=discard&resourceType=dead_letter&limit=20" 200 "$tmp_dir/audit-discard.json" -H "Authorization: Bearer $token"
assert_json "$tmp_dir/audit-discard.json" 'any(row["resourceId"] == "'"$discard_id"'" for row in data["items"])'

# ---------------------------------------------------------------------------
step "reliability: redis restart keeps authorization correct"
# ---------------------------------------------------------------------------
"${compose[@]}" restart redis >/dev/null
redis_ready=""
for _ in $(seq 1 30); do
  if "${compose[@]}" exec -T redis redis-cli ping 2>/dev/null | grep -q PONG; then redis_ready=1; break; fi
  sleep 1
done
[[ "$redis_ready" == "1" ]] || fail "redis did not recover after restart"
for _ in $(seq 1 30); do
  curl -fsS "$base_url/readyz" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS "$base_url/readyz" | grep -q '"status":"ok"' || fail "api did not recover after the redis restart"

revision_before="$(psql_value "SELECT revision FROM authorization_revisions WHERE tenant_id = 'dev-tenant';")"
request DELETE /api/v1/knowledge-bases/kb-dev/members/viewer-a 204 "$tmp_dir/remove-viewer-redis.json" -H "Authorization: Bearer $token"
revision_after="$(psql_value "SELECT revision FROM authorization_revisions WHERE tenant_id = 'dev-tenant';")"
[[ "$revision_after" -gt "$revision_before" ]] || fail "revocation after the redis restart did not bump the revision"
search "$viewer_token" "$marker" "kb-dev" 403 "$tmp_dir/viewer-revoked-redis.json"
request POST /api/v1/knowledge-bases/kb-dev/members 201 "$tmp_dir/readd-viewer-redis.json" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"user_id":"viewer-a","role":"viewer"}'
search "$viewer_token" "$marker" "kb-dev" 200 "$tmp_dir/viewer-regranted-redis.json"
assert_json "$tmp_dir/viewer-regranted-redis.json" 'len(data["items"]) > 0'

# ---------------------------------------------------------------------------
step "observability: parse failure rate, queues, retrieval latency, model errors"
# ---------------------------------------------------------------------------
python3 - "$tmp_dir/scanned.pdf" <<'PY'
import sys

objects = [
    b"<< /Type /Catalog /Pages 2 0 R >>",
    b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Contents 4 0 R >>",
    b"<< /Length 0 >>\nstream\n\nendstream",
]
output = b"%PDF-1.4\n"
offsets = [0]
for index, value in enumerate(objects, 1):
    offsets.append(len(output))
    output += f"{index} 0 obj\n".encode() + value + b"\nendobj\n"
xref = len(output)
output += b"xref\n0 5\n0000000000 65535 f \n"
output += b"".join(f"{offset:010d} 00000 n \n".encode() for offset in offsets[1:])
output += b"trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n" + str(xref).encode() + b"\n%%EOF\n"
open(sys.argv[1], "wb").write(output)
PY
request POST /api/v1/knowledge-bases/kb-dev/documents 202 "$tmp_dir/scanned-upload.json" \
  -H "Authorization: Bearer $token" -H "Idempotency-Key: m4-scanned-$run_id" \
  -F "file=@$tmp_dir/scanned.pdf;type=application/pdf"
scanned_id="$(json_field "$tmp_dir/scanned-upload.json" document.id)"
wait_pipeline_status "$scanned_id" "$token" FAILED "$tmp_dir/scanned-status.json"
request GET /api/v1/ops/metrics 200 "$tmp_dir/metrics.json" -H "Authorization: Bearer $token"
python3 - "$tmp_dir/metrics.json" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
operations = data["operations"]
assert operations["parseFailureRate"] > 0, operations
assert operations["stages"]["parse"]["failed"] >= 1, operations
assert isinstance(operations["modelErrors24h"], int), operations
assert "outboxBacklog" in operations and "deadLetters" in operations, operations
for queue in ("ingestion", "interactive", "maintenance"):
    assert queue in data["queues"], data.get("queues")
    assert set(("pending", "active", "retry", "archived")) <= set(data["queues"][queue]), data["queues"]
assert any(key.startswith("retrieval_duration_ms") for key in data["timings"]), data["timings"]
PY

# ---------------------------------------------------------------------------
step "lifecycle: purge removes versions, chunks, packages, outbox and objects"
# ---------------------------------------------------------------------------
request DELETE "/api/v1/documents/$document_id" 204 "$tmp_dir/delete-final.json" -H "Authorization: Bearer $token"
request POST "/api/v1/documents/$document_id/purge" 204 "$tmp_dir/purge.json" -H "Authorization: Bearer $token"
[[ "$(psql_value "SELECT count(*) FROM documents WHERE id = '$document_id';")" == "0" ]] || fail "purge left the document row"
[[ "$(psql_value "SELECT count(*) FROM document_versions WHERE document_id = '$document_id';")" == "0" ]] || fail "purge left document versions"
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id IN ('$version1', '$version2');")" == "0" ]] || fail "purge left chunks"
[[ "$(psql_value "SELECT count(*) FROM evidence_packages WHERE document_version_id IN ('$version1', '$version2');")" == "0" ]] || fail "purge left evidence packages"
[[ "$(psql_value "SELECT count(*) FROM pipeline_runs WHERE document_id = '$document_id';")" == "0" ]] || fail "purge left pipeline runs"
[[ "$(psql_value "SELECT count(*) FROM outbox_events WHERE aggregate_id IN ('$version1', '$version2', '$pipeline1', '$pipeline2');")" == "0" ]] || fail "purge left outbox events"
[[ "$(psql_value "SELECT count(*) FROM task_inbox WHERE idempotency_key LIKE '$pipeline1:%' OR idempotency_key LIKE '$pipeline2:%';")" == "0" ]] || fail "purge left inbox rows"
if "${compose[@]}" exec -T minio mc stat "m4accept/$bucket/$source_key" >/dev/null 2>&1; then
  fail "purge left the source object in storage"
fi

# ---------------------------------------------------------------------------
step "session teardown and graceful shutdown of api/worker"
# ---------------------------------------------------------------------------
cp "$tmp_dir/cookies.txt" "$tmp_dir/cookies-before-logout.txt"
request POST /api/v1/auth/logout 204 "$tmp_dir/logout.json" -H "Authorization: Bearer $token" -b "$tmp_dir/cookies-before-logout.txt" -c "$tmp_dir/cookies.txt"
request GET /api/v1/me 401 "$tmp_dir/me-after-logout.json" -H "Authorization: Bearer $token"
# Replay the cookie captured before logout: the response clears it client-side,
# and the server-side session must reject it too.
request GET /api/v1/me 401 "$tmp_dir/me-after-logout-cookie.json" -b "$tmp_dir/cookies-before-logout.txt"

worker_logs="$("${compose[@]}" logs --no-color worker 2>/dev/null)"
grep -q worker_shutting_down <<<"$worker_logs" || fail "worker did not log a graceful shutdown"
"${compose[@]}" restart api >/dev/null
for _ in $(seq 1 30); do
  curl -fsS "$base_url/readyz" >/dev/null 2>&1 && break
  sleep 1
done
api_logs="$("${compose[@]}" logs --no-color api 2>/dev/null)"
grep -q api_shutdown_complete <<<"$api_logs" || fail "api did not log a graceful shutdown"
curl -fsS "$base_url/readyz" | grep -q '"status":"ok"' || fail "api did not become ready again"

printf '\nM4 acceptance passed: document=%s versions=%s,%s run=%s\n' "$document_id" "$version1" "$version2" "$run_id"
