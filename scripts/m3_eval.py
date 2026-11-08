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

The set is validated before any upload: every case needs a unique id, a type
and a question; expectedDocuments and alignedDocuments must name real fixture
roles; permission cases must use known probes and an actor; sessionOverride
must be a JSON object; and every enforced gate must have at least one case that
can measure it.

Usage:
    python3 scripts/m3_eval.py [--base-url http://localhost:8082] \
        [--auth dev|api-key] [--api-key KEY] [--actor-key USER_ID=KEY] \
        [--embedding-model MODEL] [--embedding-dimension N] [--recall-samples N] \
        [--report-dir DIR] [--redact-base-url] [--keep]
    python3 scripts/m3_eval.py --validate-only

Dev auth lets the harness act as any seeded identity, so the permission probes
run with per-actor development headers. A deployment with dev auth disabled
needs --auth api-key and one --actor-key per probe identity (viewer-a,
editor-a and any cross-tenant actor): an API key cannot impersonate anybody.
A missing or mismatched actor key marks those probes unavailable and fails the
permission gate; it is never scored as a successful denial.

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
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

from evalkit import (
    AuthError,
    Client,
    Credentials,
    DatasetError,
    DEV_TENANT_ID,
    DEV_USER_ID,
    PERMISSION_PROBES,
    add_auth_arguments,
    base_url_for_report,
    load_set,
    mean,
    parse_actor_keys,
    resolve_report_path,
    run_permission_probes,
    validate_common,
    wait_pipeline_status,
    write_report,
)

SCHEMA_SET = "EvaluationSetV1"
EVAL_MEMBERS = (("viewer-a", "viewer"), ("editor-a", "editor"))
EVAL_SET = Path(__file__).resolve().parent.parent / "tests" / "eval" / "m3_eval_set.json"
REPORT = Path(__file__).resolve().parent.parent / "tests" / "eval" / "m3_eval_report.json"

# A hosted model can fail for reasons that have nothing to do with retrieval or
# answer quality. These reasons are retried, and if they persist the case is
# excluded from the aggregates rather than scored as a miss: a transient 502 must
# not read as a quality regression in the report.
TRANSIENT_REASONS = {"model_error", "timeout"}
MAX_ATTEMPTS = 3


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


def aligned(case: dict, item: dict) -> bool:
    markers = case.get("alignedEvidence")
    if not markers:
        return False
    content = item.get("content", "")
    return any(marker in content for marker in markers)


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
                    reason = probe.get("reason") or "unknown reason"
                    failures.append(f"{case['id']}: probe {probe['probe']} could not be evaluated ({reason})")
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


def validate_set(evaluation: dict) -> list[str]:
    errors = validate_common(evaluation, schema_version=SCHEMA_SET)
    fixtures = evaluation.get("fixtures") if isinstance(evaluation.get("fixtures"), list) else []
    cases = evaluation.get("cases") if isinstance(evaluation.get("cases"), list) else []
    roles = {
        fixture["role"]
        for fixture in fixtures
        if isinstance(fixture, dict)
        and isinstance(fixture.get("role"), str)
        and fixture["role"].strip()
    }
    role_counts: dict[str, int] = {}
    for fixture in fixtures:
        if not isinstance(fixture, dict):
            continue
        role = fixture.get("role")
        if isinstance(role, str) and role.strip():
            role_counts[role] = role_counts.get(role, 0) + 1
    for role, count in sorted(role_counts.items()):
        if count > 1:
            errors.append(f"duplicate fixture role {role!r}: uploaded documents are keyed by role")

    permission_cases = [
        case for case in cases
        if isinstance(case, dict) and case.get("type") == "permission_negative"
    ]
    if permission_cases and "handbook" not in roles:
        errors.append('permission_negative probes read the fixture with role "handbook"; the set needs one')

    earlier_answerable = False
    for case in cases:
        if not isinstance(case, dict):
            continue
        case_id = case.get("id") or "case"
        expected_documents = case.get("expectedDocuments")
        if expected_documents is not None:
            if not isinstance(expected_documents, list) or not all(
                isinstance(role, str) and role.strip() for role in expected_documents
            ):
                errors.append(f"{case_id}: expectedDocuments must be a list of document roles")
            else:
                unknown = sorted(set(expected_documents) - roles)
                if unknown:
                    errors.append(f"{case_id}: expectedDocuments reference unknown roles {unknown}")
        aligned_documents = case.get("alignedDocuments")
        if aligned_documents is not None:
            if not isinstance(aligned_documents, list) or not all(
                isinstance(role, str) and role.strip() for role in aligned_documents
            ):
                errors.append(f"{case_id}: alignedDocuments must be a list of document roles")
            else:
                unknown = sorted(set(aligned_documents) - roles)
                if unknown:
                    errors.append(f"{case_id}: alignedDocuments reference unknown roles {unknown}")
                if isinstance(expected_documents, list) and set(expected_documents) - set(aligned_documents):
                    errors.append(f"{case_id}: expectedDocuments must be a subset of alignedDocuments")
        aligned_evidence = case.get("alignedEvidence")
        if aligned_evidence is not None and (
            not isinstance(aligned_evidence, list)
            or not all(isinstance(marker, str) and marker for marker in aligned_evidence)
        ):
            errors.append(f"{case_id}: alignedEvidence must be a list of non-empty strings")
        session_override = case.get("sessionOverride")
        if session_override is not None:
            if not isinstance(session_override, str):
                errors.append(f"{case_id}: sessionOverride must be a JSON string")
            else:
                try:
                    parsed_override = json.loads(session_override)
                except json.JSONDecodeError as error:
                    errors.append(f"{case_id}: sessionOverride is not valid JSON: {error.msg}")
                else:
                    if not isinstance(parsed_override, dict):
                        errors.append(f"{case_id}: sessionOverride must encode a JSON object")
        for flag in ("expectInjectionFlag", "expectConflictFlag", "expectNoCitations"):
            if flag in case and not isinstance(case[flag], bool):
                errors.append(f"{case_id}: {flag} must be a boolean")

        if case.get("type") != "permission_negative":
            if not isinstance(case.get("expectRefusal"), bool):
                errors.append(f"{case_id}: expectRefusal must be a boolean")
            earlier_answerable = True
            continue

        actor = case.get("actor")
        if not isinstance(actor, dict):
            errors.append(f"{case_id}: permission case needs an actor object")
        else:
            for field in ("userId", "tenantId"):
                if not isinstance(actor.get(field), str) or not actor[field].strip():
                    errors.append(f"{case_id}: actor.{field} must be a non-empty string")
            actor_user = actor.get("userId")
            actor_tenant = actor.get("tenantId")
            member_ids = {member_id for member_id, _ in EVAL_MEMBERS}
            if actor_tenant == DEV_TENANT_ID and actor_user == DEV_USER_ID:
                errors.append(f"{case_id}: permission case actor must not be the knowledge base owner")
            elif actor_tenant == DEV_TENANT_ID and actor_user not in member_ids:
                errors.append(
                    f"{case_id}: in-tenant actor {actor_user!r} has no membership created by the harness; "
                    "the probe would be denied for the wrong reason"
                )
        probes = case.get("probes")
        if not isinstance(probes, list) or not probes:
            errors.append(f"{case_id}: permission case needs at least one probe")
            continue
        if not all(isinstance(probe, str) for probe in probes):
            errors.append(f"{case_id}: probes must be strings")
            continue
        unknown_probes = sorted(set(probes) - set(PERMISSION_PROBES))
        if unknown_probes:
            errors.append(f"{case_id}: unknown probes {unknown_probes}")
        duplicate_probes = sorted({probe for probe in probes if probes.count(probe) > 1})
        if duplicate_probes:
            errors.append(f"{case_id}: duplicate probes {duplicate_probes}")
        # chunk/events need a target id produced by an earlier answer; without
        # one they are recorded as unavailable, which fails the gate.
        if ("chunk" in probes or "events" in probes) and not earlier_answerable:
            errors.append(f"{case_id}: chunk/events probes require an earlier answerable case")

    if not permission_cases:
        errors.append("permissionNegativeDenials would be unmeasurable: no permission_negative case")
    if not any(isinstance(case, dict) and case.get("expectRefusal") for case in cases):
        errors.append("refusalRate would be unmeasurable: no case sets expectRefusal")
    if not any(isinstance(case, dict) and case.get("alignedEvidence") for case in cases):
        errors.append("recallAtK and citationAccuracy would be unmeasurable: no case declares alignedEvidence")
    if not any(isinstance(case, dict) and case.get("expectedDocuments") for case in cases):
        errors.append("documentHitRate would be unmeasurable: no case declares expectedDocuments")
    if not any(isinstance(case, dict) and case.get("expectInjectionFlag") for case in cases):
        errors.append("injectionFlagRate would be unmeasurable: no case sets expectInjectionFlag")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    default_base = f"http://localhost:{os.environ.get('API_PORT', '8082')}"
    parser.add_argument("--base-url", default=default_base)
    parser.add_argument("--embedding-model", default=None, help="embedding model name to record in the report")
    parser.add_argument("--embedding-dimension", type=int, default=None, help="embedding vector dimension to record in the report")
    parser.add_argument("--recall-samples", type=int, default=1, help="how many times to repeat each retrieval call for recallAtK (default 1, no model calls)")
    parser.add_argument("--validate-only", action="store_true", help="validate the evaluation set without contacting the stack")
    parser.add_argument("--report-dir", default=None, help="directory for the report (default: tests/eval)")
    parser.add_argument("--redact-base-url", action="store_true", help="write 'redacted' instead of the stack URL into the report")
    parser.add_argument("--keep", action="store_true", help="keep the evaluation knowledge base for inspection")
    add_auth_arguments(parser)
    arguments = parser.parse_args()

    if arguments.recall_samples < 1:
        print("--recall-samples must be at least 1", file=sys.stderr)
        return 2
    if arguments.embedding_dimension is not None and arguments.embedding_dimension <= 0:
        print("--embedding-dimension must be greater than 0", file=sys.stderr)
        return 2
    if arguments.embedding_model is not None and not arguments.embedding_model.strip():
        print("--embedding-model must not be empty when provided", file=sys.stderr)
        return 2

    try:
        evaluation = load_set(EVAL_SET)
    except DatasetError as error:
        print(f"set validation: {error}", file=sys.stderr)
        return 2
    errors = validate_set(evaluation)
    if errors:
        for error in errors:
            print(f"set validation: {error}", file=sys.stderr)
        return 2
    print(
        f"set valid: {len(evaluation['fixtures'])} fixtures / {len(evaluation['cases'])} cases"
    )
    if arguments.validate_only:
        return 0

    try:
        credentials = Credentials(
            arguments.base_url,
            arguments.auth,
            arguments.api_key,
            parse_actor_keys(arguments.actor_key),
        )
    except AuthError as error:
        print(f"auth: {error}", file=sys.stderr)
        return 2
    client = credentials.owner_client()

    try:
        status, _ = client.json("GET", "/healthz")
    except RuntimeError as error:
        print(f"stack is not reachable at {arguments.base_url}: {error}", file=sys.stderr)
        return 2
    if status != 200:
        print(f"stack is not reachable at {arguments.base_url}", file=sys.stderr)
        return 2
    try:
        owner = credentials.verify_owner()
    except AuthError as error:
        print(f"auth: {error}", file=sys.stderr)
        return 2
    print(f"authenticated as {owner.subject_id}@{owner.tenant_id} via {arguments.auth}")

    suffix = datetime.now(timezone.utc).strftime("%Y%m%d%H%M%S")
    status, knowledge_base = client.json("POST", "/api/v1/knowledge-bases", {"name": f"m3-eval-{suffix}", "description": "M3 evaluation fixture"})
    if status not in (200, 201):
        print(f"could not create the evaluation knowledge base: {knowledge_base}", file=sys.stderr)
        return 2
    knowledge_base_id = knowledge_base["id"]

    try:
        documents: dict[str, dict] = {}
        for fixture in evaluation["fixtures"]:
            upload = client.upload(knowledge_base_id, fixture["filename"], fixture["content"])
            document_id = upload["document"]["id"]
            version_id = upload["version"]["id"]
            pipeline_status = wait_pipeline_status(client, document_id)
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
        for member_id, member_role in EVAL_MEMBERS:
            status, payload = client.json(
                "POST", f"/api/v1/knowledge-bases/{knowledge_base_id}/members", {"user_id": member_id, "role": member_role},
            )
            if status not in (200, 201):
                print(f"could not add the {member_role} evaluation member {member_id}: {status} {payload}", file=sys.stderr)
                return 2

        # Resolve and verify every probe identity before the case loop. A probe
        # denied because its key was missing or belonged to somebody else would
        # be a false positive, so those probes run as unavailable instead.
        probe_actors: dict[str, dict[str, str]] = {}
        for case in evaluation["cases"]:
            if case["type"] != "permission_negative":
                continue
            actor = credentials.resolve_actor(case["actor"])
            probe_actors[case["id"]] = actor
            credentials.actor_client(actor)
        for error in credentials.actor_errors():
            print(f"warning: probe credential unavailable: {error}", file=sys.stderr)

        case_results = []
        observed_chat_models: set[str] = set()
        owner_request_ids: list[str] = []
        owner_chunk_ids: list[str] = []
        for case in evaluation["cases"]:
            if case["type"] == "permission_negative":
                actor = probe_actors[case["id"]]
                probes = run_permission_probes(
                    actor,
                    knowledge_base_id,
                    conversation_id,
                    documents["handbook"]["documentId"],
                    owner_chunk_ids[-1] if owner_chunk_ids else None,
                    owner_request_ids[-1] if owner_request_ids else None,
                    case["probes"],
                    client_for_actor=credentials.actor_client,
                    actor_reason=credentials.actor_error(actor),
                )
                case_results.append({
                    "id": case["id"],
                    "type": case["type"],
                    # The resolved identity, not the dataset placeholder: in
                    # api-key mode an in-tenant actor runs in the owner tenant.
                    "actor": actor,
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
            "baseUrl": base_url_for_report(arguments.base_url, arguments.redact_base_url),
            "auth": {
                "mode": arguments.auth,
                "owner": {"subjectId": owner.subject_id, "tenantId": owner.tenant_id},
                "probeCredentials": credentials.actor_summary(),
            },
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
        report_path = write_report(
            report,
            resolve_report_path(arguments.report_dir, REPORT.parent, REPORT.name),
        )

        print(json.dumps(metrics, indent=2, ensure_ascii=False))
        print(f"gates: {gates['status']} (enforced: {', '.join(gates['enforced'])})")
        print("gate thresholds: " + ", ".join(f"{name} >= {value}" for name, value in GATE_THRESHOLDS.items()))
        for failure in failures:
            print(f"gate failure: {failure}", file=sys.stderr)
        print(f"report written to {report_path}")
        print("diagnostics (no release threshold): " + ", ".join(DIAGNOSTIC_METRICS))
        return 1 if failures else 0
    finally:
        if not arguments.keep:
            status, payload = client.json("DELETE", f"/api/v1/knowledge-bases/{knowledge_base_id}")
            if status not in (200, 204):
                print(f"warning: evaluation knowledge base cleanup failed: {status} {payload}", file=sys.stderr)


if __name__ == "__main__":
    sys.exit(main())