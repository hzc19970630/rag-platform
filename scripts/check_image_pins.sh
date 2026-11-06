#!/usr/bin/env bash
# Fails when a container image reference is not pinned by digest. The digest is
# the multi-arch index digest, so the same pin works on amd64 and arm64.
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

failures=0

require_digest() {
  local source="$1" reference="$2"
  if [[ "$reference" != *"@sha256:"* ]]; then
    printf 'unpinned image in %s: %s\n' "$source" "$reference" >&2
    failures=1
  fi
}

shopt -s nullglob
for compose_file in deploy/docker-compose/*.yml; do
  while IFS= read -r reference; do
    [[ -n "$reference" ]] || continue
    require_digest "$compose_file" "$reference"
  done < <(sed -nE 's/^[[:space:]]+image:[[:space:]]*([^[:space:]#]+).*/\1/p' "$compose_file")
done

for dockerfile in Dockerfile services/*/Dockerfile web/Dockerfile; do
  [[ -f "$dockerfile" ]] || continue
  while IFS= read -r reference; do
    [[ -n "$reference" ]] || continue
    [[ "$reference" == "scratch" ]] && continue
    require_digest "$dockerfile" "$reference"
  done < <(sed -nE 's/^[[:space:]]*FROM[[:space:]]+([^[:space:]]+).*/\1/p' "$dockerfile")
done

while IFS= read -r reference; do
  [[ -n "$reference" ]] || continue
  require_digest "Makefile GO_IMAGE" "$reference"
done < <(sed -nE 's/^GO_IMAGE[[:space:]]*\?=[[:space:]]*([^[:space:]]+).*/\1/p' Makefile)

if [[ "$failures" -ne 0 ]]; then
  printf 'image pin check failed; use tag@sha256:<multi-arch digest>\n' >&2
  exit 1
fi
printf 'all image references are digest-pinned\n'
