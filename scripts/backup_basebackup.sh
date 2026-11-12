#!/usr/bin/env bash
# Physical base backup for PITR. A logical pg_dump cannot seed WAL replay, so
# this script is the required starting point for point-in-time recovery; WAL
# archiving (deploy/postgres/pitr.conf.example) covers the interval after it.
#
# Required: BACKUP_DIR (operator-owned directory outside the repository).
# Optional: BASEBACKUP_RETENTION_DAYS (default 7), POSTGRES_USER,
#           POSTGRES_PASSWORD (needed when pg_hba does not trust 127.0.0.1).
# Flags:    --allow-no-archive  development only; production must fail here.
#
# The backup runs in a one-off ParadeDB container that shares the postgres
# network namespace, so it connects over 127.0.0.1 and uses the image's own
# pg_basebackup (same major version). It writes directly to BACKUP_DIR.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() { printf 'backup_basebackup: %s\n' "$*" >&2; exit 1; }
step() { printf '\n== %s\n' "$*"; }

allow_no_archive=0
for arg in "$@"; do
  case "$arg" in
    --allow-no-archive) allow_no_archive=1 ;;
    *) printf 'usage: %s [--allow-no-archive]\n' "$0" >&2; exit 2 ;;
  esac
done

backup_dir="${BACKUP_DIR:-}"
[[ -n "$backup_dir" ]] || fail "BACKUP_DIR is required (operator-owned directory)"
[[ "$backup_dir" != "/" ]] || fail "BACKUP_DIR must not be /"
retention_days="${BASEBACKUP_RETENTION_DAYS:-7}"
[[ "$retention_days" =~ ^[0-9]+$ ]] || fail "BASEBACKUP_RETENTION_DAYS must be a non-negative integer"
source_user="${POSTGRES_USER:-rag_platform}"
source_db="${POSTGRES_DB:-rag_platform}"
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)
pg_id="$("${compose[@]}" ps -q postgres 2>/dev/null || true)"
[[ -n "$pg_id" ]] || fail "the postgres service is not running"
pg_name="$(docker inspect -f '{{.Name}}' "$pg_id" | sed 's|^/||')"
image="$(docker inspect -f '{{.Config.Image}}' "$pg_id")"

psql() { "${compose[@]}" exec -T postgres psql -U "$source_user" -d "$source_db" -Atc "$1" 2>/dev/null; }

step "preflight: WAL archiving must be able to cover the interval after the backup"
wal_level="$(psql 'SHOW wal_level;')"
archive_mode="$(psql 'SHOW archive_mode;')"
archive_timeout="$(psql "SELECT setting FROM pg_settings WHERE name = 'archive_timeout';")"
[[ "$wal_level" == "replica" || "$wal_level" == "logical" ]] || fail "wal_level must be replica or logical, got $wal_level"
if [[ "$archive_mode" != "on" && "$archive_mode" != "always" ]]; then
  [[ "$allow_no_archive" == "1" ]] || fail "archive_mode is off; enable WAL archiving for PITR (or pass --allow-no-archive for development only)"
  printf 'warning: archive_mode is off; this backup cannot be used for point-in-time recovery\n' >&2
fi
failed_archives="$(psql 'SELECT failed_count FROM pg_stat_archiver;' 2>/dev/null || echo 0)"
[[ "$failed_archives" =~ ^[0-9]+$ ]] || failed_archives=0
[[ "$failed_archives" == "0" ]] || printf 'warning: pg_stat_archiver reports %s failed WAL archive attempts\n' "$failed_archives" >&2

mkdir -p "$backup_dir"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
final="$backup_dir/rag-base-$timestamp"
tmp="$backup_dir/.rag-base-$timestamp.tmp"
[[ ! -e "$final" ]] || fail "backup directory already exists: $final"
tmp_base="$(basename "$tmp")"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

step "base backup: $final"
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
docker run --rm --network "container:$pg_name" \
  --user "$(id -u):$(id -g)" \
  -e PGPASSWORD="${POSTGRES_PASSWORD:-}" \
  -v "$backup_dir:/backup" \
  --entrypoint pg_basebackup \
  "$image" \
  -h 127.0.0.1 -U "$source_user" -D "/backup/$tmp_base" \
  -Fp -Xs -c fast --manifest-checksums=SHA256
mv "$tmp" "$final"

[[ -f "$final/PG_VERSION" ]] || fail "backup is missing PG_VERSION"
[[ -f "$final/backup_manifest" ]] || fail "backup is missing backup_manifest"
[[ "$(cat "$final/PG_VERSION")" == "17" ]] || fail "backup major version $(cat "$final/PG_VERSION") does not match the ParadeDB image (17)"
bytes="$(du -sk "$final" | awk '{print $1 * 1024}')"
[[ "$bytes" -gt 1024 ]] || fail "backup is suspiciously small ($bytes bytes)"

step "verify: pg_verifybackup checksums"
docker run --rm -v "$final:/backup:ro" --entrypoint pg_verifybackup "$image" /backup

finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
cat > "$final.json" <<JSON
{
  "schemaVersion": "M13BaseBackupV1",
  "startedAt": "$started_at",
  "finishedAt": "$finished_at",
  "bytes": $bytes,
  "walLevel": "$wal_level",
  "archiveMode": "$archive_mode",
  "archiveTimeoutSeconds": $([[ "$archive_timeout" =~ ^[0-9]+$ ]] && echo "$archive_timeout" || echo 0),
  "manifestChecksums": "SHA256",
  "postgresImage": "$image"
}
JSON

if [[ "$retention_days" -gt 0 ]]; then
  step "retention: remove rag-base-* older than ${retention_days} days"
  find "$backup_dir" -maxdepth 1 -type d -name 'rag-base-*' -mtime +"$retention_days" -print0 |
    while IFS= read -r -d '' old; do
      [[ -f "$old/backup_manifest" ]] || continue
      [[ "$old" == "$final" ]] && continue
      printf 'removing %s\n' "$old"
      rm -rf "$old"
      rm -f "$old.json"
    done
fi

printf 'backup_basebackup: wrote %s (%s bytes), archive_mode=%s retention=%s days\n' \
  "$final" "$bytes" "$archive_mode" "$retention_days"
