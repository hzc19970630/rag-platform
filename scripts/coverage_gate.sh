#!/usr/bin/env bash
# Coverage gate for changed Go packages.
#
# Aggregates statement coverage for every internal package touched by the
# change set and fails when a package is below the threshold. This stops
# coverage from regressing where it matters without demanding a global number.
# cmd/ and tests/ are intentionally out of scope: they are covered by
# acceptance and e2e runs instead.
#
# Usage:
#   scripts/coverage_gate.sh [--profile coverage.out] [--base origin/main]
#                             [--threshold 70] [--mode warn|enforce]
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

profile="coverage.out"
base="origin/main"
threshold="70"
mode="warn"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile) profile="$2"; shift 2 ;;
    --base) base="$2"; shift 2 ;;
    --threshold) threshold="$2"; shift 2 ;;
    --mode) mode="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      printf 'unknown argument: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

case "$mode" in
  warn|enforce) ;;
  *) printf 'invalid mode: %s\n' "$mode" >&2; exit 2 ;;
esac
case "$threshold" in
  ''|*[!0-9]*) printf 'invalid threshold: %s\n' "$threshold" >&2; exit 2 ;;
esac
if [[ "$threshold" -lt 0 || "$threshold" -gt 100 ]]; then
  printf 'threshold must be between 0 and 100\n' >&2
  exit 2
fi
if [[ ! -f "$profile" ]]; then
  printf 'coverage profile not found: %s\n' "$profile" >&2
  exit 2
fi

merge_base="$(git merge-base "$base" HEAD 2>/dev/null || true)"
if [[ -z "$merge_base" ]]; then
  printf '::notice title=Coverage gate::cannot resolve merge base for %s; gate skipped\n' "$base"
  exit 0
fi

# Include committed changes plus local edits so the gate can be run before a
# commit. Untracked files are picked up explicitly. Wiring-only (internal/app)
# and declarative contract packages are excluded: they are exercised through
# acceptance/e2e runs or contain no executable statements.
changed_dirs="$(
  {
    git diff --name-only "$merge_base" -- internal/ || true
    git ls-files --others --exclude-standard -- internal/ || true
  } | tr -d '\r' \
    | grep -E '\.go$' \
    | grep -v '_test\.go$' \
    | sed 's|/[^/]*$||' \
    | grep -v -E '^internal/(app|contracts)(/|$)' \
    | sort -u \
    || true
)"

if [[ -z "$changed_dirs" ]]; then
  printf '::notice title=Coverage gate::no changed internal packages; gate skipped\n'
  exit 0
fi

module="$(awk '$1 == "module" {print $2; exit}' go.mod)"
stats="$(
  awk -v module="$module" -v dirs="$changed_dirs" '
    BEGIN {
      n = split(dirs, d, "\n")
      for (i = 1; i <= n; i++) if (d[i] != "") want[d[i]] = 1
    }
    NR == 1 { next }
    {
      file = $1
      sub(/:.*/, "", file)
      if (index(file, module "/") == 1) file = substr(file, length(module) + 2)
      dir = file
      sub(/\/[^\/]*$/, "", dir)
      if (dir in want) {
        total[dir] += $2
        if ($3 + 0 > 0) covered[dir] += $2
      }
    }
    END {
      for (dir in want) printf "%s\t%d\t%d\n", dir, covered[dir] + 0, total[dir] + 0
    }
  ' "$profile"
)"

failures=0
while IFS=$'\t' read -r dir covered total; do
  [[ -n "$dir" ]] || continue
  if [[ "$total" -gt 0 ]]; then
    permille=$(( covered * 1000 / total ))
    printf '%-45s %3d.%d%%  (%d/%d statements)\n' \
      "$dir" "$(( permille / 10 ))" "$(( permille % 10 ))" "$covered" "$total"
    if [[ $(( covered * 100 )) -lt $(( total * threshold )) ]]; then
      failures=$(( failures + 1 ))
    fi
  else
    printf '%-45s    n/a  (no coverage record)\n' "$dir"
    failures=$(( failures + 1 ))
  fi
done <<<"$stats"

total_packages="$(printf '%s\n' "$changed_dirs" | grep -c . || true)"
if [[ "$failures" -eq 0 ]]; then
  printf 'coverage gate: %s changed package(s), all >= %s%%\n' "$total_packages" "$threshold"
  exit 0
fi

message="coverage gate: $failures of $total_packages changed package(s) below ${threshold}%"
if [[ "$mode" == "enforce" ]]; then
  printf '::error title=Coverage gate::%s\n' "$message"
  exit 1
fi
printf '::warning title=Coverage gate::%s\n' "$message"
exit 0
