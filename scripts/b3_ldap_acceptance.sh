#!/usr/bin/env bash
# B3 LDAP acceptance against a running dev stack started with:
#   docker compose --env-file .env \
#     -f deploy/docker-compose/docker-compose.yml \
#     -f deploy/docker-compose/quickstart.compose.yml \
#     -f deploy/docker-compose/ldap.compose.yml up -d --build ldap api worker
# and after `migrate` has applied 0028_ldap_identity.
#
# B3_DEPROVISION=1 additionally deletes bob from the disposable directory and
# verifies membership + session revocation. It leaves the test directory without
# bob until the LDAP volume is recreated.
set -euo pipefail

api_port="${API_PORT:-8082}"
base="http://localhost:${api_port}/api/v1"
root_url="http://localhost:${api_port}"
compose=(docker compose --env-file .env \
  -f deploy/docker-compose/docker-compose.yml \
  -f deploy/docker-compose/quickstart.compose.yml \
  -f deploy/docker-compose/ldap.compose.yml)
tenant="${LDAP_TENANT_ID:-dev-tenant}"

fail() { printf 'B3 LDAP acceptance failed: %s\n' "$*" >&2; exit 1; }
step() { printf '\n== %s\n' "$*"; }
json() { python -c "$1"; }

login() {
  local username="$1" password="$2"
  curl -sS -o /dev/null -w '%{http_code}' -X POST "$base/auth/ldap/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"${username}\",\"password\":\"${password}\"}"
}

login_body() {
  local username="$1" password="$2"
  curl -sS -X POST "$base/auth/ldap/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"${username}\",\"password\":\"${password}\"}"
}

step "preflight"
curl -fsS "$root_url/readyz" >/dev/null || fail "API is not ready at $root_url/readyz"
curl -fsS "$base/auth/config" | json 'import json,sys;d=json.load(sys.stdin);raise SystemExit(0 if d.get("ldapEnabled") else 1)' \
  || fail "LDAP is not enabled; start the ldap overlay and recreate api/worker"

step "alice (admin group) logs in with tenant:admin"
alice="$(login_body alice alice-secret)"
alice_code="$(printf '%s' "$alice" | json 'import json,sys;print("200" if json.load(sys.stdin).get("token") else "bad")')"
[[ "$alice_code" == "200" ]] || fail "alice login failed: $alice"
alice_token="$(printf '%s' "$alice" | json 'import json,sys;print(json.load(sys.stdin)["token"])')"
curl -fsS -H "Authorization: Bearer $alice_token" "$base/me" \
  | json 'import json,sys;caps=json.load(sys.stdin)["scope"]["capabilities"];raise SystemExit(0 if "tenant:admin" in caps else 1)' \
  || fail "alice did not receive tenant:admin"

step "bob (viewer group) logs in read-only"
bob="$(login_body bob bob-secret)"
bob_token="$(printf '%s' "$bob" | json 'import json,sys;print(json.load(sys.stdin)["token"])')"
[[ -n "$bob_token" ]] || fail "bob login failed: $bob"
curl -fsS -H "Authorization: Bearer $bob_token" "$base/me" \
  | json 'import json,sys;caps=json.load(sys.stdin)["scope"]["capabilities"];raise SystemExit(0 if caps == ["knowledge_base:read"] else 1)' \
  || fail "bob capabilities are not viewer-only"

step "wrong password and unmapped user are rejected with 401"
[[ "$(login alice wrong)" == "401" ]] || fail "wrong password was not rejected"
[[ "$(login dave dave-secret)" == "401" ]] || fail "unmapped dave was not rejected"

step "periodic organization sync created memberships"
sleep "${B3_SYNC_WAIT_SECONDS:-15}"
"${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -t -A \
  -c "select count(*) from memberships m join users u on u.id=m.user_id where m.tenant_id='${tenant}' and u.ldap_dn <> '' and u.email in ('alice@example.com','bob@example.com')" \
  | grep -qx '2' || fail "LDAP sync did not create alice/bob memberships"

if [[ "${B3_DEPROVISION:-0}" == "1" ]]; then
  step "removing bob from LDAP revokes membership and session"
  container="$("${compose[@]}" ps -q ldap)"
  docker exec "$container" ldapdelete -x -H ldap://localhost:389 \
    -D cn=admin,dc=example,dc=com -w admin-secret 'uid=bob,ou=people,dc=example,dc=com'
  sleep "${B3_SYNC_WAIT_SECONDS:-15}"
  remaining="$("${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -t -A \
    -c "select count(*) from memberships m join users u on u.id=m.user_id where m.tenant_id='${tenant}' and u.ldap_dn <> '' and u.email='bob@example.com'" | tr -d '\r')"
  [[ "$remaining" == "0" ]] || fail "bob membership survived directory removal"
  [[ "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $bob_token" "$base/me")" == "401" ]] \
    || fail "bob session was not revoked"
fi

printf '\nB3 LDAP acceptance passed (alice=admin, bob=viewer, dave/wrong=401, sync ok)\n'
