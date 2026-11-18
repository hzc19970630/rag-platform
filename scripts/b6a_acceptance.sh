#!/usr/bin/env bash
# B6-a acceptance: folder tree, manual tags, recursive folder filtering, AND tag
# filtering, batch partial success and retrieval tag narrowing.
#
# Runs against the development compose stack and cleans up its temporary
# knowledge base on exit.
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
kb_id=""
trap 'cleanup' EXIT

headers=(-H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant')

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

python_bin="${PYTHON_BIN:-python}"
if ! "$python_bin" -c 'import sys' >/dev/null 2>&1; then
  python_bin=python3
fi

json() {
  "$python_bin" -c "import json,sys; d=json.load(open(sys.argv[1], encoding='utf-8')); print(eval(sys.argv[2], {'d': d}))" "$2" "$1"
}

cleanup() {
  if [[ -n "$kb_id" ]]; then
    curl -sS -o /dev/null -X DELETE "$base_url/api/v1/knowledge-bases/$kb_id" "${headers[@]}" || true
  fi
  rm -rf "$tmp_dir"
}

curl -fsS "$base_url/readyz" | grep -q '"status":"ok"'

request POST /api/v1/knowledge-bases 201 "$tmp_dir/kb.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d '{"name":"B6-a Acceptance"}'
kb_id="$("$python_bin" -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d["id"])' "$tmp_dir/kb.json")"

# ---------------------------------------------------------------------------
# Folder tree.
# ---------------------------------------------------------------------------
request POST "/api/v1/knowledge-bases/$kb_id/folders" 201 "$tmp_dir/root.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d '{"name":"Contracts"}'
root_folder="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$tmp_dir/root.json")"

request POST "/api/v1/knowledge-bases/$kb_id/folders" 201 "$tmp_dir/child.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"name\":\"2026\",\"parentId\":\"$root_folder\"}"
child_folder="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$tmp_dir/child.json")"
[[ "$(json 'd["depth"]' "$tmp_dir/child.json")" == "2" ]]

request POST "/api/v1/knowledge-bases/$kb_id/folders" 400 "$tmp_dir/dup.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d '{"name":"contracts"}'
request PATCH /api/v1/folders/fld-missing 404 "$tmp_dir/forged-folder.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d '{"name":"x"}'
request PATCH "/api/v1/folders/$root_folder" 400 "$tmp_dir/cycle.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"parentId\":\"$child_folder\"}"

request GET "/api/v1/knowledge-bases/$kb_id/folders" 200 "$tmp_dir/folders.json" "${headers[@]}"
[[ "$(json 'len(d["items"])' "$tmp_dir/folders.json")" == "2" ]]

# ---------------------------------------------------------------------------
# Tags.
# ---------------------------------------------------------------------------
request POST "/api/v1/knowledge-bases/$kb_id/tags" 201 "$tmp_dir/tag1.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d '{"name":"Finance"}'
tag_one="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$tmp_dir/tag1.json")"
request POST "/api/v1/knowledge-bases/$kb_id/tags" 201 "$tmp_dir/tag2.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d '{"name":"Signed"}'
tag_two="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$tmp_dir/tag2.json")"
request POST "/api/v1/knowledge-bases/$kb_id/tags" 400 "$tmp_dir/tag-dup.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d '{"name":"finance"}'

# ---------------------------------------------------------------------------
# Uploads: one inside the nested folder, one at the root.
# ---------------------------------------------------------------------------
printf 'B6-a acceptance contract text: evergreen clause\n' > "$tmp_dir/inside.md"
printf 'B6-a acceptance root text: appendix material\n' > "$tmp_dir/root.md"
request POST "/api/v1/knowledge-bases/$kb_id/documents" 202 "$tmp_dir/upload-inside.json" "${headers[@]}" \
  -H "Idempotency-Key: b6a-inside-$(date +%s)" -F "file=@$tmp_dir/inside.md;type=text/markdown" -F "folder_id=$child_folder"
inside_doc="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["document"]["id"])' "$tmp_dir/upload-inside.json")"
inside_version="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"]["id"])' "$tmp_dir/upload-inside.json")"

request POST "/api/v1/knowledge-bases/$kb_id/documents" 202 "$tmp_dir/upload-root.json" "${headers[@]}" \
  -H "Idempotency-Key: b6a-root-$(date +%s)" -F "file=@$tmp_dir/root.md;type=text/markdown"
root_doc="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["document"]["id"])' "$tmp_dir/upload-root.json")"

wait_ready() {
  local document="$1" output="$2" status
  for _ in $(seq 1 40); do
    request GET "/api/v1/documents/$document/status" 200 "$output" "${headers[@]}"
    status="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"]["status"])' "$output")"
    [[ "$status" == "EVIDENCE_READY" ]] && return 0
    [[ "$status" == "FAILED" ]] && { cat "$output" >&2; return 1; }
    sleep 1
  done
  echo "document $document did not become searchable" >&2
  return 1
}
wait_ready "$inside_doc" "$tmp_dir/inside-status.json"
wait_ready "$root_doc" "$tmp_dir/root-status.json"

# ---------------------------------------------------------------------------
# Recursive folder filter, root filter and AND tag filter.
# ---------------------------------------------------------------------------
request GET "/api/v1/knowledge-bases/$kb_id/documents?folder_id=$root_folder" 200 "$tmp_dir/tree-list.json" "${headers[@]}"
[[ "$(json 'len(d["items"])' "$tmp_dir/tree-list.json")" == "1" ]]
[[ "$(json 'd["items"][0]["document"]["id"]' "$tmp_dir/tree-list.json")" == "$inside_doc" ]]
request GET "/api/v1/knowledge-bases/$kb_id/documents?folder_id=root" 200 "$tmp_dir/root-list.json" "${headers[@]}"
[[ "$(json 'len(d["items"])' "$tmp_dir/root-list.json")" == "1" ]]
[[ "$(json 'd["items"][0]["document"]["id"]' "$tmp_dir/root-list.json")" == "$root_doc" ]]

request POST "/api/v1/knowledge-bases/$kb_id/documents/batch" 200 "$tmp_dir/tag-batch.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"action\":\"tag\",\"documentIds\":[\"$inside_doc\"],\"tagIds\":[\"$tag_one\",\"$tag_two\"]}"
request GET "/api/v1/knowledge-bases/$kb_id/documents?tag_ids=$tag_one,$tag_two" 200 "$tmp_dir/tag-list.json" "${headers[@]}"
[[ "$(json 'len(d["items"])' "$tmp_dir/tag-list.json")" == "1" ]]
[[ "$(json 'd["items"][0]["document"]["id"]' "$tmp_dir/tag-list.json")" == "$inside_doc" ]]
[[ "$(json 'len(d["items"][0]["tags"])' "$tmp_dir/tag-list.json")" == "2" ]]

# ---------------------------------------------------------------------------
# Batch move keeps the version and content; partial success is explicit.
# ---------------------------------------------------------------------------
chunks_before="$(curl -sS "$base_url/api/v1/documents/$inside_doc/chunks" "${headers[@]}" | "$python_bin" -c 'import json,sys; print(len(json.load(sys.stdin)["items"]))')"
request POST "/api/v1/knowledge-bases/$kb_id/documents/batch" 200 "$tmp_dir/move-batch.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"action\":\"move\",\"documentIds\":[\"$inside_doc\",\"doc-missing\"],\"folderId\":\"$root_folder\"}"
[[ "$(json 'len(d["items"])' "$tmp_dir/move-batch.json")" == "2" ]]
[[ "$(json 'd["items"][0]["ok"]' "$tmp_dir/move-batch.json")" == "True" ]]
[[ "$(json 'd["items"][1]["errorCode"]' "$tmp_dir/move-batch.json")" == "not_found" ]]
chunks_after="$(curl -sS "$base_url/api/v1/documents/$inside_doc/chunks" "${headers[@]}" | "$python_bin" -c 'import json,sys; print(len(json.load(sys.stdin)["items"]))')"
[[ "$chunks_before" == "$chunks_after" ]]
[[ "$(curl -sS "$base_url/api/v1/documents/$inside_doc/status" "${headers[@]}" | "$python_bin" -c 'import json,sys; print(json.load(sys.stdin)["version"]["id"])')" == "$inside_version" ]]

many="$("$python_bin" -c 'print(",".join(f"doc-{i}" for i in range(101)))')"
request POST "/api/v1/knowledge-bases/$kb_id/documents/batch" 400 "$tmp_dir/too-many.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"action\":\"move\",\"documentIds\":[$many],\"folderId\":\"\"}"
request POST "/api/v1/knowledge-bases/$kb_id/documents/batch" 400 "$tmp_dir/bad-action.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"action\":\"nope\",\"documentIds\":[\"$root_doc\"]}"

# ---------------------------------------------------------------------------
# Retrieval narrowing and empty-folder deletion.
# ---------------------------------------------------------------------------
request POST /api/v1/search 200 "$tmp_dir/search.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"query\":\"evergreen clause\",\"knowledgeBaseId\":\"$kb_id\",\"tagIds\":[\"$tag_two\"]}"
[[ "$("$python_bin" -c 'import json,sys; d=json.load(open(sys.argv[1])); print(all(item.get("documentId") == sys.argv[2] for item in d.get("items", [])))' "$tmp_dir/search.json" "$inside_doc")" == "True" ]]

# ---------------------------------------------------------------------------
# Recursive folder delete removes the subtree and its documents.
# ---------------------------------------------------------------------------
request POST "/api/v1/knowledge-bases/$kb_id/folders" 201 "$tmp_dir/archive.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d '{"name":"Archive"}'
archive_folder="$("$python_bin" -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$tmp_dir/archive.json")"
request POST "/api/v1/knowledge-bases/$kb_id/documents/batch" 200 "$tmp_dir/move-archive.json" "${headers[@]}" \
  -H 'Content-Type: application/json' -d "{\"action\":\"move\",\"documentIds\":[\"$inside_doc\"],\"folderId\":\"$archive_folder\"}"
request DELETE "/api/v1/folders/$archive_folder?recursive=true" 204 "$tmp_dir/delete-archive.json" "${headers[@]}"
request GET "/api/v1/knowledge-bases/$kb_id/documents" 200 "$tmp_dir/after-recursive.json" "${headers[@]}"
[[ "$(json 'len(d["items"])' "$tmp_dir/after-recursive.json")" == "0" ]]
request DELETE "/api/v1/folders/$child_folder" 204 "$tmp_dir/delete-child.json" "${headers[@]}"
request DELETE "/api/v1/folders/$root_folder" 204 "$tmp_dir/delete-root.json" "${headers[@]}"
request DELETE "/api/v1/tags/$tag_one" 204 "$tmp_dir/delete-tag.json" "${headers[@]}"

echo "B6-a acceptance passed"