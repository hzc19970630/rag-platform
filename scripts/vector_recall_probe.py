#!/usr/bin/env python3
"""Measure the multi-tenant vector recall trade-off for hnsw.ef_search and
over-sampling.

The probe builds a throwaway schema with the same query shape and index shape
as `internal/platform/vector/search.go`, then runs a small matrix of
(ef_search, oversample, iterative_scan) against a 100:1 tenant split. It never
touches the application's chunks/documents tables and drops its schema on exit.

The output is a schema-versioned JSON report. `EXPLAIN (ANALYZE, BUFFERS,
FORMAT JSON)` is captured for every sample so a result that looks good only
because the planner chose a sequential scan is visible as such.
"""

from __future__ import annotations

import argparse
import json
import os
import shlex
import statistics
import subprocess
import sys
import time
from pathlib import Path

REPORT_SCHEMA = "VectorRecallProbeV1"
PROBE_MODEL = "probe-model"
MAJORITY_TENANT = "tenant-a"
MAJORITY_KB = "kb-a"
MINORITY_TENANT = "tenant-b"
MINORITY_KB = "kb-b"
RELEVANT_COUNT = 5


def repo_root() -> Path:
    return Path(__file__).resolve().parent.parent


def default_psql_command() -> list[str]:
    root = repo_root()
    return [
        "docker",
        "compose",
        "--env-file",
        str(root / ".env"),
        "-f",
        str(root / "deploy/docker-compose/docker-compose.yml"),
        "exec",
        "-T",
        "postgres",
        "psql",
        "-U",
        "rag_platform",
        "-d",
        "rag_platform",
    ]


def run_sql(command: list[str], sql: str, timeout: int = 900) -> str:
    result = subprocess.run(
        command + ["-At", "-v", "ON_ERROR_STOP=1", "-f", "-"],
        input=sql,
        text=True,
        capture_output=True,
        timeout=timeout,
    )
    if result.returncode != 0:
        raise RuntimeError(
            "psql failed:\n" + (result.stderr or result.stdout or "").strip()
        )
    return result.stdout


def quote_list(values: list[str]) -> str:
    return ", ".join("'" + value.replace("'", "''") + "'" for value in values)


def percentile(samples: list[float], fraction: float) -> float:
    if not samples:
        return 0.0
    ordered = sorted(samples)
    index = min(len(ordered) - 1, max(0, int(round((len(ordered) - 1) * fraction))))
    return round(ordered[index], 3)


def plan_kinds(plan_json: list[dict]) -> dict:
    kinds = set()
    indexes = set()

    def walk(node):
        if not isinstance(node, dict):
            return
        node_type = str(node.get("Node Type", ""))
        if node_type:
            kinds.add(node_type)
        index_name = node.get("Index Name")
        if index_name:
            indexes.add(str(index_name))
        for child in node.get("Plans") or []:
            walk(child)

    if plan_json:
        walk(plan_json[0].get("Plan") or {})
    if any("vector_idx" in name for name in indexes):
        kind = "hnsw-index-scan"
    elif any("Bitmap" in name for name in kinds):
        kind = "bitmap-scan"
    elif any(name.startswith("Seq Scan") for name in kinds):
        kind = "seq-scan"
    else:
        kind = "other"
    return {"kind": kind, "nodeTypes": sorted(kinds), "indexes": sorted(indexes)}


def build_recall_sql(dimension: int, limit: int, relevant_ids: list[str]) -> str:
    """The production query shape, with a centroid query vector over the
    relevant minority chunks."""
    relevant = quote_list(relevant_ids)
    return f"""
SELECT c.id AS chunk_id
FROM chunks c
JOIN document_versions dv ON dv.id = c.document_version_id
JOIN documents d ON d.id = dv.document_id
WHERE c.tenant_id = '{MINORITY_TENANT}'
  AND c.knowledge_base_id = '{MINORITY_KB}'
  AND c.status = 'EVIDENCE_READY'
  AND c.parent_id IS NOT NULL
  AND c.embedding_dimension = {dimension}
  AND c.embedding_model = '{PROBE_MODEL}'
  AND c.embedding IS NOT NULL
  AND d.latest_version_id = c.document_version_id
  AND d.status = 'EVIDENCE_READY'
  AND d.deleted_at IS NULL
ORDER BY c.embedding::halfvec({dimension}) <=> (
    SELECT avg(embedding::vector)::halfvec({dimension})
    FROM chunks
    WHERE id IN ({relevant})
) ASC
LIMIT {limit}
""".strip()


def metrics_sql(recall_sql: str, top_k: int, relevant_ids: list[str]) -> str:
    relevant = quote_list(relevant_ids)
    return f"""
SELECT json_build_object(
    'returned', count(*),
    'relevantHits', count(*) FILTER (WHERE chunk_id IN ({relevant})),
    'targetHit', coalesce(bool_or(chunk_id = '{relevant_ids[0]}'), false)
)
FROM (
    SELECT chunk_id FROM ({recall_sql}) recall LIMIT {top_k}
) visible;
""".strip()


def setup_sql(schema: str, dimension: int, majority: int, minority: int) -> str:
    return f"""
SET search_path TO {schema}, public;
CREATE TABLE documents (
    id text PRIMARY KEY,
    status text NOT NULL,
    latest_version_id text NOT NULL,
    deleted_at timestamptz
);
CREATE TABLE document_versions (
    id text PRIMARY KEY,
    document_id text NOT NULL
);
CREATE TABLE chunks (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    knowledge_base_id text NOT NULL,
    document_version_id text NOT NULL,
    parent_id text,
    status text NOT NULL,
    embedding_dimension integer NOT NULL,
    embedding_model text NOT NULL,
    embedding halfvec
);
INSERT INTO documents (id, status, latest_version_id) VALUES
    ('doc-a', 'EVIDENCE_READY', 'dv-a'),
    ('doc-b', 'EVIDENCE_READY', 'dv-b');
CREATE FUNCTION probe_vec(dim integer) RETURNS halfvec AS $$
DECLARE
    vec_values real[];
BEGIN
    SELECT array_agg((random() * 2 - 1)::real) INTO vec_values
    FROM generate_series(1, dim);
    RETURN vec_values::halfvec;
END;
$$ LANGUAGE plpgsql VOLATILE;
INSERT INTO document_versions (id, document_id) VALUES
    ('dv-a', 'doc-a'),
    ('dv-b', 'doc-b');
SELECT setseed(0.42);
INSERT INTO chunks (id, tenant_id, knowledge_base_id, document_version_id, parent_id, status, embedding_dimension, embedding_model, embedding)
SELECT 'major-' || i, '{MAJORITY_TENANT}', '{MAJORITY_KB}', 'dv-a', 'parent', 'EVIDENCE_READY', {dimension}, '{PROBE_MODEL}', probe_vec({dimension})
FROM generate_series(1, {majority}) i;
INSERT INTO chunks (id, tenant_id, knowledge_base_id, document_version_id, parent_id, status, embedding_dimension, embedding_model, embedding)
SELECT 'minor-' || lpad(i::text, 4, '0'), '{MINORITY_TENANT}', '{MINORITY_KB}', 'dv-b', 'parent', 'EVIDENCE_READY', {dimension}, '{PROBE_MODEL}', probe_vec({dimension})
FROM generate_series(1, {minority}) i;
CREATE INDEX probe_chunks_vector_idx ON chunks
    USING hnsw ((embedding::halfvec({dimension})) halfvec_cosine_ops)
    WITH (m = 16, ef_construction = 64)
    WHERE embedding_dimension = {dimension};
CREATE INDEX probe_chunks_scope_idx ON chunks(tenant_id, knowledge_base_id, document_version_id, status);
CREATE INDEX probe_chunks_embedding_model_idx ON chunks(embedding_model, embedding_dimension);
ANALYZE chunks;
SELECT json_build_object(
    'rows', (SELECT count(*) FROM chunks),
    'distinctMajorityVectors', (SELECT count(DISTINCT md5(embedding::text)) FROM chunks WHERE tenant_id = '{MAJORITY_TENANT}'),
    'distinctMinorityVectors', (SELECT count(DISTINCT md5(embedding::text)) FROM chunks WHERE tenant_id = '{MINORITY_TENANT}')
);
""".strip()


def probe_sql(schema: str, dimension: int, top_k: int, limit: int, relevant_ids: list[str],
              ef_search: int, oversample: int, iterative_scan: str, force_hnsw: bool) -> str:
    del oversample  # encoded in limit by the caller
    recall = build_recall_sql(dimension, limit, relevant_ids)
    settings = [f"SET search_path TO {schema}, public;", "SELECT '[0]'::vector;"]
    if ef_search > 0:
        settings.append(f"SET LOCAL hnsw.ef_search = {ef_search};")
    if iterative_scan:
        settings.append(f"SET LOCAL hnsw.iterative_scan = {iterative_scan};")
    if force_hnsw:
        # A selective tenant filter plus a cheap sort can beat HNSW on a small
        # corpus. Disabling sort makes the ordered HNSW path the only way to
        # satisfy ORDER BY, so the probe measures the path under test.
        settings.append("SET LOCAL enable_seqscan = off;")
        settings.append("SET LOCAL enable_bitmapscan = off;")
        settings.append("SET LOCAL enable_sort = off;")
    plan = f"EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) {recall};"
    metrics = metrics_sql(recall, top_k, relevant_ids)
    return "\n".join(
        [
            "BEGIN;",
            *settings,
            "SELECT '__PLAN_START__';",
            plan,
            "SELECT '__PLAN_END__';",
            "SELECT '__RESULT_START__';",
            metrics + ";",
            "SELECT '__RESULT_END__';",
            "COMMIT;",
        ]
    )


def parse_probe_output(stdout: str) -> tuple[list[dict], dict]:
    lines = stdout.splitlines()
    try:
        plan_start = lines.index("__PLAN_START__")
        plan_end = lines.index("__PLAN_END__")
        result_start = lines.index("__RESULT_START__")
        result_end = lines.index("__RESULT_END__")
    except ValueError as exc:
        raise RuntimeError(f"probe output is missing a marker:\n{stdout}") from exc
    plan_json = json.loads("\n".join(lines[plan_start + 1:plan_end]))
    result_json = json.loads("\n".join(lines[result_start + 1:result_end]))
    return plan_json, result_json


def summarize(samples: list[dict], top_k: int) -> dict:
    returned = [sample["returned"] for sample in samples]
    latencies = [sample["latencyMs"] for sample in samples]
    relevant = [sample["relevantHits"] for sample in samples]
    return {
        "samples": len(samples),
        "returned": {
            "min": min(returned) if returned else 0,
            "mean": round(statistics.fmean(returned), 3) if returned else 0,
            "max": max(returned) if returned else 0,
        },
        "underfillRate": round(sum(1 for value in returned if value < top_k) / len(samples), 4) if samples else 0,
        "targetHitRate": round(sum(1 for sample in samples if sample["targetHit"]) / len(samples), 4) if samples else 0,
        "relevantRecall": round(
            statistics.fmean([hits / RELEVANT_COUNT for hits in relevant]), 4
        ) if samples else 0,
        "latencyMs": {
            "p50": percentile(latencies, 0.50),
            "p95": percentile(latencies, 0.95),
            "mean": round(statistics.fmean(latencies), 3) if latencies else 0,
        },
        "planKinds": sorted({sample["planKind"] for sample in samples}),
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--psql-cmd", default="", help="override the psql command, e.g. 'psql -U rag_platform -d rag_platform'")
    parser.add_argument("--dimension", type=int, default=1024)
    parser.add_argument("--majority", type=int, default=10000)
    parser.add_argument("--minority", type=int, default=100)
    parser.add_argument("--top-k", type=int, default=20)
    parser.add_argument("--ef-search", default="40,100,200,500")
    parser.add_argument("--oversample", default="1,4,10")
    parser.add_argument("--iterative-scan", default="off,relaxed_order")
    parser.add_argument("--repeats", type=int, default=5)
    parser.add_argument("--report", default=str(repo_root() / "tests/eval/vector_recall_report.json"))
    parser.add_argument("--keep", action="store_true", help="keep the probe schema for inspection")
    arguments = parser.parse_args()

    command = shlex.split(arguments.psql_cmd) if arguments.psql_cmd else default_psql_command()
    ef_values = [int(value) for value in arguments.ef_search.split(",") if value.strip()]
    oversample_values = [int(value) for value in arguments.oversample.split(",") if value.strip()]
    iterative_values = [value.strip() for value in arguments.iterative_scan.split(",") if value.strip()]
    top_k = max(1, arguments.top_k)
    repeats = max(1, arguments.repeats)
    relevant_ids = [f"minor-{index:04d}" for index in range(1, RELEVANT_COUNT + 1)]

    schema = f"vector_probe_{os.getpid()}_{int(time.time())}"
    report_path = Path(arguments.report)
    report_path.parent.mkdir(parents=True, exist_ok=True)
    started = time.time()

    try:
        version_output = run_sql(command, "SELECT extversion FROM pg_extension WHERE extname = 'vector';")
        pgvector_version = version_output.strip().splitlines()[-1] if version_output.strip() else "unknown"
        run_sql(command, f"CREATE SCHEMA {schema};")
        setup_output = run_sql(command, setup_sql(schema, arguments.dimension, arguments.majority, arguments.minority))
        setup = json.loads(setup_output.strip().splitlines()[-1])
        if setup["distinctMajorityVectors"] < arguments.majority * 0.9:
            raise RuntimeError(
                "the LATERAL vector generator was evaluated once instead of per row: "
                + json.dumps(setup)
            )

        results = []
        for force_hnsw in (False, True):
            for ef_search in ef_values:
                for oversample in oversample_values:
                    for iterative_scan in iterative_values:
                        limit = top_k * oversample
                        samples = []
                        for _ in range(repeats):
                            sql = probe_sql(
                                schema, arguments.dimension, top_k, limit, relevant_ids,
                                ef_search, oversample, iterative_scan, force_hnsw,
                            )
                            plan_json, result = parse_probe_output(run_sql(command, sql))
                            plan = plan_kinds(plan_json)
                            samples.append(
                                {
                                    "returned": int(result.get("returned") or 0),
                                    "relevantHits": int(result.get("relevantHits") or 0),
                                    "targetHit": bool(result.get("targetHit")),
                                    "latencyMs": float(plan_json[0].get("Execution Time", 0.0)),
                                    "planKind": plan["kind"],
                                }
                            )
                        summary = summarize(samples, top_k)
                        summary.update(
                            {
                                "forcedHNSW": force_hnsw,
                                "efSearch": ef_search,
                                "oversample": oversample,
                                "iterativeScan": iterative_scan,
                                "sqlLimit": limit,
                            }
                        )
                        results.append(summary)
                        print(
                            f"forced={force_hnsw} ef={ef_search} oversample={oversample} "
                            f"scan={iterative_scan} returned={summary['returned']['mean']} "
                            f"underfill={summary['underfillRate']} relevantRecall={summary['relevantRecall']} "
                            f"p95={summary['latencyMs']['p95']}ms plan={','.join(summary['planKinds'])}"
                        )

        eligible = [
            result for result in results
            if result["forcedHNSW"]
            and result["underfillRate"] == 0
            and result["targetHitRate"] == 1.0
            and result["relevantRecall"] == 1.0
            and result["latencyMs"]["p95"] <= 100
        ]
        recommended = None
        if eligible:
            best = min(eligible, key=lambda item: (item["efSearch"], item["oversample"], item["latencyMs"]["p95"]))
            recommended = {
                "efSearch": best["efSearch"],
                "oversample": best["oversample"],
                "iterativeScan": best["iterativeScan"],
                "p95Ms": best["latencyMs"]["p95"],
                "reason": "forced-HNSW run met underfill=0, target/relevant recall=1.0 and p95<=100ms",
            }

        report = {
            "schemaVersion": REPORT_SCHEMA,
            "generatedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "durationSeconds": round(time.time() - started, 1),
            "pgvectorVersion": pgvector_version,
            "corpus": {
                "dimension": arguments.dimension,
                "majorityDocuments": arguments.majority,
                "minorityDocuments": arguments.minority,
                "minorityShare": round(arguments.minority / (arguments.majority + arguments.minority), 6),
                "topK": top_k,
                "relevantChunks": relevant_ids,
                "distinctMajorityVectors": setup["distinctMajorityVectors"],
                "distinctMinorityVectors": setup["distinctMinorityVectors"],
            },
            "matrix": {
                "efSearch": ef_values,
                "oversample": oversample_values,
                "iterativeScan": iterative_values,
                "repeats": repeats,
            },
            "results": results,
            "recommendation": recommended,
            "limitations": [
                "Synthetic random vectors are not semantic embeddings; this probe measures ANN/filter behavior, not answer quality.",
                "The corpus is one 100:1 tenant split with 10k rows by default; rerun on the M8 corpus before production defaults.",
                "EXPLAIN ANALYZE Execution Time is used as latency; it excludes client/network and psql overhead.",
                "A natural-plan result may be an exact scan. Tuning conclusions use the forced-HNSW rows; natural plan kinds are recorded separately.",
                "The schema is dropped on exit unless --keep is set; the application's tables are never touched.",
            ],
        }
        report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(f"\nreport: {report_path}")
        if recommended:
            print(
                "recommended: "
                f"ef_search={recommended['efSearch']} oversample={recommended['oversample']} "
                f"iterative_scan={recommended['iterativeScan']} p95={recommended['p95Ms']}ms"
            )
        else:
            print("recommended: none (no forced-HNSW config met the thresholds; consider partitioning or an external vector store)")
        return 0
    finally:
        if not arguments.keep:
            try:
                run_sql(command, f"DROP SCHEMA IF EXISTS {schema} CASCADE;")
            except Exception as cleanup_error:  # noqa: BLE001 - cleanup must not hide the probe result
                print(f"warning: failed to drop probe schema {schema}: {cleanup_error}", file=sys.stderr)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except KeyboardInterrupt:
        raise SystemExit(130)