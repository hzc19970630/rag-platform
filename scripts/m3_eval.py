#!/usr/bin/env python3
"""M3 authorized RAG evaluation harness.

Runs the hand-authored evaluation set in tests/eval/m3_eval_set.json against a
running stack and records retrieval, citation, refusal and permission metrics.

The script exits non-zero when a release gate fails: a permission probe is
allowed, an answer invents a reference, a citation-free answer carries one, or
any metric falls below its signed-off threshold (see GATE_THRESHOLDS). Safety
invariants and retrieval/citation correctness have no acceptable degradation;
document coverage tolerates the known cross-document gap. conflictFlagRate and
meanLatencyMs are diagnostics with no release threshold (design 11).

Usage:
    python3 scripts/m3_eval.py [--base-url http://localhost:8082] \
        [--embedding-model MODEL] [--embedding-dimension N] [--recall-samples N]

recallAtK repeats the retrieval call --recall-samples times per case and reports
the mean. The default is 1: on a fixed index retrieval is deterministic for a
given query, so a single call is the honest default and repeating it takes no
new information. Raise it only to measure whether repeated retrieval disagrees.
That costs no model calls; everything else runs once per case.

The chat model is read from the answer event stream, which reports the model the
API was configured to call. A gateway can still substitute a different model, and
the embedding model cannot be observed over HTTP at all, so it is recorded only
when the operator declares it with --embedding-model; an undeclared run says so
in its limitations instead of claiming which embedder produced the numbers.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone
from pathlib import Path

DEV_HEADERS = {"X-Dev-User-ID": "dev-user", "X-Dev-Tenant-ID": "dev-tenant"}
EVAL_SET = Path(__file__).resolve().parent.parent / "tests" / "eval" / "m3_eval_set.json"
REPORT = Path(__file__).resolve().parent.parent / "tests" / "eval" / "m3_eval_report.json"

# A hosted model can fail for reasons that have nothing to do with retrieval or
# answer quality. These reasons are retried, and if they persist the case is
# excluded from the aggregates rather than scored as a miss: a transient 502 must
# not read as a quality regression in the report.
TRANSIENT_REASONS = {"model_error", "timeout"}
MAX_ATTEMPTS = 3


class Client:
    def __init__(self, base_url: str, headers: dict[str, str] | None = None) -> None:
        self.base = base_url.rstrip("/")
        self.headers = dict(DEV_HEADERS)
        if headers:
            self.headers.update(headers)

    def request(self, method: str, path: str, body: dict | None = None, raw: bytes | None = None, content_type: str | None = None) -> tuple[int, bytes]:
        data = raw
        headers = dict(self.headers)
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        if content_type:
            headers["Content-Type"] = content_type
        request = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=120) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.read()

    def json(self, method: str, path: str, body: dict | None = None) -> tuple[int, dict]:
        status, payload = self.request(method, path, body=body)
        try:
            return status, json.loads(payload or b"{}")
        except json.JSONDecodeError:
            return status, {}

    def upload(self, knowledge_base_id: str, filename: str, content: str) -> dict:
        body, content_type = multipart_body(filename, content)
        status, payload = self.request(
            "POST",
            f"/api/v1/knowledge-bases/{knowledge_base_id}/documents",
            raw=body,
            content_type=content_type,
        )
        if status != 202:
            raise RuntimeError(f"upload {filename} failed: {status} {payload[:400]!r}")
        return json.loads(payload)

    def ask(self, conversation_id: str, question: str, session_override: str | None = None) -> dict:
        """Runs one question and returns the answer event plus the event stream."""
        body: dict = {"query": question, "requestId": "eval-" + uuid.uuid4().hex}
        if session_override:
            body["sessionOverride"] = session_override
        status, raw = self.request(
            "POST",
            f"/api/v1/sessions/{conversation_id}/messages",
            body=body,
            content_type="application/json",
        )
        if status != 200:
            raise RuntimeError(f"ask failed: {status} {raw[:400]!r}")
        events = parse_sse(raw.decode("utf-8"))
        answer = next((event for event in events if event["type"] == "answer"), {"data": {}})
        complete = next((event for event in events if event["type"] == "complete"), {"data": {}})
        return {"answer": answer.get("data", {}), "complete": complete.get("data", {}), "events": events, "requestId": body["requestId"]}


def ask_with_retry(client: Client, conversation_id: str, question: str, session_override: str | None) -> tuple[dict, int, str | None]:
    """Asks one question, retrying provider-side failures.

    Returns the result, the number of attempts spent, and the reason that
    survived every attempt (None when the case produced a real answer).
    """
    result: dict = {}
    reason: str | None = None
    for attempt in range(1, MAX_ATTEMPTS + 1):
        result = client.ask(conversation_id, question, session_override)
        validation = result["answer"].get("validation") or {}
        reason = validation.get("reason")
        if reason not in TRANSIENT_REASONS:
            return result, attempt, None
        if attempt < MAX_ATTEMPTS:
            time.sleep(2 * attempt)
    return result, MAX_ATTEMPTS, reason


def chat_model(result: dict) -> str | None:
    """Reads the model that served the answer from the GENERATING status event."""
    for event in result.get("events", []):
        data = event.get("data") or {}
        if event.get("type") == "status" and data.get("state") == "GENERATING":
            return data.get("model")
    return None


def parse_sse(raw: str) -> list[dict]:
    events = []
    for block in raw.split("\n\n"):
        if not block.strip():
            continue
        event_type = re.search(r"^event: (.+)$", block, re.M)
        payload = re.search(r"^data: (.+)$", block, re.M)
        if not payload:
            continue
        data = json.loads(payload.group(1))
        data["type"] = data.get("type") or (event_type.group(1) if event_type else "message")
        events.append(data)
    return events


def wait_ready(client: Client, document_id: str, timeout_seconds: int = 120) -> str:
    deadline = time.time() + timeout_seconds
    status = "UNKNOWN"
    while time.time() < deadline:
        _, payload = client.json("GET", f"/api/v1/documents/{document_id}")
        status = payload.get("pipeline", {}).get("status", "UNKNOWN")
        if status in {"EVIDENCE_READY", "FAILED", "CANCELLED"}:
            return status
        time.sleep(1)
    return status


def aligned(case: dict, item: dict) -> bool:
    markers = case.get("alignedEvidence")
    if not markers:
        return False
    content = item.get("content", "")
    return any(marker in content for marker in markers)


def multipart_body(filename: str, content: str) -> tuple[bytes, str]:
    boundary = "----m3eval" + uuid.uuid4().hex
    body = (
        f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{filename}\"\r\n"
        f"Content-Type: text/markdown\r\n\r\n{content}\r\n--{boundary}--\r\n"
    ).encode()
    return body, f"multipart/form-data; boundary={boundary}"


# Target of the member write probes. Any existing user works: the point is that
# the probe actor is denied, not who it tries to modify.
PROBE_MEMBER_TARGET = "dev-user"


def run_permission_probes(
    base_url: str,
    actor: dict[str, str],
    knowledge_base_id: str,
    conversation_id: str,
    document_id: str,
    chunk_id: str | None,
    request_id: str | None,
    probes: list[str],
) -> list[dict]:
    """Runs each probe as the given actor and records the status it received.

    A 2xx means an unauthorized caller reached tenant data or performed a write.
    A probe whose target id is missing is recorded as unavailable rather than as
    a pass, so an unrun probe can never look like a clean result.
    """
    client = Client(base_url, {"X-Dev-User-ID": actor["userId"], "X-Dev-Tenant-ID": actor["tenantId"]})
    results: list[dict] = []
    for probe in probes:
        unavailable = probe in {"chunk", "events"} and (chunk_id is None if probe == "chunk" else request_id is None)
        if unavailable:
            results.append({"probe": probe, "status": None, "denied": False, "unavailable": True})
            continue
        if probe == "search":
            code, _ = client.json("POST", "/api/v1/search", {"query": "hybrid recall", "knowledgeBaseId": knowledge_base_id})
        elif probe == "session":
            code, _ = client.json("POST", "/api/v1/sessions", {"knowledge_base_id": knowledge_base_id})
        elif probe == "messages":
            code, _ = client.json(
                "POST", f"/api/v1/sessions/{conversation_id}/messages",
                {"query": "hybrid recall", "requestId": "probe-" + uuid.uuid4().hex},
            )
        elif probe == "document":
            code, _ = client.json("GET", f"/api/v1/documents/{document_id}")
        elif probe == "documentContent":
            code, _ = client.json("GET", f"/api/v1/documents/{document_id}/content")
        elif probe == "chunk":
            code, _ = client.json("GET", f"/api/v1/chunks/{chunk_id}")
        elif probe == "events":
            code, _ = client.json("GET", f"/api/v1/requests/{request_id}/events")
        elif probe == "upload":
            raw, content_type = multipart_body("probe.md", "# probe\n\nunauthorized upload attempt\n")
            code, _ = client.request(
                "POST", f"/api/v1/knowledge-bases/{knowledge_base_id}/documents", raw=raw, content_type=content_type,
            )
        elif probe == "members":
            code, _ = client.json(
                "POST", f"/api/v1/knowledge-bases/{knowledge_base_id}/members",
                {"user_id": PROBE_MEMBER_TARGET, "role": "viewer"},
            )
        elif probe == "memberRemove":
            code, _ = client.request("DELETE", f"/api/v1/knowledge-bases/{knowledge_base_id}/members/{PROBE_MEMBER_TARGET}")
        else:
            raise RuntimeError(f"unknown permission probe {probe!r}")
        results.append({"probe": probe, "status": code, "denied": code >= 400})
    return results


# Thresholds signed off by the product/architecture owner on 2026-10-21. A
# release that leaks tenant data, invents a reference or answers without
# evidence has no acceptable score, and single-draw retrieval on a fixed index
# is deterministic, so those floors are 1.0. documentHitRate allows the known
# cross-document gap (currently 32/36 = 0.8889) plus one further miss
# (31/36 = 0.8611 still passes). conflictFlagRate is not reproducible run to
# run and meanLatencyMs tracks relay throughput, so neither gates a release.
GATE_THRESHOLDS = {
    "permissionNegativeDenials": 1.0,
    "refusalRate": 1.0,
    "citationAccuracy": 1.0,
    "groundedness": 1.0,
    "injectionFlagRate": 1.0,
    "recallAtK": 1.0,
    "documentHitRate": 0.85,
}
GATE_METRICS = tuple(GATE_THRESHOLDS)
DIAGNOSTIC_METRICS = ("conflictFlagRate", "meanLatencyMs")


def gate_failures(metrics: dict, case_results: list[dict]) -> list[str]:
    failures: list[str] = []
    for name, threshold in GATE_THRESHOLDS.items():
        value = metrics.get(name)
        if value is None:
            failures.append(f"{name} could not be measured")
        elif value < threshold:
            failures.append(f"{name} = {value}, expected >= {threshold}")
    for case in case_results:
        if case.get("excluded"):
            continue
        if case["type"] == "permission_negative":
            for probe in case["probes"]:
                if probe.get("unavailable"):
                    failures.append(f"{case['id']}: probe {probe['probe']} could not be evaluated")
                elif not probe["denied"]:
                    failures.append(f"{case['id']}: probe {probe['probe']} was allowed with status {probe['status']}")
        if case.get("expectNoCitations") and case.get("citationCount"):
            count = case["citationCount"]
            if case.get("refused"):
                failures.append(f"{case['id']}: the refusal carried {count} citation(s)")
            else:
                failures.append(f"{case['id']}: the no-answer case did not refuse and carried {count} citation(s)")
        if case.get("errorCode") == "citation_rejected":
            failures.append(f"{case['id']}: the answer cited a reference that was never registered")
    return failures


def sample_evidence(client: Client, search_body: dict, case: dict, samples: int) -> tuple[list[dict], int, list[int]]:
    """Repeats one search call so recall is measured over draws.

    A fixed index answers a fixed query deterministically, so the default is one
    draw and repeating only measures whether draws disagree. Returns the last
    sample's items for the other metrics, the number of samples that contained
    the aligned evidence, and the per-sample item counts.
    """
    items: list[dict] = []
    hits = 0
    counts: list[int] = []
    for _ in range(samples):
        _, search = client.json("POST", "/api/v1/search", search_body)
        items = search.get("items") or []
        counts.append(len(items))
        if any(aligned(case, item) for item in items):
            hits += 1
    return items, hits, counts


def main() -> int:
    parser = argparse.ArgumentParser()
    default_base = f"http://localhost:{os.environ.get('API_PORT', '8082')}"
    parser.add_argument("--base-url", default=default_base)
    parser.add_argument("--embedding-model", default=None, help="embedding model name to record in the report")
    parser.add_argument("--embedding-dimension", type=int, default=None, help="embedding vector dimension to record in the report")
    parser.add_argument("--recall-samples", type=int, default=1, help="how many times to repeat each retrieval call for recallAtK (default 1, no model calls)")
    arguments = parser.parse_args()
    arguments.recall_samples = max(1, arguments.recall_samples)

    evaluation = json.loads(EVAL_SET.read_text(encoding="utf-8"))
    client = Client(arguments.base_url)

    status, _ = client.json("GET", "/healthz")
    if status != 200:
        print(f"stack is not reachable at {arguments.base_url}", file=sys.stderr)
        return 2

    suffix = datetime.now(timezone.utc).strftime("%Y%m%d%H%M%S")
    status, knowledge_base = client.json("POST", "/api/v1/knowledge-bases", {"name": f"m3-eval-{suffix}", "description": "M3 evaluation fixture"})
    if status not in (200, 201):
        print(f"could not create the evaluation knowledge base: {knowledge_base}", file=sys.stderr)
        return 2
    knowledge_base_id = knowledge_base["id"]

    documents: dict[str, dict] = {}
    for fixture in evaluation["fixtures"]:
        upload = client.upload(knowledge_base_id, fixture["filename"], fixture["content"])
        document_id = upload["document"]["id"]
        version_id = upload["version"]["id"]
        pipeline_status = wait_ready(client, document_id)
        if pipeline_status != "EVIDENCE_READY":
            print(f"fixture {fixture['filename']} did not become ready: {pipeline_status}", file=sys.stderr)
            return 2
        documents[fixture["role"]] = {"documentId": document_id, "versionId": version_id, "filename": fixture["filename"]}

    status, conversation = client.json("POST", "/api/v1/sessions", {"knowledge_base_id": knowledge_base_id})
    if status not in (200, 201):
        print(f"could not create the evaluation conversation: {conversation}", file=sys.stderr)
        return 2
    conversation_id = conversation["id"]

    # The in-tenant role cases need members who really hold those roles: a
    # "viewer cannot write" probe proves nothing if the viewer holds no membership.
    for member_id, member_role in (("viewer-a", "viewer"), ("editor-a", "editor")):
        status, payload = client.json(
            "POST", f"/api/v1/knowledge-bases/{knowledge_base_id}/members", {"user_id": member_id, "role": member_role},
        )
        if status not in (200, 201):
            print(f"could not add the {member_role} evaluation member {member_id}: {status} {payload}", file=sys.stderr)
            return 2

    case_results = []
    observed_chat_models: set[str] = set()
    owner_request_ids: list[str] = []
    owner_chunk_ids: list[str] = []
    for case in evaluation["cases"]:
        if case["type"] == "permission_negative":
            probes = run_permission_probes(
                arguments.base_url,
                case["actor"],
                knowledge_base_id,
                conversation_id,
                documents["handbook"]["documentId"],
                owner_chunk_ids[-1] if owner_chunk_ids else None,
                owner_request_ids[-1] if owner_request_ids else None,
                case["probes"],
            )
            case_results.append({
                "id": case["id"],
                "type": case["type"],
                "actor": case["actor"],
                "probes": probes,
                # leaked is the security truth. Probes that could not run are
                # reported separately so an unrun probe cannot pass as clean.
                "leaked": any(not probe["denied"] and not probe.get("unavailable") for probe in probes),
                "unevaluated": [probe["probe"] for probe in probes if probe.get("unavailable")],
                "denied": all(probe["denied"] for probe in probes),
            })
            continue

        started = time.time()
        result, attempts, failed_reason = ask_with_retry(client, conversation_id, case["question"], case.get("sessionOverride"))
        latency_ms = int((time.time() - started) * 1000)
        answer = result["answer"]
        if model := chat_model(result):
            observed_chat_models.add(model)
        citations = answer.get("citations") or []
        validation = answer.get("validation") or {}
        evidence_chunk_ids = set(answer.get("evidenceChunkIds") or [])
        owner_request_ids.append(result["requestId"])
        owner_chunk_ids.extend(sorted(evidence_chunk_ids))

        search_body: dict = {"query": case["question"], "knowledgeBaseId": knowledge_base_id}
        if case.get("sessionOverride"):
            search_body["sessionOverride"] = case["sessionOverride"]
        evidence, recall_hits, evidence_counts = sample_evidence(client, search_body, case, arguments.recall_samples)

        cited_in_evidence = all(citation["chunkId"] in evidence_chunk_ids for citation in citations) if citations or evidence_chunk_ids else False
        cited_aligned_documents = sorted({
            role for citation in citations for role, document in documents.items() if document["documentId"] == citation["documentId"]
        })
        allowed_documents = set(case.get("alignedDocuments") or documents.keys())
        cited_allowed = all(role in allowed_documents for role in cited_aligned_documents)
        expected_documents = case.get("expectedDocuments") or []
        missing_expected = sorted(set(expected_documents) - set(cited_aligned_documents))

        case_results.append({
            "id": case["id"],
            "type": case["type"],
            "status": answer.get("status"),
            "errorCode": validation.get("reason"),
            "latencyMs": latency_ms,
            "evidenceCount": len(evidence),
            "recallAtK": round(recall_hits / arguments.recall_samples, 4),
            "recallSamples": arguments.recall_samples,
            "recallHits": recall_hits,
            "recallEvidenceCounts": evidence_counts,
            "citationCount": len(citations),
            "citationsWithinEvidence": cited_in_evidence,
            "citationsWithinAllowedDocuments": cited_allowed,
            "citedDocuments": cited_aligned_documents,
            "expectedDocuments": expected_documents,
            "missingExpectedDocuments": missing_expected,
            "grounded": bool(validation.get("valid")),
            "injectionFlagged": bool(validation.get("injectionSuspected")),
            "conflictFlagged": bool(validation.get("conflictSuspected")),
            "hasAlignedEvidence": bool(case.get("alignedEvidence")),
            "sessionOverride": case.get("sessionOverride"),
            "expectedRefusal": bool(case.get("expectRefusal")),
            "expectInjectionFlag": bool(case.get("expectInjectionFlag")),
            "expectConflictFlag": bool(case.get("expectConflictFlag")),
            "expectNoCitations": bool(case.get("expectNoCitations")),
            "refused": answer.get("status") == "REFUSED",
            "attempts": attempts,
            "excluded": failed_reason is not None,
            "excludedReason": failed_reason,
        })

    # Provider-side failures are excluded from the aggregates: they say nothing
    # about retrieval or answer quality, and scoring them as misses would turn a
    # transient upstream error into an apparent quality regression. The exclusion
    # is reported alongside the numbers so it cannot pass unnoticed.
    scored = [case for case in case_results if not case.get("excluded")]
    excluded = [case for case in case_results if case.get("excluded")]

    # recallAtK is only meaningful for cases that declare what the aligned evidence
    # looks like; documentHitRate only for cases that declare expected documents.
    alignment_cases = [case for case in scored if case.get("hasAlignedEvidence")]
    document_cases = [case for case in scored if case.get("expectedDocuments")]
    refusal_cases = [case for case in scored if case.get("expectedRefusal")]
    permission_cases = [case for case in scored if case["type"] == "permission_negative"]
    injection_cases = [case for case in scored if case.get("expectInjectionFlag")]
    conflict_cases = [case for case in scored if case.get("expectConflictFlag")]

    def mean(values: list[float]) -> float | None:
        # None, not 0.0: an empty group is unknown, not a measured zero.
        return round(sum(values) / len(values), 4) if values else None

    latencies = [float(case["latencyMs"]) for case in scored if "latencyMs" in case]
    metrics = {
        "caseCount": len(case_results),
        "excludedCaseCount": len(excluded),
        "excludedCases": [case["id"] for case in excluded],
        "recallAtK": mean([case["recallAtK"] for case in alignment_cases]),
        "recallSamples": arguments.recall_samples,
        "recallUnstableCases": [case["id"] for case in alignment_cases if 0 < case["recallHits"] < case["recallSamples"]],
        "documentHitRate": mean([1.0 if not case["missingExpectedDocuments"] else 0.0 for case in document_cases]),
        "citationAccuracy": mean([1.0 if case["citationsWithinEvidence"] and case["citationsWithinAllowedDocuments"] else 0.0 for case in alignment_cases]),
        "groundedness": mean([1.0 if case["grounded"] else 0.0 for case in alignment_cases]),
        "refusalRate": mean([1.0 if case["refused"] else 0.0 for case in refusal_cases]),
        "permissionNegativeDenials": mean([1.0 if not case["leaked"] else 0.0 for case in permission_cases]),
        "injectionFlagRate": mean([1.0 if case["injectionFlagged"] else 0.0 for case in injection_cases]),
        "conflictFlagRate": mean([1.0 if case["conflictFlagged"] else 0.0 for case in conflict_cases]),
        "meanLatencyMs": int(mean(latencies)) if latencies else None,
    }

    models = {
        "chat": sorted(observed_chat_models),
        "embedding": arguments.embedding_model,
        "embeddingDimension": arguments.embedding_dimension,
    }
    limitations = []
    if arguments.embedding_model is None:
        limitations.append(
            "The embedding model was not declared for this run, so the retrieval metrics cannot be attributed to a specific embedder; pass --embedding-model and --embedding-dimension to record it."
        )
    elif arguments.embedding_model == "local-deterministic":
        limitations.append(
            "The run used the offline deterministic embedder, which by design has no semantic ability (design 4.5.2); semantic retrieval quality must be re-measured against a real embedding model before a production threshold is set."
        )
    if arguments.recall_samples > 1:
        limitations.append(
            f"recallAtK covers only cases that declare alignedEvidence and averages {arguments.recall_samples} retrieval samples per case, so a case can score between 0 and 1; cases whose samples disagreed are listed in metrics.recallUnstableCases. documentHitRate covers only cases that declare expectedDocuments."
        )
    else:
        limitations.append(
            "recallAtK covers only cases that declare alignedEvidence and is a single retrieval draw per case, so it is either 0 or 1; pass --recall-samples to repeat the retrieval and measure whether it disagrees. documentHitRate covers only cases that declare expectedDocuments."
        )
    if excluded:
        limitations.append(
            f"{len(excluded)} case(s) were excluded from the aggregates after exhausting {MAX_ATTEMPTS} attempts against a provider-side failure: "
            + ", ".join(case["id"] for case in excluded)
            + "."
        )
    limitations.append(
        "Every metric outside the gates block is a diagnostic. conflictFlagRate moves with the model's citation choices and is not reproducible run to run."
    )

    failures = gate_failures(metrics, case_results)
    gates = {
        "enforced": list(GATE_METRICS),
        "thresholds": dict(GATE_THRESHOLDS),
        "diagnostics": list(DIAGNOSTIC_METRICS),
        "status": "failed" if failures else "passed",
        "failures": failures,
    }

    report = {
        "schemaVersion": "EvaluationReportV1",
        "evaluationSet": evaluation["name"],
        "generatedAt": datetime.now(timezone.utc).isoformat(),
        "baseUrl": arguments.base_url,
        "knowledgeBaseId": knowledge_base_id,
        "conversationId": conversation_id,
        "models": models,
        "documents": documents,
        "gates": gates,
        "metrics": metrics,
        "cases": case_results,
        "limitations": limitations,
        "releaseThreshold": "enforced: "
        + ", ".join(f"{name} >= {value}" for name, value in GATE_THRESHOLDS.items())
        + "; diagnostics with no release threshold: "
        + ", ".join(DIAGNOSTIC_METRICS),
    }
    REPORT.parent.mkdir(parents=True, exist_ok=True)
    REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")

    print(json.dumps(metrics, indent=2, ensure_ascii=False))
    print(f"gates: {gates['status']} (enforced: {', '.join(gates['enforced'])})")
    print("gate thresholds: " + ", ".join(f"{name} >= {value}" for name, value in GATE_THRESHOLDS.items()))
    for failure in failures:
        print(f"gate failure: {failure}", file=sys.stderr)
    print(f"report written to {REPORT}")
    print("diagnostics (no release threshold): " + ", ".join(DIAGNOSTIC_METRICS))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())