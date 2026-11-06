#!/usr/bin/env bash
# M13-A backup drill: run the logical dump/restore drill and verify it against
# explicit RTO/RPO targets. Targets are deployment inputs, not defaults; the
# script refuses to run without them so a drill cannot silently use invented
# numbers.
#
# Usage:
#   RTO_TARGET_SECONDS=900 RPO_TARGET_SECONDS=300 ./scripts/m13_backup_drill.sh
#   RTO_TARGET_SECONDS=900 RPO_TARGET_SECONDS=300 ./scripts/m13_backup_drill.sh --dry-run
#
# This drill covers the logical dump path. Production WAL/PITR and object
# storage versioning are deployment configuration; see docs/operations.md §3.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

dry_run=0
if [[ "${1:-}" == "--dry-run" ]]; then
  dry_run=1
elif [[ $# -gt 0 ]]; then
  printf 'usage: %s [--dry-run]\n' "$0" >&2
  exit 2
fi

fail() { printf 'M13 backup drill failed: %s\n' "$*" >&2; exit 1; }

positive_int() {
  [[ "$1" =~ ^[1-9][0-9]*$ ]]
}

rto_target="${RTO_TARGET_SECONDS:-}"
rpo_target="${RPO_TARGET_SECONDS:-}"
positive_int "$rto_target" || fail "RTO_TARGET_SECONDS must be a positive integer (owner-provided target)"
positive_int "$rpo_target" || fail "RPO_TARGET_SECONDS must be a positive integer (owner-provided target)"

printf 'RTO target: %ss\nRPO target: %ss\n' "$rto_target" "$rpo_target"
if [[ "$dry_run" == "1" ]]; then
  printf 'dry run: would execute ./scripts/m4_backup_restore.sh and compare the measured wall time\n'
  exit 0
fi

report_dir="tests/eval"
mkdir -p "$report_dir"
report="$report_dir/m13_backup_drill_report.json"
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
start_epoch="$(date +%s)"

./scripts/m4_backup_restore.sh

finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
elapsed=$(( $(date +%s) - start_epoch ))

# A logical dump is taken at the start of the drill, so the elapsed restore
# window is a conservative upper bound on the data-loss window (RPO).
rto_observed="$elapsed"
rpo_observed="$elapsed"
rto_ok=false
rpo_ok=false
[[ "$rto_observed" -le "$rto_target" ]] && rto_ok=true
[[ "$rpo_observed" -le "$rpo_target" ]] && rpo_ok=true

cat > "$report" <<JSON
{
  "schemaVersion": "M13BackupDrillReportV1",
  "startedAt": "$started_at",
  "finishedAt": "$finished_at",
  "rtoTargetSeconds": $rto_target,
  "rpoTargetSeconds": $rpo_target,
  "rtoObservedSeconds": $rto_observed,
  "rpoObservedSeconds": $rpo_observed,
  "rtoPassed": $rto_ok,
  "rpoPassed": $rpo_ok,
  "scope": "logical pg_dump/pg_restore drill; production WAL/PITR and object-storage versioning are deployment inputs"
}
JSON
printf 'report: %s\n' "$report"

[[ "$rto_ok" == "true" ]] || fail "observed RTO ${rto_observed}s exceeds target ${rto_target}s"
[[ "$rpo_ok" == "true" ]] || fail "observed RPO ${rpo_observed}s exceeds target ${rpo_target}s"
printf 'M13 backup drill passed: RTO=%ss <= %ss, RPO=%ss <= %ss\n' \
  "$rto_observed" "$rto_target" "$rpo_observed" "$rpo_target"
