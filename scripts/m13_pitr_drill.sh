#!/usr/bin/env bash
# M13-A PITR verification. Two modes:
#
# 1. Disposable drill (default): start a ParadeDB container with WAL archiving,
#    write marker A, take a base backup, write marker B, restore to a timestamp
#    between them and assert the point-in-time boundary.
#      RTO_TARGET_SECONDS=3600 RPO_TARGET_SECONDS=900 ./scripts/m13_pitr_drill.sh
#
# 2. Existing archives: restore a real base backup against a real WAL archive
#    into a disposable container. This is the production-media check; the
#    archive directory must be readable by the container's postgres user.
#      RTO_TARGET_SECONDS=3600 RPO_TARGET_SECONDS=900 ./scripts/m13_pitr_drill.sh \
#        --existing-base /backups/rag-base-... --existing-archive /wal-archive \
#        [--target-time "2026-11-12 03:00:00+00"]
#
# The disposable archive volume proves the recovery path only. Production must
# place the WAL archive on replicated storage outside the database node.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() { printf 'M13 PITR drill failed: %s\n' "$*" >&2; exit 1; }
step() { printf '\n== %s\n' "$*"; }
positive_int() { [[ "$1" =~ ^[1-9][0-9]*$ ]]; }
usage() {
  cat <<'EOF'
usage: m13_pitr_drill.sh [--existing-base DIR --existing-archive DIR [--target-time TS]]

  (no mode flags)     run the disposable archive/base-backup/restore drill
  --existing-base     plain-format pg_basebackup directory (must contain PG_VERSION
                      and backup_manifest)
  --existing-archive  directory of archived WAL segments, readable by postgres
  --target-time       optional recovery target (PostgreSQL timestamp), default latest

RTO_TARGET_SECONDS and RPO_TARGET_SECONDS are always required.
EOF
}

mode=disposable
existing_base=""
existing_archive=""
target_time=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --existing-base)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      existing_base="$2"; shift 2 ;;
    --existing-archive)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      existing_archive="$2"; shift 2 ;;
    --target-time)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      target_time="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'unknown option: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done
if [[ -n "$existing_base" || -n "$existing_archive" ]]; then
  [[ -n "$existing_base" && -n "$existing_archive" ]] || fail "--existing-base and --existing-archive must be provided together"
  mode=existing
fi
if [[ -n "$target_time" && "$mode" != "existing" ]]; then
  fail "--target-time is only valid with --existing-base/--existing-archive"
fi

rto_target="${RTO_TARGET_SECONDS:-}"
rpo_target="${RPO_TARGET_SECONDS:-}"
positive_int "$rto_target" || fail "RTO_TARGET_SECONDS must be a positive integer (owner-provided target)"
positive_int "$rpo_target" || fail "RPO_TARGET_SECONDS must be a positive integer (owner-provided target)"
archive_timeout="${PITR_ARCHIVE_TIMEOUT_SECONDS:-60}"
positive_int "$archive_timeout" || fail "PITR_ARCHIVE_TIMEOUT_SECONDS must be a positive integer"
if [[ "$mode" == "disposable" && "$archive_timeout" -gt "$rpo_target" ]]; then
  fail "archive_timeout ${archive_timeout}s exceeds the RPO target ${rpo_target}s"
fi

[[ -f .env ]] || fail ".env is required to resolve the pinned ParadeDB image"
image="$(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml config --images 2>/dev/null | grep -m1 paradedb || true)"
[[ -n "$image" ]] || fail "could not resolve the ParadeDB image from the compose file"

suffix="$RANDOM-$$"
restore="rag-pitr-restore-$suffix"
restore_vol="rag-pitr-restore-$suffix"
tmp_dir="$(mktemp -d)"
password="pitr-drill-$RANDOM"

cleanup() {
  docker rm -f "$restore" >/dev/null 2>&1 || true
  docker volume rm "$restore_vol" >/dev/null 2>&1 || true
  if [[ "$mode" == "disposable" ]]; then
    docker rm -f "rag-pitr-primary-$suffix" >/dev/null 2>&1 || true
    docker volume rm "rag-pitr-archive-$suffix" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

psql_restore() {
  docker exec -u postgres "$restore" psql -U "$restore_user" -d "$verify_db" -Atc "$1"
}

prepare_restore_volume() {
  local source_dir="$1"
  docker volume create "$restore_vol" >/dev/null
  # The restore container is disposable and unpublished; replace pg_hba with
  # local trust so verification does not depend on the production auth rules.
  docker run --rm -v "$restore_vol:/target" -v "$source_dir:/source:ro" --entrypoint sh "$image" -c \
    "cp -a /source/. /target/ && chown -R postgres:postgres /target && rm -f /target/standby.signal && touch /target/recovery.signal && sed -i '/^primary_conninfo/d' /target/postgresql.auto.conf && printf 'local all all trust\nhost all all 127.0.0.1/32 trust\n' > /target/pg_hba.conf" >/dev/null
}

run_restore() {
  local archive_source="$1" target="$2"
  local args=(
    -d --name "$restore"
    -e POSTGRES_PASSWORD="$password"
    -e POSTGRES_DB="$verify_db"
    -v "$restore_vol:/var/lib/postgresql/data"
    -v "$archive_source:/archive:ro"
    "$image" postgres
    -c "restore_command=cp /archive/%f %p"
  )
  [[ -n "$target" ]] && args+=(-c "recovery_target_time=$target")
  args+=(-c 'recovery_target_action=promote')
  restore_started_epoch="$(date +%s)"
  docker run "${args[@]}" >/dev/null

  local deadline=$((SECONDS + 300)) in_recovery=t running
  while (( SECONDS < deadline )); do
    running="$(docker inspect -f '{{.State.Running}}' "$restore" 2>/dev/null || echo false)"
    if [[ "$running" != "true" ]]; then
      docker logs "$restore" >&2
      fail "restore container exited during WAL replay"
    fi
    in_recovery="$(psql_restore 'SELECT pg_is_in_recovery();' 2>/dev/null || echo t)"
    [[ "$in_recovery" == "f" ]] && break
    sleep 1
  done
  [[ "$in_recovery" == "f" ]] || { docker logs "$restore" >&2; fail "restore did not finish WAL replay within 300s"; }
  rto_observed=$(( $(date +%s) - restore_started_epoch ))
}

file_mtime() {
  if stat -c %Y "$1" >/dev/null 2>&1; then
    stat -c %Y "$1"
  elif stat -f %m "$1" >/dev/null 2>&1; then
    stat -f %m "$1"
  else
    echo 0
  fi
}

started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

if [[ "$mode" == "disposable" ]]; then
  primary="rag-pitr-primary-$suffix"
  archive_vol="rag-pitr-archive-$suffix"
  db=pitr_drill
  verify_db="$db"
  restore_user=postgres

  psql_primary() { docker exec -u postgres "$primary" psql -U postgres -d "$db" -Atc "$1"; }
  wait_ready() {
    local deadline=$((SECONDS + 120))
    while (( SECONDS < deadline )); do
      if docker exec -u postgres "$primary" pg_isready -U postgres -d "$db" >/dev/null 2>&1; then
        return 0
      fi
      sleep 1
    done
    return 1
  }
  wait_archived_above() {
    local before="$1" deadline=$((SECONDS + 120)) count
    while (( SECONDS < deadline )); do
      count="$(psql_primary 'SELECT archived_count FROM pg_stat_archiver;' 2>/dev/null || true)"
      if [[ "$count" =~ ^[0-9]+$ ]] && (( count > before )); then
        printf '%s' "$count"
        return 0
      fi
      sleep 1
    done
    return 1
  }

  step "start primary with WAL archiving"
  printf 'image: %s\n' "$image"
  # A fresh named volume is root-owned; the postgres process archives as the
  # postgres user, so hand it the archive mount before the server starts.
  docker volume create "$archive_vol" >/dev/null
  docker run --rm -v "$archive_vol:/archive" --entrypoint sh "$image" -c 'chown postgres:postgres /archive' >/dev/null
  docker run -d --name "$primary" \
    -e POSTGRES_PASSWORD="$password" \
    -e POSTGRES_DB="$db" \
    -v "$archive_vol:/archive" \
    "$image" postgres \
    -c wal_level=replica \
    -c archive_mode=on \
    -c "archive_command=test ! -f /archive/%f && cp %p /archive/%f" \
    -c "archive_timeout=$archive_timeout" >/dev/null

  init_logs=""
  for _ in $(seq 1 120); do
    init_logs="$(docker logs "$primary" 2>&1 || true)"
    grep -q 'PostgreSQL init process complete' <<<"$init_logs" && break
    sleep 1
  done
  grep -q 'PostgreSQL init process complete' <<<"$init_logs" || {
    printf '%s\n' "$init_logs" >&2
    fail "primary container did not finish initialization"
  }
  wait_ready || { docker logs "$primary" >&2; fail "primary did not become ready"; }

  step "write marker row A and archive its WAL"
  psql_primary "CREATE TABLE pitr_marker (id int primary key, note text not null, created_at timestamptz not null default clock_timestamp());"
  psql_primary "INSERT INTO pitr_marker (id, note) VALUES (1, 'before-target');"
  archived_before_a="$(psql_primary 'SELECT archived_count FROM pg_stat_archiver;')"
  psql_primary 'SELECT pg_switch_wal();' >/dev/null
  wait_archived_above "$archived_before_a" >/dev/null || {
    psql_primary 'SELECT archived_count, failed_count, last_failed_wal, last_failed_time FROM pg_stat_archiver;' >&2 || true
    docker logs --tail=50 "$primary" >&2 || true
    fail "WAL for marker A was not archived"
  }

  step "take base backup"
  docker exec -u postgres "$primary" pg_basebackup -D /tmp/pitr-base -Fp -Xs -c fast -U postgres >/dev/null
  mkdir -p "$tmp_dir/base"
  docker cp "$primary:/tmp/pitr-base/." "$tmp_dir/base/" >/dev/null
  base_bytes="$(du -sk "$tmp_dir/base" | awk '{print $1 * 1024}')"
  [[ "$base_bytes" -gt 1024 ]] || fail "base backup is suspiciously small ($base_bytes bytes)"
  printf 'base backup: %s bytes\n' "$base_bytes"

  step "write marker row B, then archive its WAL"
  target_time="$(psql_primary "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US') || '+00';")"
  psql_primary "INSERT INTO pitr_marker (id, note) VALUES (2, 'after-target');"
  archived_before_b="$(psql_primary 'SELECT archived_count FROM pg_stat_archiver;')"
  psql_primary 'SELECT pg_switch_wal();' >/dev/null
  archived_after_b="$(wait_archived_above "$archived_before_b")" || {
    psql_primary 'SELECT archived_count, failed_count, last_failed_wal, last_failed_time FROM pg_stat_archiver;' >&2 || true
    docker logs --tail=50 "$primary" >&2 || true
    fail "WAL for marker B was not archived"
  }
  failed_archives="$(psql_primary 'SELECT failed_count FROM pg_stat_archiver;')"
  [[ "$failed_archives" == "0" ]] || fail "pg_stat_archiver reports ${failed_archives} failed WAL archive attempts"
  docker stop "$primary" >/dev/null

  step "prepare restore volume from the base backup"
  prepare_restore_volume "$tmp_dir/base"

  step "restore to $target_time"
  run_restore "$archive_vol" "$target_time"

  step "verify point-in-time boundary"
  row_a="$(psql_restore 'SELECT count(*) FROM pitr_marker WHERE id = 1;')"
  row_b="$(psql_restore 'SELECT count(*) FROM pitr_marker WHERE id = 2;')"
  row_a_recovered=false
  row_b_excluded=false
  [[ "$row_a" == "1" ]] && row_a_recovered=true
  [[ "$row_b" == "0" ]] && row_b_excluded=true
  rto_passed=false
  [[ "$rto_observed" -le "$rto_target" ]] && rto_passed=true
  passed=false
  [[ "$rto_passed" == "true" && "$row_a_recovered" == "true" && "$row_b_excluded" == "true" ]] && passed=true

  finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  mkdir -p tests/eval
  report=tests/eval/m13_pitr_drill_report.json
  cat > "$report" <<JSON
{
  "schemaVersion": "M13PitrDrillReportV1",
  "mode": "disposable",
  "startedAt": "$started_at",
  "finishedAt": "$finished_at",
  "rtoTargetSeconds": $rto_target,
  "rtoObservedSeconds": $rto_observed,
  "rpoTargetSeconds": $rpo_target,
  "rpoDesignSeconds": $archive_timeout,
  "image": "$image",
  "recoveryTargetTime": "$target_time",
  "baseBackupBytes": $base_bytes,
  "walArchivedBeforeRestore": $archived_after_b,
  "failedArchives": $failed_archives,
  "rowBeforeTargetRecovered": $row_a_recovered,
  "rowAfterTargetExcluded": $row_b_excluded,
  "rtoPassed": $rto_passed,
  "passed": $passed,
  "scope": "single-node PITR drill on a disposable ParadeDB container; production must place the WAL archive on replicated storage outside the database node"
}
JSON
  printf 'report: %s\n' "$report"

  [[ "$row_a_recovered" == "true" ]] || fail "marker written before the target time is missing after restore"
  [[ "$row_b_excluded" == "true" ]] || fail "marker written after the target time was replayed; point-in-time boundary failed"
  [[ "$rto_passed" == "true" ]] || fail "observed RTO ${rto_observed}s exceeds target ${rto_target}s"
  printf 'M13 PITR drill passed: RTO=%ss <= %ss, archive_timeout=%ss <= RPO=%ss, boundary verified at %s\n' \
    "$rto_observed" "$rto_target" "$archive_timeout" "$rpo_target" "$target_time"
  exit 0
fi

# Existing base backup + WAL archive (production-media check).
[[ -d "$existing_base" ]] || fail "existing base backup is not a directory: $existing_base"
[[ -d "$existing_archive" ]] || fail "existing archive is not a directory: $existing_archive"
[[ -f "$existing_base/PG_VERSION" ]] || fail "base backup is missing PG_VERSION"
[[ -f "$existing_base/backup_manifest" ]] || fail "base backup is missing backup_manifest"
[[ "$(cat "$existing_base/PG_VERSION")" == "17" ]] || fail "base backup major version $(cat "$existing_base/PG_VERSION") does not match the ParadeDB image (17)"
verify_db="${RESTORE_DB:-${POSTGRES_DB:-rag_platform}}"
restore_user="${RESTORE_DB_USER:-${POSTGRES_USER:-rag_platform}}"

archive_segments=0
newest_wal=""
while IFS= read -r wal; do
  name="$(basename "$wal")"
  [[ "$name" =~ ^[0-9A-F]{24}$ ]] || continue
  archive_segments=$((archive_segments + 1))
  newest_wal="$wal"
done < <(find "$existing_archive" -maxdepth 1 -type f | sort)
[[ "$archive_segments" -gt 0 ]] || fail "no 24-character WAL segments found in $existing_archive"
archive_lag_seconds="null"
if [[ -n "$newest_wal" ]]; then
  lag=$(( $(date +%s) - $(file_mtime "$newest_wal") ))
  [[ "$lag" -ge 0 ]] || lag=0
  archive_lag_seconds="$lag"
  if [[ "$lag" -gt "$rpo_target" ]]; then
    printf 'warning: newest archived WAL is %ss old, exceeding the RPO target %ss\n' "$lag" "$rpo_target" >&2
  fi
fi

step "verify existing base backup: $(basename "$existing_base")"
docker run --rm -v "$existing_base:/backup:ro" --entrypoint pg_verifybackup "$image" /backup

step "prepare restore volume from the existing base backup"
prepare_restore_volume "$existing_base"

step "restore existing archive: ${archive_segments} WAL segments, target ${target_time:-latest}"
run_restore "$existing_archive" "$target_time"

schema_migrations="$(psql_restore 'SELECT count(*) FROM schema_migrations;' 2>/dev/null || echo 0)"
rto_passed=false
[[ "$rto_observed" -le "$rto_target" ]] && rto_passed=true
passed=false
[[ "$rto_passed" == "true" && "$schema_migrations" =~ ^[0-9]+$ && "$schema_migrations" -gt 0 ]] && passed=true

finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
mkdir -p tests/eval
report=tests/eval/m13_pitr_restore_report.json
cat > "$report" <<JSON
{
  "schemaVersion": "M13PitrRestoreReportV1",
  "mode": "existing",
  "startedAt": "$started_at",
  "finishedAt": "$finished_at",
  "rtoTargetSeconds": $rto_target,
  "rtoObservedSeconds": $rto_observed,
  "rpoTargetSeconds": $rpo_target,
  "archiveLagSeconds": $archive_lag_seconds,
  "image": "$image",
  "baseBackup": "$(basename "$existing_base")",
  "archiveDirectory": "$(basename "$existing_archive")",
  "archiveSegments": $archive_segments,
  "recoveryTargetTime": "${target_time:-latest}",
  "schemaMigrations": $schema_migrations,
  "rtoPassed": $rto_passed,
  "passed": $passed,
  "scope": "existing production-style base backup and WAL archive restored into a disposable container; the archive path must be readable by the container postgres user"
}
JSON
printf 'report: %s\n' "$report"

[[ "$rto_passed" == "true" ]] || fail "observed RTO ${rto_observed}s exceeds target ${rto_target}s"
[[ "$schema_migrations" =~ ^[0-9]+$ && "$schema_migrations" -gt 0 ]] || fail "restored database has no schema_migrations rows; wrong archive, wrong database, or incomplete replay"
printf 'M13 PITR restore passed: RTO=%ss <= %ss, WAL segments=%s, recovery target=%s\n' \
  "$rto_observed" "$rto_target" "$archive_segments" "${target_time:-latest}"
