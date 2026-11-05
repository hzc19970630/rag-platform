#!/usr/bin/env bash
# M7 acceptance: relation projection, graph store, review and bounded queries.
#
# Scenario A (graph only) proves the extraction contract end to end:
#   * graph_enabled starts one relation run; COLLECT/EXTRACT/FINALIZE succeed;
#   * every relation carries the current document version and chunk, and a
#     snapshot with an unsourced relation cannot be reviewed into ACTIVE;
#   * normalization/alias rows are auditable, review is permissioned and only
#     PENDING snapshots under the current ACL can be approved;
#   * depth and node limits are enforced and every node, edge and path is
#     filtered by the caller's authorization scope;
#   * deleting a source document hides the hidden node and edge immediately,
#     then stales the snapshot and the rebuilt snapshot no longer contains it;
#   * replacing a document version hides the old relations immediately and the
#     rebuild cites only the new version;
#   * re-extracting the same evidence is logically idempotent;
#   * an ACL change stales the active snapshot and a rebuild under the new ACL
#     must be reviewed again.
#
# Scenario B (wiki + graph) proves the ProjectionJoin contract: a failed
# relation run turns the join PARTIAL while the wiki run, pages and Evidence RAG
# stay usable, and retrying only the relation run restores KNOWLEDGE_READY
# without touching wiki page versions or evidence chunks.
#
# Scenario C proves the graph is optional: Evidence RAG works before the switch,
# with the switch on, and after disabling it; no query requires a graph lookup.
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
kb=""
kb2=""
dev_token=""
viewer_token=""
editor_token=""
other_token=""
api_key_id=""
api_key_id2=""
api_key_token=""

cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'M7 acceptance failed: %s\n' "$*" >&2; exit 1; }

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

request_code() {
  local method="$1" path="$2" output="$3"
  shift 3
  curl -sS -o "$output" -w '%{http_code}' -X "$method" "$base_url$path" "$@"
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
# Generator expressions resolve free names from globals, so data and the
# helpers must share one namespace.
namespace = {
    "__builtins__": {}, "data": data, "len": len, "any": any, "all": all,
    "str": str, "int": int, "set": set, "sorted": sorted, "sum": sum,
    "min": min, "max": max,
}
if not eval(expression, namespace):
    raise SystemExit(f"assertion failed: {expression}\n{json.dumps(data)[:3000]}")
PY
}

json_field() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
try:
    for part in sys.argv[2].split("."):
        data = data[int(part)] if part.isdigit() else data[part]
except (KeyError, IndexError, TypeError, ValueError):
    data = ""
print(data)
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

relation_run_id() {
  request GET "/api/v1/knowledge-bases/$1/projection" 200 "$2" -H "$(auth_header "$dev_token")"
  json_field "$2" relationRuns.0.id
}

wait_relation_ready() {
  local knowledge_base="$1" previous_id="$2" output="$3"
  local status="" current=""
  for _ in $(seq 1 300); do
    request GET "/api/v1/knowledge-bases/$knowledge_base/projection" 200 "$output" -H "$(auth_header "$dev_token")"
    current="$(json_field "$output" relationRuns.0.id)"
    status="$(json_field "$output" relationRuns.0.status)"
    if [[ "$current" != "$previous_id" && "$status" == "RELATION_READY" ]]; then
      return 0
    fi
    case "$status" in
      FAILED_RELATION|CANCELLED)
        cat "$output" >&2
        fail "relation run reached $status while waiting for RELATION_READY"
        ;;
    esac
    sleep 1
  done
  cat "$output" >&2
  fail "relation run did not reach RELATION_READY (last: $status)"
}

wait_projection_both_ready() {
  local knowledge_base="$1" output="$2"
  local wiki="" relation="" join=""
  for _ in $(seq 1 300); do
    request GET "/api/v1/knowledge-bases/$knowledge_base/projection" 200 "$output" -H "$(auth_header "$dev_token")"
    wiki="$(json_field "$output" runs.0.status)"
    relation="$(json_field "$output" relationRuns.0.status)"
    join="$(json_field "$output" joinStatus)"
    if [[ "$wiki" == "WIKI_READY" && "$relation" == "RELATION_READY" && "$join" == "KNOWLEDGE_READY" ]]; then
      return 0
    fi
    case "$wiki" in
      FAILED_WIKI|CANCELLED)
        cat "$output" >&2
        fail "wiki run reached $wiki while waiting for both projections"
        ;;
    esac
    case "$relation" in
      FAILED_RELATION|CANCELLED)
        cat "$output" >&2
        fail "relation run reached $relation while waiting for both projections"
        ;;
    esac
    sleep 1
  done
  cat "$output" >&2
  fail "projections did not both become ready (last: $wiki/$relation/$join)"
}

latest_snapshot_id() {
  local knowledge_base="$1" output="$2"
  request GET "/api/v1/knowledge-bases/$knowledge_base/graph/snapshots" 200 "$output" -H "$(auth_header "$dev_token")"
  json_field "$output" snapshots.0.id
}

latest_snapshot_status() {
  local knowledge_base="$1" output="$2"
  request GET "/api/v1/knowledge-bases/$knowledge_base/graph/snapshots" 200 "$output" -H "$(auth_header "$dev_token")"
  json_field "$output" snapshots.0.status
}

wait_snapshot_status() {
  local knowledge_base="$1" expected="$2" output="$3"
  local status=""
  for _ in $(seq 1 180); do
    request GET "/api/v1/knowledge-bases/$knowledge_base/graph/snapshots" 200 "$output" -H "$(auth_header "$dev_token")"
    status="$(json_field "$output" snapshots.0.status)"
    [[ "$status" == "$expected" ]] && return 0
    sleep 1
  done
  cat "$output" >&2
  fail "newest snapshot did not reach $expected (last: $status)"
}

graph_query() {
  local bearer="$1" knowledge_base="$2" query="$3" depth="$4" limit="$5" expected="$6" output="$7"
  local code
  code="$(curl -sS -o "$output" -w '%{http_code}' -G "$base_url/api/v1/knowledge-bases/$knowledge_base/graph/query" \
    --data-urlencode "q=$query" --data-urlencode "depth=$depth" --data-urlencode "limit=$limit" \
    -H "$(auth_header "$bearer")")"
  if [[ "$code" != "$expected" ]]; then
    printf 'expected graph query %q, got %s: ' "$query" "$code" >&2
    cat "$output" >&2
    exit 1
  fi
}

evidence_search() {
  local bearer="$1" knowledge_base="$2" query="$3" output="$4"
  local body
  body="$(python3 - "$query" "$knowledge_base" <<'PY'
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

assert_graph_shape() {
  # Validates that nodes, edges and paths are consistent and every returned
  # element carries a source chunk.
  python3 - "$1" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
node_ids = {node["id"] for node in data["nodes"]}
edge_ids = {edge["id"] for edge in data["edges"]}
for node in data["nodes"]:
    assert node["depth"] >= 0, node
    assert node["sources"], f"node without sources: {node}"
    for source in node["sources"]:
        assert source["documentId"] and source["documentVersionId"] and source["chunkId"], source
for edge in data["edges"]:
    assert edge["subjectId"] in node_ids, edge
    assert edge["objectId"] in node_ids, edge
    assert edge["sources"], f"edge without sources: {edge}"
    for source in edge["sources"]:
        assert source["documentId"] and source["documentVersionId"] and source["chunkId"], source
for path in data["paths"]:
    assert path["nodeIds"], path
    assert set(path["nodeIds"]) <= node_ids, (path, node_ids)
    assert set(path["edgeIds"]) <= edge_ids, (path, edge_ids)
    assert len(path["edgeIds"]) == len(path["nodeIds"]) - 1, path
PY
}

assert_snapshot_traceable() {
  # Every relation has at least one source chunk, and every source still names
  # the current, evidence-ready document version and chunk.
  python3 - "$1" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
snapshot_id = data["snapshot"]["id"]
assert data["snapshot"]["stats"], data["snapshot"]
assert data["relations"], "snapshot has no relations"
for relation in data["relations"]:
    assert relation["sources"], f"untraceable relation {relation['id']}"
    for source in relation["sources"]:
        assert source["documentId"], source
        assert source["documentVersionId"], source
        assert source["chunkId"], source
PY
}

entity_count() {
  psql_value "SELECT count(*) FROM graph_entities WHERE knowledge_base_id = '$1';"
}

relation_count() {
  psql_value "SELECT count(*) FROM graph_relations WHERE knowledge_base_id = '$1';"
}

alias_count() {
  psql_value "SELECT count(*) FROM graph_entity_aliases WHERE knowledge_base_id = '$1';"
}

source_count() {
  psql_value "SELECT count(*) FROM graph_relation_sources WHERE knowledge_base_id = '$1';"
}

untraceable_relation_count() {
  psql_value "SELECT count(*) FROM graph_relations r WHERE r.knowledge_base_id = '$1' AND NOT EXISTS (SELECT 1 FROM graph_relation_sources s WHERE s.relation_id = r.id);"
}

# ---------------------------------------------------------------------------
step "preflight: health, migrations and M7 tables"
request GET /healthz 200 "$tmp_dir/health.json"
migrate_output="$("${compose[@]}" run --rm --no-deps migrate -check 2>&1)"
printf '%s\n' "$migrate_output" | grep -q migrations_compatible || {
  printf '%s\n' "$migrate_output" >&2
  fail "migration -check did not report compatibility"
}
# 0020 touches an existing table on upgrades, so re-execute it here: the
# guarded ADD COLUMN statements must succeed a second time, and the four
# runtime model-binding columns must be present afterwards.
"${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -v ON_ERROR_STOP=1 -q \
  < migrations/postgres/0020_model_registry.sql || fail "migration 0020 is not repeatable"
model_columns="$(psql_value "SELECT string_agg(column_name, ',' ORDER BY column_name) FROM information_schema.columns WHERE table_name = 'knowledge_bases' AND column_name IN ('chat_model','chat_model_version','rerank_model','rerank_model_version');")"
[[ "$model_columns" == "chat_model,chat_model_version,rerank_model,rerank_model_version" ]] \
  || fail "model registry columns missing after 0020: $model_columns"
tables="$(psql_value "SELECT string_agg(table_name, ',' ORDER BY table_name) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('graph_snapshots','graph_entities','graph_entity_aliases','graph_relations','graph_relation_sources');")"
[[ "$tables" == "graph_entities,graph_entity_aliases,graph_relation_sources,graph_relations,graph_snapshots" ]] \
  || fail "M7 tables missing: $tables"

# ---------------------------------------------------------------------------
step "identity and fixture knowledge base"
dev_token="$(login dev-user dev-tenant "$tmp_dir/login-dev.json")"
viewer_token="$(login viewer-a dev-tenant "$tmp_dir/login-viewer.json")"
editor_token="$(login editor-a dev-tenant "$tmp_dir/login-editor.json")"
other_token="$(login other-user other-tenant "$tmp_dir/login-other.json")"

request POST /api/v1/knowledge-bases 201 "$tmp_dir/kb.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d "{\"name\":\"M7 acceptance $run_id\",\"description\":\"created by m7_acceptance.sh\"}"
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

The Alpha launch code is ALPHA-$run_id.

[[Acme|supplies|Beta Corp|ACME;Acme Corporation]]

[[Beta Corp|operates|Gamma Plant]]

[[Gamma Plant|located_in|Delta Region]]
EOF
cat > "$tmp_dir/shared.md" <<EOF
# Shared registry

The shared registry links Alpha partners.

[[Acme|partner_of|Shared Co]]
EOF
cat > "$tmp_dir/hidden.md" <<EOF
# Hidden annex

This annex is only reachable through the hidden document.

[[Shared Co|owns|Secret Project]]

[[Secret Project|managed_by|Hidden Team]]
EOF

request POST "/api/v1/knowledge-bases/$kb/documents" 202 "$tmp_dir/upload-alpha.json" \
  -H "$(auth_header "$dev_token")" -F "file=@$tmp_dir/alpha.md;type=text/markdown"
request POST "/api/v1/knowledge-bases/$kb/documents" 202 "$tmp_dir/upload-shared.json" \
  -H "$(auth_header "$dev_token")" -F "file=@$tmp_dir/shared.md;type=text/markdown"
request POST "/api/v1/knowledge-bases/$kb/documents" 202 "$tmp_dir/upload-hidden.json" \
  -H "$(auth_header "$dev_token")" -F "file=@$tmp_dir/hidden.md;type=text/markdown"
doc_alpha="$(json_field "$tmp_dir/upload-alpha.json" document.id)"
doc_shared="$(json_field "$tmp_dir/upload-shared.json" document.id)"
doc_hidden="$(json_field "$tmp_dir/upload-hidden.json" document.id)"
wait_document_ready "$doc_alpha" "$tmp_dir/doc-alpha.json"
wait_document_ready "$doc_shared" "$tmp_dir/doc-shared.json"
wait_document_ready "$doc_hidden" "$tmp_dir/doc-hidden.json"
ver_alpha="$(json_field "$tmp_dir/doc-alpha.json" document.latestVersionId)"
ver_hidden="$(json_field "$tmp_dir/doc-hidden.json" document.latestVersionId)"

request GET "/api/v1/knowledge-bases/$kb/projection" 200 "$tmp_dir/projection-off.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/projection-off.json" \
  'data["graphEnabled"] is False and data["joinStatus"] == "EVIDENCE_READY" and len(data["relationRuns"]) == 0'
evidence_search "$dev_token" "$kb" "ALPHA-$run_id" "$tmp_dir/evidence-before-graph.json"
assert_evidence_has_document "$tmp_dir/evidence-before-graph.json" "$doc_alpha"

# ---------------------------------------------------------------------------
step "graph switch validation and graph-only enablement"
request PATCH "/api/v1/knowledge-bases/$kb" 400 "$tmp_dir/strategy-graph-switch.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":false,"required_capabilities":["graph"]}}'
grep -q 'must enable graph_enabled' "$tmp_dir/strategy-graph-switch.json" \
  || fail "graph capability rejection did not explain itself"
request PATCH "/api/v1/knowledge-bases/$kb" 400 "$tmp_dir/strategy-relation-switch.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":false,"required_capabilities":["relation"]}}'
grep -q 'must enable graph_enabled' "$tmp_dir/strategy-relation-switch.json" \
  || fail "relation capability rejection did not explain itself"

request PATCH "/api/v1/knowledge-bases/$kb" 200 "$tmp_dir/strategy-on.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":true,"required_capabilities":["graph"]}}'
acl_enabled="$(psql_value "SELECT acl_version FROM knowledge_bases WHERE id = '$kb';")"
wait_relation_ready "$kb" "" "$tmp_dir/projection-ready.json"
run_initial="$(relation_run_id "$kb" "$tmp_dir/projection-ready-runs.json")"
request GET "/api/v1/graph/runs/$run_initial" 200 "$tmp_dir/run-initial.json" -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/run-initial.json" \
  'data["run"]["status"] == "RELATION_READY" and data["run"]["projectionType"] == "relation" and len(data["stages"]) == 3 and all(stage["status"] == "SUCCEEDED" for stage in data["stages"]) and sorted(stage["stage"] for stage in data["stages"]) == ["COLLECT","EXTRACT","FINALIZE"] and data["run"]["model"] != "" and data["run"]["promptVersion"].startswith("relation_extraction/") and len(data["run"]["sourceHash"]) == 64 and data["run"]["aclVersion"] == '"$acl_enabled"' and data["snapshot"]["status"] == "PENDING_REVIEW"'
snapshot_initial="$(json_field "$tmp_dir/run-initial.json" snapshot.id)"
[[ "$(json_field "$tmp_dir/run-initial.json" snapshot.stats)" != "{}" ]] || fail "snapshot stats missing"

# A PENDING snapshot is not queryable: review is the only publication gate.
graph_query "$viewer_token" "$kb" "Acme" 3 30 200 "$tmp_dir/query-pending.json"
assert_json "$tmp_dir/query-pending.json" 'data.get("snapshotId", "") == "" and data["nodes"] == [] and data["edges"] == []'

# ---------------------------------------------------------------------------
step "traceability: every relation names its current document version and chunk"
request GET "/api/v1/graph/snapshots/$snapshot_initial" 200 "$tmp_dir/snapshot-initial.json" \
  -H "$(auth_header "$dev_token")"
assert_snapshot_traceable "$tmp_dir/snapshot-initial.json"
python3 - "$tmp_dir/snapshot-initial.json" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
actual = {}
for relation in data["relations"]:
    key = (relation["subject"].lower(), relation["predicate"].lower(), relation["object"].lower())
    actual[key] = actual.get(key, 0) + 1
expected = [
    ("acme", "supplies", "beta corp"),
    ("acme", "partner_of", "shared co"),
    ("beta corp", "operates", "gamma plant"),
    ("gamma plant", "located_in", "delta region"),
    ("shared co", "owns", "secret project"),
    ("secret project", "managed_by", "hidden team"),
]
for key in expected:
    assert actual.get(key) == 1, (key, actual.get(key), sorted(actual)[:30])
PY

expected_sources="$(psql_value "SELECT count(*) FROM graph_relations r JOIN graph_entities su ON su.id = r.subject_id JOIN graph_entities ob ON ob.id = r.object_id JOIN graph_relation_sources s ON s.relation_id = r.id JOIN chunks c ON c.id = s.chunk_id WHERE r.snapshot_id = '$snapshot_initial' AND su.canonical_key = 'acme' AND ob.canonical_key = 'beta corp' AND lower(r.predicate) = 'supplies' AND s.document_id = '$doc_alpha' AND s.document_version_id = '$ver_alpha' AND c.document_version_id = '$ver_alpha' AND c.status = 'EVIDENCE_READY';")"
[[ "$expected_sources" -ge 1 ]] || fail "supplies relation does not cite the current alpha version and chunk"
[[ "$(psql_value "SELECT count(*) FROM graph_relations r WHERE r.snapshot_id = '$snapshot_initial' AND NOT EXISTS (SELECT 1 FROM graph_relation_sources s WHERE s.relation_id = r.id);")" == "0" ]] \
  || fail "snapshot contains a relation without a source"
[[ "$(psql_value "SELECT count(*) FROM graph_relation_sources s JOIN graph_relations r ON r.id = s.relation_id JOIN document_versions dv ON dv.id = s.document_version_id JOIN documents d ON d.id = s.document_id JOIN chunks c ON c.id = s.chunk_id WHERE r.snapshot_id = '$snapshot_initial' AND (d.latest_version_id <> s.document_version_id OR d.status <> 'EVIDENCE_READY' OR dv.status <> 'EVIDENCE_READY' OR c.status <> 'EVIDENCE_READY');")" == "0" ]] \
  || fail "snapshot cites a stale source"
[[ "$(psql_value "SELECT rule FROM graph_entity_aliases a JOIN graph_entities e ON e.id = a.entity_id WHERE e.snapshot_id = '$snapshot_initial' AND e.canonical_key = 'acme' AND a.normalized = 'acme corporation';")" == "declared_alias" ]] \
  || fail "declared alias is missing or its audit rule changed"
[[ "$(untraceable_relation_count "$kb")" == "0" ]] || fail "untraceable relations exist"

# ---------------------------------------------------------------------------
step "review permissions and activation"
request POST "/api/v1/graph/snapshots/$snapshot_initial/review" 400 "$tmp_dir/review-invalid.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"maybe"}'
request POST "/api/v1/graph/snapshots/$snapshot_initial/review" 403 "$tmp_dir/review-viewer.json" \
  -H "$(auth_header "$viewer_token")" -H 'Content-Type: application/json' -d '{"action":"approve"}'
request POST "/api/v1/graph/snapshots/$snapshot_initial/review" 403 "$tmp_dir/review-editor.json" \
  -H "$(auth_header "$editor_token")" -H 'Content-Type: application/json' -d '{"action":"approve"}'
request POST "/api/v1/graph/snapshots/$snapshot_initial/review" 200 "$tmp_dir/review-ok.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"approve","note":"m7 acceptance"}'
assert_json "$tmp_dir/review-ok.json" 'data["status"] == "ACTIVE" and data["reviewedBy"] == "dev-user"'
request POST "/api/v1/graph/snapshots/$snapshot_initial/review" 409 "$tmp_dir/review-twice.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"approve"}'

# ---------------------------------------------------------------------------
step "bounded queries: depth, node limit, aliases and full path provenance"
graph_query "$viewer_token" "$kb" "ACME" 1 30 200 "$tmp_dir/query-depth1.json"
assert_graph_shape "$tmp_dir/query-depth1.json"
python3 - "$tmp_dir/query-depth1.json" "$snapshot_initial" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
assert data.get("snapshotId") == sys.argv[2], data.get("snapshotId")
names = {node["name"] for node in data["nodes"]}
assert {"Acme", "Beta Corp", "Shared Co"} <= names, names
assert "Gamma Plant" not in names, names
assert "Delta Region" not in names, names
predicates = {edge["predicate"] for edge in data["edges"]}
assert {"supplies", "partner_of"} <= predicates, predicates
PY

graph_query "$viewer_token" "$kb" "Acme" 2 30 200 "$tmp_dir/query-depth2.json"
assert_graph_shape "$tmp_dir/query-depth2.json"
assert_json "$tmp_dir/query-depth2.json" 'any(node["name"] == "Gamma Plant" for node in data["nodes"]) and not any(node["name"] == "Delta Region" for node in data["nodes"])'
assert_json "$tmp_dir/query-depth2.json" 'any(len(path["nodeIds"]) == 3 for path in data["paths"])'

graph_query "$viewer_token" "$kb" "Acme" 3 30 200 "$tmp_dir/query-depth3.json"
assert_graph_shape "$tmp_dir/query-depth3.json"
assert_json "$tmp_dir/query-depth3.json" 'any(node["name"] == "Delta Region" for node in data["nodes"]) and any(len(path["nodeIds"]) == 4 for path in data["paths"]) and data["joined"] is True'

graph_query "$viewer_token" "$kb" "Acme" 3 2 200 "$tmp_dir/query-limited.json"
python3 - "$tmp_dir/query-limited.json" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
assert len(data["nodes"]) <= 2, data["nodes"]
assert data["truncated"] is True, data
assert len(data["edges"]) == len(data["nodes"]) - 1, (data["nodes"], data["edges"])
PY

graph_query "$other_token" "$kb" "Acme" 3 30 403 "$tmp_dir/query-other.json"
request GET "/api/v1/knowledge-bases/$kb/graph/snapshots" 403 "$tmp_dir/snapshots-other.json" \
  -H "$(auth_header "$other_token")"
request POST "/api/v1/knowledge-bases/$kb/graph/rebuild" 403 "$tmp_dir/rebuild-viewer.json" \
  -H "$(auth_header "$viewer_token")"

# ---------------------------------------------------------------------------
step "hidden source: hidden node and hidden edge leave no side channel"
request DELETE "/api/v1/documents/$doc_hidden" 204 "$tmp_dir/delete-hidden.json" \
  -H "$(auth_header "$dev_token")"

graph_query "$viewer_token" "$kb" "Shared Co" 2 30 200 "$tmp_dir/query-hidden-live.json"
assert_graph_shape "$tmp_dir/query-hidden-live.json"
python3 - "$tmp_dir/query-hidden-live.json" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
names = {node["name"] for node in data["nodes"]}
assert "Shared Co" in names, names
assert "Secret Project" not in names, names
assert "Hidden Team" not in names, names
predicates = {edge["predicate"] for edge in data["edges"]}
assert "owns" not in predicates, predicates
assert "partner_of" in predicates, predicates
PY
graph_query "$viewer_token" "$kb" "Secret Project" 3 30 200 "$tmp_dir/query-hidden-seed.json"
assert_json "$tmp_dir/query-hidden-seed.json" 'data["nodes"] == [] and data["edges"] == [] and data["paths"] == []'

wait_snapshot_status "$kb" STALE "$tmp_dir/snapshots-stale-hidden.json"
graph_query "$viewer_token" "$kb" "Acme" 3 30 200 "$tmp_dir/query-stale.json"
assert_json "$tmp_dir/query-stale.json" 'data["nodes"] == [] and data["edges"] == []'

request POST "/api/v1/knowledge-bases/$kb/graph/rebuild" 202 "$tmp_dir/rebuild-after-hidden.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/rebuild-after-hidden.json" 'data["started"] == 1'
wait_relation_ready "$kb" "$run_initial" "$tmp_dir/projection-after-hidden.json"
run_hidden="$(relation_run_id "$kb" "$tmp_dir/projection-after-hidden-runs.json")"
snapshot_hidden="$(latest_snapshot_id "$kb" "$tmp_dir/snapshots-after-hidden.json")"
[[ "$(latest_snapshot_status "$kb" "$tmp_dir/snapshots-after-hidden-status.json")" == "PENDING_REVIEW" ]] \
  || fail "rebuilt snapshot after source deletion is not PENDING_REVIEW"
request POST "/api/v1/graph/snapshots/$snapshot_hidden/review" 200 "$tmp_dir/review-hidden.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"approve"}'

graph_query "$viewer_token" "$kb" "Shared Co" 2 30 200 "$tmp_dir/query-hidden-rebuilt.json"
assert_graph_shape "$tmp_dir/query-hidden-rebuilt.json"
python3 - "$tmp_dir/query-hidden-rebuilt.json" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
names = {node["name"] for node in data["nodes"]}
assert "Shared Co" in names and "Acme" in names, names
assert "Secret Project" not in names and "Hidden Team" not in names, names
assert all(edge["predicate"] != "owns" for edge in data["edges"]), data["edges"]
PY
[[ "$(psql_value "SELECT count(*) FROM graph_entities WHERE snapshot_id = '$snapshot_hidden' AND canonical_key IN ('secret project','hidden team');")" == "0" ]] \
  || fail "deleted document entities survived the rebuild"
[[ "$(psql_value "SELECT count(*) FROM graph_relation_sources WHERE relation_id IN (SELECT id FROM graph_relations WHERE snapshot_id = '$snapshot_hidden') AND document_id = '$doc_hidden';")" == "0" ]] \
  || fail "deleted document is still a relation source"

# ---------------------------------------------------------------------------
step "version replacement: old-version relations vanish, rebuild cites the new version"
request POST "/api/v1/documents/$doc_alpha/reparse" 202 "$tmp_dir/reparse-alpha.json" \
  -H "$(auth_header "$dev_token")"
ver_alpha_new="$(json_field "$tmp_dir/reparse-alpha.json" version.id)"
[[ "$ver_alpha_new" != "$ver_alpha" ]] || fail "reparse did not create a new version"
wait_document_ready "$doc_alpha" "$tmp_dir/doc-alpha-reparsed.json"

graph_query "$viewer_token" "$kb" "Acme" 3 30 200 "$tmp_dir/query-before-version-rebuild.json"
python3 - "$tmp_dir/query-before-version-rebuild.json" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
names = {node["name"] for node in data["nodes"]}
# A concurrent reconcile may already have staled the snapshot; either way the
# superseded version must not remain queryable.
assert "Beta Corp" not in names and "Gamma Plant" not in names, names
if data.get("snapshotId"):
    assert "Acme" in names, names
PY

request POST "/api/v1/knowledge-bases/$kb/graph/rebuild" 202 "$tmp_dir/rebuild-version.json" \
  -H "$(auth_header "$dev_token")"
wait_relation_ready "$kb" "$run_hidden" "$tmp_dir/projection-after-version.json"
run_version="$(relation_run_id "$kb" "$tmp_dir/projection-after-version-runs.json")"
snapshot_version="$(latest_snapshot_id "$kb" "$tmp_dir/snapshots-after-version.json")"
request POST "/api/v1/graph/snapshots/$snapshot_version/review" 200 "$tmp_dir/review-version.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"approve"}'

[[ "$(psql_value "SELECT count(*) FROM graph_relation_sources s JOIN graph_relations r ON r.id = s.relation_id WHERE r.snapshot_id = '$snapshot_version' AND s.document_version_id = '$ver_alpha';")" == "0" ]] \
  || fail "rebuilt snapshot still cites the superseded alpha version"
[[ "$(psql_value "SELECT count(*) FROM graph_relation_sources s JOIN graph_relations r ON r.id = s.relation_id JOIN graph_entities su ON su.id = r.subject_id JOIN graph_entities ob ON ob.id = r.object_id WHERE r.snapshot_id = '$snapshot_version' AND su.canonical_key = 'acme' AND ob.canonical_key = 'beta corp' AND s.document_version_id = '$ver_alpha_new';")" -ge 1 ]] \
  || fail "rebuilt snapshot does not cite the new alpha version"
graph_query "$viewer_token" "$kb" "Acme" 3 30 200 "$tmp_dir/query-after-version-rebuild.json"
assert_json "$tmp_dir/query-after-version-rebuild.json" 'any(node["name"] == "Beta Corp" for node in data["nodes"]) and any(node["name"] == "Delta Region" for node in data["nodes"])'

# ---------------------------------------------------------------------------
step "idempotent re-extraction and unsourced relations never become usable"
request POST "/api/v1/knowledge-bases/$kb/graph/rebuild" 202 "$tmp_dir/rebuild-idempotent.json" \
  -H "$(auth_header "$dev_token")"
wait_relation_ready "$kb" "$run_version" "$tmp_dir/projection-idempotent.json"
run_idempotent="$(relation_run_id "$kb" "$tmp_dir/projection-idempotent-runs.json")"
snapshot_idempotent="$(latest_snapshot_id "$kb" "$tmp_dir/snapshots-idempotent.json")"
[[ "$snapshot_idempotent" != "$snapshot_version" ]] || fail "second rebuild did not create a new snapshot"
request GET "/api/v1/graph/snapshots/$snapshot_version" 200 "$tmp_dir/snapshot-first-pass.json" \
  -H "$(auth_header "$dev_token")"
request GET "/api/v1/graph/snapshots/$snapshot_idempotent" 200 "$tmp_dir/snapshot-second-pass.json" \
  -H "$(auth_header "$dev_token")"
python3 - "$tmp_dir/snapshot-first-pass.json" "$tmp_dir/snapshot-second-pass.json" <<'PY'
import json
import sys

def triples(path):
    data = json.load(open(path))
    return sorted((relation["subject"].lower(), relation["predicate"].lower(), relation["object"].lower())
                  for relation in data["relations"])

expected = [
    ("acme", "supplies", "beta corp"),
    ("acme", "partner_of", "shared co"),
    ("beta corp", "operates", "gamma plant"),
    ("gamma plant", "located_in", "delta region"),
]
first, second = triples(sys.argv[1]), triples(sys.argv[2])
for key in expected:
    assert first.count(key) == 1, (key, first)
    assert second.count(key) == 1, (key, second)
PY

# Inject a relation without any source into the pending snapshot. Review must
# refuse to activate it; the row is removed afterwards so the approval is clean.
psql_value "INSERT INTO graph_relations (id, tenant_id, knowledge_base_id, snapshot_id, subject_id, predicate, object_id, properties, confidence, acl_version, status, created_at, updated_at) SELECT 'gr-unsourced-$run_id', tenant_id, knowledge_base_id, id, (SELECT id FROM graph_entities WHERE snapshot_id = '$snapshot_idempotent' ORDER BY id LIMIT 1), 'acceptance_unsourced', (SELECT id FROM graph_entities WHERE snapshot_id = '$snapshot_idempotent' ORDER BY id OFFSET 1 LIMIT 1), '{}', 1, acl_version, 'ACTIVE', NOW(), NOW() FROM graph_snapshots WHERE id = '$snapshot_idempotent';" >/dev/null
code="$(request_code POST "/api/v1/graph/snapshots/$snapshot_idempotent/review" "$tmp_dir/review-unsourced.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"approve"}')"
[[ "$code" != "200" ]] || fail "review activated a snapshot containing an unsourced relation"
[[ "$(psql_value "SELECT status FROM graph_snapshots WHERE id = '$snapshot_idempotent';")" == "PENDING_REVIEW" ]] \
  || fail "unsourced review changed the snapshot state"
psql_value "DELETE FROM graph_relations WHERE id = 'gr-unsourced-$run_id';" >/dev/null

request POST "/api/v1/graph/snapshots/$snapshot_idempotent/review" 200 "$tmp_dir/review-idempotent.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"approve"}'
[[ "$(psql_value "SELECT status FROM graph_snapshots WHERE id = '$snapshot_version';")" == "SUPERSEDED" ]] \
  || fail "approving the newer snapshot did not supersede the previous active snapshot"
[[ "$(psql_value "SELECT count(*) FROM graph_snapshots WHERE knowledge_base_id = '$kb' AND status = 'ACTIVE';")" == "1" ]] \
  || fail "more than one active snapshot exists"

# ---------------------------------------------------------------------------
step "ACL invalidation stales the snapshot; rebuild and review under the new ACL"
acl_before="$(psql_value "SELECT acl_version FROM knowledge_bases WHERE id = '$kb';")"
# ACL changes are membership changes. Metadata edits (name/description) leave
# derived content and its ACL version intact on purpose.
request POST "/api/v1/knowledge-bases/$kb/members" 201 "$tmp_dir/acl-member.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"user_id":"dev-user","role":"admin"}'
acl_after="$(psql_value "SELECT acl_version FROM knowledge_bases WHERE id = '$kb';")"
[[ "$acl_after" -eq $((acl_before + 1)) ]] || fail "ACL version did not advance"
[[ "$(psql_value "SELECT count(*) FROM graph_snapshots WHERE knowledge_base_id = '$kb' AND status IN ('ACTIVE','PENDING_REVIEW');")" == "0" ]] \
  || fail "ACL change did not invalidate snapshots immediately"
graph_query "$viewer_token" "$kb" "Acme" 3 30 200 "$tmp_dir/query-acl-stale.json"
assert_json "$tmp_dir/query-acl-stale.json" 'data["nodes"] == [] and data["edges"] == []'
request POST "/api/v1/graph/snapshots/$snapshot_idempotent/review" 409 "$tmp_dir/review-stale-acl.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"approve"}'

request POST "/api/v1/knowledge-bases/$kb/graph/rebuild" 202 "$tmp_dir/rebuild-acl.json" \
  -H "$(auth_header "$dev_token")"
wait_relation_ready "$kb" "$run_idempotent" "$tmp_dir/projection-acl.json"
run_acl="$(relation_run_id "$kb" "$tmp_dir/projection-acl-runs.json")"
[[ "$(psql_value "SELECT acl_version FROM projection_runs WHERE id = '$run_acl';")" == "$acl_after" ]] \
  || fail "rebuild run does not carry the current ACL version"
snapshot_acl="$(latest_snapshot_id "$kb" "$tmp_dir/snapshots-acl.json")"
[[ "$(psql_value "SELECT acl_version FROM graph_snapshots WHERE id = '$snapshot_acl';")" == "$acl_after" ]] \
  || fail "rebuilt snapshot does not carry the current ACL version"
request POST "/api/v1/graph/snapshots/$snapshot_acl/review" 200 "$tmp_dir/review-acl.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' -d '{"action":"approve"}'
graph_query "$viewer_token" "$kb" "Acme" 3 30 200 "$tmp_dir/query-acl-restored.json"
assert_json "$tmp_dir/query-acl-restored.json" 'any(node["name"] == "Delta Region" for node in data["nodes"])'

# ---------------------------------------------------------------------------
step "switch off: graph queries return nothing, Evidence RAG is unchanged"
request PATCH "/api/v1/knowledge-bases/$kb" 200 "$tmp_dir/strategy-off.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":false,"required_capabilities":["evidence"]}}'
request GET "/api/v1/knowledge-bases/$kb/projection" 200 "$tmp_dir/projection-disabled.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/projection-disabled.json" 'data["graphEnabled"] is False and data["joinStatus"] == "EVIDENCE_READY"'
graph_query "$viewer_token" "$kb" "Acme" 3 30 200 "$tmp_dir/query-disabled.json"
assert_json "$tmp_dir/query-disabled.json" 'data.get("snapshotId", "") == "" and data["nodes"] == [] and data["edges"] == [] and data["paths"] == []'
evidence_search "$dev_token" "$kb" "ALPHA-$run_id" "$tmp_dir/evidence-after-graph.json"
assert_evidence_has_document "$tmp_dir/evidence-after-graph.json" "$doc_alpha"

# ---------------------------------------------------------------------------
step "ProjectionJoin: a failed relation run does not block wiki or Evidence RAG"
request POST /api/v1/knowledge-bases 201 "$tmp_dir/kb2.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d "{\"name\":\"M7 join $run_id\",\"description\":\"created by m7_acceptance.sh\"}"
kb2="$(json_field "$tmp_dir/kb2.json" id)"
request POST "/api/v1/knowledge-bases/$kb2/members" 201 "$tmp_dir/member2-viewer.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"user_id":"viewer-a","role":"viewer"}'
cat > "$tmp_dir/join.md" <<EOF
# Northwind handbook

The join marker is JOIN-$run_id.

[[Northwind|ships|Contoso]]
EOF
request POST "/api/v1/knowledge-bases/$kb2/documents" 202 "$tmp_dir/upload-join.json" \
  -H "$(auth_header "$dev_token")" -F "file=@$tmp_dir/join.md;type=text/markdown"
doc_join="$(json_field "$tmp_dir/upload-join.json" document.id)"
wait_document_ready "$doc_join" "$tmp_dir/doc-join.json"
# graph_enabled but not required: the relation projection is built, yet a
# failed relation run must not gate evidence or wiki queries.
request PATCH "/api/v1/knowledge-bases/$kb2" 200 "$tmp_dir/strategy-both.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d '{"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":true,"graph_enabled":true,"required_capabilities":["evidence","wiki"]}}'
wait_projection_both_ready "$kb2" "$tmp_dir/projection-both-ready.json"
run_wiki="$(json_field "$tmp_dir/projection-both-ready.json" runs.0.id)"
run_relation="$(json_field "$tmp_dir/projection-both-ready.json" relationRuns.0.id)"
wiki_versions_before="$(psql_value "SELECT count(*) FROM wiki_page_versions WHERE knowledge_base_id = '$kb2';")"
[[ "$wiki_versions_before" -gt 0 ]] || fail "wiki produced no page versions"

psql_value "UPDATE projection_stage_runs SET status = 'FAILED', error_code = 'acceptance_injected_failure', error_message = 'injected for acceptance' WHERE projection_run_id = '$run_relation' AND stage = 'FINALIZE'; UPDATE projection_runs SET status = 'FAILED_RELATION', error_code = 'acceptance_injected_failure', error_message = 'injected for acceptance' WHERE id = '$run_relation';" >/dev/null
request GET "/api/v1/knowledge-bases/$kb2/projection" 200 "$tmp_dir/projection-partial.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/projection-partial.json" 'data["joinStatus"] == "KNOWLEDGE_PARTIAL" and data["runs"][0]["status"] == "WIKI_READY" and data["relationRuns"][0]["status"] == "FAILED_RELATION"'
request GET "/api/v1/knowledge-bases/$kb2/wiki/pages" 200 "$tmp_dir/pages-partial.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/pages-partial.json" 'data["pages"] != [] and all(item["page"]["status"] != "STALE" for item in data["pages"])'
evidence_search "$dev_token" "$kb2" "JOIN-$run_id" "$tmp_dir/evidence-partial.json"
assert_evidence_has_document "$tmp_dir/evidence-partial.json" "$doc_join"
# The failed run's snapshot was never reviewed, so the graph stays empty even
# though evidence and wiki remain usable.
graph_query "$dev_token" "$kb2" "Northwind" 2 30 200 "$tmp_dir/query-partial.json"
assert_json "$tmp_dir/query-partial.json" 'data.get("snapshotId", "") == "" and data["nodes"] == [] and data["edges"] == [] and data["joined"] is False'

request POST /api/v1/api-keys 201 "$tmp_dir/api-key2.json" \
  -H "$(auth_header "$dev_token")" -H 'Content-Type: application/json' \
  -d "{\"name\":\"m7 acceptance read\",\"capabilities\":[\"knowledge_base:read\",\"chat\"],\"knowledgeBaseIds\":[\"$kb2\"]}"
api_key_id2="$(json_field "$tmp_dir/api-key2.json" apiKey.id)"
api_key_token="$(json_field "$tmp_dir/api-key2.json" token)"
request POST "/api/v1/graph/runs/$run_relation/retry" 403 "$tmp_dir/retry-viewer.json" \
  -H "$(auth_header "$viewer_token")"
request POST "/api/v1/graph/runs/$run_relation/retry" 403 "$tmp_dir/retry-key.json" \
  -H "$(auth_header "$api_key_token")"
request POST "/api/v1/graph/runs/$run_relation/retry" 202 "$tmp_dir/retry-ok.json" \
  -H "$(auth_header "$dev_token")"
assert_json "$tmp_dir/retry-ok.json" 'data["status"] == "RETRY_RELATION" and data["projectionType"] == "relation"'
for _ in $(seq 1 300); do
  request GET "/api/v1/knowledge-bases/$kb2/projection" 200 "$tmp_dir/projection-retried.json" \
    -H "$(auth_header "$dev_token")"
  status="$(json_field "$tmp_dir/projection-retried.json" relationRuns.0.status)"
  join="$(json_field "$tmp_dir/projection-retried.json" joinStatus)"
  [[ "$status" == "RELATION_READY" && "$join" == "KNOWLEDGE_READY" ]] && break
  case "$status" in
    FAILED_RELATION|CANCELLED)
      cat "$tmp_dir/projection-retried.json" >&2
      fail "retried relation run reached $status"
      ;;
  esac
  sleep 1
done
assert_json "$tmp_dir/projection-retried.json" 'data["joinStatus"] == "KNOWLEDGE_READY" and data["relationRuns"][0]["status"] == "RELATION_READY" and data["runs"][0]["status"] == "WIKI_READY" and data["runs"][0]["id"] == "'"$run_wiki"'"'
[[ "$(psql_value "SELECT count(*) FROM wiki_page_versions WHERE knowledge_base_id = '$kb2';")" == "$wiki_versions_before" ]] \
  || fail "relation retry regenerated wiki page versions"
[[ "$(psql_value "SELECT count(*) FROM projection_runs WHERE knowledge_base_id = '$kb2' AND projection_type = 'wiki';")" == "1" ]] \
  || fail "relation retry created a wiki run"
evidence_search "$dev_token" "$kb2" "JOIN-$run_id" "$tmp_dir/evidence-after-retry.json"
assert_evidence_has_document "$tmp_dir/evidence-after-retry.json" "$doc_join"

# ---------------------------------------------------------------------------
step "cleanup: cascade removes every graph row with the knowledge base"
request DELETE "/api/v1/api-keys/$api_key_id2" 204 "$tmp_dir/delete-key2.json" \
  -H "$(auth_header "$dev_token")"
request DELETE "/api/v1/knowledge-bases/$kb2" 204 "$tmp_dir/delete-kb2.json" \
  -H "$(auth_header "$dev_token")"
request DELETE "/api/v1/knowledge-bases/$kb" 204 "$tmp_dir/delete-kb.json" \
  -H "$(auth_header "$dev_token")"
remaining="$(psql_value "SELECT (SELECT count(*) FROM graph_snapshots WHERE knowledge_base_id IN ('$kb','$kb2')), (SELECT count(*) FROM graph_entities WHERE knowledge_base_id IN ('$kb','$kb2')), (SELECT count(*) FROM graph_entity_aliases WHERE knowledge_base_id IN ('$kb','$kb2')), (SELECT count(*) FROM graph_relations WHERE knowledge_base_id IN ('$kb','$kb2')), (SELECT count(*) FROM graph_relation_sources WHERE knowledge_base_id IN ('$kb','$kb2'));")"
[[ "$remaining" == "0|0|0|0|0" ]] || fail "graph rows survived knowledge base deletion: $remaining"
kb=""
kb2=""

printf '\nM7 acceptance passed\n'
