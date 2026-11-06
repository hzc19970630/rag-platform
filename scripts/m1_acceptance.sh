#!/usr/bin/env sh
set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root_dir"

env_file="${ACCEPTANCE_ENV_FILE:-.env.example}"
if [ -f "$env_file" ]; then
  set -a
  . "$env_file"
  set +a
fi
compose="docker compose --env-file $env_file -f deploy/docker-compose/docker-compose.yml"
api_base="http://localhost:${API_PORT:-8082}"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

$compose config --quiet
$compose up --build --force-recreate -d

ready=0
for _ in $(seq 1 30); do
    if curl -fsS "$api_base/readyz" | grep -q '"status":"ok"'; then
        ready=1
        break
    fi
    sleep 2
done
if [ "$ready" -ne 1 ]; then
    $compose logs api worker migrate seed
    exit 1
fi

request() {
    method=$1
    path=$2
    expected=$3
    output=$4
    shift 4
    status=$(curl -sS -o "$output" -w '%{http_code}' -X "$method" "$api_base$path" "$@")
    if [ "$status" != "$expected" ]; then
        printf 'unexpected status for %s %s: expected %s, got %s\n' "$method" "$path" "$expected" "$status" >&2
        cat "$output" >&2
        exit 1
    fi
}

request GET /api/v1/me 200 "$tmp_dir/me.json" \
    -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant'
grep -q 'dev-tenant' "$tmp_dir/me.json"
grep -q 'kb-dev' "$tmp_dir/me.json"

request GET /api/v1/knowledge-bases 200 "$tmp_dir/list.json" \
    -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant'
grep -q 'kb-dev' "$tmp_dir/list.json"

request POST /api/v1/knowledge-bases 201 "$tmp_dir/create.json" \
    -H 'Content-Type: application/json' \
    -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
    -d '{"name":"M1 Acceptance Knowledge Base","description":"created by acceptance test"}'
created_id=$(python3 -c 'import json,sys; data=json.load(open(sys.argv[1])); print(data.get("id") or data["ID"])' "$tmp_dir/create.json")
case "$created_id" in
    kb-*) ;;
    *) printf 'unexpected created knowledge base id: %s\n' "$created_id" >&2; exit 1 ;;
esac

request GET /api/v1/knowledge-bases/kb-dev 200 "$tmp_dir/viewer-read.json" \
    -H 'X-Dev-User-ID: viewer-a' -H 'X-Dev-Tenant-ID: dev-tenant'
request PATCH /api/v1/knowledge-bases/kb-dev 403 "$tmp_dir/viewer-write.json" \
    -H 'Content-Type: application/json' \
    -H 'X-Dev-User-ID: viewer-a' -H 'X-Dev-Tenant-ID: dev-tenant' \
    -d '{"name":"viewer must not update"}'
request GET /api/v1/knowledge-bases/kb-dev/members 403 "$tmp_dir/viewer-members.json" \
    -H 'X-Dev-User-ID: viewer-a' -H 'X-Dev-Tenant-ID: dev-tenant'
request GET /api/v1/knowledge-bases/kb-other 403 "$tmp_dir/cross-tenant.json" \
    -H 'X-Dev-User-ID: viewer-a' -H 'X-Dev-Tenant-ID: dev-tenant'

request GET /api/v1/knowledge-bases/kb-dev 200 "$tmp_dir/editor-read.json" \
    -H 'X-Dev-User-ID: editor-a' -H 'X-Dev-Tenant-ID: dev-tenant'
request PATCH /api/v1/knowledge-bases/kb-dev 200 "$tmp_dir/editor-write.json" \
    -H 'Content-Type: application/json' \
    -H 'X-Dev-User-ID: editor-a' -H 'X-Dev-Tenant-ID: dev-tenant' \
    -d '{"description":"editor update is allowed"}'
request GET /api/v1/knowledge-bases/kb-dev/members 403 "$tmp_dir/editor-members.json" \
    -H 'X-Dev-User-ID: editor-a' -H 'X-Dev-Tenant-ID: dev-tenant'

request GET /api/v1/knowledge-bases/kb-dev 403 "$tmp_dir/other-read.json" \
    -H 'X-Dev-User-ID: other-user' -H 'X-Dev-Tenant-ID: other-tenant'
request GET /api/v1/knowledge-bases/kb-other 200 "$tmp_dir/other-own-read.json" \
    -H 'X-Dev-User-ID: other-user' -H 'X-Dev-Tenant-ID: other-tenant'

request POST "/api/v1/knowledge-bases/$created_id/members" 201 "$tmp_dir/add-member.json" \
    -H 'Content-Type: application/json' \
    -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
    -d '{"user_id":"viewer-a","role":"viewer"}'
grep -q 'viewer-a' "$tmp_dir/add-member.json"
request GET "/api/v1/knowledge-bases/$created_id" 200 "$tmp_dir/new-member-read.json" \
    -H 'X-Dev-User-ID: viewer-a' -H 'X-Dev-Tenant-ID: dev-tenant'
request DELETE "/api/v1/knowledge-bases/$created_id" 204 "$tmp_dir/delete.json" \
    -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant'

request GET /api/v1/audit/authorization 200 "$tmp_dir/audit.json" \
    -H 'X-Dev-User-ID: viewer-a' -H 'X-Dev-Tenant-ID: dev-tenant'
grep -q 'policy_denied' "$tmp_dir/audit.json"

# Authorization policies are projected in memory from memberships; casbin_rule
# is a retired legacy table, so assert on the real source of truth.
sql="SELECT count(*) FROM memberships;"
membership_count=$($compose exec -T postgres psql -U rag_platform -d rag_platform -Atc "$sql")
test "$membership_count" -gt 0
audit_count=$($compose exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT count(*) FROM audit_logs WHERE allowed = false;")
test "$audit_count" -gt 0
worker_status=$($compose ps --format '{{.Service}} {{.State}}' worker)
printf '%s\n' "$worker_status" | grep -q 'running'

echo "M1 acceptance passed"
