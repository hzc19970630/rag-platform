#!/usr/bin/env bash
# Coverage gate for changed Go packages.
#
# Aggregates statement coverage for every internal package touched by the
# change set and fails when a package is below the threshold. This stops
# coverage from regressing where it matters without demanding a global number.
# cmd/ and tests/ are intentionally out of scope: they are covered by
# acceptance and e2e runs instead.
#
# Per-package floors come from scripts/coverage_tiers.txt: security and
# reliability packages are held higher than the core default, declarative
# packages can be marked 0 to opt out. Longest matching path prefix wins.
#
# Usage:
#   scripts/coverage_gate.sh [--profile coverage.out] [--base origin/main]
#                             [--tiers scripts/coverage_tiers.txt]
#                             [--threshold 70] [--mode warn|enforce]
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

profile="coverage.out"
base="origin/main"
threshold="70"
mode="warn"
tiers_file="scripts/coverage_tiers.txt"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile) profile="$2"; shift 2 ;;
    --base) base="$2"; shift 2 ;;
    --threshold) threshold="$2"; shift 2 ;;
    --tiers) tiers_file="$2"; shift 2 ;;
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
if [[ -n "$tiers_file" && ! -f "$tiers_file" ]]; then
  printf 'coverage tiers file not found: %s\n' "$tiers_file" >&2
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

# A package that was deleted in the change set has no profile record; only
# directories that still contain Go sources are ratcheted.
changed_dirs="$(
  printf '%s\n' "$changed_dirs" | while IFS= read -r dir; do
    [[ -n "$dir" ]] || continue
    compgen -G "$dir/*.go" >/dev/null || continue
    printf '%s\n' "$dir"
  done
)"

if [[ -z "$changed_dirs" ]]; then
  printf '::notice title=Coverage gate::no changed internal packages; gate skipped\n'
  exit 0
fi

# Temp files instead of -v lists and associative arrays: bash 3.2 (macOS
# /bin/bash) has no `declare -A`, and BSD awk rejects a newline inside -v.
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/coverage_gate.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
dirs_file="$work_dir/dirs"
thresholds_file="$work_dir/thresholds"
printf '%s\n' "$changed_dirs" > "$dirs_file"
awk -v dirsfile="$dirs_file" -v tiers="$tiers_file" -v fallback="$threshold" '
    BEGIN {
      while ((getline entry < dirsfile) > 0) {
        sub(/\r$/, "", entry)
        if (entry != "") d[++n] = entry
      }
      close(dirsfile)
      if (tiers != "") {
        while ((getline line < tiers) > 0) {
          sub(/\r$/, "", line)
          if (line ~ /^[[:space:]]*(#|$)/) continue
          split(line, fields, /[[:space:]]+/)
          if (fields[1] !~ /^[0-9]+$/ || fields[2] == "") continue
          count++
          values[count] = fields[1]
          prefixes[count] = fields[2]
        }
      }
      for (i = 1; i <= n; i++) {
        if (d[i] == "") continue
        best = fallback
        bestLength = -1
        for (j = 1; j <= count; j++) {
          lengthOfPrefix = length(prefixes[j])
          if ((d[i] == prefixes[j] || index(d[i], prefixes[j] "/") == 1) && lengthOfPrefix > bestLength) {
            best = values[j]
            bestLength = lengthOfPrefix
          }
        }
        printf "%s\t%s\n", d[i], best
      }
    }
  ' > "$thresholds_file"

threshold_for() {
  local dir="$1" value
  value="$(awk -F'\t' -v d="$dir" '$1 == d {print $2; exit}' "$thresholds_file")"
  printf '%s' "${value:-$threshold}"
}

module="$(awk '$1 == "module" {print $2; exit}' go.mod)"
stats="$(
  awk -v module="$module" -v dirsfile="$dirs_file" '
    BEGIN {
      while ((getline entry < dirsfile) > 0) {
        sub(/\r$/, "", entry)
        if (entry != "") want[entry] = 1
      }
      close(dirsfile)
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
evaluated=0
skipped=0
while IFS=$'\t' read -r dir covered total; do
  [[ -n "$dir" ]] || continue
  package_threshold="$(threshold_for "$dir")"
  if [[ "$package_threshold" -eq 0 ]]; then
    printf '%-45s    n/a  (not ratcheted, threshold 0)\n' "$dir"
    skipped=$(( skipped + 1 ))
    continue
  fi
  evaluated=$(( evaluated + 1 ))
  if [[ "$total" -gt 0 ]]; then
    permille=$(( covered * 1000 / total ))
    printf '%-45s %3d.%d%%  (%d/%d statements, threshold %s%%)\n' \
      "$dir" "$(( permille / 10 ))" "$(( permille % 10 ))" "$covered" "$total" "$package_threshold"
    if [[ $(( covered * 100 )) -lt $(( total * package_threshold )) ]]; then
      failures=$(( failures + 1 ))
    fi
  else
    printf '%-45s    n/a  (no coverage record, threshold %s%%)\n' "$dir" "$package_threshold"
    failures=$(( failures + 1 ))
  fi
done <<<"$stats"

total_packages="$evaluated"
if [[ "$failures" -eq 0 ]]; then
  printf 'coverage gate: %s changed package(s) met their tier thresholds' "$total_packages"
  [[ "$skipped" -gt 0 ]] && printf ' (%s not ratcheted)' "$skipped"
  printf '\n'
  exit 0
fi

message="coverage gate: $failures of $total_packages changed package(s) below their tier thresholds"
if [[ "$mode" == "enforce" ]]; then
  printf '::error title=Coverage gate::%s\n' "$message"
  exit 1
fi
printf '::warning title=Coverage gate::%s\n' "$message"
exit 0
