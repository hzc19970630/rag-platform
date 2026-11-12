#!/usr/bin/env bash
# M13-A PITR prerequisite check. Verifies the running PostgreSQL has WAL
# archiving configured for the approved RPO, and that the archiver is healthy.
# This is a preflight, not a restore drill; use m13_backup_drill.sh for the
# logical restore measurement.
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
source_user="${POSTGRES_USER:-rag_platform}"
source_db="${POSTGRES_DB:-rag_platform}"
max_archive_timeout="${PITR_MAX_ARCHIVE_TIMEOUT_SECONDS:-60}"
allow_disabled=0
[[ "${1:-}" == "--allow-disabled" ]] && allow_disabled=1

fail() { printf 'M13 PITR check failed: %s\n' "$*" >&2; exit 1; }
psql() { "${compose[@]}" exec -T postgres psql -U "$source_user" -d "$source_db" -Atc "$1" 2>/dev/null; }

wal_level="$(psql 'SHOW wal_level;')"
archive_mode="$(psql 'SHOW archive_mode;')"
archive_command="$(psql 'SHOW archive_command;')"
archive_timeout="$(psql "SELECT setting FROM pg_settings WHERE name = 'archive_timeout';")"
archiver="$(psql "SELECT archived_count || '|' || failed_count || '|' || COALESCE(last_failed_time::text, '') FROM pg_stat_archiver;")"
printf 'wal_level=%s\narchive_mode=%s\narchive_command=%s\narchive_timeout=%s\npg_stat_archiver=%s\n' \
  "$wal_level" "$archive_mode" "$archive_command" "$archive_timeout" "$archiver"

if [[ "$allow_disabled" == "1" && ( ( "$archive_mode" != "on" && "$archive_mode" != "always" ) || -z "$archive_command" || "$archive_command" == "(disabled)" ) ]]; then
  printf 'M13 PITR check: archiving is disabled; allowed because --allow-disabled was passed (development only)\n'
  exit 0
fi

[[ "$wal_level" == "replica" || "$wal_level" == "logical" ]] || fail "wal_level must be replica or logical, got $wal_level"
[[ "$archive_mode" == "on" || "$archive_mode" == "always" ]] || fail "archive_mode is off; enable WAL archiving for the approved RPO"
[[ -n "$archive_command" && "$archive_command" != "(disabled)" ]] || fail "archive_command is empty/disabled"
[[ "$archive_timeout" =~ ^[0-9]+$ && "$archive_timeout" -gt 0 && "$archive_timeout" -le "$max_archive_timeout" ]] || \
  fail "archive_timeout must be 1..${max_archive_timeout}s for RPO 900s, got $archive_timeout"

failed_count="${archiver#*|}"; failed_count="${failed_count%%|*}"
[[ "$failed_count" == "0" ]] || fail "pg_stat_archiver reports ${failed_count} failed WAL archive attempts"
printf 'M13 PITR check passed: wal_level=%s archive_mode=%s archive_timeout=%ss failed_archives=0\n' "$wal_level" "$archive_mode" "$archive_timeout"
