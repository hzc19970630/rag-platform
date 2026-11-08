#!/usr/bin/env python3
"""M7 evaluation: bounded graph queries versus pure Evidence RAG.

The harness validates the hand-authored corpus in tests/eval/m7_qa_set.json,
uploads it, enables graph_enabled, reviews the resulting snapshot, and then
asks every question through both channels:

  * pure RAG: POST /api/v1/search, scored strictly when every expected marker
    appears in a returned chunk from the expected document; the loose "marker
    anywhere" reading is kept as a diagnostic;
  * graph: GET /knowledge-bases/{id}/graph/query from the case seed at the
    declared depth, scored on expected nodes, forbidden nodes and shortest hop.

Both channels are scored against the same hand-authored answers, so the report
measures reachability rather than chat-model prose. The M7 checklist asks for
this comparison before deciding whether the bounded multi-hop surface earns its
complexity; the report records the evidence and a diagnostic recommendation.

The set is validated before any upload: unique ids, resolvable seeds, triple
BFS hop consistency, marker/document placement and negative-kind semantics.
Unknown-seed negatives require an empty graph with an active snapshot;
depth-bound negatives require a forbidden node to stay outside the queried
depth while the graph itself may be non-empty.

Usage:
    python3 scripts/m7_eval.py [--base-url http://localhost:8082] [--keep] \
        [--auth dev|api-key] [--api-key KEY] [--report-dir DIR] [--redact-base-url]
    python3 scripts/m7_eval.py --validate-only
    python3 scripts/m7_eval.py --rag-top-k 3          # diagnostic only

The owner credential is either a development identity (default) or an API key
for a deployment with dev auth disabled. M7 has no permission probes, so it
needs only the owner credential.

The script exits non-zero when the harness itself fails (an invalid set, a
document that never becomes EVIDENCE_READY, a failed relation run, a snapshot
that cannot be approved, or a query error). A low graph hit rate is a result,
not a harness failure.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import statistics
import sys
import time
import urllib.parse
from datetime import datetime, timezone
from pathlib import Path

from evalkit import (
    AuthError,
    Client,
    Credentials,
    DatasetError,
    add_auth_arguments,
    base_url_for_report,
    load_set,
    percentile,
    rate,
    resolve_report_path,
    validate_common,
    wait_document_ready,
    wait_for,
    write_report,
)

EVAL_SET = Path(__file__).resolve().parent.parent / "tests" / "eval" / "m7_qa_set.json"
REPORT = Path(__file__).resolve().parent.parent / "tests" / "eval" / "m7_eval_report.json"

SCHEMA_SET = "M7EvaluationSetV2"
SCHEMA_REPORT = "M7EvaluationReportV2"
GRAPH_LIMIT = 40
MAX_GRAPH_DEPTH = 3
MAX_SOURCE_LINE_RUNES = 900
MIN_MULTIHOP_CASES = 10
MIN_ANSWERABLE_CASES = 20

TRIPLE_PATTERN = re.compile(
    r"\[\[\s*([^\[\]|]+?)\s*\|\s*([^\[\]|]+?)\s*\|\s*([^\[\]|]+?)\s*(?:\|\s*([^\[\]]*?)\s*)?\]\]"
)


def wait_relation_ready(client: Client, knowledge_base_id: str) -> dict:
    def probe():
        status, payload = client.json("GET", f"/api/v1/knowledge-bases/{knowledge_base_id}/projection")
        if status != 200:
            raise RuntimeError(f"projection status failed: {status} {payload}")
        runs = payload.get("relationRuns") or []
        if not runs:
            return None
        run = runs[0]
        if run.get("status") == "RELATION_READY":
            return run
        if run.get("status") in ("FAILED_RELATION", "CANCELLED"):
            raise RuntimeError(f"relation run ended as {run.get('status')}: {run.get('errorMessage') or run.get('errorCode')}")
        return None

    return wait_for("relation projection RELATION_READY", 1200, probe)


def approve_latest_snapshot(client: Client, knowledge_base_id: str) -> dict:
    status, payload = client.json("GET", f"/api/v1/knowledge-bases/{knowledge_base_id}/graph/snapshots")
    if status != 200:
        raise RuntimeError(f"list snapshots failed: {status} {payload}")
    snapshots = payload.get("snapshots") or []
    if not snapshots:
        raise RuntimeError("relation run finished without a snapshot")
    snapshot = snapshots[0]
    if snapshot.get("status") == "PENDING_REVIEW":
        status, reviewed = client.json("POST", f"/api/v1/graph/snapshots/{snapshot['id']}/review", {"action": "approve", "note": "m7 evaluation"})
        if status != 200:
            raise RuntimeError(f"snapshot review failed: {status} {reviewed}")
        snapshot = reviewed
    if snapshot.get("status") != "ACTIVE":
        raise RuntimeError(f"snapshot is not queryable after review: {snapshot.get('status')}")
    return snapshot


def snapshot_detail(client: Client, snapshot_id: str) -> dict:
    status, payload = client.json("GET", f"/api/v1/graph/snapshots/{snapshot_id}")
    if status != 200:
        raise RuntimeError(f"snapshot detail failed: {status} {payload}")
    return payload


def normalize_entity_key(value: str) -> str:
    builder: list[str] = []
    space = False
    for char in value.strip().lower():
        if char.isalnum():
            builder.append(char)
            space = False
            continue
        if char.isspace() or char in "-_.":
            if builder and not space:
                builder.append(" ")
                space = True
    return "".join(builder).strip()


def parse_triples(fixtures: list[dict]) -> dict:
    graph = {
        "entities": {},
        "edges": [],
        "adjacency": {},
        "docs": {},
        "alias_index": {},
        "normalized_index": {},
    }
    for fixture in fixtures:
        if not isinstance(fixture, dict):
            continue
        filename = fixture.get("filename")
        content = fixture.get("content")
        if not isinstance(filename, str) or not isinstance(content, str):
            continue
        graph["docs"][filename] = content
        for match in TRIPLE_PATTERN.finditer(content):
            subject = match.group(1).strip()
            predicate = match.group(2).strip()
            obj = match.group(3).strip()
            if not subject or not predicate or not obj or subject.casefold() == "alias":
                continue
            aliases = [alias.strip() for alias in (match.group(4) or "").split(";") if alias.strip()]
            for name, is_subject in ((subject, True), (obj, False)):
                entry = graph["entities"].setdefault(
                    name,
                    {"name": name, "aliases": set(), "docs": set(), "subject_docs": set()},
                )
                entry["docs"].add(filename)
                if is_subject:
                    entry["subject_docs"].add(filename)
            graph["entities"][subject]["aliases"].update(aliases)
            graph["edges"].append({"subject": subject, "predicate": predicate, "object": obj, "file": filename})
            graph["adjacency"].setdefault(subject, set()).add(obj)
            graph["adjacency"].setdefault(obj, set()).add(subject)
    for name, entry in graph["entities"].items():
        graph["normalized_index"].setdefault(normalize_entity_key(name), name)
        for alias in entry["aliases"]:
            graph["alias_index"].setdefault(normalize_entity_key(alias), name)
    return graph


def resolve_seed(seed: str, graph: dict, kind: str) -> str | None:
    key = normalize_entity_key(seed)
    canonical = graph["normalized_index"].get(key)
    alias = graph["alias_index"].get(key)
    if kind == "canonical":
        return canonical if canonical and seed == canonical else None
    if kind == "alias":
        return alias if alias and alias != canonical else None
    if kind == "normalized":
        return canonical if canonical and seed != canonical else None
    return None


def bfs_distances(adjacency: dict, seed: str) -> dict:
    distances = {seed: 0}
    queue = [seed]
    for node in queue:
        for neighbour in adjacency.get(node, ()):
            if neighbour not in distances:
                distances[neighbour] = distances[node] + 1
                queue.append(neighbour)
    return distances


def shortest_path_count(adjacency: dict, seed: str, target: str) -> int:
    distances = bfs_distances(adjacency, seed)
    if target not in distances:
        return 0
    counts = {seed: 1}
    for node in sorted(distances, key=lambda name: distances[name]):
        for neighbour in adjacency.get(node, ()):
            if distances.get(neighbour) == distances[node] + 1:
                counts[neighbour] = counts.get(neighbour, 0) + counts.get(node, 0)
    return counts.get(target, 0)


def validate_set(evaluation: dict) -> list[str]:
    errors = validate_common(evaluation, schema_version=SCHEMA_SET)
    fixtures = evaluation.get("fixtures") or []
    cases = evaluation.get("cases") or []

    filenames: set[str] = set()
    role_counts: dict[str, int] = {}
    total_lines = 0
    all_content: list[str] = []
    for fixture in fixtures:
        if not isinstance(fixture, dict):
            continue
        filename = fixture.get("filename") or ""
        content = fixture.get("content")
        if filename:
            filenames.add(filename)
        role = fixture.get("role") or ""
        role_counts[role] = role_counts.get(role, 0) + 1
        if not isinstance(content, str):
            continue
        all_content.append(content)
        for line in content.splitlines():
            if not line.strip():
                continue
            total_lines += 1
            if len(line) > MAX_SOURCE_LINE_RUNES:
                errors.append(f"{filename}: a line exceeds {MAX_SOURCE_LINE_RUNES} runes and would split into extra chunks")
    corpus_text = "\n".join(all_content).casefold()

    corpus = evaluation.get("corpus") or {}
    if not isinstance(corpus, dict):
        errors.append("corpus must be an object")
        corpus = {}
    chain_count = sum(role_counts.get(role, 0) for role in ("chain-a", "chain-b", "chain-c"))
    expected_counts = {
        "fixtures": len(fixtures),
        "chains": chain_count,
        "distractors": role_counts.get("distractor", 0),
        "fillers": role_counts.get("filler", 0),
        "expectedChunks": total_lines,
    }
    for key, actual in expected_counts.items():
        if corpus.get(key) != actual:
            errors.append(f"corpus.{key} is {corpus.get(key)!r}, expected {actual}")

    graph = parse_triples(fixtures)

    normalized_owner: dict[str, str] = {}
    for name in graph["entities"]:
        key = normalize_entity_key(name)
        if not key:
            errors.append(f"entity {name!r} normalizes to an empty key")
            continue
        owner = normalized_owner.get(key)
        if owner and owner != name:
            errors.append(f"entities {owner!r} and {name!r} share normalized key {key!r}")
        normalized_owner[key] = name
    for name, entry in graph["entities"].items():
        for alias in entry["aliases"]:
            key = normalize_entity_key(alias)
            owner = normalized_owner.get(key)
            if owner and owner != name:
                errors.append(f"alias {alias!r} of {name!r} collides with entity {owner!r}")
            normalized_owner.setdefault(key, name)

    for case in cases:
        if not isinstance(case, dict):
            continue
        case_id = case.get("id") or ""
        if not case_id:
            continue

        question = case.get("question") if isinstance(case.get("question"), str) else ""
        seed = case.get("seed")
        if not isinstance(seed, str) or not seed.strip():
            errors.append(f"{case_id}: seed must be a non-empty string")
            seed = ""
        kind = case.get("seedKind") or "canonical"
        if not isinstance(kind, str):
            errors.append(f"{case_id}: seedKind must be a string")
            kind = "canonical"
        depth = case.get("depth")
        case_type = case.get("type") if isinstance(case.get("type"), str) else ""
        expected_nodes = case.get("expectedNodes") or []
        if not isinstance(expected_nodes, list) or not all(isinstance(name, str) for name in expected_nodes):
            errors.append(f"{case_id}: expectedNodes must be a list of strings")
            expected_nodes = []
        forbidden_nodes = case.get("forbiddenNodes") or []
        if not isinstance(forbidden_nodes, list) or not all(isinstance(name, str) for name in forbidden_nodes):
            errors.append(f"{case_id}: forbiddenNodes must be a list of strings")
            forbidden_nodes = []
        expected_document = case.get("expectedDocument") or ""
        if not isinstance(expected_document, str):
            errors.append(f"{case_id}: expectedDocument must be a string")
            expected_document = ""
        aligned_evidence = case.get("alignedEvidence") or []
        if not isinstance(aligned_evidence, list) or not all(
            isinstance(marker, str) for marker in aligned_evidence
        ):
            errors.append(f"{case_id}: alignedEvidence must be a list of strings")
            aligned_evidence = []
        forbidden_evidence = case.get("forbiddenEvidence") or []
        if not isinstance(forbidden_evidence, list) or not all(
            isinstance(phrase, str) for phrase in forbidden_evidence
        ):
            errors.append(f"{case_id}: forbiddenEvidence must be a list of strings")
            forbidden_evidence = []

        if case_type not in ("single-hop", "multi-hop", "branch", "negative"):
            errors.append(f"{case_id}: unknown type {case_type!r}")
        if kind not in ("canonical", "alias", "normalized", "unknown"):
            errors.append(f"{case_id}: unknown seedKind {kind!r}")
        if not isinstance(depth, int) or not 1 <= depth <= MAX_GRAPH_DEPTH:
            errors.append(f"{case_id}: depth must be an integer between 1 and {MAX_GRAPH_DEPTH}")
            depth = 1
        if seed and seed.casefold() not in question.casefold():
            errors.append(f"{case_id}: question does not contain the seed {seed!r}")

        canonical = resolve_seed(seed, graph, kind)
        if kind == "unknown":
            if canonical is not None or normalize_entity_key(seed) in graph["alias_index"]:
                errors.append(f"{case_id}: unknown seed {seed!r} resolves to an entity")
            if seed.casefold() in corpus_text:
                errors.append(f"{case_id}: unknown seed {seed!r} appears in the corpus")
        elif not canonical:
            errors.append(f"{case_id}: seed {seed!r} does not resolve as {kind}")
            continue

        for name in expected_nodes + forbidden_nodes:
            if name not in graph["entities"]:
                errors.append(f"{case_id}: node {name!r} is not declared by any triple")
        for name in expected_nodes + forbidden_nodes:
            if name and name.casefold() in question.casefold():
                errors.append(f"{case_id}: question contains the answer or forbidden node {name!r}")
        if expected_document and expected_document not in filenames:
            errors.append(f"{case_id}: expectedDocument {expected_document!r} is not a fixture")
        if expected_document and not aligned_evidence:
            errors.append(f"{case_id}: expectedDocument is set but alignedEvidence is empty")

        if case_type == "negative":
            if not case.get("expectNoAnswer"):
                errors.append(f"{case_id}: negative case must set expectNoAnswer")
            if expected_nodes:
                errors.append(f"{case_id}: negative case must not declare expectedNodes")
            negative_kind = case.get("negativeKind")
            if negative_kind == "unknown-seed":
                if not case.get("expectEmptyGraph"):
                    errors.append(f"{case_id}: unknown-seed negative must set expectEmptyGraph")
                if kind != "unknown":
                    errors.append(f"{case_id}: unknown-seed negative must use seedKind unknown")
            elif negative_kind == "depth-bound":
                if case.get("expectEmptyGraph"):
                    errors.append(f"{case_id}: depth-bound negative must not set expectEmptyGraph")
                if not forbidden_nodes:
                    errors.append(f"{case_id}: depth-bound negative needs forbiddenNodes")
                distances = bfs_distances(graph["adjacency"], canonical)
                for name in forbidden_nodes:
                    hops = distances.get(name)
                    if hops is None:
                        errors.append(f"{case_id}: forbidden node {name!r} is unreachable from the seed")
                    elif hops <= depth:
                        errors.append(f"{case_id}: forbidden node {name!r} is reachable within depth {depth}")
                    elif hops > MAX_GRAPH_DEPTH:
                        errors.append(f"{case_id}: forbidden node {name!r} is beyond the maximum depth")
            else:
                errors.append(f"{case_id}: negativeKind must be unknown-seed or depth-bound")
            continue

        if case.get("expectNoAnswer"):
            errors.append(f"{case_id}: non-negative case must not set expectNoAnswer")
        if not expected_nodes:
            errors.append(f"{case_id}: answerable case needs expectedNodes")
            continue
        if not expected_document:
            errors.append(f"{case_id}: answerable case needs expectedDocument")
            continue
        distances = bfs_distances(graph["adjacency"], canonical)
        for name in expected_nodes:
            if name not in distances:
                errors.append(f"{case_id}: expected node {name!r} is unreachable from the seed")
            elif distances[name] > depth:
                errors.append(f"{case_id}: expected node {name!r} needs {distances[name]} hops but depth is {depth}")
        expected_hops = case.get("expectedHops")
        if expected_hops is not None and (not isinstance(expected_hops, int) or expected_hops < 1):
            errors.append(f"{case_id}: expectedHops must be a positive integer")
            expected_hops = None
        if expected_hops is not None:
            if len(expected_nodes) != 1:
                errors.append(f"{case_id}: expectedHops requires exactly one expected node")
            else:
                target = expected_nodes[0]
                actual = distances.get(target)
                if actual != expected_hops:
                    errors.append(f"{case_id}: expectedHops is {expected_hops}, triple graph says {actual}")
                if actual is not None and shortest_path_count(graph["adjacency"], canonical, target) != 1:
                    errors.append(f"{case_id}: shortest path to {target!r} is ambiguous")
        document_content = graph["docs"].get(expected_document)
        if document_content is None:
            errors.append(f"{case_id}: expectedDocument {expected_document!r} has no fixture content")
            continue
        for marker in aligned_evidence:
            if marker not in document_content:
                errors.append(f"{case_id}: marker {marker!r} does not appear in {expected_document}")
        hops = min(distances.get(name, MAX_GRAPH_DEPTH + 1) for name in expected_nodes)
        if hops >= 2:
            if canonical.casefold() in document_content.casefold():
                errors.append(f"{case_id}: seed {canonical!r} leaks into {expected_document}")
            for seed_document in graph["entities"][canonical]["subject_docs"]:
                content = graph["docs"][seed_document]
                for marker in aligned_evidence:
                    if marker in content:
                        errors.append(f"{case_id}: marker {marker!r} already appears in seed document {seed_document}")
        for phrase in forbidden_evidence:
            if phrase in document_content:
                errors.append(f"{case_id}: forbiddenEvidence {phrase!r} appears in {expected_document}")
            if not any(phrase in content for content in graph["docs"].values()):
                errors.append(f"{case_id}: forbiddenEvidence {phrase!r} does not appear anywhere in the corpus")

    return errors


def search(client: Client, knowledge_base_id: str, question: str, top_k: int = 0) -> tuple[dict, float]:
    body: dict = {"query": question, "knowledgeBaseId": knowledge_base_id}
    if top_k:
        body["sessionOverride"] = json.dumps({"top_k": top_k})
    started = time.perf_counter()
    status, payload = client.json("POST", "/api/v1/search", body)
    elapsed = (time.perf_counter() - started) * 1000
    if status != 200:
        raise RuntimeError(f"search failed for {question!r}: {status} {payload}")
    return payload, elapsed


def graph_query(client: Client, knowledge_base_id: str, seed: str, depth: int, limit: int = GRAPH_LIMIT) -> tuple[dict, float]:
    query = urllib.parse.urlencode({"q": seed, "depth": depth, "limit": limit})
    started = time.perf_counter()
    status, payload = client.json("GET", f"/api/v1/knowledge-bases/{knowledge_base_id}/graph/query?{query}")
    elapsed = (time.perf_counter() - started) * 1000
    if status != 200:
        raise RuntimeError(f"graph query failed for seed {seed!r}: {status} {payload}")
    return payload, elapsed


def shortest_hops(payload: dict, expected_node: str) -> int | None:
    target = expected_node.casefold()
    depths = [node.get("depth") for node in payload.get("nodes") or [] if (node.get("name") or "").casefold() == target]
    return min(depths) if depths else None


def evaluate_case(client: Client, knowledge_base_id: str, case: dict, document_ids: dict[str, str], rag_top_k: int, observed_params: dict) -> dict:
    question = case["question"]
    expected_document = case.get("expectedDocument") or ""
    expected_document_id = document_ids.get(expected_document, "")
    markers = case.get("alignedEvidence") or []
    forbidden_phrases = [phrase.casefold() for phrase in case.get("forbiddenEvidence") or []]

    rag_payload, rag_ms = search(client, knowledge_base_id, question, rag_top_k)
    if not observed_params.get("plan"):
        observed_params["plan"] = (rag_payload.get("plan") or {}).get("params") or {}
    items = rag_payload.get("items") or []

    marker_ranks: list[int | None] = []
    for marker in markers:
        rank = None
        for index, item in enumerate(items, start=1):
            if expected_document_id and item.get("documentId") == expected_document_id and marker in (item.get("content") or ""):
                rank = index
                break
        marker_ranks.append(rank)

    forbidden_evidence_hit = any(
        phrase in (item.get("content") or "").casefold() for item in items for phrase in forbidden_phrases
    )
    rag_document_hit = bool(expected_document_id) and any(item.get("documentId") == expected_document_id for item in items)
    rag_loose_hit = any(marker in (item.get("content") or "") for item in items for marker in markers)
    rag_strict_hit = (
        bool(markers)
        and all(rank is not None for rank in marker_ranks)
        and not forbidden_evidence_hit
    )
    rag_answer_rank = max((rank for rank in marker_ranks if rank is not None), default=None) if rag_strict_hit else None

    graph_payload, graph_ms = graph_query(client, knowledge_base_id, case["seed"], case["depth"])
    nodes = graph_payload.get("nodes") or []
    edges = graph_payload.get("edges") or []
    paths = graph_payload.get("paths") or []
    node_names = {(node.get("name") or "").casefold() for node in nodes}
    snapshot_id = graph_payload.get("snapshotId") or ""
    expected_nodes = case.get("expectedNodes") or []
    forbidden_nodes = case.get("forbiddenNodes") or []
    expected_present = {name: name.casefold() in node_names for name in expected_nodes}
    forbidden_hit = any(name.casefold() in node_names for name in forbidden_nodes)
    hops = shortest_hops(graph_payload, expected_nodes[0]) if len(expected_nodes) == 1 else None
    expected_hops = case.get("expectedHops")

    if case.get("expectEmptyGraph"):
        graph_hit = not nodes and not edges and not paths and bool(snapshot_id)
    elif case.get("negativeKind") == "depth-bound":
        graph_hit = not forbidden_hit and bool(snapshot_id) and bool(nodes)
    else:
        graph_hit = (
            bool(snapshot_id)
            and all(expected_present.values())
            and not forbidden_hit
            and (expected_hops is None or hops == expected_hops)
        )

    return {
        "id": case["id"],
        "type": case["type"],
        "tags": case.get("tags") or [],
        "question": question,
        "seed": case["seed"],
        "seedKind": case.get("seedKind") or "canonical",
        "depth": case["depth"],
        "negativeKind": case.get("negativeKind") or "",
        "expectEmptyGraph": bool(case.get("expectEmptyGraph")),
        "expectedNodes": expected_nodes,
        "expectedHops": expected_hops,
        "expectedDocument": expected_document,
        "forbiddenNodes": forbidden_nodes,
        "forbiddenEvidence": case.get("forbiddenEvidence") or [],
        "ragStrictHit": rag_strict_hit,
        "ragLooseHit": rag_loose_hit,
        "ragDocumentHit": rag_document_hit,
        "ragAnswerRank": rag_answer_rank,
        "ragForbiddenEvidenceHit": forbidden_evidence_hit,
        "ragReturnedDocuments": sorted({item.get("documentId", "") for item in items if item.get("documentId")}),
        "ragLatencyMs": round(rag_ms, 1),
        "graphHit": graph_hit,
        "graphSeedResolved": bool(snapshot_id) and bool(nodes),
        "graphSnapshotId": snapshot_id,
        "graphNodeCount": len(nodes),
        "graphEdgeCount": len(edges),
        "graphPathCount": len(paths),
        "graphExpectedPresent": expected_present,
        "graphForbiddenHit": forbidden_hit,
        "graphHops": hops,
        "graphJoined": graph_payload.get("joined", False),
        "graphTruncated": graph_payload.get("truncated", False),
        "graphLatencyMs": round(graph_ms, 1),
    }


def summarize(cases: list[dict], set_stats: dict) -> dict:
    answerable = [case for case in cases if case["type"] != "negative"]
    multi_hop = [case for case in answerable if case["type"] == "multi-hop"]
    negatives = [case for case in cases if case["type"] == "negative"]
    hop_checked = [case for case in multi_hop if case.get("expectedHops") is not None]
    distractor_cases = [case for case in answerable if case.get("forbiddenEvidence")]

    negative_by_kind: dict[str, dict] = {}
    for kind in sorted({case.get("negativeKind") or "unknown" for case in negatives}):
        group = [case for case in negatives if (case.get("negativeKind") or "unknown") == kind]
        passed = sum(1 for case in group if case["graphHit"])
        negative_by_kind[kind] = {"count": len(group), "passCount": passed, "passRate": rate(passed, len(group))}

    per_type: dict[str, dict] = {}
    for case_type in sorted({case["type"] for case in cases}):
        group = [case for case in cases if case["type"] == case_type]
        if case_type == "negative":
            passed = sum(1 for case in group if case["graphHit"])
            per_type[case_type] = {"count": len(group), "graphPassRate": rate(passed, len(group))}
        else:
            hop_checked_group = [case for case in group if case.get("expectedHops") is not None]
            per_type[case_type] = {
                "count": len(group),
                "ragStrictHitRate": rate(sum(1 for case in group if case["ragStrictHit"]), len(group)),
                "ragLooseHitRate": rate(sum(1 for case in group if case["ragLooseHit"]), len(group)),
                "graphHitRate": rate(sum(1 for case in group if case["graphHit"]), len(group)),
                "graphHopExactRate": (
                    rate(sum(1 for case in hop_checked_group if case["graphHops"] == case["expectedHops"]), len(hop_checked_group))
                    if hop_checked_group
                    else None
                ),
            }

    per_tag: dict[str, dict] = {}
    for tag in sorted({tag for case in cases for tag in case.get("tags") or []}):
        group = [case for case in cases if tag in (case.get("tags") or [])]
        per_tag[tag] = {
            "count": len(group),
            "ragStrictHitRate": rate(sum(1 for case in group if case["ragStrictHit"]), len(group)),
            "graphHitRate": rate(sum(1 for case in group if case["graphHit"]), len(group)),
        }

    ranks = [case["ragAnswerRank"] for case in answerable if case["ragAnswerRank"] is not None]
    return {
        "caseCount": len(cases),
        "answerableCaseCount": len(answerable),
        "multiHopCaseCount": len(multi_hop),
        "negativeCaseCount": len(negatives),
        "ragStrictHitRate": rate(sum(1 for case in answerable if case["ragStrictHit"]), len(answerable)),
        "ragLooseHitRate": rate(sum(1 for case in answerable if case["ragLooseHit"]), len(answerable)),
        "ragDocumentHitRate": rate(sum(1 for case in answerable if case["ragDocumentHit"]), len(answerable)),
        "ragAnswerRankMedian": round(statistics.median(ranks), 1) if ranks else None,
        "ragAnswerRankP95": percentile(ranks, 0.95),
        "multiHopRagStrictHitRate": rate(sum(1 for case in multi_hop if case["ragStrictHit"]), len(multi_hop)),
        "multiHopRagLooseHitRate": rate(sum(1 for case in multi_hop if case["ragLooseHit"]), len(multi_hop)),
        "multiHopGraphHitRate": rate(sum(1 for case in multi_hop if case["graphHit"]), len(multi_hop)),
        "multiHopHopExactRate": rate(
            sum(1 for case in hop_checked if case["graphHops"] == case["expectedHops"]), len(hop_checked)
        ),
        "multiHopHopCheckedCount": len(hop_checked),
        "graphSeedResolveRate": rate(sum(1 for case in answerable if case["graphSeedResolved"]), len(answerable)),
        "graphRescueCount": sum(1 for case in multi_hop if case["graphHit"] and not case["ragStrictHit"]),
        "graphRescueIds": [case["id"] for case in multi_hop if case["graphHit"] and not case["ragStrictHit"]],
        "ragOnlyCount": sum(1 for case in multi_hop if case["ragStrictHit"] and not case["graphHit"]),
        "ragOnlyIds": [case["id"] for case in multi_hop if case["ragStrictHit"] and not case["graphHit"]],
        "bothHitCount": sum(1 for case in multi_hop if case["ragStrictHit"] and case["graphHit"]),
        "neitherCount": sum(1 for case in multi_hop if not case["ragStrictHit"] and not case["graphHit"]),
        "negativePassRate": rate(sum(1 for case in negatives if case["graphHit"]), len(negatives)),
        "negativeGraphFalsePositiveCount": sum(1 for case in negatives if not case["graphHit"]),
        "negativeByKind": negative_by_kind,
        "ragDistractorCaseCount": len(distractor_cases),
        "ragDistractorFalsePositiveCount": sum(1 for case in distractor_cases if case["ragForbiddenEvidenceHit"]),
        "ragDistractorFalsePositiveRate": rate(
            sum(1 for case in distractor_cases if case["ragForbiddenEvidenceHit"]), len(distractor_cases)
        ),
        "truncatedQueryCount": sum(1 for case in cases if case["graphTruncated"]),
        "meanRagLatencyMs": round(sum(case["ragLatencyMs"] for case in cases) / len(cases), 1),
        "p95RagLatencyMs": percentile([case["ragLatencyMs"] for case in cases], 0.95),
        "meanGraphLatencyMs": round(sum(case["graphLatencyMs"] for case in cases) / len(cases), 1),
        "p95GraphLatencyMs": percentile([case["graphLatencyMs"] for case in cases], 0.95),
        "perType": per_type,
        "perTag": per_tag,
        "setStats": set_stats,
    }


def recommend(metrics: dict) -> dict:
    delta = round(metrics["multiHopGraphHitRate"] - metrics["multiHopRagStrictHitRate"], 4)
    guard = {
        "minMultiHopCases": MIN_MULTIHOP_CASES,
        "minAnswerableCases": MIN_ANSWERABLE_CASES,
        "multiHopCaseCount": metrics["multiHopCaseCount"],
        "answerableCaseCount": metrics["answerableCaseCount"],
    }
    if metrics["multiHopCaseCount"] < MIN_MULTIHOP_CASES or metrics["answerableCaseCount"] < MIN_ANSWERABLE_CASES:
        return {
            "recommendation": "inconclusive",
            "rationale": (
                f"the set has {metrics['multiHopCaseCount']} multi-hop and "
                f"{metrics['answerableCaseCount']} answerable cases, below the "
                f"{MIN_MULTIHOP_CASES}/{MIN_ANSWERABLE_CASES} guard"
            ),
            "delta": delta,
            "guard": guard,
        }
    unknown = metrics["negativeByKind"].get("unknown-seed") or {}
    unknown_ok = unknown.get("count", 0) > 0 and unknown.get("passRate", 0) == 1.0
    guard["unknownSeedNegativesPassed"] = unknown_ok
    if delta >= 0.2 and metrics["graphRescueCount"] >= 2 and unknown_ok:
        return {
            "recommendation": "keep-bounded-graph-query",
            "rationale": (
                f"on multi-hop cases graph reached the answer {metrics['multiHopGraphHitRate']:.0%} "
                f"versus strict RAG {metrics['multiHopRagStrictHitRate']:.0%} (+{delta:.0%}), with "
                f"{metrics['graphRescueCount']} graph-only rescue(s)"
            ),
            "delta": delta,
            "guard": guard,
        }
    if delta <= 0:
        return {
            "recommendation": "simplify",
            "rationale": (
                f"graph did not beat strict RAG on multi-hop cases ({metrics['multiHopGraphHitRate']:.0%} "
                f"versus {metrics['multiHopRagStrictHitRate']:.0%}); a depth-1 entity lookup would carry the same value"
            ),
            "delta": delta,
            "guard": guard,
        }
    return {
        "recommendation": "inconclusive",
        "rationale": (
            f"graph led by {delta:.0%} but only {metrics['graphRescueCount']} graph-only rescue(s); "
            "the evidence does not justify the bounded multi-hop surface"
        ),
        "delta": delta,
        "guard": guard,
    }


def dynamic_limitations(evaluation: dict, metrics: dict, rag_top_k: int, extractor_model: str) -> list[str]:
    fixture_count = len(evaluation.get("fixtures") or [])
    case_count = len(evaluation.get("cases") or [])
    chunk_count = (evaluation.get("corpus") or {}).get("expectedChunks", 0)
    limitations = [
        "Relations are seeded with the deterministic [[subject|predicate|object]] annotation, so extraction quality is not measured here; the comparison isolates bounded graph reachability versus Evidence RAG.",
        "Graph queries need a caller-supplied seed entity, taken from the question; alias and normalized seed forms are exercised, but automatic seed extraction from free text is out of scope.",
        f"The set has {case_count} hand-authored cases over {fixture_count} fixtures (about {chunk_count} child chunks). It is a directional benefit check, not a statistically powered benchmark.",
        "Strict Evidence RAG hit requires the answer marker in a chunk from the expected document; the loose marker-anywhere reading is reported only as a diagnostic. Neither measures whether the chat model would answer correctly.",
        "RAG retrieval is measured against a single freshly built index per run; HNSW construction and ingestion-time embedding variation can change top-k membership across rebuilds. The graph side is deterministic because relations come from seeded triples, so the delta and the recommendation can shift between otherwise identical runs; treat this report as one sample and repeat the run before a release decision.",
    ]
    if extractor_model.startswith("local"):
        limitations.append(
            "Extraction ran with the deterministic local chat adapter (CHAT_SOURCE=local), so the graph contains the seeded triples without model-added relations and depth-bound negatives cannot be polluted by extra edges; remote extraction may add edges."
        )
    else:
        limitations.append(
            "Remote chat extraction may add relations and entities beyond the seeded triples; extra graph reachability is included in the result rather than filtered, and depth-bound negatives are reported separately for that reason."
        )
    limitations.append(
        "The corpus is synthetic operational prose; it does not represent production document length, formatting or query distribution."
    )
    if rag_top_k:
        limitations.append(
            f"RAG top_k was overridden to {rag_top_k} for this run; the headline comparison is therefore a diagnostic and not the production-default retrieval setting."
        )
    if metrics["multiHopCaseCount"] < MIN_MULTIHOP_CASES:
        limitations.append(
            f"Only {metrics['multiHopCaseCount']} multi-hop cases were scored, below the {MIN_MULTIHOP_CASES}-case guard."
        )
    return limitations


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default=f"http://localhost:{os.environ.get('API_PORT', '8082')}")
    parser.add_argument("--keep", action="store_true", help="keep the evaluation knowledge base for inspection")
    parser.add_argument("--validate-only", action="store_true", help="validate the QA set without contacting the stack")
    parser.add_argument("--rag-top-k", type=int, default=0, help="diagnostic RAG top_k override; 0 keeps the service default")
    parser.add_argument("--report-dir", default=None, help="directory for the report (default: tests/eval)")
    parser.add_argument("--redact-base-url", action="store_true", help="write 'redacted' instead of the stack URL into the report")
    add_auth_arguments(parser, actor_keys=False)
    arguments = parser.parse_args()

    if arguments.rag_top_k and not 1 <= arguments.rag_top_k <= 200:
        print("--rag-top-k must be between 1 and 200", file=sys.stderr)
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
    corpus = evaluation.get("corpus") or {}
    fixture_count = len(evaluation.get("fixtures") or [])
    case_count = len(evaluation.get("cases") or [])
    print(
        f"set valid: {fixture_count} fixtures / {case_count} cases / "
        f"{corpus.get('expectedChunks')} chunks"
    )
    if arguments.validate_only:
        return 0

    try:
        credentials = Credentials(arguments.base_url, arguments.auth, arguments.api_key)
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
    status, knowledge_base = client.json(
        "POST", "/api/v1/knowledge-bases",
        {"name": f"m7-eval-{suffix}", "description": "M7 graph versus RAG evaluation fixture"},
    )
    if status not in (200, 201):
        raise RuntimeError(f"create knowledge base failed: {status} {knowledge_base}")
    knowledge_base_id = knowledge_base["id"]
    print(f"knowledge base {knowledge_base_id}")

    document_ids: dict[str, str] = {}
    try:
        uploaded: list[tuple[str, str, str]] = []
        for fixture in evaluation["fixtures"]:
            result = client.upload(knowledge_base_id, fixture["filename"], fixture["content"])
            document_id = result["document"]["id"]
            document_ids[fixture["filename"]] = document_id
            uploaded.append((fixture["role"], fixture["filename"], document_id))
        for role, filename, document_id in uploaded:
            wait_document_ready(client, document_id)
            print(f"  {role}: {filename} {document_id} EVIDENCE_READY")

        status, patched = client.json(
            "PATCH", f"/api/v1/knowledge-bases/{knowledge_base_id}",
            {"indexing_strategy": {
                "vector_enabled": True, "keyword_enabled": True,
                "wiki_enabled": False, "graph_enabled": True,
                "required_capabilities": ["graph"],
            }},
        )
        if status != 200:
            raise RuntimeError(f"enable graph failed: {status} {patched}")

        run = wait_relation_ready(client, knowledge_base_id)
        snapshot = approve_latest_snapshot(client, knowledge_base_id)
        detail = snapshot_detail(client, snapshot["id"])
        relations = detail.get("relations") or []
        traceable = bool(relations) and all(relation.get("sources") for relation in relations)
        if not traceable:
            raise RuntimeError("approved snapshot contains a relation without sources")
        print(
            f"relation run {run['id']} {run.get('status')} model={run.get('model')} "
            f"snapshot {snapshot['id']} {snapshot.get('status')} relations={len(relations)}"
        )

        observed_params: dict = {}
        cases: list[dict] = []
        for case in evaluation["cases"]:
            result = evaluate_case(client, knowledge_base_id, case, document_ids, arguments.rag_top_k, observed_params)
            cases.append(result)
            if case["type"] == "negative":
                marker = "pass" if result["graphHit"] else "fail"
                print(f"  {result['id']:<26} graph-negative={marker} nodes={result['graphNodeCount']}")
            else:
                marker = "hit " if result["graphHit"] else "miss"
                print(
                    f"  {result['id']:<26} graph={marker} ({result['graphHops'] if result['graphHops'] is not None else '-'} hops) "
                    f"rag={'strict' if result['ragStrictHit'] else 'loose' if result['ragLooseHit'] else 'miss'} "
                    f"{result['graphLatencyMs']:.0f}ms/{result['ragLatencyMs']:.0f}ms"
                )

        by_type: dict[str, int] = {}
        for case in cases:
            by_type[case["type"]] = by_type.get(case["type"], 0) + 1
        set_stats = {
            "fixtures": fixture_count,
            "fixtureRoles": {
                role: sum(1 for fixture in evaluation["fixtures"] if fixture.get("role") == role)
                for role in sorted({fixture.get("role") for fixture in evaluation["fixtures"]})
            },
            "cases": case_count,
            "casesByType": by_type,
            "answerableCases": sum(1 for case in cases if case["type"] != "negative"),
            "multiHopCases": by_type.get("multi-hop", 0),
            "negativeCases": by_type.get("negative", 0),
            "expectedChunks": corpus.get("expectedChunks"),
        }
        metrics = summarize(cases, set_stats)
        decision = recommend(metrics)

        report = {
            "schemaVersion": SCHEMA_REPORT,
            "generatedAt": datetime.now(timezone.utc).isoformat(),
            "baseUrl": base_url_for_report(arguments.base_url, arguments.redact_base_url),
            "auth": {
                "mode": arguments.auth,
                "owner": {"subjectId": owner.subject_id, "tenantId": owner.tenant_id},
            },
            "set": evaluation["name"],
            "setSchemaVersion": evaluation["schemaVersion"],
            "knowledgeBaseId": knowledge_base_id,
            "kept": bool(arguments.keep),
            "setStats": set_stats,
            "searchParams": {
                "ragTopK": arguments.rag_top_k or "service-default",
                "observed": observed_params.get("plan") or {},
            },
            "graphQuery": {"limit": GRAPH_LIMIT, "maxDepth": MAX_GRAPH_DEPTH},
            "extractor": {
                "model": run.get("model", ""),
                "promptVersion": run.get("promptVersion", ""),
                "sourceHash": run.get("sourceHash", ""),
            },
            "snapshot": {
                "id": snapshot["id"],
                "status": snapshot.get("status", ""),
                "aclVersion": snapshot.get("aclVersion"),
                "entities": len(detail.get("entities") or []),
                "relations": len(relations),
                "aliases": len(detail.get("aliases") or []),
                "traceable": traceable,
            },
            "metrics": metrics,
            "decision": decision,
            "cases": cases,
            "limitations": dynamic_limitations(evaluation, metrics, arguments.rag_top_k, run.get("model", "")),
        }
        report_path = write_report(
            report,
            resolve_report_path(arguments.report_dir, REPORT.parent, REPORT.name),
        )
        print(f"\nreport: {report_path}")
        print(
            f"multi-hop graph {metrics['multiHopGraphHitRate']:.0%} vs strict RAG {metrics['multiHopRagStrictHitRate']:.0%} "
            f"(loose {metrics['multiHopRagLooseHitRate']:.0%}); "
            f"rescues={metrics['graphRescueCount']} ragOnly={metrics['ragOnlyCount']} "
            f"both={metrics['bothHitCount']} neither={metrics['neitherCount']}"
        )
        print(
            f"hop exact {metrics['multiHopHopExactRate']:.0%} on {metrics['multiHopHopCheckedCount']} checked; "
            f"negatives {metrics['negativePassRate']:.0%} ({metrics['negativeCaseCount']}); "
            f"rag distractor false positives {metrics['ragDistractorFalsePositiveCount']}/{metrics['ragDistractorCaseCount']}"
        )
        print(f"recommendation: {decision['recommendation']} — {decision['rationale']}")
        return 0
    finally:
        if not arguments.keep:
            status, payload = client.json("DELETE", f"/api/v1/knowledge-bases/{knowledge_base_id}")
            if status not in (200, 204):
                print(f"warning: evaluation knowledge base cleanup failed: {status} {payload}", file=sys.stderr)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except RuntimeError as error:
        print(f"m7 eval failed: {error}", file=sys.stderr)
        sys.exit(1)
