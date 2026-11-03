#!/usr/bin/env python3
"""M8 capacity baseline: reproducible write-path and query-path measurements.

MILESTONES M8 only starts a candidate extension (OpenSearch/Milvus, Neo4j,
Agent orchestration, OpenFGA, Kubernetes, MCP/IM/Widget, OCR) when the request
carries reproducible capacity data or a confirmed product need, a cost
assessment, a migration/rollback plan and acceptance tests. This harness
produces the first of those artifacts: it builds a deterministic corpus in a
throwaway knowledge base and measures the current stack instead of introducing
any new framework.

What is measured:

  * write path: per-document upload latency, ingestion wall time, documents and
    chunks per second, and per-stage durations (parse/normalize/chunk/embed/
    index) taken from the pipeline status surface;
  * search path: repeated POST /api/v1/search calls on generated queries,
    reported as P50/P95/mean/min/max together with empty-result and marker
    document-hit counts;
  * graph path (optional): relation projection wall time, snapshot scale and
    repeated GET /graph/query calls at depths 1-3 plus unknown-seed probes;
  * storage footprint (optional): database and table sizes plus knowledge-base
    row counts when the local compose Postgres is reachable.

Every HTTP call is paced below the tenant rate limit (RATE_LIMIT_PER_MINUTE,
600 by default): one document-list poll per second instead of one status call
per document, and a minimum interval between requests. Pacing happens between
requests, so per-call latency samples stay clean; concurrency is not measured.

The report also carries a gate register mapping the seven MILESTONES M8
candidates to the evidence this run collected and the artifacts that are still
missing. Running the baseline never starts a candidate.

Usage:
    python3 scripts/m8_capacity.py [--documents 40] [--lines 10] [--queries 12]
    python3 scripts/m8_capacity.py --no-graph --keep
    python3 scripts/m8_capacity.py --self-test
    python3 scripts/m8_capacity.py --validate-report tests/eval/m8_capacity_report.json

Full graph measurement calls the chat adapter once per evidence batch, so a
remote extractor makes the run latency-bound; use CHAT_SOURCE=local for a
deterministic infrastructure baseline and restore the remote adapter
afterwards. The embedding adapter is recorded from the database probe when it
is available.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import random
import re
import statistics
import subprocess
import sys
import time
import urllib.parse
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from m7_eval import Client, percentile  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_REPORT = ROOT / "tests" / "eval" / "m8_capacity_report.json"
DEFAULT_COMPOSE_FILE = ROOT / "deploy" / "docker-compose" / "docker-compose.yml"
DEFAULT_ENV_FILE = ROOT / ".env"

SCHEMA_REPORT = "M8CapacityReportV1"
GRAPH_LIMIT = 40
MAX_GRAPH_DEPTH = 3
MAX_SOURCE_LINE_RUNES = 900
STAGE_ORDER = ("parse", "normalize", "chunk", "embed", "index")
# 0.15s between calls is ~400 requests/minute, leaving headroom under the
# default 600/minute tenant limiter for the one-per-second readiness poll.
REQUEST_INTERVAL_SECONDS = 0.15

TOPIC_WORDS = (
    "inspection", "schedule", "quarterly", "maintenance", "logistics",
    "inventory", "safety", "drill", "throughput", "handover", "calibration",
    "dispatch", "winter", "coastal", "reserve", "rotation", "audit", "supply",
)

GATE_CANDIDATES = (
    {
        "id": "search-index-backend",
        "item": "OpenSearch/Milvus migration",
        "needs": "capacity data: document volume, index write, query latency, operating cost",
        "capacityKeys": ("documentVolume", "ingestionThroughput", "searchLatency"),
    },
    {
        "id": "graph-database",
        "item": "Neo4j/other graph database",
        "needs": "capacity data: multi-hop query latency, query complexity, relation scale",
        "capacityKeys": ("graphLatency", "relationScale"),
    },
    {
        "id": "agent-orchestration",
        "item": "LangGraph/Agent orchestration",
        "needs": "confirmed need: dynamic branching, loops, tool calls, human approval or recoverable long tasks",
        "capacityKeys": (),
    },
    {
        "id": "openfga",
        "item": "OpenFGA authorization engine",
        "needs": "multi-tenant scale and access-relationship complexity",
        "capacityKeys": (),
    },
    {
        "id": "kubernetes",
        "item": "Kubernetes/sharding/multi-region/HA",
        "needs": "deployment scale and RTO/RPO requirements",
        "capacityKeys": (),
    },
    {
        "id": "channels",
        "item": "MCP/IM/Widget and complex connectors",
        "needs": "confirmed demand from a real channel",
        "capacityKeys": (),
    },
    {
        "id": "ocr",
        "item": "Separate OCR milestone",
        "needs": "product data confirming scanned documents",
        "capacityKeys": (),
    },
)

COMMON_ARTIFACTS = (
    "confirmedCapacityThresholdOrNeed",
    "costAssessment",
    "migrationRollbackPlan",
    "acceptanceTests",
)


def parse_timestamp(value: str) -> datetime | None:
    text = (value or "").strip()
    if not text:
        return None
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    # Go emits nanosecond precision; Python 3.9 accepts at most microseconds.
    match = re.match(r"(.*\.\d{6})\d+(.*)", text)
    if match:
        text = match.group(1) + match.group(2)
    try:
        return datetime.fromisoformat(text)
    except ValueError:
        return None


def latency_stats(samples: list[float]) -> dict:
    values = [float(sample) for sample in samples]
    if not values:
        return {"count": 0, "p50": None, "p95": None, "mean": None, "min": None, "max": None}
    return {
        "count": len(values),
        "p50": percentile(values, 0.5),
        "p95": percentile(values, 0.95),
        "mean": round(statistics.fmean(values), 1),
        "min": round(min(values), 1),
        "max": round(max(values), 1),
    }


def parse_depths(raw: str) -> list[int]:
    depths: list[int] = []
    for part in raw.split(","):
        part = part.strip()
        if not part:
            continue
        depth = int(part)
        if depth < 1 or depth > MAX_GRAPH_DEPTH:
            raise ValueError(f"depth {depth} outside 1..{MAX_GRAPH_DEPTH}")
        if depth not in depths:
            depths.append(depth)
    if not depths:
        raise ValueError("at least one depth is required")
    return sorted(depths)


class PacedClient:
    """Spaces requests so a baseline run does not trip the tenant limiter.

    Non-timed calls go through json()/upload(). Timed calls pace first and then
    time the raw client, so rate-limit waits never enter a latency sample.
    """

    def __init__(self, client: Client, interval_seconds: float = REQUEST_INTERVAL_SECONDS):
        self.raw = client
        self.interval = max(0.0, interval_seconds)
        self._last = 0.0

    def pace(self) -> None:
        wait = self._last + self.interval - time.monotonic()
        if wait > 0:
            time.sleep(wait)
        self._last = time.monotonic()

    def json(self, method: str, path: str, body: dict | None = None) -> tuple[int, dict]:
        self.pace()
        return self.raw.json(method, path, body)

    def upload(self, knowledge_base_id: str, filename: str, content: str) -> dict:
        self.pace()
        return self.raw.upload(knowledge_base_id, filename, content)


def build_corpus(documents: int, lines_per_document: int, seed: int) -> dict:
    if documents < 1:
        raise ValueError("documents must be at least 1")
    if lines_per_document < 6:
        raise ValueError("lines per document must be at least 6 (header, prose, four triples)")
    rng = random.Random(seed)
    fixtures: list[dict] = []
    expected_chunks = 0
    total_bytes = 0
    for index in range(documents):
        serial = index + 1
        marker = f"qz-{serial:04d}"
        station = f"Station {serial:04d}"
        corridor = f"Corridor {serial:04d}"
        depot = f"Depot {serial:04d}"
        region = f"Region {serial:04d}"
        operator = f"Operator {serial:04d}"
        lines = [
            f"# Capacity fixture {serial:04d}",
            f"Quarterly review for {station} records audit marker {marker} in the operating log.",
            f"[[{station}|supplies|{corridor}]]",
            f"[[{corridor}|routes_to|{depot}]]",
            f"[[{depot}|reports_to|{region}]]",
            f"[[{station}|operated_by|{operator}|S{serial:04d}]]",
        ]
        while len(lines) < lines_per_document:
            words = " ".join(rng.choice(TOPIC_WORDS) for _ in range(12))
            lines.append(f"{station} routine note {len(lines) - 5}: {words}.")
        for line in lines:
            if len(line) > MAX_SOURCE_LINE_RUNES:
                raise ValueError("generated line exceeds the chunker block limit")
        content = "\n".join(lines) + "\n"
        fixtures.append(
            {
                "filename": f"capacity-{serial:04d}.md",
                "content": content,
                "marker": marker,
                "station": station,
                "serial": serial,
            }
        )
        expected_chunks += len(lines)
        total_bytes += len(content.encode())
    return {
        "seed": seed,
        "documents": documents,
        "linesPerDocument": lines_per_document,
        "expectedChunks": expected_chunks,
        "bytes": total_bytes,
        "fixtures": fixtures,
    }


def pick_fixtures(fixtures: list[dict], count: int) -> list[dict]:
    if count <= 0:
        return []
    step = max(1, len(fixtures) // count)
    picked: list[dict] = []
    seen: set[int] = set()
    position = 0
    while len(picked) < count:
        index = (position * step) % len(fixtures)
        while index in seen and len(seen) < len(fixtures):
            index = (index + 1) % len(fixtures)
        seen.add(index)
        picked.append(fixtures[index])
        position += 1
    return picked


def build_queries(fixtures: list[dict], count: int) -> list[dict]:
    if count < 1:
        raise ValueError("queries must be at least 1")
    if count > 2 * len(fixtures):
        raise ValueError(f"queries must not exceed twice the fixture count ({2 * len(fixtures)})")
    marker_count = (count + 1) // 2
    topical_count = count - marker_count
    queries: list[dict] = []
    for fixture in pick_fixtures(fixtures, marker_count):
        queries.append(
            {
                "id": f"marker-{fixture['serial']:04d}",
                "family": "marker",
                "query": f"audit marker {fixture['marker']}",
                "expectedFilename": fixture["filename"],
            }
        )
    for fixture in pick_fixtures(fixtures, topical_count):
        queries.append(
            {
                "id": f"topical-{fixture['serial']:04d}",
                "family": "topical",
                "query": f"quarterly review operating log {fixture['station']}",
                "expectedFilename": fixture["filename"],
            }
        )
    return queries


def list_document_versions(client: PacedClient, knowledge_base_id: str) -> dict[str, dict]:
    versions: dict[str, dict] = {}
    cursor = ""
    while True:
        path = f"/api/v1/knowledge-bases/{knowledge_base_id}/documents?limit=200"
        if cursor:
            path += "&cursor=" + urllib.parse.quote(cursor, safe="")
        status, payload = client.json("GET", path)
        if status != 200:
            raise RuntimeError(f"document list failed: {status} {payload}")
        for item in payload.get("items") or []:
            document_id = (item.get("document") or {}).get("id")
            if document_id:
                versions[document_id] = item.get("version") or {}
        cursor = payload.get("nextCursor") or ""
        if not payload.get("hasMore") or not cursor:
            return versions


def wait_documents_ready(client: PacedClient, knowledge_base_id: str, uploaded: list[dict], timeout_seconds: int) -> dict:
    # One list request per second covers every pending document and stays well
    # below the tenant rate limit; per-document status is fetched once at the end
    # to collect stage timings.
    pending = {entry["documentId"]: entry for entry in uploaded}
    observed_at: dict[str, float] = {}
    deadline = time.monotonic() + timeout_seconds
    while pending:
        versions = list_document_versions(client, knowledge_base_id)
        now = time.monotonic()
        for document_id in list(pending):
            version = versions.get(document_id) or {}
            state = version.get("status")
            if state == "EVIDENCE_READY":
                observed_at[document_id] = now
                pending.pop(document_id)
            elif state in ("FAILED", "CANCELLED"):
                raise RuntimeError(f"document {document_id} ended as {state}: {version.get('errorMessage')}")
        if pending:
            if now >= deadline:
                names = ", ".join(entry["filename"] for entry in pending.values())
                raise RuntimeError(f"timed out after {timeout_seconds}s waiting for {len(pending)} document(s): {names}")
            time.sleep(1.0)
    ready: dict[str, dict] = {}
    for document_id, ready_at in observed_at.items():
        status, payload = client.json("GET", f"/api/v1/documents/{document_id}/status")
        if status != 200:
            raise RuntimeError(f"document {document_id} status failed: {status} {payload}")
        ready[document_id] = {"readyAt": ready_at, "status": payload}
    return ready


def stage_timings(ready: dict) -> dict:
    per_stage: dict[str, list[float]] = {stage: [] for stage in STAGE_ORDER}
    for entry in ready.values():
        for stage in entry["status"].get("stages") or []:
            name = stage.get("stage") or ""
            started = parse_timestamp(stage.get("startedAt") or "")
            completed = parse_timestamp(stage.get("completedAt") or "")
            if not started or not completed or name not in per_stage:
                continue
            per_stage[name].append((completed - started).total_seconds() * 1000)
    summary = {}
    for name in STAGE_ORDER:
        stats = latency_stats(per_stage[name])
        stats["documents"] = len(per_stage[name])
        summary[name] = stats
    return summary


def measure_write_path(client: PacedClient, knowledge_base_id: str, corpus: dict, timeout_seconds: int) -> dict:
    uploads: list[dict] = []
    started = time.perf_counter()
    for fixture in corpus["fixtures"]:
        client.pace()
        begun = time.perf_counter()
        result = client.upload(knowledge_base_id, fixture["filename"], fixture["content"])
        upload_ms = (time.perf_counter() - begun) * 1000
        uploads.append(
            {
                "filename": fixture["filename"],
                "documentId": result["document"]["id"],
                "uploadMs": upload_ms,
                "uploadedAt": time.monotonic(),
            }
        )
    ready = wait_documents_ready(client, knowledge_base_id, uploads, timeout_seconds)
    finished = max(entry["readyAt"] for entry in ready.values())
    wall_ms = (finished - started) * 1000
    ready_latencies = [
        (ready[entry["documentId"]]["readyAt"] - entry["uploadedAt"]) * 1000
        for entry in uploads
        if entry["documentId"] in ready
    ]
    first_upload = uploads[0]["uploadedAt"] if uploads else started
    first_ready = min(entry["readyAt"] for entry in ready.values()) if ready else finished
    stage_attempts: dict[str, int] = {stage: 0 for stage in STAGE_ORDER}
    for entry in ready.values():
        for stage in entry["status"].get("stages") or []:
            name = stage.get("stage") or ""
            if name in stage_attempts:
                stage_attempts[name] = max(stage_attempts[name], int(stage.get("attempts") or 0))
    return {
        "documentsUploaded": len(uploads),
        "documentsReady": len(ready),
        "firstDocumentId": uploads[0]["documentId"] if uploads else "",
        "uploadLatencyMs": latency_stats([entry["uploadMs"] for entry in uploads]),
        "readyLatencyMs": latency_stats(ready_latencies),
        "ingestionWallMs": round(wall_ms, 1),
        "timeToFirstReadyMs": round((first_ready - first_upload) * 1000, 1),
        "documentsPerSecond": round(len(ready) / (wall_ms / 1000), 4) if wall_ms > 0 else None,
        "chunksPerSecond": round(corpus["expectedChunks"] / (wall_ms / 1000), 4) if wall_ms > 0 else None,
        "stages": stage_timings(ready),
        "attempts": stage_attempts,
    }


def count_document_chunks(client: PacedClient, document_id: str) -> int:
    total = 0
    cursor = 0
    while True:
        path = f"/api/v1/documents/{document_id}/chunks?limit=200"
        if cursor:
            path += f"&cursor={cursor}"
        status, payload = client.json("GET", path)
        if status != 200:
            raise RuntimeError(f"chunk page failed for {document_id}: {status} {payload}")
        total += len(payload.get("items") or [])
        cursor = payload.get("nextCursor") or 0
        if not payload.get("hasMore") or not cursor:
            return total


def timed_search(client: PacedClient, knowledge_base_id: str, question: str) -> tuple[dict, float]:
    # Pacing happens before the timer so the sample measures the service, not
    # the harness's rate-limit wait.
    body = {"query": question, "knowledgeBaseId": knowledge_base_id}
    client.pace()
    started = time.perf_counter()
    status, payload = client.raw.json("POST", "/api/v1/search", body)
    elapsed = (time.perf_counter() - started) * 1000
    if status != 200:
        raise RuntimeError(f"search failed for {question!r}: {status} {payload}")
    return payload, elapsed


def timed_graph_query(client: PacedClient, knowledge_base_id: str, seed: str, depth: int, limit: int = GRAPH_LIMIT) -> tuple[dict, float]:
    query = urllib.parse.urlencode({"q": seed, "depth": depth, "limit": limit})
    client.pace()
    started = time.perf_counter()
    status, payload = client.raw.json("GET", f"/api/v1/knowledge-bases/{knowledge_base_id}/graph/query?{query}")
    elapsed = (time.perf_counter() - started) * 1000
    if status != 200:
        raise RuntimeError(f"graph query failed for seed {seed!r}: {status} {payload}")
    return payload, elapsed


def settle_search(client: PacedClient, knowledge_base_id: str, queries: list[dict], timeout_seconds: int) -> float:
    # Right after ingestion or a neighbouring deletion the BM25 index can still
    # reference tuples that are being vacuumed; ParadeDB may fail that channel
    # for a few seconds. A full warmup sweep keeps that maintenance window out
    # of the measured samples instead of reporting transient empty results.
    started = time.monotonic()
    while True:
        empties = 0
        for query in queries:
            payload, _ = timed_search(client, knowledge_base_id, query["query"])
            if not payload.get("items"):
                empties += 1
        elapsed = time.monotonic() - started
        if empties == 0:
            return round(elapsed * 1000, 1)
        if elapsed >= timeout_seconds:
            raise RuntimeError(
                f"search still returned empty results after {timeout_seconds}s of warmup ({empties} of {len(queries)} queries)"
            )
        time.sleep(1.0)


def measure_search(client: PacedClient, knowledge_base_id: str, queries: list[dict], repeats: int, settle_timeout: int) -> dict:
    settle_ms = settle_search(client, knowledge_base_id, queries, settle_timeout)
    samples: list[float] = []
    per_query: list[dict] = []
    raw_samples: dict[str, list[float]] = {}
    observed_params: dict = {}
    for query in queries:
        query_samples: list[float] = []
        empty = 0
        expected_hit = 0
        item_counts: list[int] = []
        for _ in range(repeats):
            payload, elapsed = timed_search(client, knowledge_base_id, query["query"])
            query_samples.append(elapsed)
            samples.append(elapsed)
            if not observed_params:
                observed_params = (payload.get("plan") or {}).get("params") or {}
            items = payload.get("items") or []
            item_counts.append(len(items))
            if not items:
                empty += 1
            document_ids = {item.get("documentId") for item in items}
            if document_ids:
                expected_hit += 1
        raw_samples[query["id"]] = query_samples
        per_query.append(
            {
                "id": query["id"],
                "family": query["family"],
                "query": query["query"],
                "latencyMs": latency_stats(query_samples),
                "meanItems": round(statistics.fmean(item_counts), 1) if item_counts else 0,
                "emptyResults": empty,
                "expectedDocumentHits": expected_hit,
                "expectedDocumentSamples": repeats,
            }
        )
    families: dict[str, dict] = {}
    for family in sorted({query["family"] for query in per_query}):
        group = [entry for entry in per_query if entry["family"] == family]
        family_samples = [sample for entry in group for sample in raw_samples[entry["id"]]]
        families[family] = {
            "count": len(group),
            "latencyMs": latency_stats(family_samples),
        }
    empty_total = sum(entry["emptyResults"] for entry in per_query)
    expected_total = sum(entry["expectedDocumentHits"] for entry in per_query)
    expected_samples = sum(entry["expectedDocumentSamples"] for entry in per_query)
    return {
        "queries": len(queries),
        "repeats": repeats,
        "samples": len(samples),
        "settleMs": settle_ms,
        "latencyMs": latency_stats(samples),
        "families": families,
        "emptyResultCount": empty_total,
        "expectedDocumentHitCount": expected_total,
        "expectedDocumentSampleCount": expected_samples,
        "expectedDocumentHitRate": round(expected_total / expected_samples, 4) if expected_samples else None,
        "observedParams": observed_params,
        "perQuery": per_query,
    }


def wait_relation_ready(client: PacedClient, knowledge_base_id: str, timeout_seconds: int) -> dict:
    deadline = time.monotonic() + timeout_seconds
    while time.monotonic() < deadline:
        status, payload = client.json("GET", f"/api/v1/knowledge-bases/{knowledge_base_id}/projection")
        if status != 200:
            raise RuntimeError(f"projection status failed: {status} {payload}")
        runs = payload.get("relationRuns") or []
        if runs:
            run = runs[0]
            if run.get("status") == "RELATION_READY":
                return run
            if run.get("status") in ("FAILED_RELATION", "CANCELLED"):
                raise RuntimeError(f"relation run ended as {run.get('status')}: {run.get('errorMessage') or run.get('errorCode')}")
        time.sleep(1.0)
    raise RuntimeError(f"timed out after {timeout_seconds}s waiting for RELATION_READY")


def approve_snapshot(client: PacedClient, knowledge_base_id: str, note: str) -> dict:
    status, payload = client.json("GET", f"/api/v1/knowledge-bases/{knowledge_base_id}/graph/snapshots")
    if status != 200:
        raise RuntimeError(f"list snapshots failed: {status} {payload}")
    snapshots = payload.get("snapshots") or []
    if not snapshots:
        raise RuntimeError("relation run finished without a snapshot")
    snapshot = snapshots[0]
    if snapshot.get("status") == "PENDING_REVIEW":
        status, reviewed = client.json(
            "POST", f"/api/v1/graph/snapshots/{snapshot['id']}/review", {"action": "approve", "note": note}
        )
        if status != 200:
            raise RuntimeError(f"snapshot review failed: {status} {reviewed}")
        snapshot = reviewed
    if snapshot.get("status") != "ACTIVE":
        raise RuntimeError(f"snapshot is not queryable after review: {snapshot.get('status')}")
    return snapshot


def snapshot_detail(client: PacedClient, snapshot_id: str) -> dict:
    status, payload = client.json("GET", f"/api/v1/graph/snapshots/{snapshot_id}")
    if status != 200:
        raise RuntimeError(f"snapshot detail failed: {status} {payload}")
    return payload


def measure_graph(client: PacedClient, knowledge_base_id: str, fixtures: list[dict], depths: list[int], repeats: int, projection_timeout: int) -> dict:
    started = time.perf_counter()
    status, patched = client.json(
        "PATCH",
        f"/api/v1/knowledge-bases/{knowledge_base_id}",
        {
            "indexing_strategy": {
                "vector_enabled": True,
                "keyword_enabled": True,
                "wiki_enabled": False,
                "graph_enabled": True,
                "required_capabilities": ["graph"],
            }
        },
    )
    if status != 200:
        raise RuntimeError(f"enable graph failed: {status} {patched}")
    run = wait_relation_ready(client, knowledge_base_id, projection_timeout)
    projection_ms = (time.perf_counter() - started) * 1000
    snapshot = approve_snapshot(client, knowledge_base_id, "m8 capacity baseline")
    detail = snapshot_detail(client, snapshot["id"])
    relations = detail.get("relations") or []

    seeds = pick_fixtures(fixtures, min(8, len(fixtures)))
    groups: list[dict] = []
    known_node_counts: list[dict] = []
    for depth in depths:
        samples: list[float] = []
        node_counts: list[int] = []
        truncated = 0
        for fixture in seeds:
            for _ in range(repeats):
                payload, elapsed = timed_graph_query(client, knowledge_base_id, fixture["station"], depth, GRAPH_LIMIT)
                samples.append(elapsed)
                nodes = payload.get("nodes") or []
                node_counts.append(len(nodes))
                if payload.get("truncated"):
                    truncated += 1
                known_node_counts.append({"seed": fixture["station"], "depth": depth, "nodes": len(nodes)})
        groups.append(
            {
                "depth": depth,
                "seeds": len(seeds),
                "samples": len(samples),
                "latencyMs": latency_stats(samples),
                "meanNodes": round(statistics.fmean(node_counts), 1) if node_counts else 0,
                "minNodes": min(node_counts) if node_counts else 0,
                "truncatedCount": truncated,
            }
        )

    unknown_samples: list[float] = []
    unknown_empty = 0
    unknown_with_snapshot = 0
    for index in range(min(3, len(fixtures))):
        for _ in range(repeats):
            payload, elapsed = timed_graph_query(client, knowledge_base_id, f"Unknown Entity {index + 1:04d}", 2, GRAPH_LIMIT)
            unknown_samples.append(elapsed)
            nodes = payload.get("nodes") or []
            if not nodes:
                unknown_empty += 1
            if payload.get("snapshotId"):
                unknown_with_snapshot += 1

    return {
        "enabled": True,
        "projection": {
            "status": run.get("status"),
            "model": run.get("model", ""),
            "promptVersion": run.get("promptVersion", ""),
            "wallMs": round(projection_ms, 1),
            "snapshot": {
                "id": snapshot["id"],
                "status": snapshot.get("status", ""),
                "entities": len(detail.get("entities") or []),
                "relations": len(relations),
                "aliases": len(detail.get("aliases") or []),
                "traceable": bool(relations) and all(relation.get("sources") for relation in relations),
            },
        },
        "queryGroups": groups,
        "knownSeeds": known_node_counts,
        "unknownSeeds": {
            "samples": len(unknown_samples),
            "latencyMs": latency_stats(unknown_samples),
            "emptyCount": unknown_empty,
            "snapshotPresentCount": unknown_with_snapshot,
        },
        "limit": GRAPH_LIMIT,
        "maxDepth": MAX_GRAPH_DEPTH,
    }


def psql_value(compose_file: Path, env_file: Path, sql: str, timeout: int = 60) -> tuple[bool, str]:
    command = ["docker", "compose"]
    if env_file.exists():
        command += ["--env-file", str(env_file)]
    command += ["-f", str(compose_file), "exec", "-T", "postgres", "sh", "-lc",
                'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "$1"', "sh", sql]
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=timeout)
    except (FileNotFoundError, subprocess.TimeoutExpired) as error:
        return False, str(error)
    if result.returncode != 0:
        return False, (result.stderr or result.stdout or "").strip()
    return True, result.stdout.strip()


def probe_storage(knowledge_base_id: str, compose_file: Path, env_file: Path) -> dict:
    if not re.fullmatch(r"[A-Za-z0-9_-]+", knowledge_base_id):
        return {"available": False, "reason": "knowledge base id is not probe-safe"}
    ok, output = psql_value(compose_file, env_file, "SELECT 1")
    if not ok or output != "1":
        return {"available": False, "reason": output or "postgres is not reachable via docker compose"}
    probes: dict = {"available": True}
    ok, output = psql_value(compose_file, env_file, "SELECT pg_database_size(current_database())")
    probes["databaseSizeBytes"] = int(output) if ok and output.isdigit() else None
    ok, output = psql_value(
        compose_file,
        env_file,
        "SELECT relname || '|' || pg_total_relation_size(oid) FROM pg_class "
        "WHERE relkind = 'r' AND relname IN ('knowledge_bases','documents','document_versions',"
        "'chunks','graph_snapshots','graph_entities','graph_entity_aliases','graph_relations',"
        "'graph_relation_sources') ORDER BY relname",
    )
    table_bytes: dict[str, int] = {}
    if ok:
        for line in output.splitlines():
            name, _, size = line.partition("|")
            if name and size.isdigit():
                table_bytes[name] = int(size)
    probes["tableBytes"] = table_bytes
    kb = knowledge_base_id
    counts_sql = (
        f"SELECT 'documents=' || count(*) FROM documents WHERE knowledge_base_id = '{kb}';"
        f"SELECT 'document_versions=' || count(*) FROM document_versions dv JOIN documents d ON d.id = dv.document_id WHERE d.knowledge_base_id = '{kb}';"
        f"SELECT 'chunks=' || count(*) FROM chunks WHERE knowledge_base_id = '{kb}';"
        f"SELECT 'graph_entities=' || count(*) FROM graph_entities WHERE knowledge_base_id = '{kb}';"
        f"SELECT 'graph_relations=' || count(*) FROM graph_relations WHERE knowledge_base_id = '{kb}';"
        f"SELECT 'graph_relation_sources=' || count(*) FROM graph_relation_sources s "
        f"JOIN graph_relations r ON r.id = s.relation_id WHERE r.knowledge_base_id = '{kb}';"
    )
    ok, output = psql_value(compose_file, env_file, counts_sql)
    counts: dict[str, int] = {}
    if ok:
        for line in output.splitlines():
            name, _, value = line.partition("=")
            if name and value.isdigit():
                counts[name] = int(value)
    probes["knowledgeBaseCounts"] = counts
    ok, output = psql_value(
        compose_file,
        env_file,
        f"SELECT DISTINCT embedding_model || ':' || embedding_dimension FROM chunks "
        f"WHERE knowledge_base_id = '{kb}' AND embedding IS NOT NULL ORDER BY 1",
    )
    probes["embeddingModels"] = [line for line in output.splitlines() if line] if ok else []
    return probes


def delete_and_verify(client: PacedClient, knowledge_base_id: str) -> dict:
    started = time.perf_counter()
    status, payload = client.json("DELETE", f"/api/v1/knowledge-bases/{knowledge_base_id}")
    delete_ms = (time.perf_counter() - started) * 1000
    deleted = status in (200, 204)
    status, _ = client.json("GET", f"/api/v1/knowledge-bases/{knowledge_base_id}")
    return {
        "deleted": deleted,
        "deleteMs": round(delete_ms, 1),
        "verifiedAbsent": status in (403, 404),
        "verifyStatus": status,
        "error": "" if deleted else f"{status} {payload}",
    }


def build_gate_evidence(parameters: dict, write_path: dict, search_section: dict, graph_section: dict, storage: dict) -> dict:
    measured = {
        "documentVolume": bool(write_path.get("documentsReady")),
        "ingestionThroughput": bool(write_path.get("documentsPerSecond")),
        "searchLatency": bool(search_section.get("latencyMs", {}).get("count")),
        "graphLatency": bool(graph_section.get("enabled") and graph_section.get("queryGroups")),
        "relationScale": bool(graph_section.get("enabled") and graph_section.get("projection", {}).get("snapshot")),
    }
    storage_ok = bool(storage.get("available") and storage.get("databaseSizeBytes"))
    candidates: list[dict] = []
    for candidate in GATE_CANDIDATES:
        keys = candidate["capacityKeys"]
        if keys:
            collected = [key for key in keys if measured.get(key)]
            missing = [key for key in keys if not measured.get(key)]
            if len(collected) == len(keys):
                status = "capacity-collected"
            elif collected:
                status = "capacity-partial"
            else:
                status = "not-collected"
            entry = {
                "id": candidate["id"],
                "item": candidate["item"],
                "needs": candidate["needs"],
                "evidenceStatus": status,
                "evidenceCollected": collected,
                "evidenceMissing": missing,
                "remainingArtifacts": list(COMMON_ARTIFACTS),
            }
            if candidate["id"] == "search-index-backend":
                entry["storageFootprintCollected"] = storage_ok
            candidates.append(entry)
            continue
        candidates.append(
            {
                "id": candidate["id"],
                "item": candidate["item"],
                "needs": candidate["needs"],
                "evidenceStatus": "not-collected",
                "evidenceCollected": [],
                "evidenceMissing": list(candidate["capacityKeys"]),
                "remainingArtifacts": list(COMMON_ARTIFACTS),
                "reason": "not measurable by an infrastructure capacity baseline",
            }
        )
    return {
        "candidates": candidates,
        "startedCandidates": [],
        "note": (
            "The capacity baseline only supplies reproducible capacity data. No candidate starts here: "
            "MILESTONES additionally requires a confirmed product need or capacity threshold, a cost "
            "assessment, a migration/rollback plan and acceptance tests for each candidate."
        ),
    }


def build_self_checks(parameters: dict, corpus: dict, write_path: dict, first_document_chunks: int | None, search_section: dict, graph_section: dict, cleanup: dict, kept: bool) -> list[dict]:
    checks: list[dict] = []
    checks.append(
        {
            "id": "documents-ready",
            "passed": write_path["documentsReady"] == parameters["documents"],
            "detail": f"{write_path['documentsReady']}/{parameters['documents']} documents reached EVIDENCE_READY",
        }
    )
    expected = corpus["linesPerDocument"]
    checks.append(
        {
            "id": "chunk-count",
            "passed": first_document_chunks == expected,
            "detail": f"first document chunks={first_document_chunks} expected={expected}",
        }
    )
    stages_with_data = [name for name, stats in write_path["stages"].items() if stats["count"] > 0]
    checks.append(
        {
            "id": "stage-timings",
            "passed": bool(stages_with_data),
            "detail": "stage timings captured for " + (", ".join(stages_with_data) if stages_with_data else "no stage"),
        }
    )
    checks.append(
        {
            "id": "search-non-empty",
            "passed": search_section["emptyResultCount"] == 0,
            "detail": f"{search_section['emptyResultCount']} empty result(s) across {search_section['samples']} search call(s)",
        }
    )
    marker_hits = sum(entry["expectedDocumentHits"] for entry in search_section["perQuery"] if entry["family"] == "marker")
    checks.append(
        {
            "id": "search-marker-document",
            "passed": marker_hits > 0,
            "detail": f"marker queries returned the source document {marker_hits} time(s)",
        }
    )
    if graph_section.get("enabled"):
        unresolved = [entry for entry in graph_section["knownSeeds"] if entry["nodes"] == 0]
        checks.append(
            {
                "id": "graph-known-seeds",
                "passed": not unresolved,
                "detail": f"{len(unresolved)} known-seed query(ies) returned zero nodes",
            }
        )
        unknown = graph_section["unknownSeeds"]
        checks.append(
            {
                "id": "graph-unknown-seeds",
                "passed": unknown["emptyCount"] == unknown["samples"] and unknown["snapshotPresentCount"] == unknown["samples"],
                "detail": f"{unknown['emptyCount']}/{unknown['samples']} unknown seeds returned an empty graph with snapshot",
            }
        )
    if parameters["graphEnabled"]:
        checks.append(
            {
                "id": "graph-relations",
                "passed": graph_section.get("projection", {}).get("snapshot", {}).get("relations", 0) > 0,
                "detail": f"snapshot relations={graph_section.get('projection', {}).get('snapshot', {}).get('relations', 0)}",
            }
        )
    checks.append(
        {
            "id": "cleanup",
            "passed": None if kept else bool(cleanup.get("verifiedAbsent")),
            "detail": "knowledge base kept for inspection" if kept else f"GET after delete returned {cleanup.get('verifyStatus')}",
        }
    )
    return checks


def dynamic_limitations(parameters: dict, graph_section: dict, storage: dict) -> list[str]:
    limitations = [
        "Latency and throughput come from one sequential client against one freshly built index; HNSW construction and ingestion-time embedding variation can move results between rebuilds, so treat this report as one sample and repeat it under identical conditions before comparing runs.",
        "All requests are sequential from a single dev user and paced at roughly 400/minute to stay below RATE_LIMIT_PER_MINUTE; no concurrent load, queue saturation, multi-tenant authorization fan-out or cache pressure is exercised.",
        "Document readiness is observed through one list request per second, so ready and ingestion wall times carry up to one second of observation granularity.",
        "Search timing starts only after a warmup sweep returned a non-empty result for every query; the settle time is reported as search.settleMs and excluded from the latency samples, because the BM25 index can briefly reference tuples under maintenance right after ingestion or deletion.",
        "The local compose stack is the measurement target; absolute values do not transfer to production hardware. Compare runs only against each other on the same machine and configuration.",
        "Generation and chat answers are out of scope: only search, graph query and ingestion are timed, so LLM answer latency and token cost are not part of this baseline.",
    ]
    if graph_section.get("enabled"):
        model = graph_section.get("projection", {}).get("model", "")
        if model.startswith("local"):
            limitations.append(
                "Relation extraction ran with the deterministic local chat adapter, so projection time excludes provider latency and the graph holds the seeded triples; a remote extractor adds chat calls per evidence batch."
            )
        else:
            limitations.append(
                "Relation extraction used a remote chat adapter; projection wall time includes provider latency and the model may add relations beyond the seeded triples."
            )
    else:
        limitations.append("Graph measurement was disabled (--no-graph); graph latency and relation scale evidence is missing from this run.")
    if storage.get("available"):
        limitations.append(
            "Database and table byte sizes cover the whole development database, while row counts are scoped to the throwaway knowledge base; other knowledge bases inflate the byte figures."
        )
    else:
        limitations.append(
            "The Postgres storage probe was unavailable, so storage footprint and the embedding model in use are not recorded in this run."
        )
    limitations.append(
        "Default graph queries use limit 40 and depths 1-3; larger relation fans, deeper traversal or other seeds are not measured."
    )
    return limitations


def validate_latency_shape(stats: dict, label: str) -> list[str]:
    errors: list[str] = []
    if not isinstance(stats, dict):
        return [f"{label} must be an object"]
    if stats.get("count", 0) == 0:
        return [f"{label} has no samples"]
    p50, p95 = stats.get("p50"), stats.get("p95")
    mean, low, high = stats.get("mean"), stats.get("min"), stats.get("max")
    for name, value in (("p50", p50), ("p95", p95), ("mean", mean), ("min", low), ("max", high)):
        if not isinstance(value, (int, float)):
            errors.append(f"{label}.{name} must be numeric")
    if errors:
        return errors
    if p50 > p95 + 0.11:
        errors.append(f"{label}.p50 {p50} exceeds p95 {p95}")
    if mean < low - 0.11 or mean > high + 0.11:
        errors.append(f"{label}.mean {mean} outside min/max")
    return errors


def validate_report(report: dict) -> list[str]:
    errors: list[str] = []
    required = ("schemaVersion", "generatedAt", "baseUrl", "knowledgeBaseId", "kept", "parameters", "corpus",
                "writePath", "search", "graph", "storageProbes", "gateEvidence", "selfChecks", "limitations")
    for key in required:
        if key not in report:
            errors.append(f"missing top-level key {key}")
    if errors:
        return errors
    if report["schemaVersion"] != SCHEMA_REPORT:
        errors.append(f"schemaVersion must be {SCHEMA_REPORT}")

    parameters = report["parameters"]
    for key in ("documents", "linesPerDocument", "queries", "repeats", "ingestTimeout", "projectionTimeout", "seed"):
        value = parameters.get(key)
        if not isinstance(value, int) or value < 1:
            errors.append(f"parameters.{key} must be a positive integer")
    if not isinstance(parameters.get("graphEnabled"), bool):
        errors.append("parameters.graphEnabled must be a boolean")
    depths = parameters.get("depths")
    if not isinstance(depths, list) or not depths or any(not isinstance(depth, int) or depth < 1 or depth > MAX_GRAPH_DEPTH for depth in depths):
        errors.append(f"parameters.depths must be integers in 1..{MAX_GRAPH_DEPTH}")

    corpus = report["corpus"]
    if not isinstance(corpus.get("expectedChunks"), int) or corpus["expectedChunks"] < parameters.get("documents", 0):
        errors.append("corpus.expectedChunks must be an integer >= documents")

    write_path = report["writePath"]
    if write_path.get("documentsReady") != parameters.get("documents"):
        errors.append("writePath.documentsReady must equal parameters.documents")
    if not isinstance(write_path.get("ingestionWallMs"), (int, float)) or write_path.get("ingestionWallMs", 0) <= 0:
        errors.append("writePath.ingestionWallMs must be positive")
    errors.extend(validate_latency_shape(write_path.get("uploadLatencyMs") or {}, "writePath.uploadLatencyMs"))
    errors.extend(validate_latency_shape(write_path.get("readyLatencyMs") or {}, "writePath.readyLatencyMs"))

    search_section = report["search"]
    if search_section.get("samples") != parameters.get("queries", 0) * parameters.get("repeats", 0):
        errors.append("search.samples must equal queries * repeats")
    errors.extend(validate_latency_shape(search_section.get("latencyMs") or {}, "search.latencyMs"))
    if not isinstance(search_section.get("perQuery"), list) or len(search_section.get("perQuery") or []) != parameters.get("queries"):
        errors.append("search.perQuery must have one entry per query")

    graph_section = report["graph"]
    if not isinstance(graph_section.get("enabled"), bool):
        errors.append("graph.enabled must be a boolean")
    if graph_section.get("enabled"):
        projection_wall = graph_section.get("projection", {}).get("wallMs")
        if not isinstance(projection_wall, (int, float)) or projection_wall < 0:
            errors.append("graph.projection.wallMs must be a non-negative number")
        groups = graph_section.get("queryGroups")
        if not isinstance(groups, list) or not groups:
            errors.append("graph.queryGroups must not be empty when graph is enabled")
        else:
            for group in groups:
                errors.extend(validate_latency_shape(group.get("latencyMs") or {}, f"graph.depth{group.get('depth')}.latencyMs"))
        snapshot = graph_section.get("projection", {}).get("snapshot") or {}
        if not isinstance(snapshot.get("relations"), int):
            errors.append("graph.projection.snapshot.relations must be an integer")
        unknown = graph_section.get("unknownSeeds") or {}
        if unknown.get("emptyCount") != unknown.get("samples"):
            errors.append("graph.unknownSeeds must be empty on every probe")

    gate = report["gateEvidence"]
    gate_ids = [candidate.get("id") for candidate in gate.get("candidates") or []]
    if gate_ids != [candidate["id"] for candidate in GATE_CANDIDATES]:
        errors.append("gateEvidence.candidates must list the seven MILESTONES M8 candidates in order")
    if gate.get("startedCandidates") != []:
        errors.append("gateEvidence.startedCandidates must stay empty; the baseline never starts a candidate")

    checks = report["selfChecks"]
    if not isinstance(checks, list) or not checks:
        errors.append("selfChecks must not be empty")
    else:
        for check in checks:
            if not check.get("id") or not isinstance(check.get("detail"), str):
                errors.append("each selfCheck needs id and detail")
            if not isinstance(check.get("passed"), bool) and check.get("passed") is not None:
                errors.append(f"selfCheck {check.get('id')} passed must be boolean or null")

    if not isinstance(report["limitations"], list) or not report["limitations"]:
        errors.append("limitations must not be empty")
    return errors


def synthetic_report() -> dict:
    stats = latency_stats([10.0, 20.0, 30.0])
    return {
        "schemaVersion": SCHEMA_REPORT,
        "generatedAt": "2026-01-01T00:00:00+00:00",
        "baseUrl": "http://localhost:8082",
        "knowledgeBaseId": "kb-test",
        "kept": False,
        "parameters": {
            "documents": 2,
            "linesPerDocument": 6,
            "queries": 2,
            "repeats": 2,
            "depths": [1, 2],
            "graphEnabled": True,
            "ingestTimeout": 60,
            "projectionTimeout": 60,
            "seed": 1,
        },
        "corpus": {"expectedChunks": 12},
        "writePath": {
            "documentsReady": 2,
            "ingestionWallMs": 100.0,
            "uploadLatencyMs": stats,
            "readyLatencyMs": stats,
        },
        "search": {
            "samples": 4,
            "latencyMs": stats,
            "perQuery": [{"id": "marker-0001"}, {"id": "topical-0002"}],
        },
        "graph": {
            "enabled": True,
            "projection": {"wallMs": 5.0, "snapshot": {"relations": 4}},
            "queryGroups": [{"depth": 1, "latencyMs": stats}],
            "unknownSeeds": {"samples": 1, "emptyCount": 1},
        },
        "storageProbes": {"available": False},
        "gateEvidence": {
            "candidates": [{"id": candidate["id"]} for candidate in GATE_CANDIDATES],
            "startedCandidates": [],
        },
        "selfChecks": [{"id": "documents-ready", "passed": True, "detail": "2/2"}],
        "limitations": ["synthetic"],
    }


def run_self_test() -> int:
    failures: list[str] = []

    def check(name: str, condition: bool, detail: str = "") -> None:
        if condition:
            print(f"  ok   {name}")
        else:
            failures.append(name)
            print(f"  fail {name} {detail}")

    check("percentile-empty", percentile([], 0.5) is None)
    check("percentile-single", percentile([10], 0.95) == 10)
    check("percentile-p50", percentile([10, 20, 30, 40], 0.5) == 20)
    check("percentile-p95", percentile([10, 20, 30, 40], 0.95) == 40)
    stats = latency_stats([1, 2, 3, 4])
    check("latency-mean", stats["mean"] == 2.5, str(stats))
    check("latency-shape", not validate_latency_shape(stats, "test"), str(validate_latency_shape(stats, "test")))

    corpus_a = build_corpus(3, 6, 7)
    corpus_b = build_corpus(3, 6, 7)
    check("corpus-deterministic", corpus_a["fixtures"] == corpus_b["fixtures"])
    check("corpus-chunks", corpus_a["expectedChunks"] == 18, str(corpus_a["expectedChunks"]))
    check("corpus-unique-markers", len({fixture["marker"] for fixture in corpus_a["fixtures"]}) == 3)
    check("corpus-line-limit", all(len(line) <= MAX_SOURCE_LINE_RUNES for fixture in corpus_a["fixtures"] for line in fixture["content"].splitlines()))

    queries = build_queries(corpus_a["fixtures"], 4)
    check("queries-count", len(queries) == 4, str(len(queries)))
    check("queries-unique-ids", len({query["id"] for query in queries}) == 4)
    check("queries-families", {query["family"] for query in queries} == {"marker", "topical"})

    report = synthetic_report()
    check("report-valid", not validate_report(report), str(validate_report(report)))
    broken = json.loads(json.dumps(report))
    broken["search"]["samples"] = 999
    check("report-rejects-bad-samples", any("samples" in error for error in validate_report(broken)))
    broken = json.loads(json.dumps(report))
    broken["graph"]["unknownSeeds"]["emptyCount"] = 0
    check("report-rejects-non-empty-unknown", any("unknownSeeds" in error for error in validate_report(broken)))
    broken = json.loads(json.dumps(report))
    broken["gateEvidence"]["startedCandidates"] = ["graph-database"]
    check("report-rejects-started-candidate", any("startedCandidates" in error for error in validate_report(broken)))

    print(f"\nself-test: {len(failures)} failure(s)")
    return 1 if failures else 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Measure the M8 capacity baseline on the local stack.")
    parser.add_argument("--base-url", default=f"http://localhost:{os.environ.get('API_PORT', '8082')}")
    parser.add_argument("--documents", type=int, default=40, help="number of generated documents (default 40)")
    parser.add_argument("--lines", type=int, default=10, help="lines per document; each non-blank line becomes a chunk (default 10)")
    parser.add_argument("--queries", type=int, default=12, help="search queries per run, split between marker and topical families")
    parser.add_argument("--repeats", type=int, default=5, help="repetitions per query and per graph probe")
    parser.add_argument("--depths", default="1,2,3", help="graph query depths, comma separated (max 3)")
    parser.add_argument("--seed", type=int, default=20261028, help="deterministic corpus seed")
    parser.add_argument("--ingest-timeout", type=int, default=1800, help="seconds to wait for all documents to reach EVIDENCE_READY")
    parser.add_argument("--search-settle-timeout", type=int, default=60, help="seconds to wait for a warmup sweep with no empty search result")
    parser.add_argument("--projection-timeout", type=int, default=1200, help="seconds to wait for RELATION_READY")
    parser.add_argument("--no-graph", action="store_true", help="skip relation projection and graph latency measurement")
    parser.add_argument("--no-storage-probe", action="store_true", help="skip the docker compose Postgres storage probe")
    parser.add_argument("--compose-file", type=Path, default=DEFAULT_COMPOSE_FILE)
    parser.add_argument("--env-file", type=Path, default=DEFAULT_ENV_FILE)
    parser.add_argument("--report", type=Path, default=DEFAULT_REPORT)
    parser.add_argument("--keep", action="store_true", help="keep the generated knowledge base for inspection")
    parser.add_argument("--self-test", action="store_true", help="run internal checks without contacting the stack")
    parser.add_argument("--validate-report", type=Path, help="validate an existing report file")
    arguments = parser.parse_args()

    if arguments.self_test:
        return run_self_test()
    if arguments.validate_report:
        try:
            report = json.loads(arguments.validate_report.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            print(f"cannot read report: {error}", file=sys.stderr)
            return 2
        errors = validate_report(report)
        if errors:
            for error in errors:
                print(f"report validation: {error}", file=sys.stderr)
            return 2
        print(f"report valid: {report['schemaVersion']} from {arguments.validate_report}")
        return 0

    try:
        depths = parse_depths(arguments.depths)
    except ValueError as error:
        print(f"--depths: {error}", file=sys.stderr)
        return 2
    if arguments.documents < 1 or arguments.queries < 1 or arguments.repeats < 1:
        print("--documents, --queries and --repeats must be positive", file=sys.stderr)
        return 2
    if arguments.lines < 6:
        print("--lines must be at least 6", file=sys.stderr)
        return 2

    corpus = build_corpus(arguments.documents, arguments.lines, arguments.seed)
    queries = build_queries(corpus["fixtures"], arguments.queries)
    print(
        f"corpus: {arguments.documents} documents / {corpus['expectedChunks']} expected chunks / "
        f"{corpus['bytes']} bytes / {len(queries)} queries / depths {depths}"
    )

    client = Client(arguments.base_url)
    status, _ = client.json("GET", "/healthz")
    if status != 200:
        print(f"stack is not reachable at {arguments.base_url}", file=sys.stderr)
        return 2
    client.login()
    client = PacedClient(client)

    suffix = datetime.now(timezone.utc).strftime("%Y%m%d%H%M%S")
    status, knowledge_base = client.json(
        "POST",
        "/api/v1/knowledge-bases",
        {"name": f"m8-capacity-{suffix}", "description": "M8 capacity baseline fixture"},
    )
    if status not in (200, 201):
        raise RuntimeError(f"create knowledge base failed: {status} {knowledge_base}")
    knowledge_base_id = knowledge_base["id"]
    print(f"knowledge base {knowledge_base_id}")

    parameters = {
        "documents": arguments.documents,
        "linesPerDocument": arguments.lines,
        "queries": len(queries),
        "repeats": arguments.repeats,
        "depths": depths,
        "graphEnabled": not arguments.no_graph,
        "ingestTimeout": arguments.ingest_timeout,
        "searchSettleTimeout": arguments.search_settle_timeout,
        "projectionTimeout": arguments.projection_timeout,
        "seed": arguments.seed,
        "storageProbe": not arguments.no_storage_probe,
        "requestIntervalMs": round(REQUEST_INTERVAL_SECONDS * 1000),
    }

    cleanup = {"deleted": False, "verifiedAbsent": None, "verifyStatus": None}
    try:
        write_path = measure_write_path(client, knowledge_base_id, corpus, arguments.ingest_timeout)
        print(
            f"  ingestion: {write_path['documentsReady']}/{arguments.documents} ready in "
            f"{write_path['ingestionWallMs']:.0f}ms ({write_path['documentsPerSecond']} docs/s, "
            f"{write_path['chunksPerSecond']} chunks/s)"
        )
        first_document_id = write_path["firstDocumentId"]
        first_document_chunks = count_document_chunks(client, first_document_id) if first_document_id else None
        print(f"  first document chunks: {first_document_chunks} (expected {arguments.lines})")

        search_section = measure_search(
            client, knowledge_base_id, queries, arguments.repeats, arguments.search_settle_timeout
        )
        print(
            f"  search: settled after {search_section['settleMs']:.0f}ms; {search_section['samples']} calls, "
            f"p50 {search_section['latencyMs']['p50']}ms, p95 {search_section['latencyMs']['p95']}ms, "
            f"empty {search_section['emptyResultCount']}"
        )

        if arguments.no_graph:
            graph_section = {"enabled": False}
            print("  graph: skipped (--no-graph)")
        else:
            graph_section = measure_graph(
                client, knowledge_base_id, corpus["fixtures"], depths, arguments.repeats, arguments.projection_timeout
            )
            group_summary = ", ".join(
                f"d{group['depth']} p50 {group['latencyMs']['p50']}ms" for group in graph_section["queryGroups"]
            )
            print(
                f"  graph: projection {graph_section['projection']['wallMs']:.0f}ms, "
                f"{graph_section['projection']['snapshot']['relations']} relations, {group_summary}"
            )

        if arguments.no_storage_probe:
            storage = {"available": False, "reason": "storage probe disabled"}
        else:
            storage = probe_storage(knowledge_base_id, arguments.compose_file, arguments.env_file)
        print(f"  storage probe: {'available' if storage.get('available') else 'unavailable'}")
    finally:
        if arguments.keep:
            cleanup = {"deleted": False, "verifiedAbsent": None, "verifyStatus": None, "kept": True}
        else:
            cleanup = delete_and_verify(client, knowledge_base_id)
            print(f"  cleanup: delete={cleanup['deleted']} verifiedAbsent={cleanup['verifiedAbsent']}")

    self_checks = build_self_checks(
        parameters, corpus, write_path, first_document_chunks, search_section, graph_section, cleanup, arguments.keep
    )
    report = {
        "schemaVersion": SCHEMA_REPORT,
        "generatedAt": datetime.now(timezone.utc).isoformat(),
        "baseUrl": arguments.base_url,
        "knowledgeBaseId": knowledge_base_id,
        "kept": bool(arguments.keep),
        "parameters": parameters,
        "corpus": {
            "documents": corpus["documents"],
            "linesPerDocument": corpus["linesPerDocument"],
            "expectedChunks": corpus["expectedChunks"],
            "bytes": corpus["bytes"],
            "seed": corpus["seed"],
            "firstDocumentChunks": first_document_chunks,
        },
        "writePath": write_path,
        "search": search_section,
        "graph": graph_section,
        "storageProbes": storage,
        "gateEvidence": build_gate_evidence(parameters, write_path, search_section, graph_section, storage),
        "cleanup": cleanup,
        "selfChecks": self_checks,
        "limitations": dynamic_limitations(parameters, graph_section, storage),
    }
    errors = validate_report(report)
    if errors:
        for error in errors:
            print(f"report validation: {error}", file=sys.stderr)
        return 2
    arguments.report.parent.mkdir(parents=True, exist_ok=True)
    arguments.report.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    failed = [check["id"] for check in self_checks if check["passed"] is False]
    print(f"\nreport: {arguments.report}")
    print(
        f"write {write_path['documentsPerSecond']} docs/s, {write_path['chunksPerSecond']} chunks/s; "
        f"search p50/p95 {search_section['latencyMs']['p50']}/{search_section['latencyMs']['p95']}ms"
    )
    if graph_section.get("enabled"):
        print(
            "graph p50 by depth: "
            + ", ".join(f"d{group['depth']} {group['latencyMs']['p50']}ms" for group in graph_section["queryGroups"])
        )
    gate_collected = [candidate["id"] for candidate in report["gateEvidence"]["candidates"] if candidate["evidenceStatus"].startswith("capacity")]
    print(f"gate evidence collected for: {', '.join(gate_collected) if gate_collected else 'none'}")
    if failed:
        print(f"self-check failures: {', '.join(failed)}", file=sys.stderr)
        return 1
    print("self-checks passed")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except RuntimeError as error:
        print(f"m8 capacity failed: {error}", file=sys.stderr)
        sys.exit(1)
