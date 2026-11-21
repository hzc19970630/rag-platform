#!/usr/bin/env bash
# Reparse every document in a knowledge base in batches of 100. Uses the
# existing batch API; no new backend endpoint or queue is introduced.
#
# Usage:
#   scripts/reparse_kb.sh <knowledge-base-id>
#
# Optional env:
#   API_BASE   default http://localhost:8082/api/v1
#   API_KEY    bearer token; when empty, development identity headers are used
#   DRY_RUN=1  list and count documents without reparse
#   PYTHON_BIN default python
set -euo pipefail

kb="${1:-}"
if [[ -z "$kb" ]]; then
  echo "usage: $0 <knowledge-base-id>" >&2
  exit 2
fi

base="${API_BASE:-http://localhost:8082/api/v1}"
python_bin="${PYTHON_BIN:-python}"
if ! "$python_bin" -c 'import sys' >/dev/null 2>&1; then python_bin=python3; fi

"$python_bin" - "$base" "$kb" "${API_KEY:-}" "${DRY_RUN:-0}" <<'PY'
import json
import sys
import urllib.error
import urllib.request

base, kb, api_key, dry_run = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4] == "1"
headers = {"Content-Type": "application/json"}
if api_key:
    headers["Authorization"] = "Bearer " + api_key
else:
    headers["X-Dev-User-ID"] = "dev-user"
    headers["X-Dev-Tenant-ID"] = "dev-tenant"


def call(method, path, payload=None):
    data = json.dumps(payload).encode() if payload is not None else None
    request = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request) as response:
            body = response.read()
            return json.loads(body) if body else {}
    except urllib.error.HTTPError as error:
        raise SystemExit(f"{method} {path} failed: {error.code} {error.read().decode(errors='replace')[:300]}")


ids = []
cursor = ""
while True:
    page = call("GET", f"/knowledge-bases/{kb}/documents?limit=100&cursor={cursor}")
    ids.extend(item["document"]["id"] for item in page.get("items", []))
    if not page.get("hasMore"):
        break
    cursor = page.get("nextCursor", "")

print(f"documents={len(ids)}")
if dry_run:
    print("dry-run: no reparse requested")
    raise SystemExit(0)

failed = 0
for start in range(0, len(ids), 100):
    batch = ids[start:start + 100]
    result = call("POST", f"/knowledge-bases/{kb}/documents/batch", {"action": "reparse", "documentIds": batch})
    for item in result.get("items", []):
        if not item.get("ok"):
            failed += 1
            print(f"failed {item.get('documentId')}: {item.get('errorCode')} {item.get('errorMessage')}")
print(f"reparse_requested={len(ids)} failed={failed}")
raise SystemExit(1 if failed else 0)
PY
