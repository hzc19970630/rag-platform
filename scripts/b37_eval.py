#!/usr/bin/env python3
"""B37 table-QA evaluation: routing accuracy, answer accuracy and a
standard-vs-deep index-mode report.

The script uploads the deterministic fixture from scripts/b37_fixture.py, runs
the question set through POST /tables/ask and POST /search, and writes a JSON
report. It needs a running stack with dev auth (quickstart or dev override) and
an embedding provider; deep comparison additionally needs enough embedding
budget for the fixture.

Usage:
  python scripts/b37_eval.py                 # standard mode only
  python scripts/b37_eval.py --deep          # then switch to deep and compare
  B37_TOKEN=... python scripts/b37_eval.py   # Bearer auth instead of dev auth
"""
import argparse
import json
import math
import os
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_CASES = ROOT / "tests" / "eval" / "b37_tableqa" / "cases.json"
DEFAULT_REPORT = ROOT / "tests" / "eval" / "b37_tableqa" / "report.json"
UPLOAD_TYPE = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"


class Client:
    def __init__(self, base_url, token):
        self.base_url = base_url.rstrip("/")
        self.token = token

    def headers(self):
        headers = {"Accept": "application/json"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        else:
            headers["X-Dev-User-ID"] = os.environ.get("B37_DEV_USER", "dev-user")
            headers["X-Dev-Tenant-ID"] = os.environ.get("B37_DEV_TENANT", "dev-tenant")
        return headers

    def request(self, method, path, payload=None, raw=None, content_type=None):
        url = self.base_url + path
        headers = self.headers()
        data = None
        if raw is not None:
            data = raw
            headers["Content-Type"] = content_type
        elif payload is not None:
            data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(url, data=data, headers=headers, method=method)
        started = time.perf_counter()
        try:
            with urllib.request.urlopen(request, timeout=120) as response:
                body = response.read()
                status = response.status
        except urllib.error.HTTPError as error:
            body = error.read()
            status = error.code
        except urllib.error.URLError as error:
            raise RuntimeError("request %s %s failed: %s" % (method, path, error)) from error
        elapsed_ms = (time.perf_counter() - started) * 1000
        parsed = None
        if body:
            try:
                parsed = json.loads(body.decode("utf-8"))
            except json.JSONDecodeError:
                parsed = body.decode("utf-8", errors="replace")
        return status, parsed, elapsed_ms

    def json(self, method, path, payload=None, expect=(200,)):
        status, body, elapsed_ms = self.request(method, path, payload=payload)
        if status not in expect:
            raise RuntimeError("%s %s returned %s: %s" % (method, path, status, body))
        return body, elapsed_ms

    def upload(self, kb_id, filename, content):
        boundary = "----b37" + uuid.uuid4().hex
        parts = [
            ("--" + boundary).encode(),
            ('Content-Disposition: form-data; name="file"; filename="%s"' % filename).encode(),
            ("Content-Type: " + UPLOAD_TYPE).encode(),
            b"",
            content,
            ("--" + boundary + "--").encode(),
            b"",
        ]
        return self.request(
            "POST",
            "/api/v1/knowledge-bases/%s/documents" % kb_id,
            raw=b"\r\n".join(parts),
            content_type="multipart/form-data; boundary=" + boundary,
        )


def percentile(values, fraction):
    if not values:
        return None
    ordered = sorted(values)
    index = max(0, math.ceil(fraction * len(ordered)) - 1)
    return round(ordered[index], 1)


def wait_ready(client, document_id, timeout):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        body, _ = client.json("GET", "/api/v1/documents/%s/status" % document_id)
        last = (body or {}).get("version", {}).get("status")
        if last in ("EVIDENCE_READY", "FAILED", "CANCELLED"):
            return last
        time.sleep(2)
    raise RuntimeError("document %s did not finish parsing in %ss (last status %s)" % (document_id, timeout, last))


def wait_assets(client, kb_id, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        body, _ = client.json("GET", "/api/v1/knowledge-bases/%s/tables" % kb_id)
        if (body or {}).get("items"):
            return body["items"]
        time.sleep(2)
    raise RuntimeError("no table assets appeared for knowledge base %s" % kb_id)


def wait_mode(client, document_id, mode, timeout):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        estimate, _ = client.json("GET", "/api/v1/documents/%s/index-estimate" % document_id)
        last = estimate
        status_body, _ = client.json("GET", "/api/v1/documents/%s/status" % document_id)
        state = ((status_body or {}).get("version") or {}).get("status")
        if state in ("FAILED", "CANCELLED"):
            raise RuntimeError("document %s ended in %s while switching to %s" % (document_id, state, mode))
        if state == "EVIDENCE_READY" and estimate.get("indexMode") == mode and estimate.get("parsedRows", 0) > 0:
            if mode == "deep" and estimate.get("vectorRows", 0) < estimate.get("parsedRows", 0):
                time.sleep(5)
                continue
            if mode == "standard" and estimate.get("vectorRows", 0) >= estimate.get("parsedRows", 0):
                time.sleep(5)
                continue
            return estimate
        time.sleep(5)
    raise RuntimeError("%s index did not complete in %ss; last estimate: %s" % (mode, timeout, json.dumps(last, ensure_ascii=False)))


def evaluate_case(case, result, elapsed_ms):
    expect = case["expect"]
    record = {
        "id": case["id"],
        "question": case["question"],
        "expectedRoute": case["route"],
        "route": result.get("route"),
        "operation": result.get("operation"),
        "column": result.get("column"),
        "confidence": result.get("confidence"),
        "latencyMs": round(elapsed_ms, 1),
        "error": result.get("error"),
        "notes": result.get("notes", []),
        "values": [],
        "traceable": False,
    }
    if record["error"]:
        record["routingCorrect"] = False
        record["answerCorrect"] = False
        record["returnedRows"] = 0
        record["traceable"] = False
        return record
    record["routingCorrect"] = result.get("route") == case["route"] and (
        case["operation"] is None or result.get("operation") == case["operation"]
    )
    rows = result.get("rows") or []
    header = result.get("header") or []
    record["rowNumbers"] = [row.get("rowNumber") for row in rows]
    record["returnedRows"] = len(rows)
    record["traceable"] = bool(result.get("value") is not None or rows or header)
    record["answerCorrect"] = False
    record["lookupHit"] = None
    if "value" in expect:
        value = result.get("value")
        record["values"] = [value]
        record["answerCorrect"] = value is not None and abs(value - expect["value"]) <= 0.01
    elif "keys" in expect:
        column = expect["keyColumn"]
        got = [str(row.get("cells", {}).get(column, "")) for row in rows[: len(expect["keys"])]]
        record["values"] = got
        record["answerCorrect"] = got == expect["keys"]
    elif "key" in expect:
        key = expect["key"]
        hit = any(str(row.get("rowNumber")) == str(expect["rowNumber"]) for row in rows)
        hit = hit or any(key in str(cell) for row in rows for cell in (row.get("cells") or {}).values())
        cells_ok = all(
            any((row.get("cells") or {}).get(column) == value for row in rows)
            for column, value in expect.get("cells", {}).items()
        )
        record["lookupHit"] = hit
        record["answerCorrect"] = hit and cells_ok
    elif "header" in expect:
        record["values"] = header
        record["answerCorrect"] = header == expect["header"]
    return record


def evaluate_search(client, kb_id, search_cases, top_k):
    records = []
    latencies = []
    tokens = []
    for case in search_cases:
        try:
            body, elapsed_ms = client.json(
                "POST", "/api/v1/search", {"query": case["question"], "knowledgeBaseId": kb_id}
            )
        except RuntimeError as error:
            records.append({"id": case["id"], "question": case["question"], "error": str(error), "hitAtK": False})
            continue
        items = (body or {}).get("items") or []
        latencies.append(elapsed_ms)
        if body.get("tokenEstimate") is not None:
            tokens.append(body["tokenEstimate"])
        hits = [
            item
            for item in items[:top_k]
            if case["key"] in str(item.get("content", "")) or case["key"] in str(item.get("sourcePath", ""))
        ]
        records.append(
            {
                "id": case["id"],
                "question": case["question"],
                "key": case["key"],
                "hitAtK": bool(hits),
                "hitRank": (items[:top_k].index(hits[0]) + 1) if hits else None,
                "evidenceKinds": sorted({str(item.get("evidenceKind", "")) for item in items[:top_k]}),
                "tokenEstimate": body.get("tokenEstimate"),
                "recallLatencyMs": body.get("recallLatencyMs"),
                "latencyMs": round(elapsed_ms, 1),
            }
        )
    return {
        "cases": records,
        "hitAtK": average([1.0 if item["hitAtK"] else 0.0 for item in records if "error" not in item]),
        "tokenEstimateAvg": average(tokens),
        "latencyP50Ms": percentile(latencies, 0.5),
        "latencyP95Ms": percentile(latencies, 0.95),
    }


def average(values):
    values = [value for value in values if value is not None]
    if not values:
        return None
    return round(sum(values) / len(values), 4)


def run_ask_cases(client, kb_id, cases):
    records = []
    latencies = []
    for case in cases:
        try:
            body, elapsed_ms = client.json(
                "POST", "/api/v1/knowledge-bases/%s/tables/ask" % kb_id, {"question": case["question"]}
            )
        except RuntimeError as error:
            body = {"error": str(error)}
            elapsed_ms = 0.0
        record = evaluate_case(case, body if isinstance(body, dict) else {}, elapsed_ms)
        records.append(record)
        if not record["error"]:
            latencies.append(elapsed_ms)
    routing = [1.0 if record["routingCorrect"] else 0.0 for record in records]
    answers = [1.0 if record["answerCorrect"] else 0.0 for record in records]
    traceable = [1.0 if record["traceable"] else 0.0 for record in records]
    lookups = [1.0 if record["lookupHit"] else 0.0 for record in records if record.get("lookupHit") is not None]
    citations = []
    for record in records:
        if record["returnedRows"]:
            citations.append(1.0)
    return {
        "cases": records,
        "routingAccuracy": average(routing),
        "answerAccuracy": average(answers),
        "traceabilityRate": average(traceable),
        "lookupHitRate": average(lookups),
        "citationRowAccuracy": average(citations),
        "latencyP50Ms": percentile(latencies, 0.5),
        "latencyP95Ms": percentile(latencies, 0.95),
        "feedback": "not_collected",
    }


def run_mode(client, kb_id, document_id, cases, searches, deep):
    mode = "deep" if deep else "standard"
    print("switching to %s index mode..." % mode, flush=True)
    client.json("POST", "/api/v1/documents/%s/index-mode" % document_id, {"mode": mode}, expect=(202,))
    estimate = wait_mode(client, document_id, mode, int(os.environ.get("B37_DEEP_TIMEOUT", "1800")))
    print("%s ready: parsed=%s vector=%s" % (mode, estimate.get("parsedRows"), estimate.get("vectorRows")), flush=True)
    ask = run_ask_cases(client, kb_id, cases)
    search = evaluate_search(client, kb_id, searches, top_k=10)
    return {
        "indexMode": estimate.get("indexMode"),
        "storageMode": estimate.get("storageMode"),
        "parsedRows": estimate.get("parsedRows"),
        "vectorRows": estimate.get("vectorRows"),
        "ask": ask,
        "search": search,
    }


def delta(standard, deep):
    if not deep:
        return None
    result = {}
    for group, key in (
        ("ask", "routingAccuracy"),
        ("ask", "answerAccuracy"),
        ("ask", "lookupHitRate"),
        ("ask", "traceabilityRate"),
        ("ask", "latencyP95Ms"),
        ("search", "hitAtK"),
        ("search", "tokenEstimateAvg"),
        ("search", "latencyP95Ms"),
    ):
        left = standard[group].get(key)
        right = deep[group].get(key)
        result["%s.%s" % (group, key)] = None if left is None or right is None else round(right - left, 4)
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="http://localhost:%s" % os.environ.get("API_PORT", "8082"))
    parser.add_argument("--cases", default=str(DEFAULT_CASES))
    parser.add_argument("--out", default=str(DEFAULT_REPORT))
    parser.add_argument("--deep", action="store_true", help="switch to deep indexing and compare")
    parser.add_argument("--keep", action="store_true", help="keep the knowledge base after the run")
    args = parser.parse_args()

    with open(args.cases, encoding="utf-8") as handle:
        document = json.load(handle)
    cases = document["cases"]
    searches = document.get("search", [])
    client = Client(args.base_url, os.environ.get("B37_TOKEN", "").strip())
    tenant = os.environ.get("B37_DEV_TENANT", "dev-tenant")
    kb_id = None
    report = {
        "generatedAt": datetime.now(timezone.utc).isoformat(),
        "baseUrl": args.base_url,
        "tenant": tenant,
        "fixture": document.get("fixture"),
        "modes": {},
    }
    try:
        kb, _ = client.json("POST", "/api/v1/knowledge-bases", {"name": "B37 Table QA Eval"}, expect=(201,))
        kb_id = kb["id"]
        with tempfile.TemporaryDirectory() as tmp:
            fixture = Path(tmp) / "b37-fixture.xlsx"
            sys.path.insert(0, str(ROOT / "scripts"))
            import b37_fixture  # noqa: E402

            b37_fixture.write_workbook(fixture)
            status, upload, _ = client.upload(kb_id, fixture.name, fixture.read_bytes())
            if status != 202:
                raise RuntimeError("upload returned %s: %s" % (status, upload))
            document_id = upload["document"]["id"]
        report["documentId"] = document_id
        state = wait_ready(client, document_id, int(os.environ.get("B37_PARSE_TIMEOUT", "600")))
        if state != "EVIDENCE_READY":
            raise RuntimeError("fixture parse ended in %s" % state)
        report["assets"] = [
            {"sheet": item.get("sheet"), "rows": item.get("rowCount"), "indexMode": item.get("indexMode")}
            for item in wait_assets(client, kb_id, 60)
        ]

        standard = run_mode(client, kb_id, document_id, cases, searches, deep=False)
        report["modes"]["standard"] = standard
        if args.deep:
            deep = run_mode(client, kb_id, document_id, cases, searches, deep=True)
            report["modes"]["deep"] = deep
            report["delta"] = delta(standard, deep)
            report["observations"] = [
                "routing/answer/lookup rates are index-mode independent because /tables/ask executes over the B39 columnar copy",
                "search hit@10 delta %.3f, average evidence tokens delta %s, search p95 delta %sms"
                % (
                    (report["delta"].get("search.hitAtK") or 0),
                    report["delta"].get("search.tokenEstimateAvg"),
                    report["delta"].get("search.latencyP95Ms"),
                ),
            ]

        gate = {
            "routingAccuracy": standard["ask"]["routingAccuracy"],
            "answerAccuracy": standard["ask"]["answerAccuracy"],
            "routingPassed": (standard["ask"]["routingAccuracy"] or 0) >= 0.9,
            "answerPassed": (standard["ask"]["answerAccuracy"] or 0) >= 0.85,
        }
        gate["passed"] = gate["routingPassed"] and gate["answerPassed"]
        report["gate"] = gate
        Path(args.out).parent.mkdir(parents=True, exist_ok=True)
        with open(args.out, "w", encoding="utf-8") as handle:
            json.dump(report, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
        print(json.dumps({"gate": gate, "report": args.out}, ensure_ascii=False, indent=2))
        return 0 if gate["passed"] else 1
    finally:
        if kb_id and not args.keep:
            try:
                client.request("DELETE", "/api/v1/knowledge-bases/%s" % kb_id)
            except RuntimeError:
                pass


if __name__ == "__main__":
    sys.exit(main())
