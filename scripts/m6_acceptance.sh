#!/usr/bin/env bash
# M6 acceptance: LLM Wiki projection, Knowledge Index and ProjectionJoin.
#
# The scenario builds a fresh knowledge base with two documents, enables wiki,
# verifies paragraph-level source traceability, review/publish/rollback and
# permission boundaries, then proves ACL invalidation, independent retry and
# that projection never needs the raw source object. It finishes by disabling
# wiki and removing the fixture knowledge base.
#
# The worker's chat adapter may be remote or the deterministic local adapter;
# assertions do not depend on generated wording. Knowledge embedding still uses
# the configured embedding provider.
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
alpha_marker="M6ALPHA-$run_id"
beta_marker="M6BETA-$run_id"
bucket="${S3_BUCKET:-rag-platform-local}"
kb=""
dev_token=""
viewer_token=""
editor_token=""
other_token=""
api_key_id=""
api_key_token=""

cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'M6 acceptance failed: %s\n' "$*" >&2; exit 1; }

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
if not eval(expression, {"__builtins__": {}}, {
    "data": data, "len": len, "any": any, "all": all, "str": str, "int": int,
    "set": set, "sorted": sorted, "sum": sum, "min": min, "max": max,
}):
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

py_count() {
  python3 - "$1" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
print(len(data))
PY
}

login() {
  local user="$1" tenant="$2" output="$3"
  request POST /api/v1/auth/dev-login 200 "$output" \
    -H 'Content-Type: application/json' -d "{\"userId\":\"$user\",\"tenantId\":\"$tenant\"}"
  json_field "$output" token
}

auth_header() { printf 'Authorization: Bearer %s' "$1"; }

wait_document_ready() {
  local document_id="$1" output="$2"
  local status=""
  for _ in $(seq 1 180); do
    request GET "/api/v1/documents/$document_id" 200 "$output" -H "$(auth_header "$dev_token")"
    status="$(json_field "$output" pipeline.status)"
    [[ "$status" == "EVIDENCE_READY" ]] && return 0
    if [[ "$status" == "FAILED" ]]; then
      cat "$output" >&2
      fail "document $document_id failed while waiting for EVIDENCE_READY"
    fi
    sleep 1
  done
  fail "document $document_id did not reach EVIDENCE_READY (last: $status)"
}

wait_projection_ready() {
  local output="$1"
  local status="" join="" run_count=""
  for _ in $(seq 1 300); do
    request GET "/api/v1/knowledge-bases/$kb/projection" 200 "$output" -H "$(auth_header "$dev_token")"
    run_count="$(python3 - "$output" <<'PY'
import json
import sys
print(len(json.load(open(sys.argv[1])).get("runs") or []))
PY
)"
    if [[ "$run_count" == "0" ]]; then
      sleep 1
      continue
    fi
    status="$(json_field "$output" runs.0.status)"
    join="$(json_field "$output" joinStatus)"
    [[ "$status" == "WIKI_READY" && "$join" == "KNOWLEDGE_READY" ]] && return 0
    case "$status" in
      FAILED_WIKI|CANCELLED)
        cat "$output" >&2
        fail "projection reached $status while waiting for WIKI_READY"
        ;;
    esac
    sleep 1
  done
  cat "$output" >&2
  fail "projection did not reach WIKI_READY (last: $status/$join)"
}

wait_run_status() {
  local run_id_value="$1" expected="$2" output="$3"
  local status=""
  for _ in $(seq 1 300); do
    request GET "/api/v1/wiki/runs/$run_id_value" 200 "$output" -H "$(auth_header "$dev_token")"
    status="$(json_field "$output" run.status)"
    [[ "$status" == "$expected" ]] && return 0
    case "$status" in
      FAILED_WIKI|CANCELLED)
        cat "$output" >&2
        fail "run $run_id_value reached $status while waiting for $expected"
        ;;
    esac
    sleep 1
  done
  fail "run $run_id_value did not reach $expected (last: $status)"
}

latest_run_id() {
  request GET "/api/v1/knowledge-bases/$kb/projection" 200 "$tmp_dir/projection-latest.json" -H "$(auth_header "$dev_token")"
  json_field "$tmp_dir/projection-latest.json" runs.0.id
}

page_id_by_type() {
  local page_type="$1" output="$2"
  python3 - "$output" "$page_type" <<'PY'
import json
import sys

pages = json.load(open(sys.argv[1]))["pages"]
for item in pages:
    if item["page"]["pageType"] == sys.argv[2]:
        print(item["page"]["id"])
        raise SystemExit(0)
raise SystemExit(f"no page of type {sys.argv[2]}")
PY
}

publish_all_current() {
  local pages="$tmp_dir/publish-pages.json"
  local info page_id published current review status
  request GET "/api/v1/knowledge-bases/$kb/wiki/pages" 200 "$pages" -H "$(auth_header "$dev_token")"
  local count
  count="$(python3 - "$pages" <<'PY'
import json
import sys
print(len(json.load(open(sys.argv[1]))["pages"]))
PY
)"
  for ((index = 0; index < count; index++)); do
    info="$(python3 - "$pages" "$index" <<'PY'
import json
import sys

item = json.load(open(sys.argv[1]))["pages"][int(sys.argv[2])]
page = item["page"]
current = item.get("currentVersion") or {}
print("|".join([page["id"], (item.get("publishedVersion") or {}).get("id", ""),
                current.get("id", ""), current.get("reviewStatus", ""), page["status"]]))
PY
)"
    IFS='|' read -r page_id published current review status <<<"$info"
    [[ -n "$current" ]] || fail "page $page_id has no current version"
    if [[ -n "$published" && "$published" == "$current" ]]; then
      continue
    fi
    if [[ "$review" == "PENDING" ]]; then
      request POST "/api/v1/wiki/pages/$page_id/versions/$current/review" 200 "$tmp_dir/review.json" \
        -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
        -d '{"action":"approve","note":"m6 acceptance"}'
      review="APPROVED"
    fi
    [[ "$review" == "APPROVED" ]] || fail "page $page_id current version $current is $review"
    request POST "/api/v1/wiki/pages/$page_id/versions/$current/publish" 200 "$tmp_dir/publish.json" \
      -H "$(auth_header "$dev_token")"
  done
}

assert_all_pages_published() {
  local pages="$tmp_dir/assert-pages.json"
  request GET "/api/v1/knowledge-bases/$kb/wiki/pages" 200 "$pages" -H "$(auth_header "$dev_token")"
  python3 - "$pages" <<'PY'
import json
import sys

for item in json.load(open(sys.argv[1]))["pages"]:
    page = item["page"]
    assert page["status"] == "PUBLISHED", page
    assert (item.get("publishedVersion") or {}).get("id"), page
PY
}

knowledge_search() {
  local bearer="$1" query="$2" expected="$3" output="$4"
  local code
  code="$(curl -sS -o "$output" -w '%{http_code}' -G "$base_url/api/v1/knowledge-bases/$kb/knowledge/search" \
    --data-urlencode "q=$query" --data-urlencode "limit=10" -H "$(auth_header "$bearer")")"
  if [[ "$code" != "$expected" ]]; then
    printf 'expected knowledge search %q, got %s: ' "$query" "$code" >&2
    cat "$output" >&2
    exit 1
  fi
}

evidence_search() {
  local bearer="$1" query="$2" output="$3"
  local body
  body="$(python3 - "$query" "$kb" <<'PY'
import json
import sys
print(json.dumps({"query": sys.argv[1], "knowledgeBaseId": sys.argv[2]}))
PY
)"
  request POST /api/v1/search 200 "$output" \
    -H "$(auth_header "$bearer")" -H 'Content-Type: application/json' -d "$body"
}

assert_evidence_has_document() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
assert any(item["documentId"] == sys.argv[2] for item in data["items"]), json.dumps(data)[:2000]
PY
}

assert_knowledge_items() {
  # Asserts every published page is reachable and each hit carries current source
  # chunks. The vector channel returns the top-N rows regardless of score, so a
  # marker query must surface all three pages of this small fixture.
  python3 - "$1" "$2" "$3" "$4" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
page_ids = set(sys.argv[2:])
assert data["joined"] is True, data
items = data["items"]
assert items, data
assert page_ids <= {item["pageId"] for item in items}, data
for item in items:
    assert item["sources"], item
    for source in item["sources"]:
        assert source["chunkId"] and source["documentVersionId"] and source["documentId"], source
PY
}

assert_knowledge_empty() {
  python3 - "$1" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
assert data["items"] == [], json.dumps(data)[:2000]
PY
}

index_row_count() {
  psql_value "SELECT count(*) FROM knowledge_index_records WHERE knowledge_base_id = '$kb' AND status = 'ACTIVE';"
}

current_acl() {
  psql_value "SELECT acl_version FROM knowledge_bases WHERE id = '$kb';"
}

page_version_count() {
  psql_value "SELECT count(*) FROM wiki_page_versions v JOIN wiki_pages p ON p.id = v.wiki_page_id WHERE p.knowledge_base_id = '$kb';"
}

evidence_chunk_count() {
  psql_value "SELECT count(*) FROM chunks c JOIN document_versions dv ON dv.id = c.document_version_id JOIN documents d ON d.id = dv.document_id WHERE d.knowledge_base_id = '$kb' AND d.deleted_at IS NULL AND c.status = 'EVIDENCE_READY';"
}

# ---------------------------------------------------------------------------
step "preflight: health, migrations and M6 tables"
request GET /healthz 200 "$tmp_dir/health.json"
migrate_output="$("${compose[@]}" run --rm --no-deps migrate -check 2>&1)"
printf '%s\n' "$migrate_output" | grep -q migrations_compatible || {
  printf '%s\n' "$migrate_output" >&2
  fail "migration -check did not report compatibility"
}
tables="$(psql_value "SELECT string_agg(table_name, ',' ORDER BY table_name) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('projection_runs','projection_stage_runs','wiki_pages','wiki_page_versions','wiki_page_sources','knowledge_index_records');")"
[[ "$tables" == "knowledge_index_records,projection_runs,projection_stage_runs,wiki_page_sources,wiki_page_versions,wiki_pages" ]] \
  || fail "M6 tables missing: $tables"

# ---------------------------------------------------------------------------
step "identity and fixture knowledge base with two evidence documents"
dev_token="$(login dev-user dev-tenant "$tmp_dir/login-dev.json")"
viewer_token="$(login viewer-a dev-tenant "$tmp_dir/login-viewer.json")"
editor_token="$(login editor-a dev-tenant "$tmp_dir/login-editor.json")"
other_token="$(login other-user other-tenant "$tmp_dir/login-other.json")"

request POST /api/v1/knowledge-bases 201 "$tmp_dir/kb.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d "{\"name\":\"M6 acceptance $run_id\",\"description\":\"created by m6_acceptance.sh\"}"
kb="$(json_field "$tmp_dir/kb.json" id)"
[[ -n "$kb" ]] || fail "knowledge base id missing"

request POST "/api/v1/knowledge-bases/$kb/members" 201 "$tmp_dir/member-viewer.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"user_id":"viewer-a","role":"viewer"}'
request POST "/api/v1/knowledge-bases/$kb/members" 201 "$tmp_dir/member-editor.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"user_id":"editor-a","role":"editor"}'

cat > "$tmp_dir/alpha.md" <<EOF
# Alpha handbook

The Alpha launch code is $alpha_marker. Alpha systems use the blue queue for ingestion.

## Alpha operations

Operators rotate the Alpha key every Monday and record it in the Alpha ledger.
EOF
cat > "$tmp_dir/beta.md" <<EOF
# Beta handbook

The Beta launch code is $beta_marker. Beta systems use the green queue for retrieval.

## Beta operations

Operators rotate the Beta key every Friday and record it in the Beta ledger.
EOF

request POST "/api/v1/knowledge-bases/$kb/documents" 202 "$tmp_dir/upload-alpha.json" \
  -H "$(auth_header "$dev_token")" -F "file=@$tmp_dir/alpha.md;type=text/markdown"
request POST "/api/v1/knowledge-bases/$kb/documents" 202 "$tmp_dir/upload-beta.json" \
  -H "$(auth_header "$dev_token")" -F "file=@$tmp_dir/beta.md;type=text/markdown"
doc_alpha="$(json_field "$tmp_dir/upload-alpha.json" document.id)"
doc_beta="$(json_field "$tmp_dir/upload-beta.json" document.id)"
wait_document_ready "$doc_alpha" "$tmp_dir/doc-alpha.json"
wait_document_ready "$doc_beta" "$tmp_dir/doc-beta.json"
baseline_chunks="$(evidence_chunk_count)"
[[ "$baseline_chunks" -gt 0 ]] || fail "no evidence chunks were produced"

request GET "/api/v1/knowledge-bases/$kb/projection" 200 "$tmp_dir/projection-off.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/projection-off.json" \
  'data["wikiEnabled"] is False and data["joinStatus"] == "EVIDENCE_READY" and data["pages"]["total"] == 0'

evidence_search "$dev_token" "$alpha_marker" "$tmp_dir/evidence-before.json"
assert_evidence_has_document "$tmp_dir/evidence-before.json" "$doc_alpha"

# ---------------------------------------------------------------------------
step "indexing strategy validation rejects inconsistent capabilities"
request PATCH "/api/v1/knowledge-bases/$kb" 400 "$tmp_dir/strategy-graph.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":false,"required_capabilities":["graph"]}}'
grep -q 'must enable graph_enabled' "$tmp_dir/strategy-graph.json" || fail "graph capability rejection did not explain itself"
request PATCH "/api/v1/knowledge-bases/$kb" 400 "$tmp_dir/strategy-wiki.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":false,"required_capabilities":["wiki"]}}'
grep -q 'must enable wiki_enabled' "$tmp_dir/strategy-wiki.json" || fail "wiki capability rejection did not explain itself"

# ---------------------------------------------------------------------------
step "enable wiki: one projection run over all current evidence packages"
request PATCH "/api/v1/knowledge-bases/$kb" 200 "$tmp_dir/strategy-on.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":true,"graph_enabled":false,"required_capabilities":["evidence"]}}'
wait_projection_ready "$tmp_dir/projection-ready.json"
statistics="$(json_field "$tmp_dir/projection-ready.json" pages.total)"
[[ "$statistics" == "3" ]] || fail "expected 3 wiki pages, got $statistics"
join="$(json_field "$tmp_dir/projection-ready.json" joinStatus)"
[[ "$join" == "KNOWLEDGE_READY" ]] || fail "join is $join"
run_initial="$(json_field "$tmp_dir/projection-ready.json" runs.0.id)"
wait_run_status "$run_initial" WIKI_READY "$tmp_dir/run-initial.json"
assert_json "$tmp_dir/run-initial.json" \
  'len(data["stages"]) == 3 and all(stage["status"] == "SUCCEEDED" for stage in data["stages"]) and all(stage["stage"] in ["COLLECT","GENERATE","FINALIZE"] for stage in data["stages"]) and data["run"]["model"] != "" and data["run"]["promptVersion"].startswith("wiki_generation/") and len(data["run"]["sourceHash"]) == 64'
[[ "$(index_row_count)" == "0" ]] || fail "unpublished pages must not have knowledge index rows"

# ---------------------------------------------------------------------------
step "page blocks and sources are traceable to the current document versions"
request GET "/api/v1/knowledge-bases/$kb/wiki/pages" 200 "$tmp_dir/pages-initial.json" \
  -H "$(auth_header "$dev_token")"
read -r alpha_page beta_page <<<"$(python3 - "$tmp_dir/pages-initial.json" <<'PY'
import json
import sys

ids = [item["page"]["id"] for item in json.load(open(sys.argv[1]))["pages"]
       if item["page"]["pageType"] == "document"]
assert len(ids) >= 2, ids
print(ids[0], ids[1])
PY
)"
overview_page="$(page_id_by_type overview "$tmp_dir/pages-initial.json")"

for page_id_value in "$alpha_page" "$beta_page" "$overview_page"; do
  request GET "/api/v1/wiki/pages/$page_id_value" 200 "$tmp_dir/page-detail.json" \
    -H "$(auth_header "$dev_token")"
  python3 - "$tmp_dir/page-detail.json" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
blocks = data["blocks"]
sources = data["sources"]
assert blocks, "page has no blocks"
assert sources, "page has no source rows"
assert data["version"] and data["version"]["reviewStatus"] == "PENDING", data.get("version")
source_ids = {source["chunkId"] for source in sources}
for block in blocks:
    assert block["sourceChunkIds"], block
    assert set(block["sourceChunkIds"]) <= source_ids, (block, source_ids)
for source in sources:
    assert source["documentId"] and source["documentVersionId"] and source["chunkId"], source
PY
  version_id="$(json_field "$tmp_dir/page-detail.json" version.id)"
  source_rows="$(psql_value "SELECT count(*) FROM wiki_page_sources WHERE wiki_page_version_id = '$version_id';")"
  detail_sources="$(python3 - "$tmp_dir/page-detail.json" <<'PY'
import json
import sys
print(len(json.load(open(sys.argv[1]))["sources"]))
PY
)"
  [[ "$source_rows" == "$detail_sources" ]] || fail "page $page_id_value source rows mismatch"
  bad_sources="$(psql_value "SELECT count(*) FROM wiki_page_sources s JOIN chunks c ON c.id = s.chunk_id JOIN document_versions dv ON dv.id = s.document_version_id WHERE s.wiki_page_version_id = '$version_id' AND (c.document_version_id <> s.document_version_id OR dv.id <> s.document_version_id OR c.status <> 'EVIDENCE_READY');")"
  [[ "$bad_sources" == "0" ]] || fail "page $page_id_value cites a chunk outside the named document version"
done

request GET "/api/v1/wiki/pages/$overview_page" 200 "$tmp_dir/overview-detail.json" \
  -H "$(auth_header "$dev_token")"
python3 - "$tmp_dir/overview-detail.json" "$doc_alpha" "$doc_beta" <<'PY'
import json
import sys

sources = json.load(open(sys.argv[1]))["sources"]
documents = {source["documentId"] for source in sources}
assert {sys.argv[2], sys.argv[3]} <= documents, documents
PY

# ---------------------------------------------------------------------------
step "review and publish permissions; pending pages are not publishable"
alpha_version="$(json_field "$tmp_dir/pages-initial.json" pages.0.currentVersion.id)"
# page order is (document, document, overview); locate alpha explicitly
request GET "/api/v1/knowledge-bases/$kb/wiki/pages" 200 "$tmp_dir/pages-initial.json" \
  -H "$(auth_header "$dev_token")"
alpha_version="$(python3 - "$tmp_dir/pages-initial.json" "$alpha_page" <<'PY'
import json
import sys

for item in json.load(open(sys.argv[1]))["pages"]:
    if item["page"]["id"] == sys.argv[2]:
        print(item["currentVersion"]["id"])
        break
PY
)"
request POST "/api/v1/wiki/pages/$alpha_page/versions/$alpha_version/review" 400 "$tmp_dir/review-invalid.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"maybe"}'
request POST "/api/v1/wiki/pages/$alpha_page/versions/$alpha_version/review" 403 "$tmp_dir/review-viewer.json" \
  -H "$(auth_header "$viewer_token")" -H 'Content-Type: application/json' -d '{"action":"approve"}'
request POST "/api/v1/wiki/pages/$alpha_page/versions/$alpha_version/review" 403 "$tmp_dir/review-editor.json" \
  -H "$(auth_header "$editor_token")" -H 'Content-Type: application/json' -d '{"action":"approve"}'
request POST "/api/v1/wiki/pages/$alpha_page/versions/$alpha_version/publish" 409 "$tmp_dir/publish-pending.json" \
  -H "$(auth_header "$dev_token")"
request POST "/api/v1/wiki/pages/$alpha_page/versions/$alpha_version/review" 200 "$tmp_dir/review-ok.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"approve","note":"m6 acceptance"}'
assert_json "$tmp_dir/review-ok.json" 'data["reviewStatus"] == "APPROVED" and data["reviewedBy"] == "dev-user"'
request POST "/api/v1/wiki/pages/$alpha_page/versions/$alpha_version/review" 409 "$tmp_dir/review-twice.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"approve"}'
request POST "/api/v1/wiki/pages/$alpha_page/versions/$alpha_version/publish" 403 "$tmp_dir/publish-viewer.json" \
  -H "$(auth_header "$viewer_token")"
request POST "/api/v1/wiki/pages/$alpha_page/versions/$alpha_version/publish" 200 "$tmp_dir/publish-ok.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/publish-ok.json" 'data["status"] == "PUBLISHED" and data["publishedVersionId"] == "'"$alpha_version"'"'

publish_all_current
assert_all_pages_published
[[ "$(index_row_count)" == "3" ]] || fail "expected 3 active knowledge index rows"

# ---------------------------------------------------------------------------
step "knowledge search is scoped, sourced and invisible to other tenants"
knowledge_search "$dev_token" "$alpha_marker" 200 "$tmp_dir/knowledge-alpha.json"
assert_knowledge_items "$tmp_dir/knowledge-alpha.json" "$alpha_page" "$beta_page" "$overview_page"
knowledge_search "$viewer_token" "$beta_marker" 200 "$tmp_dir/knowledge-viewer.json"
assert_knowledge_items "$tmp_dir/knowledge-viewer.json" "$alpha_page" "$beta_page" "$overview_page"
knowledge_search "$other_token" "$alpha_marker" 403 "$tmp_dir/knowledge-other.json"
request GET "/api/v1/knowledge-bases/$kb/projection" 403 "$tmp_dir/projection-other.json" \
  -H "$(auth_header "$other_token")"
request GET "/api/v1/knowledge-bases/$kb/wiki/pages" 403 "$tmp_dir/pages-other.json" \
  -H "$(auth_header "$other_token")"

# ---------------------------------------------------------------------------
step "API key can read the projection but cannot rebuild or retry"
request POST /api/v1/api-keys 201 "$tmp_dir/api-key.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d "{\"name\":\"m6 acceptance read\",\"capabilities\":[\"knowledge_base:read\",\"chat\"],\"knowledgeBaseIds\":[\"$kb\"]}"
api_key_id="$(json_field "$tmp_dir/api-key.json" apiKey.id)"
api_key_token="$(json_field "$tmp_dir/api-key.json" token)"
request GET "/api/v1/knowledge-bases/$kb/projection" 200 "$tmp_dir/projection-key.json" \
  -H "$(auth_header "$api_key_token")"
request POST "/api/v1/knowledge-bases/$kb/wiki/rebuild" 403 "$tmp_dir/rebuild-key.json" \
  -H "$(auth_header "$api_key_token")"

# ---------------------------------------------------------------------------
step "description-only update keeps ACL; member removal invalidates and rebuilds from EvidencePackage only"
"${compose[@]}" exec -T minio mc alias set m6accept http://127.0.0.1:9000 \
  "${MINIO_ROOT_USER:-rag-local}" "${MINIO_ROOT_PASSWORD:-change-me-local-only}" >/dev/null
alpha_object_key="$(psql_value "SELECT v.source_object_key FROM document_versions v JOIN documents d ON d.latest_version_id = v.id WHERE d.id = '$doc_alpha';")"
[[ -n "$alpha_object_key" ]] || fail "alpha source object key missing"
"${compose[@]}" exec -T minio mc rm "m6accept/$bucket/$alpha_object_key" >/dev/null
if "${compose[@]}" exec -T minio mc stat "m6accept/$bucket/$alpha_object_key" >/dev/null 2>&1; then
  fail "source object still present after deletion"
fi

acl_before="$(current_acl)"
index_before="$(index_row_count)"
request PATCH "/api/v1/knowledge-bases/$kb" 200 "$tmp_dir/acl-patch.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"description":"m6 acceptance description only"}'
[[ "$(current_acl)" -eq "$acl_before" ]] || fail "description-only update must not advance the ACL version"
[[ "$(index_row_count)" == "$index_before" ]] || fail "description-only update must not invalidate knowledge index rows"

request DELETE "/api/v1/knowledge-bases/$kb/members/editor-a" 204 "$tmp_dir/acl-member-remove.json" \
  -H "$(auth_header "$dev_token")"
acl_after="$(current_acl)"
[[ "$acl_after" -eq $((acl_before + 1)) ]] || fail "member removal did not advance the ACL version"
[[ "$(index_row_count)" == "0" ]] || fail "ACL change did not invalidate knowledge index rows immediately"
request POST "/api/v1/knowledge-bases/$kb/wiki/rebuild" 202 "$tmp_dir/acl-rebuild.json" \
  -H "$(auth_header "$dev_token")"
request GET "/api/v1/knowledge-bases/$kb/wiki/pages" 200 "$tmp_dir/pages-stale.json" \
  -H "$(auth_header "$dev_token")"
python3 - "$tmp_dir/pages-stale.json" <<'PY'
import json
import sys

for item in json.load(open(sys.argv[1]))["pages"]:
    assert item["page"]["status"] in ("STALE", "REVIEW_REQUIRED"), item["page"]
PY
knowledge_search "$dev_token" "$alpha_marker" 200 "$tmp_dir/knowledge-stale.json"
assert_knowledge_empty "$tmp_dir/knowledge-stale.json"

wait_projection_ready "$tmp_dir/projection-acl.json"
run_acl="$(json_field "$tmp_dir/projection-acl.json" runs.0.id)"
[[ "$(psql_value "SELECT acl_version FROM projection_runs WHERE id = '$run_acl';")" == "$acl_after" ]] \
  || fail "rebuild run does not carry the current ACL version"
if "${compose[@]}" exec -T minio mc stat "m6accept/$bucket/$alpha_object_key" >/dev/null 2>&1; then
  fail "source object unexpectedly restored"
fi
evidence_search "$dev_token" "$alpha_marker" "$tmp_dir/evidence-after-rebuild.json"
assert_evidence_has_document "$tmp_dir/evidence-after-rebuild.json" "$doc_alpha"
[[ "$(evidence_chunk_count)" == "$baseline_chunks" ]] || fail "projection rebuild changed the evidence chunks"
knowledge_search "$dev_token" "$alpha_marker" 200 "$tmp_dir/knowledge-pending.json"
assert_knowledge_empty "$tmp_dir/knowledge-pending.json"

publish_all_current
assert_all_pages_published
[[ "$(index_row_count)" == "3" ]] || fail "re-published pages did not restore the knowledge index"
knowledge_search "$dev_token" "$alpha_marker" 200 "$tmp_dir/knowledge-restored.json"
assert_knowledge_items "$tmp_dir/knowledge-restored.json" "$alpha_page" "$beta_page" "$overview_page"

# ---------------------------------------------------------------------------
step "rollback: stale-ACL version refused, current-ACL version restored"
request GET "/api/v1/wiki/pages/$alpha_page/versions" 200 "$tmp_dir/versions-alpha.json" \
  -H "$(auth_header "$dev_token")"
old_version="$(python3 - "$tmp_dir/versions-alpha.json" <<'PY'
import json
import sys

versions = json.load(open(sys.argv[1]))["versions"]
print(versions[-1]["id"])
PY
)"
current_version="$(json_field "$tmp_dir/versions-alpha.json" versions.0.id)"
request POST "/api/v1/wiki/pages/$alpha_page/rollback" 409 "$tmp_dir/rollback-stale.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d "{\"version_id\":\"$old_version\"}"

clone_version="wpv-m6accept-$run_id"
clone_number=$(( $(json_field "$tmp_dir/versions-alpha.json" versions.0.version) + 1 ))
psql_value "INSERT INTO wiki_page_versions (id, wiki_page_id, tenant_id, knowledge_base_id, version, content, blocks, source_hash, generation_model, prompt_version, review_status, review_note, reviewed_by, reviewed_at, acl_version, created_by, created_at) SELECT '$clone_version', wiki_page_id, tenant_id, knowledge_base_id, $clone_number, content, blocks, source_hash, generation_model, prompt_version, 'PENDING', '', '', NULL, acl_version, 'acceptance', NOW() FROM wiki_page_versions WHERE id = '$current_version';" >/dev/null
psql_value "INSERT INTO wiki_page_sources (tenant_id, knowledge_base_id, wiki_page_version_id, document_id, document_version_id, chunk_id, source_path, heading_path, page_number) SELECT tenant_id, knowledge_base_id, '$clone_version', document_id, document_version_id, chunk_id, source_path, heading_path, page_number FROM wiki_page_sources WHERE wiki_page_version_id = '$current_version';" >/dev/null
[[ "$(psql_value "SELECT count(*) FROM wiki_page_sources WHERE wiki_page_version_id = '$clone_version';")" -gt 0 ]] \
  || fail "acceptance clone has no sources"
request POST "/api/v1/wiki/pages/$alpha_page/versions/$clone_version/review" 200 "$tmp_dir/review-clone.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"approve","note":"rollback fixture"}'
request POST "/api/v1/wiki/pages/$alpha_page/rollback" 200 "$tmp_dir/rollback-clone.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d "{\"version_id\":\"$clone_version\"}"
assert_json "$tmp_dir/rollback-clone.json" 'data["publishedVersionId"] == "'"$clone_version"'"'
[[ "$(psql_value "SELECT wiki_page_version_id FROM knowledge_index_records WHERE wiki_page_id = '$alpha_page' AND status = 'ACTIVE';")" == "$clone_version" ]] \
  || fail "rollback did not move the knowledge index row"
request POST "/api/v1/wiki/pages/$alpha_page/rollback" 200 "$tmp_dir/rollback-back.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d "{\"version_id\":\"$current_version\"}"
assert_json "$tmp_dir/rollback-back.json" 'data["publishedVersionId"] == "'"$current_version"'"'

# ---------------------------------------------------------------------------
step "failed projection: join partial, evidence untouched, independent retry"
run_acl="$(latest_run_id)"
psql_value "UPDATE projection_stage_runs SET status = 'FAILED' WHERE projection_run_id = '$run_acl' AND stage = 'FINALIZE';" >/dev/null
psql_value "UPDATE projection_runs SET status = 'FAILED_WIKI', error_code = 'acceptance_injected_failure', error_message = 'injected for acceptance' WHERE id = '$run_acl';" >/dev/null
request GET "/api/v1/knowledge-bases/$kb/projection" 200 "$tmp_dir/projection-failed.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/projection-failed.json" 'data["joinStatus"] == "KNOWLEDGE_PARTIAL" and data["runs"][0]["status"] == "FAILED_WIKI"'
evidence_search "$dev_token" "$alpha_marker" "$tmp_dir/evidence-failed.json"
assert_evidence_has_document "$tmp_dir/evidence-failed.json" "$doc_alpha"
[[ "$(evidence_chunk_count)" == "$baseline_chunks" ]] || fail "failed projection changed the evidence chunks"
knowledge_search "$dev_token" "$alpha_marker" 200 "$tmp_dir/knowledge-failed.json"
python3 - "$tmp_dir/knowledge-failed.json" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
assert data["joined"] is False, data
assert data["items"], "published pages should remain readable while the wiki run is failed"
PY
request POST "/api/v1/wiki/runs/$run_acl/retry" 403 "$tmp_dir/retry-key.json" \
  -H "$(auth_header "$api_key_token")"
request POST "/api/v1/wiki/runs/$run_acl/retry" 403 "$tmp_dir/retry-viewer.json" \
  -H "$(auth_header "$viewer_token")"
versions_before_retry="$(page_version_count)"
request POST "/api/v1/wiki/runs/$run_acl/retry" 202 "$tmp_dir/retry-ok.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/retry-ok.json" 'data["status"] == "RETRY_WIKI"'
wait_run_status "$run_acl" WIKI_READY "$tmp_dir/retry-done.json"
[[ "$(page_version_count)" == "$versions_before_retry" ]] || fail "retry regenerated page versions"
request GET "/api/v1/knowledge-bases/$kb/projection" 200 "$tmp_dir/projection-retried.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/projection-retried.json" 'data["joinStatus"] == "KNOWLEDGE_READY" and data["runs"][0]["status"] == "WIKI_READY"'
knowledge_search "$dev_token" "$alpha_marker" 200 "$tmp_dir/knowledge-retried.json"
assert_knowledge_items "$tmp_dir/knowledge-retried.json" "$alpha_page" "$beta_page" "$overview_page"
request POST "/api/v1/wiki/runs/$run_acl/retry" 409 "$tmp_dir/retry-ready.json" \
  -H "$(auth_header "$dev_token")"

# ---------------------------------------------------------------------------
step "manual rebuild keeps the evidence index unchanged"
request POST "/api/v1/knowledge-bases/$kb/wiki/rebuild" 202 "$tmp_dir/rebuild.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/rebuild.json" 'data["started"] == 1'
wait_projection_ready "$tmp_dir/projection-rebuilt.json"
[[ "$(evidence_chunk_count)" == "$baseline_chunks" ]] || fail "manual rebuild changed the evidence chunks"
evidence_search "$dev_token" "$beta_marker" "$tmp_dir/evidence-rebuilt.json"
assert_evidence_has_document "$tmp_dir/evidence-rebuilt.json" "$doc_beta"
[[ "$(index_row_count)" == "3" ]] || fail "manual rebuild removed published knowledge index rows"
request GET "/api/v1/knowledge-bases/$kb/wiki/pages" 200 "$tmp_dir/pages-rebuilt.json" \
  -H "$(auth_header "$dev_token")"
python3 - "$tmp_dir/pages-rebuilt.json" <<'PY'
import json
import sys

for item in json.load(open(sys.argv[1]))["pages"]:
    if item["page"]["status"] != "PUBLISHED":
        raise SystemExit(f"published page regressed: {item['page']['id']} {item['page']['status']}")
PY
knowledge_search "$dev_token" "$alpha_marker" 200 "$tmp_dir/knowledge-rebuilt.json"
assert_knowledge_items "$tmp_dir/knowledge-rebuilt.json" "$alpha_page" "$beta_page" "$overview_page"

# ---------------------------------------------------------------------------
step "revoked membership and disabled wiki remove derived content immediately"
request DELETE "/api/v1/knowledge-bases/$kb/members/viewer-a" 204 "$tmp_dir/remove-viewer.json" \
  -H "$(auth_header "$dev_token")"
[[ "$(index_row_count)" == "0" ]] || fail "member removal did not delete knowledge index rows"
request GET "/api/v1/knowledge-bases/$kb/wiki/pages" 403 "$tmp_dir/pages-revoked.json" \
  -H "$(auth_header "$viewer_token")"
knowledge_search "$viewer_token" "$alpha_marker" 403 "$tmp_dir/knowledge-revoked.json"
request GET "/api/v1/knowledge-bases/$kb/wiki/pages" 200 "$tmp_dir/pages-stale-member.json" \
  -H "$(auth_header "$dev_token")"
python3 - "$tmp_dir/pages-stale-member.json" <<'PY'
import json
import sys

for item in json.load(open(sys.argv[1]))["pages"]:
    assert item["page"]["status"] == "STALE", item["page"]
PY

request PATCH "/api/v1/knowledge-bases/$kb" 200 "$tmp_dir/strategy-off.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":false,"required_capabilities":["evidence"]}}'
request GET "/api/v1/knowledge-bases/$kb/projection" 200 "$tmp_dir/projection-disabled.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/projection-disabled.json" \
  'data["wikiEnabled"] is False and data["joinStatus"] == "EVIDENCE_READY"'
knowledge_search "$dev_token" "$alpha_marker" 200 "$tmp_dir/knowledge-disabled.json"
assert_knowledge_empty "$tmp_dir/knowledge-disabled.json"
evidence_search "$dev_token" "$alpha_marker" "$tmp_dir/evidence-disabled.json"
assert_evidence_has_document "$tmp_dir/evidence-disabled.json" "$doc_alpha"

# ---------------------------------------------------------------------------
step "cleanup: delete the fixture knowledge base and verify projection cascade"
request DELETE "/api/v1/api-keys/$api_key_id" 204 "$tmp_dir/delete-key.json" \
  -H "$(auth_header "$dev_token")"
request DELETE "/api/v1/knowledge-bases/$kb" 204 "$tmp_dir/delete-kb.json" \
  -H "$(auth_header "$dev_token")"
remaining="$(psql_value "SELECT (SELECT count(*) FROM projection_runs WHERE knowledge_base_id = '$kb'), (SELECT count(*) FROM projection_stage_runs r JOIN projection_runs p ON p.id = r.projection_run_id WHERE p.knowledge_base_id = '$kb'), (SELECT count(*) FROM wiki_pages WHERE knowledge_base_id = '$kb'), (SELECT count(*) FROM wiki_page_versions WHERE knowledge_base_id = '$kb'), (SELECT count(*) FROM knowledge_index_records WHERE knowledge_base_id = '$kb');")"
[[ "$remaining" == "0|0|0|0|0" ]] || fail "projection rows survived knowledge base deletion: $remaining"
kb=""

printf '\nM6 acceptance passed\n'
