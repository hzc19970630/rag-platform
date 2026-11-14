#!/usr/bin/env python3
"""D3 data-plane gate: variant-table vector recall vs the legacy chunk vector.

Creates a throwaway schema, fills it with synthetic 1024-dimensional vectors,
builds the production HNSW index shapes, and compares the exact production SQL
shapes:

  * legacy: ORDER BY chunks.embedding <=> q with the chunks index
  * variant: JOIN chunk_embeddings and ORDER BY its embedding with the
    chunk_embeddings_<dim>_vN index

The report records the plan (the assertion is that the vector recall uses the
variant HNSW index), P50/P95 execution time for both paths, index build time and
sizes. The schema is dropped unless --keep is set.

Usage:
    python3 scripts/d3_dataplane.py                     # 1M gate
    python3 scripts/d3_dataplane.py --rows 20000        # smoke
    python3 scripts/d3_dataplane.py --validate-report tests/eval/d3_dataplane_report.json
"""

from __future__ import annotations

import argparse
import json
import statistics
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_REPORT = ROOT / "tests" / "eval" / "d3_dataplane_report.json"
SCHEMA = "d3_dataplane"
SCHEMA_REPORT = "D3DataplaneReportV1"
MODEL = "dataplane-model"
DIMENSION = 1024
QUERY_ITERATIONS = 20
HNSW_INDEX = f"chunk_embeddings_{DIMENSION}_v1"
LEGACY_INDEX = f"chunks_vector_idx_{DIMENSION}_v1"

LEGACY_SQL = f"""
SELECT c.id AS chunk_id, dv.document_id, c.document_version_id, c.knowledge_base_id,
       COALESCE(c.parent_id, '') AS parent_id, c.ordinal, c.content, c.content_hash,
       c.heading_path, c.page_number, c.source_path,
       1 - (c.embedding::halfvec({DIMENSION}) <=> array_fill(0.1::real, ARRAY[{DIMENSION}])::halfvec({DIMENSION})) AS score
FROM {SCHEMA}.chunks c
JOIN {SCHEMA}.document_versions dv ON dv.id = c.document_version_id
JOIN {SCHEMA}.documents d ON d.id = dv.document_id
WHERE c.tenant_id = 'tenant-1'
  AND c.knowledge_base_id IN ('kb-0', 'kb-1')
  AND c.status = 'EVIDENCE_READY'
  AND c.parent_id IS NOT NULL
  AND c.embedding_dimension = {DIMENSION}
  AND c.embedding_model = '{MODEL}'
  AND c.embedding IS NOT NULL
  AND d.latest_version_id = c.document_version_id
  AND d.status = 'EVIDENCE_READY'
  AND d.deleted_at IS NULL
ORDER BY c.embedding::halfvec({DIMENSION}) <=> array_fill(0.1::real, ARRAY[{DIMENSION}])::halfvec({DIMENSION}) ASC
LIMIT 20
"""

VARIANT_SQL = f"""
WITH ann AS (
  SELECT e.chunk_id AS chunk_id,
         1 - (e.embedding::halfvec({DIMENSION}) <=> array_fill(0.1::real, ARRAY[{DIMENSION}])::halfvec({DIMENSION})) AS score
  FROM {SCHEMA}.chunk_embeddings e
  WHERE e.tenant_id = 'tenant-1'
    AND e.knowledge_base_id IN ('kb-0', 'kb-1')
    AND e.model = '{MODEL}'
    AND e.dimension = {DIMENSION}
  ORDER BY e.embedding::halfvec({DIMENSION}) <=> array_fill(0.1::real, ARRAY[{DIMENSION}])::halfvec({DIMENSION}) ASC
  LIMIT 200
)
SELECT c.id AS chunk_id, dv.document_id, c.document_version_id, c.knowledge_base_id,
       COALESCE(c.parent_id, '') AS parent_id, c.ordinal, c.content, c.content_hash,
       c.heading_path, c.page_number, c.source_path, ann.score
FROM ann
JOIN {SCHEMA}.chunks c ON c.id = ann.chunk_id
JOIN {SCHEMA}.document_versions dv ON dv.id = c.document_version_id
JOIN {SCHEMA}.documents d ON d.id = dv.document_id
WHERE c.status = 'EVIDENCE_READY'
  AND c.parent_id IS NOT NULL
  AND d.latest_version_id = c.document_version_id
  AND d.status = 'EVIDENCE_READY'
  AND d.deleted_at IS NULL
ORDER BY ann.score DESC
LIMIT 20
"""


def psql_command() -> list[str]:
    return [
        "docker", "compose", "--env-file", str(ROOT / ".env"),
        "-f", str(ROOT / "deploy/docker-compose/docker-compose.yml"),
        "exec", "-T", "postgres", "psql", "-U", "rag_platform", "-d", "rag_platform",
        "-v", "ON_ERROR_STOP=1", "-q", "-At",
    ]


def psql(sql: str, timeout: int = 3600) -> str:
    result = subprocess.run(psql_command(), input=sql, text=True, capture_output=True, timeout=timeout)
    if result.returncode != 0:
        raise RuntimeError(f"psql failed: {result.stderr.strip()}\n{result.stdout[-2000:]}")
    return result.stdout


def p95(values: list[float]) -> float:
    ordered = sorted(values)
    index = max(0, min(len(ordered) - 1, int(round(0.95 * (len(ordered) - 1)))))
    return ordered[index]


def plan_indexes(plan: dict) -> list[str]:
    found = []

    def walk(node):
        if not isinstance(node, dict):
            return
        name = node.get("Index Name")
        if name:
            found.append(name)
        for key in ("Plans", "InitPlan", "Subplans"):
            value = node.get(key)
            if isinstance(value, list):
                for child in value:
                    walk(child)
            elif isinstance(value, dict):
                walk(value)

    walk(plan.get("Plan", {}))
    return found


def explain(sql: str, prefix: str = "") -> dict:
    raw = psql(f"{prefix}\nEXPLAIN (ANALYZE, FORMAT JSON) {sql};")
    return json.loads(raw)[0]


def measure(sql: str, iterations: int, prefix: str = "") -> tuple[list[float], dict]:
    samples, last_plan = [], {}
    for _ in range(iterations):
        result = explain(sql, prefix)
        samples.append(float(result["Execution Time"]))
        last_plan = result
    return samples, last_plan


def setup(rows: int) -> str:
    psql(f"""
DROP SCHEMA IF EXISTS {SCHEMA} CASCADE;
CREATE SCHEMA {SCHEMA};
CREATE TABLE {SCHEMA}.documents (
  id VARCHAR(64) PRIMARY KEY, latest_version_id VARCHAR(64) NOT NULL,
  status VARCHAR(32) NOT NULL, deleted_at TIMESTAMPTZ
);
CREATE TABLE {SCHEMA}.document_versions (
  id VARCHAR(64) PRIMARY KEY, document_id VARCHAR(64) NOT NULL
);
CREATE TABLE {SCHEMA}.chunks (
  id VARCHAR(96) PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL, knowledge_base_id VARCHAR(64) NOT NULL,
  document_version_id VARCHAR(64) NOT NULL, parent_id VARCHAR(96), ordinal INTEGER NOT NULL,
  content TEXT NOT NULL, content_hash VARCHAR(64) NOT NULL, heading_path TEXT NOT NULL DEFAULT '',
  page_number INTEGER NOT NULL DEFAULT 0, source_path VARCHAR(512) NOT NULL DEFAULT '',
  embedding halfvec, embedding_dimension INTEGER NOT NULL DEFAULT 0,
  embedding_model VARCHAR(128) NOT NULL DEFAULT '', status VARCHAR(32) NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE {SCHEMA}.chunk_embeddings (
  chunk_id VARCHAR(96) NOT NULL, tenant_id VARCHAR(64) NOT NULL, knowledge_base_id VARCHAR(64) NOT NULL,
  model VARCHAR(128) NOT NULL, dimension INTEGER NOT NULL, embedding halfvec NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (chunk_id, model)
);
""", timeout=600)
    started = time.time()
    psql(f"""
SET synchronous_commit = off;
INSERT INTO {SCHEMA}.documents (id, latest_version_id, status)
SELECT 'doc-' || i, 'ver-' || i, 'EVIDENCE_READY' FROM generate_series(1, {rows}) i;
INSERT INTO {SCHEMA}.document_versions (id, document_id)
SELECT 'ver-' || i, 'doc-' || i FROM generate_series(1, {rows}) i;
INSERT INTO {SCHEMA}.chunks
  (id, tenant_id, knowledge_base_id, document_version_id, parent_id, ordinal, content, content_hash,
   embedding, embedding_dimension, embedding_model, status)
SELECT 'chunk-' || i, 'tenant-1', 'kb-' || (i % 4), 'ver-' || i, 'chunk-' || i, 0,
       'synthetic evidence text', 'hash-' || i,
       array_fill(0.1::real, ARRAY[{DIMENSION}])::halfvec, {DIMENSION}, '{MODEL}', 'EVIDENCE_READY'
FROM generate_series(1, {rows}) i;
INSERT INTO {SCHEMA}.chunk_embeddings
  (chunk_id, tenant_id, knowledge_base_id, model, dimension, embedding)
SELECT id, tenant_id, knowledge_base_id, embedding_model, embedding_dimension, embedding
FROM {SCHEMA}.chunks;
CREATE INDEX chunks_scope_idx ON {SCHEMA}.chunks (tenant_id, knowledge_base_id, status);
CREATE INDEX chunk_embeddings_scope_idx ON {SCHEMA}.chunk_embeddings (tenant_id, knowledge_base_id, model);
ANALYZE {SCHEMA}.documents;
ANALYZE {SCHEMA}.document_versions;
ANALYZE {SCHEMA}.chunks;
ANALYZE {SCHEMA}.chunk_embeddings;
""", timeout=3600)
    fill_seconds = time.time() - started

    builds = {}
    for name, statement in {
        "legacy": f"CREATE INDEX {LEGACY_INDEX} ON {SCHEMA}.chunks USING hnsw ((embedding::halfvec({DIMENSION})) halfvec_cosine_ops) WITH (m = 16, ef_construction = 64) WHERE embedding_dimension = {DIMENSION}",
        "variant": f"CREATE INDEX {HNSW_INDEX} ON {SCHEMA}.chunk_embeddings USING hnsw ((embedding::halfvec({DIMENSION})) halfvec_cosine_ops) WITH (m = 16, ef_construction = 64) WHERE dimension = {DIMENSION}",
    }.items():
        started = time.time()
        psql("SET maintenance_work_mem = '512MB'; SET max_parallel_maintenance_workers = 0;\n" + statement, timeout=3600)
        builds[name] = round(time.time() - started, 3)
    psql(f"ANALYZE {SCHEMA}.chunks; ANALYZE {SCHEMA}.chunk_embeddings;", timeout=600)
    return json.dumps({"fillSeconds": round(fill_seconds, 3), "indexBuildSeconds": builds})


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--rows", type=int, default=1_000_000)
    parser.add_argument("--iterations", type=int, default=QUERY_ITERATIONS)
    parser.add_argument("--keep", action="store_true")
    parser.add_argument("--validate-report")
    parser.add_argument("--report", default=str(DEFAULT_REPORT))
    args = parser.parse_args()

    if args.validate_report:
        report = json.loads(Path(args.validate_report).read_text(encoding="utf-8"))
        if report.get("schemaVersion") != SCHEMA_REPORT:
            raise SystemExit("unexpected report schema")
        if not report["gate"]["passed"]:
            raise SystemExit("D3 dataplane gate did not pass: " + json.dumps(report["gate"]))
        print("D3 dataplane report is valid and the gate passed")
        return 0

    try:
        setup_info = json.loads(setup(args.rows))
        base_prefix = "SET enable_seqscan = off; SET enable_bitmapscan = off; SET hnsw.ef_search = 100; SET hnsw.iterative_scan = relaxed_order;"
        legacy_samples, legacy_plan = measure(LEGACY_SQL, args.iterations, base_prefix + " SET enable_sort = off;")
        variant_samples, variant_plan = measure(VARIANT_SQL, args.iterations, base_prefix)
        legacy_indexes = plan_indexes(legacy_plan)
        variant_indexes = plan_indexes(variant_plan)
        legacy_p95 = p95(legacy_samples)
        variant_p95 = p95(variant_samples)
        relative = (variant_p95 - legacy_p95) / legacy_p95 * 100 if legacy_p95 else 0.0
        passed = (
            HNSW_INDEX in variant_indexes
            and LEGACY_INDEX in legacy_indexes
            and variant_p95 <= legacy_p95 * 1.10 + 5
        )
        report = {
            "schemaVersion": SCHEMA_REPORT,
            "generatedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "parameters": {"rows": args.rows, "dimension": DIMENSION, "iterations": args.iterations},
            "setup": setup_info,
            "plans": {"legacy": legacy_indexes, "variant": variant_indexes},
            "latencyMs": {
                "legacy": {"p50": round(statistics.median(legacy_samples), 3), "p95": round(legacy_p95, 3)},
                "variant": {"p50": round(statistics.median(variant_samples), 3), "p95": round(variant_p95, 3)},
            },
            "gate": {
                "rule": "variant p95 <= legacy p95 * 1.10 + 5ms and both HNSW indexes are used",
                "relativeRegressionPercent": round(relative, 3),
                "passed": passed,
            },
        }
        Path(args.report).parent.mkdir(parents=True, exist_ok=True)
        Path(args.report).write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(report, indent=2))
        if not passed:
            print(f"D3 dataplane gate failed: legacy={legacy_p95}ms variant={variant_p95}ms indexes={variant_indexes}", file=sys.stderr)
            return 1
        print(f"D3 dataplane gate passed: legacy p95={legacy_p95}ms variant p95={variant_p95}ms ({relative:.1f}%)")
        return 0
    finally:
        if not args.keep:
            try:
                psql(f"DROP SCHEMA IF EXISTS {SCHEMA} CASCADE;", timeout=600)
            except Exception as exc:  # noqa: BLE001
                print(f"warning: could not drop {SCHEMA}: {exc}", file=sys.stderr)


if __name__ == "__main__":
    raise SystemExit(main())
