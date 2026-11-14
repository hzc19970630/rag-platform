#!/usr/bin/env python3
"""Import an evaluation report into the retrieval-quality view.

The report is the JSON written by m3_eval.py (or a rerank comparison produced
with the same metrics shape). Ops requires tenant:admin; pass --api-key or use
the development identity headers.

Usage:
    python3 scripts/quality_upload.py --report tests/eval/m3_eval_report.json
    python3 scripts/quality_upload.py --report report.json --kind rerank_compare \
        --knowledge-base-id kb-dev --config-hash "$(sha256sum config/config.yaml | cut -d' ' -f1)"
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path


def post(url: str, payload: dict, api_key: str) -> dict:
    data = json.dumps(payload).encode("utf-8")
    request = urllib.request.Request(url, data=data, method="POST", headers={"Content-Type": "application/json"})
    if api_key:
        request.add_header("Authorization", f"Bearer {api_key}")
    else:
        request.add_header("X-Dev-User-ID", os.environ.get("DEV_USER_ID", "dev-user"))
        request.add_header("X-Dev-Tenant-ID", os.environ.get("DEV_TENANT_ID", "dev-tenant"))
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        raise SystemExit(f"quality upload failed: {error.code} {error.read().decode('utf-8', 'replace')}") from error


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--report", required=True)
    parser.add_argument("--base-url", default=os.environ.get("RAG_BASE_URL", "http://localhost:8082"))
    parser.add_argument("--api-key", default=os.environ.get("RAG_API_KEY", ""))
    parser.add_argument("--kind", default="m3")
    parser.add_argument("--knowledge-base-id", default="")
    parser.add_argument("--config-hash", default="")
    args = parser.parse_args()

    report = json.loads(Path(args.report).read_text(encoding="utf-8"))
    metrics = report.get("metrics")
    if not isinstance(metrics, dict) or not metrics:
        raise SystemExit("report has no metrics object")
    gate_status = str((report.get("gates") or {}).get("status", "failed"))
    status = "passed" if gate_status == "passed" else "failed"
    payload = {
        "kind": args.kind,
        "knowledgeBaseId": args.knowledge_base_id,
        "status": status,
        "configHash": args.config_hash,
        "metrics": {key: value for key, value in metrics.items() if isinstance(value, (int, float))},
        "report": report,
    }
    result = post(args.base_url.rstrip("/") + "/api/v1/ops/quality/eval-runs", payload, args.api_key)
    print(json.dumps({"id": result.get("id"), "status": result.get("status"), "kind": result.get("kind")}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
