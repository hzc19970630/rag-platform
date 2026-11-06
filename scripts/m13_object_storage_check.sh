#!/usr/bin/env bash
# M13-A object-storage prerequisite check. MinIO's own image ships `mc`, so
# this runs inside the existing container and adds no new image dependency.
#
#   ./scripts/m13_object_storage_check.sh                 # require versioning + replication
#   ./scripts/m13_object_storage_check.sh --apply         # enable versioning, then check
#   ./scripts/m13_object_storage_check.sh --allow-single-node
#
# Replication needs a second endpoint and is deliberately not auto-configured:
# the operator must create the remote target, then rerun this check.
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
bucket="${S3_BUCKET:-rag-platform-local}"
apply=0
allow_single_node=0
for arg in "$@"; do
  case "$arg" in
    --apply) apply=1 ;;
    --allow-single-node) allow_single_node=1 ;;
    *) printf 'usage: %s [--apply] [--allow-single-node]\n' "$0" >&2; exit 2 ;;
  esac
done

fail() { printf 'M13 object storage check failed: %s\n' "$*" >&2; exit 1; }
mc() { "${compose[@]}" exec -T minio mc "$@"; }

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
  printf 'M13 object storage check passed: versioning + replication configured for %s\n' "$bucket"
  exit 0
fi
if [[ "$allow_single_node" == "1" ]]; then
  printf 'M13 object storage check passed for single-node development: versioning enabled, replication intentionally skipped for %s\n' "$bucket"
  exit 0
fi
fail "bucket $bucket has no replication target; configure a remote target and rerun (or pass --allow-single-node for development only)"
