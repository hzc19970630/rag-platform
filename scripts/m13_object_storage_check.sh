#!/usr/bin/env bash
# M13-A object-storage prerequisite check. MinIO's own image ships `mc`, so
# this runs inside the existing container and adds no new image dependency.
#
#   ./scripts/m13_object_storage_check.sh                 # require versioning + replication
#   ./scripts/m13_object_storage_check.sh --apply         # enable versioning, then check
#   ./scripts/m13_object_storage_check.sh --probe         # also prove delivery to the replica
#   ./scripts/m13_object_storage_check.sh --allow-single-node
#
# Replication needs a second endpoint and is deliberately not auto-configured:
# the operator must create the remote target, then rerun this check. Without
# --probe this verifies versioning and that a replication rule exists; it does
# not prove the target is reachable. --probe writes and removes one object under
# `_m13_probe/` and requires the replica credentials below:
#   M13_REPLICA_ENDPOINT, M13_REPLICA_ACCESS_KEY, M13_REPLICA_SECRET_KEY
# Optional: M13_REPLICA_BUCKET, M13_PROBE_BUCKET, M13_CHECK_BUCKET,
#           M13_REPLICA_INSECURE=1, M13_REPLICATION_PROBE_TIMEOUT_SECONDS (30).
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)
bucket="${M13_CHECK_BUCKET:-${S3_BUCKET:-rag-platform-local}}"
apply=0
probe=0
allow_single_node=0
for arg in "$@"; do
  case "$arg" in
    --apply) apply=1 ;;
    --probe) probe=1 ;;
    --allow-single-node) allow_single_node=1 ;;
    *) printf 'usage: %s [--apply] [--probe] [--allow-single-node]\n' "$0" >&2; exit 2 ;;
  esac
done
probe_timeout="${M13_REPLICATION_PROBE_TIMEOUT_SECONDS:-30}"
[[ "$probe_timeout" =~ ^[1-9][0-9]*$ ]] || {
  printf 'M13 object storage check failed: M13_REPLICATION_PROBE_TIMEOUT_SECONDS must be a positive integer\n' >&2
  exit 1
}

fail() { printf 'M13 object storage check failed: %s\n' "$*" >&2; exit 1; }
mc() { "${compose[@]}" exec -T minio mc "$@"; }
json_field() { sed -n 's/.*"'"$1"'":"\([^"]*\)".*/\1/p'; }

probe_replication() {
  local endpoint="${M13_REPLICA_ENDPOINT:-}"
  local access="${M13_REPLICA_ACCESS_KEY:-}"
  local secret="${M13_REPLICA_SECRET_KEY:-}"
  local source_bucket="${M13_PROBE_BUCKET:-$bucket}"
  local target_bucket="${M13_REPLICA_BUCKET:-$source_bucket}"
  [[ -n "$endpoint" && -n "$access" && -n "$secret" ]] || \
    fail "--probe requires M13_REPLICA_ENDPOINT, M13_REPLICA_ACCESS_KEY and M13_REPLICA_SECRET_KEY"
  if [[ "$source_bucket" != "$bucket" ]]; then
    local source_rules
    source_rules="$(mc replicate ls "m13/$source_bucket" 2>&1 || true)"
    grep -qi 'arn:minio:replication' <<<"$source_rules" || \
      fail "probe source bucket $source_bucket has no replication rule"
  fi
  local alias_args=(--api S3v4)
  [[ "${M13_REPLICA_INSECURE:-0}" == "1" ]] && alias_args+=(--insecure)
  mc alias set m13probe "$endpoint" "$access" "$secret" "${alias_args[@]}" >/dev/null || \
    fail "could not configure the replica alias (check endpoint and credentials)"
  local key="_m13_probe/$(date -u +%Y%m%dT%H%M%SZ)-$RANDOM"
  printf 'm13 replication probe %s\n' "$key" | mc pipe "m13/$source_bucket/$key" >/dev/null || {
    mc alias rm m13probe >/dev/null 2>&1 || true
    fail "could not write the probe object to $source_bucket"
  }
  local source_json source_version dest_json="" dest_version="" replicated=0
  source_json="$(mc stat --json "m13/$source_bucket/$key" 2>/dev/null || true)"
  source_version="$(json_field versionID <<<"$source_json")"
  for _ in $(seq 1 "$probe_timeout"); do
    if dest_json="$(mc stat --json "m13probe/$target_bucket/$key" 2>/dev/null)"; then
      replicated=1
      break
    fi
    sleep 1
  done
  dest_version="$(json_field versionID <<<"$dest_json")"
  mc rm "m13/$source_bucket/$key" >/dev/null 2>&1 || true
  mc rm "m13probe/$target_bucket/$key" >/dev/null 2>&1 || true
  mc alias rm m13probe >/dev/null 2>&1 || true
  [[ "$replicated" == "1" ]] || \
    fail "probe object did not appear in $endpoint/$target_bucket within ${probe_timeout}s; target unreachable or replication lagging"
  [[ -n "$source_version" && "$source_version" == "$dest_version" ]] || \
    fail "probe version ID differs: source='$source_version' replica='$dest_version'"
  printf 'replication probe: %s -> %s/%s version %s\n' "$source_bucket" "$endpoint" "$target_bucket" "$source_version"
}

access_key="${S3_ACCESS_KEY:-${MINIO_ROOT_USER:-}}"
secret_key="${S3_SECRET_KEY:-${MINIO_ROOT_PASSWORD:-}}"
[[ -n "$access_key" && -n "$secret_key" ]] || fail "S3_ACCESS_KEY/S3_SECRET_KEY (or MINIO_ROOT_USER/PASSWORD) are required"
mc alias set m13 http://127.0.0.1:9000 "$access_key" "$secret_key" --api S3v4 >/dev/null || \
  fail "could not configure the MinIO client alias with the S3 credentials"
mc stat "m13/$bucket" >/dev/null 2>&1 || fail "bucket $bucket is not reachable with the configured credentials"
if [[ "$apply" == "1" ]]; then
  mc version enable "m13/$bucket" >/dev/null
fi

info="$(mc version info "m13/$bucket" 2>&1 || true)"
printf '%s\n' "$info"
grep -qi 'versioning is enabled' <<<"$info" || fail "bucket $bucket does not have versioning enabled (rerun with --apply or enable it in production)"

replication="$(mc replicate ls "m13/$bucket" 2>&1 || true)"
printf 'replication:\n%s\n' "$replication"
if grep -qi 'arn:minio:replication' <<<"$replication"; then
  if [[ "$probe" == "1" ]]; then
    probe_replication
    printf 'M13 object storage check passed: versioning + replication configured and delivery verified for %s\n' "$bucket"
  else
    printf 'M13 object storage check passed: versioning + replication configured for %s (rerun with --probe to verify delivery)\n' "$bucket"
  fi
  exit 0
fi
if [[ "$allow_single_node" == "1" ]]; then
  printf 'M13 object storage check passed for single-node development: versioning enabled, replication intentionally skipped for %s\n' "$bucket"
  exit 0
fi
fail "bucket $bucket has no replication target; configure a remote target and rerun (or pass --allow-single-node for development only)"
