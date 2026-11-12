#!/usr/bin/env bash
# M13-A object-storage replication drill. Starts a disposable second MinIO
# node on the source container's network, creates a drill-only bucket on both
# sides, enables versioning and server-side replication, then writes a probe
# object and verifies the destination received the same version ID.
#
# Usage:
#   ./scripts/m13_object_storage_drill.sh
#
# The drill never touches the application bucket or its replication rules; it
# creates and removes `m13-replication-drill-*`. Production must still point
# replication at an independent failure domain; this drill proves the
# configuration path, not the durability of a real second site.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() { printf 'M13 object storage drill failed: %s\n' "$*" >&2; exit 1; }
step() { printf '\n== %s\n' "$*"; }

compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)
[[ -f .env ]] || fail ".env is required"
minio_id="$("${compose[@]}" ps -q minio 2>/dev/null || true)"
[[ -n "$minio_id" ]] || fail "the minio service is not running"
minio="$(docker inspect -f '{{.Name}}' "$minio_id" | sed 's|^/||')"
image="$(docker inspect -f '{{.Config.Image}}' "$minio_id")"
network="$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{"\n"}}{{end}}' "$minio_id" | head -n1)"
[[ -n "$network" ]] || fail "could not determine the minio container network"

access_key="${S3_ACCESS_KEY:-${MINIO_ROOT_USER:-}}"
secret_key="${S3_SECRET_KEY:-${MINIO_ROOT_PASSWORD:-}}"
[[ -n "$access_key" && -n "$secret_key" ]] || fail "S3_ACCESS_KEY/S3_SECRET_KEY (or MINIO_ROOT_USER/PASSWORD) are required"

suffix="$RANDOM-$$"
replica="rag-minio-replica-$suffix"
replica_vol="rag-minio-replica-$suffix"
replica_user="drill-replica"
replica_password="drill-replica-$RANDOM-$RANDOM"
bucket="m13-replication-drill-$suffix"
key="probe-$suffix"

cleanup() {
  "${compose[@]}" exec -T minio mc replicate rm "m13src/$bucket" --all --force >/dev/null 2>&1 || true
  "${compose[@]}" exec -T minio mc rm --recursive --force --versions "m13src/$bucket" >/dev/null 2>&1 || true
  "${compose[@]}" exec -T minio mc rb "m13src/$bucket" >/dev/null 2>&1 || true
  "${compose[@]}" exec -T minio mc alias rm m13src >/dev/null 2>&1 || true
  "${compose[@]}" exec -T minio mc alias rm m13dst >/dev/null 2>&1 || true
  docker rm -f "$replica" >/dev/null 2>&1 || true
  docker volume rm "$replica_vol" >/dev/null 2>&1 || true
}
trap cleanup EXIT

mc_source() { "${compose[@]}" exec -T minio mc "$@"; }
json_field() { sed -n 's/.*"'"$1"'":"\([^"]*\)".*/\1/p'; }

started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

step "start disposable replica on network $network"
printf 'source: %s\nreplica image: %s\n' "$minio" "$image"
docker run -d --name "$replica" --network "$network" \
  -e MINIO_ROOT_USER="$replica_user" \
  -e MINIO_ROOT_PASSWORD="$replica_password" \
  -v "$replica_vol:/data" \
  "$image" server /data --console-address :9001 >/dev/null
ready=0
for _ in $(seq 1 60); do
  if docker exec "$replica" mc ready local >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
[[ "$ready" == "1" ]] || { docker logs "$replica" >&2; fail "replica did not become ready"; }

step "configure aliases and versioned drill buckets"
mc_source alias set m13src http://127.0.0.1:9000 "$access_key" "$secret_key" --api S3v4 >/dev/null
mc_source alias set m13dst "http://$replica:9000" "$replica_user" "$replica_password" --api S3v4 >/dev/null
mc_source mb "m13src/$bucket" >/dev/null
mc_source version enable "m13src/$bucket" >/dev/null
mc_source mb "m13dst/$bucket" >/dev/null
mc_source version enable "m13dst/$bucket" >/dev/null

step "add replication rule and write probe object"
replication_latency_start="$(date +%s)"
mc_source replicate add "m13src/$bucket" --remote-bucket "m13dst/$bucket" --priority 1 --replicate "delete,delete-marker" >/dev/null
rules="$(mc_source replicate ls "m13src/$bucket")"
grep -q 'arn:minio:replication' <<<"$rules" || {
  printf '%s\n' "$rules" >&2
  fail "replication rule was not registered"
}
printf 'replication-probe-%s\n' "$suffix" | "${compose[@]}" exec -T minio mc pipe "m13src/$bucket/$key" >/dev/null

step "wait for replication and compare version IDs"
replicated=0
dest_json=""
for _ in $(seq 1 60); do
  if dest_json="$(mc_source stat --json "m13dst/$bucket/$key" 2>/dev/null)"; then
    replicated=1
    break
  fi
  sleep 1
done
[[ "$replicated" == "1" ]] || {
  mc_source replicate ls "m13src/$bucket" >&2 || true
  mc_source replicate status "m13src/$bucket" >&2 || true
  docker logs --tail=50 "$replica" >&2 || true
  fail "probe object did not replicate within 60s"
}
replication_latency=$(( $(date +%s) - replication_latency_start ))
source_json="$(mc_source stat --json "m13src/$bucket/$key")"
source_version="$(json_field versionID <<<"$source_json")"
dest_version="$(json_field versionID <<<"$dest_json")"
version_matched=false
[[ -n "$source_version" && "$source_version" == "$dest_version" ]] && version_matched=true

finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
passed=false
[[ "$version_matched" == "true" ]] && passed=true
mkdir -p tests/eval
report=tests/eval/m13_object_storage_drill_report.json
cat > "$report" <<JSON
{
  "schemaVersion": "M13ObjectStorageDrillReportV1",
  "startedAt": "$started_at",
  "finishedAt": "$finished_at",
  "sourceContainer": "$minio",
  "replicaImage": "$image",
  "bucket": "$bucket",
  "objectKey": "$key",
  "replicationLatencySeconds": $replication_latency,
  "sourceVersionId": "$source_version",
  "destinationVersionId": "$dest_version",
  "versionIdMatched": $version_matched,
  "passed": $passed,
  "scope": "two disposable MinIO nodes on the local compose network; production replication must use an independent failure domain and is verified separately by m13_object_storage_check.sh"
}
JSON
printf 'report: %s\n' "$report"

[[ "$version_matched" == "true" ]] || fail "replicated version ID differs: source='$source_version' destination='$dest_version'"
printf 'M13 object storage drill passed: bucket=%s replicated in %ss with version %s\n' \
  "$bucket" "$replication_latency" "$source_version"
