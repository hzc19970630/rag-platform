#!/usr/bin/env bash
# Scheduled logical backup for the PostgreSQL/ParadeDB database. WAL/PITR is a
# deployment setting (see deploy/postgres/pitr.conf.example); this script is the
# portable base-backup fallback used when the managed PITR path is unavailable.
#
# Required: BACKUP_DIR (an operator-owned directory outside the repository).
# Optional: BACKUP_RETENTION_DAYS (default 7), POSTGRES_USER, POSTGRES_DB.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

backup_dir="${BACKUP_DIR:-}"
if [[ -z "$backup_dir" ]]; then
  printf 'backup_postgres: BACKUP_DIR is required (operator-owned directory)\n' >&2
  exit 2
fi
retention_days="${BACKUP_RETENTION_DAYS:-7}"
if ! [[ "$retention_days" =~ ^[0-9]+$ ]]; then
  printf 'backup_postgres: BACKUP_RETENTION_DAYS must be a non-negative integer\n' >&2
  exit 2
fi
source_user="${POSTGRES_USER:-rag_platform}"
source_db="${POSTGRES_DB:-rag_platform}"
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)
mkdir -p "$backup_dir"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
dump="$backup_dir/${source_db}-${timestamp}.dump"

"${compose[@]}" exec -T postgres pg_dump -U "$source_user" -d "$source_db" --format=custom --no-owner --no-privileges > "$dump"
bytes="$(wc -c < "$dump" | tr -d ' ')"
if [[ "$bytes" -le 1024 ]]; then
  rm -f "$dump"
  printf 'backup_postgres: dump is suspiciously small (%s bytes)\n' "$bytes" >&2
  exit 1
fi
if [[ "$retention_days" -gt 0 ]]; then
  find "$backup_dir" -maxdepth 1 -type f -name "${source_db}-*.dump" -mtime +"$retention_days" -delete
fi
printf 'backup_postgres: wrote %s (%s bytes), retention %s days\n' "$dump" "$bytes" "$retention_days"
