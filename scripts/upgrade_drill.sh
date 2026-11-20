#!/usr/bin/env bash
# Real upgrade drill: released v0.1.0 -> current HEAD, built and run locally.
#
# - Creates two detached worktrees (v0.1.0 and HEAD) outside the repository.
# - Builds api/docreader images for both and runs a disposable Compose project
#   with offset host ports. The repository .env is never read or modified.
# - Applies the old migrations, ingests demo documents, upgrades with the new
#   migrate + backfill-embeddings, asserts retrieval/answer/presets/quality,
#   then switches back to the old images to prove expand-only rollback.
# - Does not run `migrate down` and never finalizes D3 migrations.
# - Writes tests/eval/upgrade_drill_report.json.
#
# Environment knobs: UPGRADE_DRILL_PARENT, UPGRADE_DRILL_PROJECT,
# UPGRADE_DRILL_API_PORT, UPGRADE_DRILL_POSTGRES_PORT, UPGRADE_DRILL_REDIS_PORT,
# UPGRADE_DRILL_MINIO_PORT, UPGRADE_DRILL_KEEP=1 (keep worktrees/stack).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

drill_parent="${UPGRADE_DRILL_PARENT:-$(cd "$repo_root/.." && pwd)}"
old_tree="$drill_parent/rag-v010"
head_tree="$drill_parent/rag-head"
project="${UPGRADE_DRILL_PROJECT:-rag-upgrade-drill}"
report="$repo_root/tests/eval/upgrade_drill_report.json"
api_port="${UPGRADE_DRILL_API_PORT:-18082}"
pg_port="${UPGRADE_DRILL_POSTGRES_PORT:-55432}"
redis_port="${UPGRADE_DRILL_REDIS_PORT:-56379}"
minio_port="${UPGRADE_DRILL_MINIO_PORT:-59000}"
minio_console_port="${UPGRADE_DRILL_MINIO_CONSOLE_PORT:-59001}"
base_url="http://localhost:$api_port"
old_api_image="rag-upgrade-drill/api:v0.1.0"
new_api_image="rag-upgrade-drill/api:head"
old_docreader_image="rag-upgrade-drill/docreader:v0.1.0"
new_docreader_image="rag-upgrade-drill/docreader:head"
build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
tmp_dir="$(mktemp -d)"
override="$head_tree/deploy/docker-compose/upgrade-drill.compose.yml"
env_file="$head_tree/.env"
report_written=0
failure=""
kb_id=""
custom_preset=""
stage_name=""
stage_start=0
dev=(-H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant')

step() { printf '\n== %s\n' "$*"; stage_name="$*"; stage_start=$SECONDS; }
stage_done() {
  local seconds=$((SECONDS - stage_start))
  record stage "$stage_name" "$seconds" "${1:-ok}"
  printf '   (%ss)\n' "$seconds"
}
fail() {
  failure="$*"
  printf '\nupgrade drill failed: %s\n' "$*" >&2
  record assert "drill_completed" false "$*"
  finalize_report failed || true
  exit 1
}
compose() {
  docker compose --project-name "$project" --env-file "$env_file" \
    -f "$head_tree/deploy/docker-compose/docker-compose.yml" \
    -f "$head_tree/deploy/docker-compose/quickstart.compose.yml" \
    -f "$head_tree/deploy/docker-compose/release.compose.yml" \
    -f "$override" "$@"
}

python_bin="${PYTHON_BIN:-python}"
if ! "$python_bin" -c 'import sys' >/dev/null 2>&1; then python_bin=python3; fi

record() {
  "$python_bin" - "$tmp_dir/report.jsonl" "$@" <<'PY'
import json, sys
path, kind, rest = sys.argv[1], sys.argv[2], sys.argv[3:]
entry = {"kind": kind}
if kind == "stage":
    entry.update(name=rest[0], seconds=float(rest[1]), status=rest[2])
elif kind == "assert":
    entry.update(name=rest[0], ok=rest[1] == "true", detail=rest[2] if len(rest) > 2 else "")
elif kind == "info":
    entry.update(key=rest[0], value=rest[1])
with open(path, "a", encoding="utf-8") as handle:
    handle.write(json.dumps(entry) + "\n")
PY
}
assert_ok() {
  local name="$1" actual="$2" expected="$3" detail="${4:-}"
  if [[ "$actual" != "$expected" ]]; then
    record assert "$name" false "expected $expected, got $actual${detail:+ ($detail)}"
    fail "$name: expected $expected, got $actual${detail:+ ($detail)}"
  fi
  record assert "$name" true "$detail"
}
assert_contains() {
  local name="$1" haystack="$2" needle="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    record assert "$name" false "missing '$needle' in: $(printf '%s' "$haystack" | tail -c 300)"
    fail "$name: output does not contain '$needle'"
  fi
  record assert "$name" true "$needle"
}
json() {
  "$python_bin" -c "import json,sys; d=json.load(open(sys.argv[2], encoding='utf-8')); print(eval(sys.argv[1], {'d': d, 'json': json}))" "$1" "$2"
}
request() {
  local method="$1" path="$2" expected="$3" output="$4"
  shift 4
  local code
  code="$(curl -sS -o "$output" -w '%{http_code}' -X "$method" "$base_url$path" "$@")"
  if [[ "$code" != "$expected" ]]; then
    printf 'expected %s %s, got %s: ' "$method" "$path" "$code" >&2
    cat "$output" >&2 || true
    record assert "http_${method}_${path//\//_}" false "expected $expected, got $code"
    fail "unexpected HTTP response for $method $path"
  fi
  record assert "http_${method}_${path//\//_}" true "$expected"
}
psql_scalar() {
  compose exec -T postgres psql -U "$pg_user" -d "$pg_db" -tAc "$1" | tr -d '[:space:]'
}
wait_api() {
  local deadline=$((SECONDS + 180))
  while (( SECONDS < deadline )); do
    if curl -fsS "$base_url/readyz" 2>/dev/null | grep -q '"status":"ok"'; then return 0; fi
    sleep 2
  done
  compose logs --tail=80 api >&2 || true
  fail "api did not become ready"
}
wait_document() {
  local document_id="$1" deadline=$((SECONDS + 240)) status=""
  while (( SECONDS < deadline )); do
    request GET "/api/v1/documents/$document_id" 200 "$tmp_dir/document.json" "${dev[@]}"
    status="$(json 'd["document"].get("status") or d["pipeline"]["status"]' "$tmp_dir/document.json")"
    case "$status" in
      EVIDENCE_READY) return 0 ;;
      FAILED|CANCELLED) fail "document $document_id reached $status" ;;
    esac
    sleep 2
  done
  fail "document $document_id did not become ready (last: $status)"
}
search_items() {
  local output="$1"
  request POST /api/v1/search 200 "$output" "${dev[@]}" -H 'Content-Type: application/json' \
    -d "{\"query\":\"retrieval evidence handbook\",\"knowledgeBaseId\":\"$kb_id\"}"
  json 'len(d["items"])' "$output"
}
ask_question() {
  local suffix="$1" output="$2"
  request POST /api/v1/sessions 201 "$tmp_dir/session-$suffix.json" "${dev[@]}" \
    -H 'Content-Type: application/json' -d "{\"knowledge_base_id\":\"$kb_id\"}"
  local conversation
  conversation="$(json 'd["id"]' "$tmp_dir/session-$suffix.json")"
  local code
  code="$(curl -sS -N -o "$output" -w '%{http_code}' -X POST \
    "$base_url/api/v1/sessions/$conversation/messages" "${dev[@]}" \
    -H 'Content-Type: application/json' -H 'Accept: text/event-stream' \
    -d "{\"query\":\"how does hybrid recall combine keyword and vector results\",\"requestId\":\"upgrade-drill-$suffix-$(date +%s)\"}")"
  assert_ok "sse_status_$suffix" "$code" "200"
  assert_contains "sse_complete_$suffix" "$(cat "$output")" "event: complete"
  assert_contains "sse_answer_completed_$suffix" "$(cat "$output")" '"status":"COMPLETED"'
}
build_image() {
  local tree="$1" tag="$2" dockerfile="$3" version="$4" commit
  commit="$(git -C "$tree" rev-parse --short HEAD)"
  docker build \
    --build-arg "GOPROXY=${GO_PROXY:-https://goproxy.cn,direct}" \
    --build-arg "VERSION=$version" \
    --build-arg "COMMIT=$commit" \
    --build-arg "BUILD_DATE=$build_date" \
    -f "$tree/$dockerfile" -t "$tag" "$tree" >"$tmp_dir/build-${tag//[\/:]/_}.log" 2>&1 \
    || { tail -50 "$tmp_dir/build-${tag//[\/:]/_}.log" >&2; fail "image build failed: $tag"; }
  docker image inspect --format '{{.Id}}' "$tag"
}
image_version() {
  local tag="$1"
  compose run --rm --no-deps --entrypoint api api --version 2>/dev/null | tr -d '\r'
}
cleanup() {
  if [[ "${UPGRADE_DRILL_KEEP:-0}" != "1" ]]; then
    compose down -v --remove-orphans >/dev/null 2>&1 || true
    # Fallback for a partially failed `down`: remove only this drill project.
    docker ps -aq --filter "label=com.docker.compose.project=$project" | xargs -r docker rm -f >/dev/null 2>&1 || true
    docker volume ls -q --filter "label=com.docker.compose.project=$project" | xargs -r docker volume rm >/dev/null 2>&1 || true
    for tree in "$old_tree" "$head_tree"; do
      [[ -e "$tree" ]] && git worktree remove --force "$tree" >/dev/null 2>&1 || true
    done
    git worktree prune >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp_dir"
}
on_exit() {
  cleanup
  if [[ "$report_written" != "1" ]]; then
    finalize_report failed || true
  fi
}
trap on_exit EXIT

finalize_report() {
  [[ "$report_written" == "1" ]] && return 0
  report_written=1
  "$python_bin" - "$tmp_dir/report.jsonl" "$report" "${1:-passed}" "$project" "$old_api_image" "$new_api_image" "$old_docreader_image" "$new_docreader_image" <<'PY'
import datetime, json, sys
source, target, status = sys.argv[1], sys.argv[2], sys.argv[3]
lines = []
try:
    with open(source, encoding="utf-8") as handle:
        lines = [json.loads(line) for line in handle if line.strip()]
except FileNotFoundError:
    pass
stages = [{"name": s["name"], "seconds": s["seconds"], "status": s["status"]} for s in lines if s["kind"] == "stage"]
assertions = [{"name": a["name"], "ok": a["ok"], "detail": a["detail"]} for a in lines if a["kind"] == "assert"]
info = {i["key"]: i["value"] for i in lines if i["kind"] == "info"}
document = {
    "generatedAt": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
    "status": status,
    "fromVersion": "v0.1.0",
    "toVersion": "HEAD",
    "project": sys.argv[4],
    "images": {
        "old": {"api": sys.argv[5], "docreader": sys.argv[7]},
        "new": {"api": sys.argv[6], "docreader": sys.argv[8]},
    },
    "info": info,
    "stages": stages,
    "assertions": assertions,
    "failedAssertions": [a["name"] for a in assertions if not a["ok"]],
    "rollback": info.get("rollback", "not-run"),
    "downMigrations": info.get("downMigrations", "not-checked"),
}
with open(target, "w", encoding="utf-8") as handle:
    json.dump(document, handle, indent=2, ensure_ascii=False)
    handle.write("\n")
print(f"report: {target}")
PY
}

command -v docker >/dev/null 2>&1 || fail "docker is required"
docker compose version >/dev/null 2>&1 || fail "docker compose v2 is required"
git rev-parse --git-dir >/dev/null 2>&1 || fail "run this script inside the repository"
git rev-parse -q --verify refs/tags/v0.1.0 >/dev/null || fail "tag v0.1.0 is required"
curl -fsS "http://localhost:$api_port/" >/dev/null 2>&1 && fail "port $api_port is already in use"

step "prepare worktrees and disposable configuration"
for tree in "$old_tree" "$head_tree"; do
  if [[ -e "$tree" ]]; then
    git worktree remove --force "$tree" >/dev/null 2>&1 \
      || fail "$tree exists and is not a managed worktree; remove it or set UPGRADE_DRILL_PARENT"
  fi
done
git -c core.autocrlf=false worktree add --detach "$old_tree" v0.1.0 >/dev/null \
  || fail "cannot create v0.1.0 worktree"
git -c core.autocrlf=false worktree add --detach "$head_tree" HEAD >/dev/null \
  || fail "cannot create HEAD worktree"
head_commit="$(git -C "$head_tree" rev-parse --short HEAD)"
old_commit="$(git -C "$old_tree" rev-parse --short HEAD)"
record info oldCommit "$old_commit"
record info newCommit "$head_commit"

crlf_files=""
for migration in "$head_tree"/migrations/postgres/003[1-6]_*.sql; do
  if file "$migration" | grep -q 'CRLF'; then crlf_files="$crlf_files $migration"; fi
done
[[ -z "$crlf_files" ]] || fail "CRLF migration files would fail migrate -check: $crlf_files"
down_missing=""
shopt -s nullglob
for version in 0028 0029 0030 0031 0032 0033 0034 0035 0036; do
  matches=("$head_tree"/migrations/postgres/"$version"*.down.sql)
  [[ ${#matches[@]} -gt 0 ]] || down_missing="$down_missing $version"
done
shopt -u nullglob
[[ -z "$down_missing" ]] || fail "down migration missing:$down_missing"
record info downMigrations "0028-0036 present; drill does not run down (expand-only rollback)"

cp "$head_tree/.env.example" "$env_file"
cat >>"$env_file" <<EOF
API_PORT=$api_port
API_PUBLIC_URL=http://localhost:$api_port
POSTGRES_PORT=$pg_port
DRILL_REDIS_PORT=$redis_port
DRILL_MINIO_PORT=$minio_port
DRILL_MINIO_CONSOLE_PORT=$minio_console_port
EOF
cat >"$override" <<'EOF'
# Generated by scripts/upgrade_drill.sh. Offset host ports so the drill can run
# next to the developer stack; service-to-service traffic is unchanged.
services:
  postgres:
    ports: !override
      - "${POSTGRES_PORT}:5432"
  redis:
    ports: !override
      - "${DRILL_REDIS_PORT}:6379"
  minio:
    ports: !override
      - "${DRILL_MINIO_PORT}:9000"
      - "${DRILL_MINIO_CONSOLE_PORT}:9001"
EOF
pg_user="$(grep -E '^POSTGRES_USER=' "$env_file" | tail -1 | cut -d= -f2)"
pg_db="$(grep -E '^POSTGRES_DB=' "$env_file" | tail -1 | cut -d= -f2)"
record info project "$project"
record info ports "api=$api_port postgres=$pg_port redis=$redis_port minio=$minio_port/$minio_console_port"
compose down -v --remove-orphans >/dev/null 2>&1 || true
stage_done

step "build images (old and new)"
export RAG_API_IMAGE="$old_api_image" RAG_DOCREADER_IMAGE="$old_docreader_image"
old_api_digest="$(build_image "$old_tree" "$old_api_image" Dockerfile v0.1.0)"
old_docreader_digest="$(build_image "$old_tree" "$old_docreader_image" services/docreader/Dockerfile v0.1.0)"
export RAG_API_IMAGE="$new_api_image" RAG_DOCREADER_IMAGE="$new_docreader_image"
new_api_digest="$(build_image "$head_tree" "$new_api_image" Dockerfile "0.2.0-head@$head_commit")"
new_docreader_digest="$(build_image "$head_tree" "$new_docreader_image" services/docreader/Dockerfile "0.2.0-head@$head_commit")"
record info oldApiDigest "$old_api_digest"
record info oldDocreaderDigest "$old_docreader_digest"
record info newApiDigest "$new_api_digest"
record info newDocreaderDigest "$new_docreader_digest"
stage_done

step "start the v0.1.0 stack"
export RAG_API_IMAGE="$old_api_image" RAG_DOCREADER_IMAGE="$old_docreader_image"
compose up -d --wait postgres redis minio || fail "old infrastructure did not start"
compose run --rm --no-deps --entrypoint migrate migrate || fail "old migrate failed"
compose run --rm --no-deps --entrypoint seed seed || fail "old seed failed"
old_version="$(image_version "$old_api_image")"
assert_contains "old_build_version" "$old_version" "v0.1.0"
compose run --rm --no-deps --entrypoint migrate migrate -check || fail "old migrate -check failed"
compose up -d --wait --no-deps docreader || fail "old docreader did not start"
compose up -d --wait --no-deps api worker || fail "old api/worker did not start"
wait_api
stage_done

step "ingest demo documents on the old stack"
request POST /api/v1/knowledge-bases 201 "$tmp_dir/kb.json" "${dev[@]}" -H 'Content-Type: application/json' \
  -d "{\"name\":\"Upgrade Drill $(date +%s)\"}"
kb_id="$(json 'd["id"]' "$tmp_dir/kb.json")"
document_ids=()
for file in product-faq.md retrieval-handbook.md; do
  request POST "/api/v1/knowledge-bases/$kb_id/documents" 202 "$tmp_dir/upload-$file.json" "${dev[@]}" \
    -H "Idempotency-Key: upgrade-drill-$file-$(date +%s)" \
    -F "file=@examples/demo/$file;type=text/markdown"
  document_ids+=("$(json 'd["document"]["id"]' "$tmp_dir/upload-$file.json")")
done
for document_id in "${document_ids[@]}"; do wait_document "$document_id"; done
chunks_before="$(psql_scalar "SELECT count(*) FROM chunks WHERE knowledge_base_id = '$kb_id';")"
embedded_before="$(psql_scalar "SELECT count(*) FROM chunks WHERE knowledge_base_id = '$kb_id' AND embedding IS NOT NULL;")"
[[ "$chunks_before" -ge 1 ]] || fail "no chunks were ingested"
[[ "$embedded_before" -ge 1 ]] || fail "ingested chunks have no legacy embedding"
record info chunksBefore "$chunks_before"
record info embeddedChunksBefore "$embedded_before"
old_search_items="$(search_items "$tmp_dir/search-old.json")"
assert_ok "old_search_items" "$([ "$old_search_items" -ge 1 ] && echo true || echo false)" "true" "items=$old_search_items"
ask_question old "$tmp_dir/stream-old.txt"
stage_done

step "upgrade to HEAD: migrate, backfill twice, restart"
compose stop api worker docreader >/dev/null || fail "cannot stop old services"
export RAG_API_IMAGE="$new_api_image" RAG_DOCREADER_IMAGE="$new_docreader_image"
compose run --rm --no-deps --entrypoint migrate migrate || fail "new migrate failed"
compose run --rm --no-deps --entrypoint migrate migrate -check || fail "new migrate -check failed"
backfill_first="$(compose run --rm --no-deps --entrypoint embedctl api backfill-embeddings 2>&1)" \
  || { printf '%s\n' "$backfill_first" >&2; fail "first backfill failed"; }
first_inserted="$(printf '%s' "$backfill_first" | grep -oE '("inserted":[0-9]+|inserted=[0-9]+)' | tail -1 | sed -E 's/.*[=:]//')"
backfill_second="$(compose run --rm --no-deps --entrypoint embedctl api backfill-embeddings 2>&1)" \
  || { printf '%s\n' "$backfill_second" >&2; fail "second backfill failed"; }
second_inserted="$(printf '%s' "$backfill_second" | grep -oE '("inserted":[0-9]+|inserted=[0-9]+)' | tail -1 | sed -E 's/.*[=:]//')"
[[ -n "$first_inserted" && -n "$second_inserted" ]] || fail "backfill did not report inserted counts"
assert_ok "backfill_first_inserted" "$first_inserted" "$embedded_before"
assert_ok "backfill_second_inserted" "$second_inserted" "0"
record info backfillInserted "$first_inserted"
record info backfillRerunInserted "$second_inserted"
compose up -d --wait --no-deps docreader || fail "new docreader did not start"
compose up -d --wait --no-deps api worker || fail "new api/worker did not start"
wait_api
ready_seconds="$(curl -sS -o "$tmp_dir/readyz.json" -w '%{time_total}' "$base_url/readyz")"
record info readyzSeconds "$ready_seconds"
assert_contains "readyz_after_upgrade" "$(cat "$tmp_dir/readyz.json")" '"status":"ok"'
new_version="$(image_version "$new_api_image")"
assert_contains "new_build_version" "$new_version" "0.2.0-head"
stage_done

step "assert upgrade results"
variant_rows="$(psql_scalar "SELECT count(*) FROM chunk_embeddings WHERE knowledge_base_id = '$kb_id';")"
assert_ok "variant_rows_match_chunks" "$variant_rows" "$embedded_before"
search_items_count="$(search_items "$tmp_dir/search-new.json")"
assert_ok "new_search_items" "$([ "$search_items_count" -ge 1 ] && echo true || echo false)" "true" "items=$search_items_count"
assert_ok "new_search_vector_enabled" "$(json 'bool(d["plan"]["vectorEnabled"])' "$tmp_dir/search-new.json")" "True"
ask_question new "$tmp_dir/stream-new.txt"
request GET /api/v1/presets 200 "$tmp_dir/presets.json" "${dev[@]}"
assert_ok "builtin_preset_listed" \
  "$(json 'any(p["name"] == "general-enterprise" and p["source"] == "builtin" for p in d["items"])' "$tmp_dir/presets.json")" "True"
custom_preset="upgrade-drill-$(date +%s)"
request POST /api/v1/presets/general-enterprise/1.0.0/clone 201 "$tmp_dir/preset-clone.json" "${dev[@]}" \
  -H 'Content-Type: application/json' -d "{\"name\":\"$custom_preset\",\"version\":\"1.0.0\"}"
request GET "/api/v1/presets/$custom_preset/1.0.0" 200 "$tmp_dir/preset-detail.json" "${dev[@]}"
"$python_bin" - "$tmp_dir/preset-detail.json" "$tmp_dir/preset-update.json" <<'PY'
import json, sys
detail = json.load(open(sys.argv[1], encoding="utf-8"))
manifest = detail["manifest"]
manifest["retrieval"]["top_k"] = 6
json.dump({"manifest": manifest}, open(sys.argv[2], "w", encoding="utf-8"))
PY
request PUT "/api/v1/presets/$custom_preset/1.0.0" 200 "$tmp_dir/preset-updated.json" "${dev[@]}" \
  -H 'Content-Type: application/json' --data-binary "@$tmp_dir/preset-update.json"
assert_ok "tenant_preset_edit" "$(json 'd["manifest"]["retrieval"]["top_k"]' "$tmp_dir/preset-updated.json")" "6"
request DELETE "/api/v1/presets/$custom_preset/1.0.0" 204 /dev/null "${dev[@]}"
custom_preset=""
request GET /api/v1/ops/quality 200 "$tmp_dir/quality.json" "${dev[@]}"
stage_done

step "rollback: switch back to v0.1.0 images only"
compose stop api worker docreader >/dev/null || fail "cannot stop new services"
export RAG_API_IMAGE="$old_api_image" RAG_DOCREADER_IMAGE="$old_docreader_image"
compose up -d --wait --no-deps docreader || fail "old docreader did not restart"
compose up -d --wait --no-deps api worker || fail "old api/worker did not restart"
wait_api
rollback_version="$(image_version "$old_api_image")"
assert_contains "rollback_build_version" "$rollback_version" "v0.1.0"
rollback_items="$(search_items "$tmp_dir/search-rollback.json")"
assert_ok "rollback_search_items" "$([ "$rollback_items" -ge 1 ] && echo true || echo false)" "true" "items=$rollback_items"
ask_question rollback "$tmp_dir/stream-rollback.txt"
variant_rows_rollback="$(psql_scalar "SELECT count(*) FROM chunk_embeddings WHERE knowledge_base_id = '$kb_id';")"
assert_ok "rollback_keeps_variants" "$variant_rows_rollback" "$embedded_before"
record info rollback "image-only; no migrate down, no finalize; expand tables retained"
stage_done

if [[ -n "$kb_id" ]]; then
  curl -sS -o /dev/null -X DELETE "$base_url/api/v1/knowledge-bases/$kb_id" "${dev[@]}" || true
  kb_id=""
fi

finalize_report passed
printf '\nUpgrade drill passed: v0.1.0 -> %s\n' "$head_commit"
