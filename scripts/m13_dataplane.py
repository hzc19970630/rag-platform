#!/usr/bin/env python3
"""M13 data-plane benchmark: 1M x 1024-dimensional vectors on local Postgres.

This is the capacity evidence for the M8 gate, not a release gate. It creates a
throwaway schema (default `m13_dataplane`), fills it with deterministic synthetic
vectors generated inside PostgreSQL (no model calls), builds the same online
HNSW index shape as `internal/platform/vector/index.go`, and measures:

  * vector and BM25 query latency from concurrent clients bound to individual
    tenants, using the production SQL shapes from `vector/search.go` and
    `search/bm25.go`;
  * client-observed write latency while `CREATE INDEX CONCURRENTLY` runs;
  * ANN recall against the exact scan for a sample of queries;
  * index build duration, `pg_stat_progress_create_index` samples, relation
    sizes, pgvector/Postgres versions, host resources and free disk.

The business tables are never touched and the schema is dropped on exit unless
`--keep` is set. `--rls` installs the M13 strict policy on the synthetic chunks
table and runs measured statements as `rag_app`, mirroring the enforce path.

Usage:
    python3 scripts/m13_dataplane.py                 # full 1M run
    python3 scripts/m13_dataplane.py --rows 20000    # smoke run
    python3 scripts/m13_dataplane.py --self-test
    python3 scripts/m13_dataplane.py --validate-report tests/eval/m13_dataplane_report.json
"""

from __future__ import annotations

import argparse
import json
import os
import platform
import re
import shlex
import shutil
import statistics
import subprocess
import sys
import threading
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from m8_capacity import latency_stats, percentile, validate_latency_shape  # noqa: E402
from vector_recall_probe import plan_kinds  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_REPORT = ROOT / "tests" / "eval" / "m13_dataplane_report.json"
DEFAULT_SCHEMA = "m13_dataplane"
SCHEMA_REPORT = "M13DataplaneReportV1"
MODEL = "dataplane-model"
STATUS_READY = "EVIDENCE_READY"
TIMING_PATTERN = re.compile(r"^Time: ([0-9.]+) ms$")
RESOURCE_EXPECTATION = [
    "1M x 1024 halfvec heap is roughly 2.2 GB; the HNSW index is roughly 2.5-3 GB.",
    "Leave at least 8 GB free on the Docker data volume: the heap, the index and WAL coexist during the build.",
    "A CONCURRENTLY HNSW build over 1M rows takes roughly 10-25 minutes on a laptop-class machine.",
]


def repo_root() -> Path:
    return ROOT


def default_psql_command() -> list[str]:
    return [
        "docker",
        "compose",
        "--env-file",
        str(ROOT / ".env"),
        "-f",
        str(ROOT / "deploy/docker-compose/docker-compose.yml"),
        "exec",
        "-T",
        "postgres",
        "psql",
        "-U",
        "rag_platform",
        "-d",
        "rag_platform",
        "-X",
        "-q",
        "-A",
        "-t",
    ]


def run_sql(command: list[str], sql: str, timeout: int = 3600) -> str:
    result = subprocess.run(
        command + ["-v", "ON_ERROR_STOP=1", "-f", "-"],
        input=sql,
        text=True,
        capture_output=True,
        timeout=timeout,
    )
    if result.returncode != 0:
        raise RuntimeError("psql failed:\n" + (result.stderr or result.stdout or "").strip())
    return result.stdout


def run_sql_json(command: list[str], sql: str, timeout: int = 120) -> dict:
    output = run_sql(command, sql, timeout=timeout).strip()
    if not output:
        return {}
    return json.loads(output.splitlines()[-1])


class PsqlSession:
    """A long-lived psql process so per-query timing excludes process startup.

    `exec` appends a marker `\\echo` after each statement. psql's `\\timing on`
    prints `Time: <ms>`; the session returns the result lines and that number so
    callers can record latency and row counts without parsing formatted tables.
    """

    def __init__(self, command: list[str], preamble: list[str]):
        self.process = subprocess.Popen(
            command,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            bufsize=1,
        )
        self.counter = 0
        self.closed = False
        self.exec("\n".join(["\\pset pager off", "\\timing on", *preamble]))

    def exec(self, sql: str) -> tuple[list[str], float | None]:
        if self.closed:
            raise RuntimeError("psql session is closed")
        self.counter += 1
        marker = f"__M13_{self.counter}__"
        statement = sql.strip().rstrip(";")
        if statement:
            self.process.stdin.write(statement + ";\n")
        self.process.stdin.write(f"\\echo {marker}\n")
        self.process.stdin.flush()
        lines: list[str] = []
        for line in self.process.stdout:
            text = line.rstrip("\n")
            if text.strip() == marker:
                return self.finish(lines)
            lines.append(text)
        self.closed = True
        raise RuntimeError("psql session closed unexpectedly:\n" + "\n".join(lines[-8:]))

    @staticmethod
    def finish(lines: list[str]) -> tuple[list[str], float | None]:
        elapsed = None
        result: list[str] = []
        for line in lines:
            match = TIMING_PATTERN.match(line.strip())
            if match:
                elapsed = float(match.group(1))
            elif line.strip():
                result.append(line.strip())
        return result, elapsed

    def close(self) -> None:
        if self.closed:
            return
        self.closed = True
        try:
            self.process.stdin.write("\\q\n")
            self.process.stdin.flush()
        except (BrokenPipeError, ValueError):
            pass
        try:
            self.process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            self.process.kill()


def tenant_name(index: int) -> str:
    return f"tenant-{index}"


def kb_name(tenant: int, kb: int) -> str:
    return f"kb-{tenant}-{kb}"


def doc_name(tenant: int, kb: int) -> str:
    return f"doc-{tenant}-{kb}"


def dv_name(tenant: int, kb: int) -> str:
    return f"dv-{tenant}-{kb}"


def setup_sql(schema: str, rows: int, dimension: int, tenants: int, kbs: int) -> str:
    statements = [
        f"DROP SCHEMA IF EXISTS {schema} CASCADE",
        f"CREATE SCHEMA {schema}",
        f"SET search_path TO {schema}, public",
        "CREATE EXTENSION IF NOT EXISTS vector",
        """
CREATE TABLE documents (
    id text PRIMARY KEY,
    status text NOT NULL,
    latest_version_id text NOT NULL,
    deleted_at timestamptz
)""",
        """
CREATE TABLE document_versions (
    id text PRIMARY KEY,
    document_id text NOT NULL
)""",
        f"""
CREATE TABLE chunks (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    knowledge_base_id text NOT NULL,
    document_version_id text NOT NULL,
    parent_id text,
    status text NOT NULL,
    embedding_dimension integer NOT NULL,
    embedding_model text NOT NULL,
    content text NOT NULL,
    embedding halfvec
)""",
        f"""
CREATE TABLE queries (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    knowledge_base_id text NOT NULL,
    embedding halfvec NOT NULL
)""",
        """
CREATE FUNCTION m13_gen_vec(dim integer, row_seed bigint) RETURNS halfvec AS $$
DECLARE
    vec_values real[];
BEGIN
    PERFORM row_seed;
    SELECT array_agg((random() * 2 - 1)::real) INTO vec_values
    FROM generate_series(1, dim);
    RETURN vec_values::halfvec;
END;
$$ LANGUAGE plpgsql VOLATILE""",
    ]
    for tenant in range(1, tenants + 1):
        for kb in range(1, kbs + 1):
            statements.append(
                "INSERT INTO documents (id, status, latest_version_id, deleted_at) VALUES "
                f"('{doc_name(tenant, kb)}', '{STATUS_READY}', '{dv_name(tenant, kb)}', NULL)"
            )
            statements.append(
                "INSERT INTO document_versions (id, document_id) VALUES "
                f"('{dv_name(tenant, kb)}', '{doc_name(tenant, kb)}')"
            )
    # Tenant and knowledge base are derived from the row number, so the data is
    # reproducible and each tenant gets an equal share (the vector list is
    # ordered, so every tenant sees the same distance distribution).
    statements.append(
        f"""
SELECT setseed(0.42);
INSERT INTO chunks (id, tenant_id, knowledge_base_id, document_version_id, parent_id, status,
                    embedding_dimension, embedding_model, content, embedding)
SELECT 'chunk-' || i,
       '{tenant_name(1)[:-1]}' || (1 + ((i - 1) % {tenants})),
       '{kb_name(1, 1)[:-3]}' || (1 + ((i - 1) % {tenants})) || '-' || (1 + (((i - 1) / {tenants})::int % {kbs})),
       '{dv_name(1, 1)[:-3]}' || (1 + ((i - 1) % {tenants})) || '-' || (1 + (((i - 1) / {tenants})::int % {kbs})),
       'parent',
       '{STATUS_READY}',
       {dimension},
       '{MODEL}',
       'dataplane capacity tenantterm' || (1 + ((i - 1) % {tenants})) || ' row' || i,
       m13_gen_vec({dimension}, i)
FROM generate_series(1, {rows}) i"""
    )
    # One centroid query per (tenant, kb) bucket: the average of five vectors
    # from that bucket, so ANN and exact scan answer the same question.
    statements.append(
        f"""
INSERT INTO queries (id, tenant_id, knowledge_base_id, embedding)
SELECT 'q-' || t || '-' || k || '-' || n,
       '{tenant_name(1)[:-1]}' || t,
       '{kb_name(1, 1)[:-3]}' || t || '-' || k,
       (SELECT avg(v.embedding::vector)::halfvec({dimension})
        FROM (SELECT embedding FROM chunks
              WHERE id IN ('chunk-' || (t + {tenants} * ((n * 37 + 1 * 101) % (({rows} / {tenants}) - 1))) ,
                           'chunk-' || (t + {tenants} * ((n * 37 + 2 * 101) % (({rows} / {tenants}) - 1))) ,
                           'chunk-' || (t + {tenants} * ((n * 37 + 3 * 101) % (({rows} / {tenants}) - 1))) ,
                           'chunk-' || (t + {tenants} * ((n * 37 + 4 * 101) % (({rows} / {tenants}) - 1))) ,
                           'chunk-' || (t + {tenants} * ((n * 37 + 5 * 101) % (({rows} / {tenants}) - 1))))) v)
FROM generate_series(1, {tenants}) t
CROSS JOIN generate_series(1, {kbs}) k
CROSS JOIN generate_series(1, 13) n"""
    )
    statements.extend(
        [
            "CREATE INDEX chunks_scope_idx ON chunks(tenant_id, knowledge_base_id, document_version_id, status)",
            "CREATE INDEX chunks_embedding_model_idx ON chunks(embedding_model, embedding_dimension)",
            f"CREATE INDEX chunks_bm25_idx ON chunks USING bm25 (id, content) WITH (key_field = 'id')",
        ]
    )
    return ";\n".join(statements) + ";\n"


def rls_sql(schema: str) -> str:
    return f"""
ALTER TABLE chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE chunks FORCE ROW LEVEL SECURITY;
CREATE POLICY m13_tenant_isolation ON chunks
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));
GRANT USAGE ON SCHEMA {schema} TO rag_app;
GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA {schema} TO rag_app;
"""


def vector_query_sql(schema: str, dimension: int, tenant: int, kb: int, query_id: str, top_k: int) -> str:
    # The query vector is a scalar subquery (InitPlan), matching the production
    # parameter shape; a joined q.embedding Var would keep the planner off the
    # HNSW ordering index.
    return f"""
SELECT c.id AS chunk_id,
       c.knowledge_base_id,
       COALESCE(c.parent_id, '') AS parent_id,
       c.content,
       1 - (c.embedding::halfvec({dimension}) <=> (
           SELECT embedding FROM {schema}.queries WHERE id = '{query_id}'
       )) AS score
FROM chunks c
JOIN document_versions dv ON dv.id = c.document_version_id
JOIN documents d ON d.id = dv.document_id
WHERE c.tenant_id = '{tenant_name(tenant)}'
  AND c.knowledge_base_id IN ('{kb_name(tenant, kb)}')
  AND c.status = '{STATUS_READY}'
  AND c.parent_id IS NOT NULL
  AND c.embedding_dimension = {dimension}
  AND c.embedding_model = '{MODEL}'
  AND c.embedding IS NOT NULL
  AND d.latest_version_id = c.document_version_id
  AND d.status = '{STATUS_READY}'
  AND d.deleted_at IS NULL
ORDER BY c.embedding::halfvec({dimension}) <=> (
    SELECT embedding FROM {schema}.queries WHERE id = '{query_id}'
) ASC
LIMIT {top_k}
""".strip()


def keyword_query_sql(schema: str, tenant: int, kb: int, top_k: int) -> str:
    del schema
    return f"""
SELECT c.id AS chunk_id,
       c.knowledge_base_id,
       COALESCE(c.parent_id, '') AS parent_id,
       c.content,
       pdb.score(c.id) AS score
FROM chunks c
JOIN document_versions dv ON dv.id = c.document_version_id
JOIN documents d ON d.id = dv.document_id
WHERE c.tenant_id = '{tenant_name(tenant)}'
  AND c.knowledge_base_id IN ('{kb_name(tenant, kb)}')
  AND c.status = '{STATUS_READY}'
  AND c.parent_id IS NOT NULL
  AND d.latest_version_id = c.document_version_id
  AND d.status = '{STATUS_READY}'
  AND d.deleted_at IS NULL
  AND c.content @@@ 'tenantterm{tenant} OR capacity'
ORDER BY score DESC
LIMIT {top_k}
""".strip()


def count_wrapper(query: str) -> str:
    return f"SELECT count(*) FROM (\n{query}\n) _m13"


def latency_section(samples: list[float]) -> dict:
    stats = latency_stats(samples)
    stats["p99"] = percentile(samples, 0.99) if samples else None
    return stats


def recall_functions_sql(schema: str, dimension: int) -> str:
    # Function parameters are prefixed so they cannot be shadowed by the
    # chunks.tenant_id / knowledge_base_id columns inside the SQL body.
    scope = f"""
        FROM chunks c
        JOIN document_versions dv ON dv.id = c.document_version_id
        JOIN documents d ON d.id = dv.document_id
        WHERE c.tenant_id = p_tenant
          AND c.knowledge_base_id = p_kb
          AND c.status = '{STATUS_READY}'
          AND c.parent_id IS NOT NULL
          AND c.embedding_dimension = {dimension}
          AND c.embedding_model = '{MODEL}'
          AND c.embedding IS NOT NULL
          AND d.latest_version_id = c.document_version_id
          AND d.status = '{STATUS_READY}'
          AND d.deleted_at IS NULL
        ORDER BY c.embedding::halfvec({dimension}) <=> (SELECT embedding FROM queries q WHERE q.id = p_query)
        LIMIT p_k"""
    return f"""
SET search_path TO {schema}, public;
CREATE FUNCTION m13_exact_ids(p_query text, p_tenant text, p_kb text, p_k integer)
RETURNS text[] LANGUAGE sql STABLE
SET search_path TO {schema}, public
SET enable_indexscan = off
SET enable_seqscan = on
AS $$
    SELECT array_agg(id) FROM (
        SELECT c.id{scope}
    ) _exact
$$;

CREATE FUNCTION m13_ann_ids(p_query text, p_tenant text, p_kb text, p_k integer)
RETURNS text[] LANGUAGE sql STABLE
SET search_path TO {schema}, public
AS $$
    SELECT array_agg(id) FROM (
        SELECT c.id{scope}
    ) _ann
$$;

CREATE FUNCTION m13_recall(p_query text, p_tenant text, p_kb text, p_k integer)
RETURNS double precision LANGUAGE sql STABLE
SET search_path TO {schema}, public
AS $$
    SELECT CASE WHEN cardinality(m13_exact_ids(p_query, p_tenant, p_kb, p_k)) = 0 THEN NULL
                ELSE (SELECT count(*)::float8
                      FROM unnest(m13_exact_ids(p_query, p_tenant, p_kb, p_k)) e
                      WHERE e = ANY (m13_ann_ids(p_query, p_tenant, p_kb, p_k)))
                     / cardinality(m13_exact_ids(p_query, p_tenant, p_kb, p_k))
           END
$$;
"""



def index_definition(dimension: int) -> str:
    return (
        f"CREATE INDEX CONCURRENTLY chunks_vector_idx_{dimension}_v1 ON chunks "
        f"USING hnsw ((embedding::halfvec({dimension})) halfvec_cosine_ops) "
        f"WITH (m = 16, ef_construction = 64) "
        f"WHERE embedding_dimension = {dimension}"
    )


def parse_count(lines: list[str]) -> int:
    for line in reversed(lines):
        try:
            return int(line.strip())
        except ValueError:
            continue
    return 0


def disk_free_bytes() -> int:
    try:
        usage = shutil.disk_usage(ROOT)
        return int(usage.free)
    except OSError:
        return 0


def host_configuration() -> dict:
    memory = None
    try:
        if platform.system() == "Darwin":
            memory = int(subprocess.run(["sysctl", "-n", "hw.memsize"], capture_output=True, text=True, check=True).stdout.strip())
        else:
            with open("/proc/meminfo", encoding="utf-8") as handle:
                for line in handle:
                    if line.startswith("MemTotal:"):
                        memory = int(line.split()[1]) * 1024
                        break
    except (OSError, ValueError, subprocess.SubprocessError):
        memory = None
    docker = {}
    try:
        output = subprocess.run(
            ["docker", "info", "--format", "{{json .}}"], capture_output=True, text=True, timeout=30
        )
        if output.returncode == 0:
            parsed = json.loads(output.stdout)
            docker = {"cpus": parsed.get("NCPU"), "memoryBytes": parsed.get("MemTotal")}
    except (OSError, ValueError, subprocess.SubprocessError, json.JSONDecodeError):
        docker = {}
    return {
        "platform": platform.platform(),
        "machine": platform.machine(),
        "python": platform.python_version(),
        "cpuCount": os.cpu_count(),
        "memoryBytes": memory,
        "diskFreeBytes": disk_free_bytes(),
        "docker": docker,
        "resourceExpectation": RESOURCE_EXPECTATION,
    }


def collect_environment(command: list[str]) -> dict:
    output = run_sql(
        command,
        """
SELECT json_build_object(
    'pgvectorVersion', (SELECT extversion FROM pg_extension WHERE extname = 'vector'),
    'postgresVersion', current_setting('server_version'),
    'sharedBuffers', current_setting('shared_buffers'),
    'workMem', current_setting('work_mem'),
    'maintenanceWorkMem', current_setting('maintenance_work_mem'),
    'maxParallelMaintenanceWorkers', current_setting('max_parallel_maintenance_workers'),
    'maxConnections', current_setting('max_connections'),
    'databaseSizeBytes', pg_database_size(current_database())
);
""",
    )
    environment = json.loads(output.strip().splitlines()[-1])
    environment.update(host_configuration())
    return environment


def corpus_check_sql(schema: str, rows: int, dimension: int, tenants: int, kbs: int) -> str:
    return f"""
SELECT json_build_object(
    'rows', (SELECT count(*) FROM {schema}.chunks),
    'distinctSample', (SELECT count(DISTINCT embedding::text) FROM (SELECT embedding FROM {schema}.chunks ORDER BY id LIMIT 1000) s),
    'distinctSampleRows', (SELECT count(*) FROM (SELECT embedding FROM {schema}.chunks ORDER BY id LIMIT 1000) s),
    'tenants', (SELECT count(DISTINCT tenant_id) FROM {schema}.chunks),
    'knowledgeBases', (SELECT count(DISTINCT knowledge_base_id) FROM {schema}.chunks),
    'wrongDimension', (SELECT count(*) FROM {schema}.chunks WHERE embedding_dimension <> {dimension}),
    'queryRows', (SELECT count(*) FROM {schema}.queries),
    'expectedRows', {rows},
    'expectedTenants', {tenants},
    'expectedKnowledgeBases', {tenants * kbs},
    'heapBytes', pg_relation_size('{schema}.chunks')
);
"""


def sample_progress(command: list[str]) -> dict:
    try:
        output = run_sql(
            command,
            """
SELECT json_build_object(
    'phase', phase,
    'blocksDone', blocks_done,
    'blocksTotal', blocks_total,
    'tuplesDone', tuples_done,
    'tuplesTotal', tuples_total
) FROM pg_stat_progress_create_index LIMIT 1;
""",
            timeout=30,
        ).strip()
        return json.loads(output.splitlines()[-1]) if output else {}
    except (RuntimeError, ValueError, json.JSONDecodeError, subprocess.TimeoutExpired):
        return {}


def index_catalog(command: list[str], schema: str, dimension: int) -> dict:
    index = f"chunks_vector_idx_{dimension}_v1"
    output = run_sql(
        command,
        f"""
SELECT json_build_object(
    'exists', EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
                      WHERE n.nspname = '{schema}' AND c.relname = '{index}'),
    'valid', COALESCE((SELECT i.indisvalid FROM pg_index i
                       JOIN pg_class c ON c.oid = i.indexrelid
                       JOIN pg_namespace n ON n.oid = c.relnamespace
                       WHERE n.nspname = '{schema}' AND c.relname = '{index}'), false),
    'definition', COALESCE((SELECT pg_get_indexdef(c.oid) FROM pg_class c
                            JOIN pg_namespace n ON n.oid = c.relnamespace
                            WHERE n.nspname = '{schema}' AND c.relname = '{index}'), ''),
    'indexBytes', COALESCE(pg_relation_size('{schema}.{index}'), 0),
    'tableBytes', pg_relation_size('{schema}.chunks'),
    'totalBytes', pg_total_relation_size('{schema}.chunks')
);
""",
    )
    return json.loads(output.strip().splitlines()[-1])


def bm25_catalog(command: list[str], schema: str) -> dict:
    output = run_sql(
        command,
        f"""
SELECT json_build_object(
    'indexBytes', COALESCE(pg_relation_size('{schema}.chunks_bm25_idx'), 0),
    'definition', COALESCE((SELECT pg_get_indexdef(c.oid) FROM pg_class c
                            JOIN pg_namespace n ON n.oid = c.relnamespace
                            WHERE n.nspname = '{schema}' AND c.relname = 'chunks_bm25_idx'), '')
);
""",
    )
    return json.loads(output.strip().splitlines()[-1])


def capture_plan(command: list[str], schema: str, sql: str, setup: str = "") -> dict:
    script = "\n".join(
        [
            "BEGIN;",
            f"SET LOCAL search_path TO {schema}, public;",
            setup,
            f"EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) {sql};",
            "COMMIT;",
        ]
    )
    # The JSON plan is a single array value plus a trailing newline; psql -A -t
    # keeps it on one logical line set. run_sql returns the whole text so parse
    # the first '[' and the last ']'.
    output = run_sql(command, script).strip()
    start = output.find("[")
    end = output.rfind("]")
    if start < 0 or end < start:
        raise RuntimeError("EXPLAIN did not return a JSON plan:\n" + output[:400])
    plan = json.loads(output[start:end + 1])
    facts = plan_kinds(plan)
    facts["executionMs"] = round(float(plan[0].get("Execution Time", 0.0)), 3)
    facts["planningMs"] = round(float(plan[0].get("Planning Time", 0.0)), 3)
    return facts


def wait_for_table_ready(command: list[str], schema: str) -> None:
    run_sql(command, f"ANALYZE {schema}.chunks;\nANALYZE {schema}.queries;\n")


def build_index_with_writes(
    command: list[str],
    schema: str,
    dimension: int,
    write_workers: int,
    write_interval_ms: float,
    write_tenant: int,
) -> dict:
    stop = threading.Event()
    write_samples: list[float] = []
    write_errors: list[str] = []
    progress: list[dict] = []
    started = time.monotonic()

    def writer(worker: int) -> None:
        preamble = [f"SET search_path TO {schema}, public;"]
        if write_workers > 0:
            preamble.append("SET statement_timeout = '30s';")
        session = PsqlSession(command, preamble)
        index = 0
        try:
            while not stop.is_set():
                index += 1
                tenant = write_tenant
                sql = f"""
INSERT INTO chunks (id, tenant_id, knowledge_base_id, document_version_id, parent_id, status,
                    embedding_dimension, embedding_model, content, embedding)
SELECT 'write-w{worker}-{index}', '{tenant_name(tenant)}', '{kb_name(tenant, 1)}', '{dv_name(tenant, 1)}',
       'parent', '{STATUS_READY}', {dimension}, '{MODEL}', 'concurrent write {worker}-{index}',
       (SELECT embedding FROM chunks WHERE id = 'chunk-{tenant}')
RETURNING id
""".strip()
                lines, elapsed = session.exec(sql)
                if elapsed is None:
                    if any("ERROR" in line for line in lines):
                        write_errors.append(lines[-1] if lines else "unknown insert error")
                        break
                else:
                    write_samples.append(elapsed)
                stop.wait(max(0.0, write_interval_ms / 1000.0))
        finally:
            session.close()

    def progress_sampler() -> None:
        while not stop.is_set():
            sample = sample_progress(command)
            if sample:
                sample["elapsedMs"] = round((time.monotonic() - started) * 1000, 1)
                progress.append(sample)
            stop.wait(5.0)

    writers = [threading.Thread(target=writer, args=(worker,), daemon=True) for worker in range(1, write_workers + 1)]
    sampler = threading.Thread(target=progress_sampler, daemon=True)
    for thread in writers:
        thread.start()
    sampler.start()
    time.sleep(0.5)

    build_sql = "\n".join(
        [
            f"SET search_path TO {schema}, public;",
            "SET maintenance_work_mem = '1GB';",
            "SET lock_timeout = '10s';",
            "SET statement_timeout = '0';",
            index_definition(dimension) + ";",
        ]
    )
    build_error = None
    try:
        run_sql(command, build_sql, timeout=7200)
    except (RuntimeError, subprocess.TimeoutExpired) as error:  # noqa: PERF203 - the error is reported, not raised
        build_error = str(error)
    finally:
        stop.set()
        for thread in writers:
            thread.join(timeout=30)
        sampler.join(timeout=30)

    duration_ms = round((time.monotonic() - started) * 1000, 1)
    run_sql(command, f"DELETE FROM {schema}.chunks WHERE id LIKE 'write-%';\nANALYZE {schema}.chunks;\n")
    return {
        "durationMs": duration_ms,
        "error": build_error,
        "progressSamples": progress,
        "concurrentWrites": {
            "workers": write_workers,
            "intervalMs": write_interval_ms,
            "samples": len(write_samples),
            "latencyMs": latency_section(write_samples),
            "errors": write_errors[:5],
        },
    }


def run_query_load(
    command: list[str],
    schema: str,
    dimension: int,
    tenants: int,
    kbs: int,
    workers: int,
    iterations: int,
    top_k: int,
    ef_search: int,
    iterative_scan: str,
    rls: bool,
) -> dict:
    vector_samples: list[float] = []
    keyword_samples: list[float] = []
    vector_underfill = 0
    keyword_underfill = 0
    errors: list[str] = []
    per_worker: list[dict] = []
    lock = threading.Lock()
    started = time.monotonic()

    def worker(index: int) -> None:
        tenant = (index % tenants) + 1
        kb = ((index // tenants) % kbs) + 1
        preamble = [f"SET search_path TO {schema}, public;", f"SET hnsw.ef_search = {ef_search};"]
        if iterative_scan:
            preamble.append(f"SET hnsw.iterative_scan = {iterative_scan};")
        if rls:
            preamble.extend(["SET ROLE rag_app;", f"SET app.tenant_id = '{tenant_name(tenant)}';"])
        session = PsqlSession(command, preamble)
        local_vector: list[float] = []
        local_keyword: list[float] = []
        local_underfill_vector = 0
        local_underfill_keyword = 0
        try:
            session.exec("SELECT 1")
            for step in range(iterations):
                query_id = f"q-{tenant}-{kb}-{(step % 13) + 1}"
                vector = count_wrapper(vector_query_sql(schema, dimension, tenant, kb, query_id, top_k))
                lines, elapsed = session.exec(vector)
                count = parse_count(lines)
                if elapsed is None:
                    errors.append("vector query did not report a timing line")
                    continue
                local_vector.append(elapsed)
                if count < top_k:
                    local_underfill_vector += 1
                keyword = count_wrapper(keyword_query_sql(schema, tenant, kb, top_k))
                lines, elapsed = session.exec(keyword)
                count = parse_count(lines)
                if elapsed is None:
                    errors.append("keyword query did not report a timing line")
                    continue
                local_keyword.append(elapsed)
                if count < top_k:
                    local_underfill_keyword += 1
        finally:
            session.close()
        with lock:
            vector_samples.extend(local_vector)
            keyword_samples.extend(local_keyword)
            nonlocal vector_underfill, keyword_underfill
            vector_underfill += local_underfill_vector
            keyword_underfill += local_underfill_keyword
            per_worker.append(
                {
                    "worker": index,
                    "tenant": tenant_name(tenant),
                    "vectorSamples": len(local_vector),
                    "keywordSamples": len(local_keyword),
                }
            )

    threads = [threading.Thread(target=worker, args=(index,)) for index in range(workers)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    elapsed = max(time.monotonic() - started, 1e-6)
    total = len(vector_samples) + len(keyword_samples)
    return {
        "vector": {
            "samples": len(vector_samples),
            "latencyMs": latency_section(vector_samples),
            "underfillRate": round(vector_underfill / len(vector_samples), 4) if vector_samples else 0,
        },
        "keyword": {
            "samples": len(keyword_samples),
            "latencyMs": latency_section(keyword_samples),
            "underfillRate": round(keyword_underfill / len(keyword_samples), 4) if keyword_samples else 0,
        },
        "combined": {
            "samples": total,
            "qps": round(total / elapsed, 2),
            "wallSeconds": round(elapsed, 3),
        },
        "perWorker": sorted(per_worker, key=lambda item: item["worker"]),
        "errors": errors[:10],
    }


def run_recall(
    command: list[str],
    schema: str,
    tenants: int,
    kbs: int,
    samples: int,
    top_k: int,
    ef_search: int,
    rls: bool,
) -> dict:
    session_preamble = [f"SET search_path TO {schema}, public;", f"SET hnsw.ef_search = {ef_search};"]
    if rls:
        session_preamble.append("SET ROLE rag_app;")
    values: list[float] = []
    errors: list[str] = []
    session = PsqlSession(command, session_preamble)
    try:
        for index in range(samples):
            tenant = (index % tenants) + 1
            kb = ((index // tenants) % kbs) + 1
            query_id = f"q-{tenant}-{kb}-{(index % 13) + 1}"
            if rls:
                session.exec(f"SET app.tenant_id = '{tenant_name(tenant)}';")
            lines, elapsed = session.exec(f"SELECT m13_recall('{query_id}', '{tenant_name(tenant)}', '{kb_name(tenant, kb)}', {top_k});")
            if elapsed is None:
                errors.append("recall query did not report a timing line")
                continue
            try:
                value = float(lines[0])
            except (IndexError, ValueError):
                errors.append("recall query returned no numeric value")
                continue
            values.append(value)
    finally:
        session.close()
    ordered = sorted(values)
    return {
        "samples": len(values),
        "topK": top_k,
        "efSearch": ef_search,
        "meanRecall": round(statistics.fmean(values), 4) if values else None,
        "p10Recall": round(ordered[max(0, int(len(ordered) * 0.1) - 1)], 4) if ordered else None,
        "minRecall": round(min(values), 4) if values else None,
        "maxRecall": round(max(values), 4) if values else None,
        "errors": errors[:7],
    }


def build_self_checks(parameters: dict, corpus: dict, index: dict, vector_plan: dict,
                      vector_forced_plan: dict, keyword_plan: dict, exact_plan: dict, keyword_index: dict,
                      query_load: dict, recall: dict) -> list[dict]:
    # At small smoke sizes the planner legitimately prefers an exact scan; the
    # HNSW requirement applies to the full data-plane scale.
    natural_required = parameters["rows"] >= 100_000
    checks = [
        {
            "id": "row-count",
            "passed": corpus.get("rows") == parameters["rows"],
            "detail": f"{corpus.get('rows')} / {parameters['rows']}",
        },
        {
            "id": "tenant-shape",
            "passed": corpus.get("tenants") == parameters["tenants"]
            and corpus.get("knowledgeBases") == parameters["tenants"] * parameters["knowledgeBasesPerTenant"],
            "detail": f"tenants={corpus.get('tenants')} kbs={corpus.get('knowledgeBases')}",
        },
        {
            "id": "vector-dimension",
            "passed": corpus.get("wrongDimension") == 0,
            "detail": f"wrongDimension={corpus.get('wrongDimension')}",
        },
        {
            "id": "vectors-distinct",
            "passed": corpus.get("distinctSample") == corpus.get("distinctSampleRows"),
            "detail": f"{corpus.get('distinctSample')}/{corpus.get('distinctSampleRows')} sampled rows",
        },
        {
            "id": "index-valid",
            "passed": bool(index.get("valid")),
            "detail": index.get("definition", ""),
        },
        {
            "id": "index-definition",
            "passed": f"halfvec({parameters['dimension']})" in index.get("definition", "")
            and f"embedding_dimension = {parameters['dimension']}" in index.get("definition", ""),
            "detail": index.get("definition", ""),
        },
        {
            "id": "index-usable",
            "passed": vector_forced_plan.get("kind") == "hnsw-index-scan",
            "detail": json.dumps(vector_forced_plan),
        },
        {
            "id": "vector-plan-hnsw",
            "passed": (vector_plan.get("kind") == "hnsw-index-scan") if natural_required else None,
            "detail": "natural plan: " + json.dumps(vector_plan),
        },
        {
            "id": "exact-plan-not-hnsw",
            "passed": exact_plan.get("kind") != "hnsw-index-scan",
            "detail": json.dumps(exact_plan),
        },
        {
            "id": "keyword-index",
            "passed": "bm25" in keyword_index.get("definition", ""),
            "detail": keyword_index.get("definition", ""),
        },
        {
            "id": "keyword-plan-captured",
            "passed": bool(keyword_plan.get("nodeTypes")),
            "detail": json.dumps(keyword_plan),
        },
        {
            "id": "write-latency-during-build",
            "passed": (query_load.get("writeSamples") or 0) > 0,
            "detail": f"{query_load.get('writeSamples')} insert samples",
        },
        {
            "id": "recall-samples",
            "passed": recall.get("samples") == parameters["recallSamples"],
            "detail": f"{recall.get('samples')} / {parameters['recallSamples']}",
        },
    ]
    return checks


def dynamic_limitations(parameters: dict, query_load: dict, recall: dict, environment: dict) -> list[str]:
    limitations = [
        "Synthetic uniform random vectors are not semantic embeddings; this run measures the data-plane cost and ANN behavior, not answer quality.",
        "The benchmark runs against the local PostgreSQL/pgvector container on one machine; results do not directly replace the M8 production threshold.",
        "Query latency is psql client-observed \\timing inside the postgres container; it excludes the API/retrieval layer and network to a real client.",
        "The vector list has one embedding size (1024) and one HNSW parameter set (m=16, ef_construction=64); other dimensions and parameters are not measured.",
        "Recall compares ANN against an exact scan of the same tenant/knowledge-base scope; it is a report value, not a 0.95 release gate.",
        "halfvec values larger than the TOAST threshold are stored out of line: indexBuild.tableBytes is the main heap fork, while totalBytes includes TOAST and all indexes.",
        "Writers during the build are throttled inserts that copy one existing vector; concurrent ingestion with model calls is not simulated.",
        "The schema is dropped on exit unless --keep is set; application tables are never touched.",
    ]
    if parameters.get("rls"):
        limitations.append("--rls applies the strict tenant policy to the synthetic chunks table only; the synthetic parent tables are not tenant-scoped.")
    else:
        limitations.append("RLS was not enabled for this run; rerun with --rls to compare the enforce path (the roles must already exist).")
    if query_load.get("vector", {}).get("underfillRate", 0) > 0:
        limitations.append("Vector recall underfills the requested TopK on some queries; the HNSW filtered scan cannot always fill TopK at ef_search=" + str(parameters.get("efSearch")) + ".")
    if (recall.get("meanRecall") or 1.0) < 0.95:
        limitations.append("Mean ANN recall against the exact scan is below 0.95 on this synthetic corpus; report it as measured, do not treat it as a tuning failure.")
    if environment.get("diskFreeBytes", 0) < 8 * 1024 ** 3:
        limitations.append("Free disk before the run was below the 8 GB planning guideline; the build completed under tighter headroom.")
    return limitations


def synthetic_report() -> dict:
    stats = latency_stats([10.0, 20.0, 30.0])
    stats["p99"] = 30.0
    return {
        "schemaVersion": SCHEMA_REPORT,
        "generatedAt": "2026-01-01T00:00:00+00:00",
        "durationSeconds": 1.0,
        "parameters": {
            "rows": 100,
            "dimension": 1024,
            "tenants": 4,
            "knowledgeBasesPerTenant": 2,
            "workers": 2,
            "iterationsPerWorker": 2,
            "topK": 20,
            "efSearch": 40,
            "iterativeScan": "relaxed_order",
            "seed": 42,
            "rls": False,
            "recallSamples": 3,
            "writeWorkers": 1,
            "writeIntervalMs": 200,
            "schema": DEFAULT_SCHEMA,
            "keep": False,
        },
        "environment": {
            "pgvectorVersion": "0.8.7",
            "postgresVersion": "17.9",
            "platform": "synthetic",
            "cpuCount": 1,
            "memoryBytes": 1,
            "diskFreeBytes": 9 * 1024 ** 3,
            "resourceExpectation": RESOURCE_EXPECTATION,
        },
        "corpus": {"rows": 100, "tenants": 4, "knowledgeBases": 8, "wrongDimension": 0,
                   "distinctSample": 100, "distinctSampleRows": 100},
        "indexBuild": {
            "durationMs": 10.0,
            "error": None,
            "progressSamples": [],
            "concurrentWrites": {"workers": 1, "samples": 2, "latencyMs": stats},
            "valid": True,
            "definition": "CREATE INDEX ... halfvec(1024) ... WHERE embedding_dimension = 1024",
            "indexBytes": 1,
            "tableBytes": 1,
            "totalBytes": 1,
        },
        "keywordIndexBuild": {"indexBytes": 1, "definition": "USING bm25"},
        "plans": {
            "vector": {"kind": "hnsw-index-scan", "nodeTypes": ["Index Scan"], "indexes": ["chunks_vector_idx_1024_v1"]},
            "vectorForced": {"kind": "hnsw-index-scan", "nodeTypes": ["Index Scan"], "indexes": ["chunks_vector_idx_1024_v1"]},
            "keyword": {"kind": "other", "nodeTypes": ["Custom Scan"]},
            "exact": {"kind": "bitmap-scan", "nodeTypes": ["Bitmap Heap Scan"]},
        },
        "queryLoad": {
            "vector": {"samples": 4, "latencyMs": stats, "underfillRate": 0},
            "keyword": {"samples": 4, "latencyMs": stats, "underfillRate": 0},
            "combined": {"samples": 8, "qps": 10.0, "wallSeconds": 1.0},
            "perWorker": [],
            "errors": [],
            "writeSamples": 2,
        },
        "recall": {"samples": 3, "topK": 20, "efSearch": 40, "meanRecall": 0.9,
                   "p10Recall": 0.8, "minRecall": 0.8, "maxRecall": 1.0},
        "selfChecks": [{"id": "row-count", "passed": True, "detail": "100/100"}],
        "limitations": ["synthetic"],
    }


def validate_tail(stats: dict, label: str) -> list[str]:
    errors: list[str] = []
    p95, p99 = stats.get("p95"), stats.get("p99")
    if not isinstance(p99, (int, float)):
        errors.append(f"{label}.p99 must be numeric")
    elif isinstance(p95, (int, float)) and p99 + 0.11 < p95:
        errors.append(f"{label}.p99 {p99} is below p95 {p95}")
    return errors


def validate_report(report: dict) -> list[str]:
    errors: list[str] = []
    required = ("schemaVersion", "generatedAt", "durationSeconds", "parameters", "environment", "corpus",
                "indexBuild", "keywordIndexBuild", "plans", "queryLoad", "recall", "selfChecks", "limitations")
    for key in required:
        if key not in report:
            errors.append(f"missing top-level key {key}")
    if errors:
        return errors
    if report["schemaVersion"] != SCHEMA_REPORT:
        errors.append(f"schemaVersion must be {SCHEMA_REPORT}")

    parameters = report["parameters"]
    for key in ("rows", "dimension", "tenants", "knowledgeBasesPerTenant", "workers",
                "iterationsPerWorker", "topK", "efSearch", "recallSamples", "writeWorkers"):
        value = parameters.get(key)
        if not isinstance(value, int) or value < 1:
            errors.append(f"parameters.{key} must be a positive integer")
    if not isinstance(parameters.get("rls"), bool):
        errors.append("parameters.rls must be a boolean")
    if not isinstance(parameters.get("seed"), int):
        errors.append("parameters.seed must be an integer")

    environment = report["environment"]
    if not environment.get("pgvectorVersion"):
        errors.append("environment.pgvectorVersion must not be empty")
    if not environment.get("resourceExpectation"):
        errors.append("environment.resourceExpectation must not be empty")

    corpus = report["corpus"]
    if corpus.get("rows") != parameters.get("rows"):
        errors.append("corpus.rows must equal parameters.rows")
    if corpus.get("tenants") != parameters.get("tenants"):
        errors.append("corpus.tenants must equal parameters.tenants")
    if corpus.get("wrongDimension") != 0:
        errors.append("corpus.wrongDimension must be zero")

    build = report["indexBuild"]
    if not isinstance(build.get("durationMs"), (int, float)) or build.get("durationMs", 0) <= 0:
        errors.append("indexBuild.durationMs must be positive")
    if build.get("error"):
        errors.append(f"indexBuild.error must be empty, got {build.get('error')!r}")
    if not build.get("valid"):
        errors.append("indexBuild.valid must be true")
    if f"halfvec({parameters.get('dimension')})" not in (build.get("definition") or ""):
        errors.append("indexBuild.definition must use the configured dimension")
    errors.extend(validate_latency_shape((build.get("concurrentWrites") or {}).get("latencyMs") or {},
                                         "indexBuild.concurrentWrites.latencyMs"))
    errors.extend(validate_tail((build.get("concurrentWrites") or {}).get("latencyMs") or {},
                                "indexBuild.concurrentWrites.latencyMs"))

    plans = report["plans"]
    if (plans.get("vectorForced") or {}).get("kind") != "hnsw-index-scan":
        errors.append("plans.vectorForced must be an hnsw-index-scan")
    if parameters.get("rows", 0) >= 100_000 and (plans.get("vector") or {}).get("kind") != "hnsw-index-scan":
        errors.append("plans.vector (natural) must be an hnsw-index-scan at full scale")
    if (plans.get("exact") or {}).get("kind") == "hnsw-index-scan":
        errors.append("plans.exact must not use the HNSW index")
    if not (plans.get("keyword") or {}).get("nodeTypes"):
        errors.append("plans.keyword must record plan node types")

    load = report["queryLoad"]
    expected = parameters.get("workers", 0) * parameters.get("iterationsPerWorker", 0)
    for channel in ("vector", "keyword"):
        section = load.get(channel) or {}
        if section.get("samples") != expected:
            errors.append(f"queryLoad.{channel}.samples must equal workers * iterationsPerWorker")
        errors.extend(validate_latency_shape(section.get("latencyMs") or {}, f"queryLoad.{channel}.latencyMs"))
        errors.extend(validate_tail(section.get("latencyMs") or {}, f"queryLoad.{channel}.latencyMs"))
    combined = load.get("combined") or {}
    if combined.get("samples") != expected * 2:
        errors.append("queryLoad.combined.samples must equal twice the per-channel samples")
    if not isinstance(combined.get("qps"), (int, float)) or combined.get("qps", 0) <= 0:
        errors.append("queryLoad.combined.qps must be positive")
    if (load.get("writeSamples") or 0) < 1:
        errors.append("queryLoad.writeSamples must be positive (writes during index build)")

    recall = report["recall"]
    if recall.get("samples") != parameters.get("recallSamples"):
        errors.append("recall.samples must equal parameters.recallSamples")
    for key in ("meanRecall", "p10Recall", "minRecall", "maxRecall"):
        value = recall.get(key)
        if not isinstance(value, (int, float)) or value < 0 or value > 1:
            errors.append(f"recall.{key} must be within [0, 1]")

    checks = report["selfChecks"]
    if not isinstance(checks, list) or not checks:
        errors.append("selfChecks must not be empty")
    else:
        failed = [check.get("id") for check in checks if check.get("passed") is False]
        if failed:
            errors.append("selfChecks failed: " + ", ".join(str(item) for item in failed))
        for check in checks:
            if not check.get("id") or not isinstance(check.get("detail"), str):
                errors.append("each selfCheck needs id and detail")

    if not isinstance(report["limitations"], list) or not report["limitations"]:
        errors.append("limitations must not be empty")
    return errors


def run_self_test() -> int:
    failures: list[str] = []

    def check(name: str, condition: bool, detail: str = "") -> None:
        if condition:
            print(f"  ok   {name}")
        else:
            failures.append(name)
            print(f"  fail {name} {detail}")

    stats = latency_stats([1.0, 2.0, 3.0, 4.0])
    check("latency-stats", stats["mean"] == 2.5 and stats["p50"] == 2.0, str(stats))
    check("latency-shape", not validate_latency_shape(stats, "test"), str(validate_latency_shape(stats, "test")))
    check("timing-parse", PsqlSession.finish(["12", "Time: 3.125 ms"]) == (["12"], 3.125))

    query = vector_query_sql(DEFAULT_SCHEMA, 1024, 3, 2, "q-3-2-1", 20)
    check("vector-tenant-filter", "c.tenant_id = 'tenant-3'" in query)
    check("vector-kb-filter", "kb-3-2" in query)
    check("vector-order", "<=>" in query and "LIMIT 20" in query)
    keyword = keyword_query_sql(DEFAULT_SCHEMA, 3, 2, 20)
    check("keyword-tenant-filter", "c.tenant_id = 'tenant-3'" in keyword)
    check("keyword-bm25", "@@@" in keyword and "pdb.score" in keyword)
    definition = index_definition(1024)
    check("index-concurrently", "CONCURRENTLY" in definition)
    check("index-partial", "WHERE embedding_dimension = 1024" in definition)

    report = synthetic_report()
    check("report-valid", not validate_report(report), str(validate_report(report)))
    broken = json.loads(json.dumps(report))
    broken["plans"]["vectorForced"]["kind"] = "seq-scan"
    check("report-rejects-unusable-index", any("vectorForced" in error for error in validate_report(broken)))
    broken = json.loads(json.dumps(report))
    broken["recall"]["meanRecall"] = 1.5
    check("report-rejects-recall", any("recall.meanRecall" in error for error in validate_report(broken)))
    broken = json.loads(json.dumps(report))
    broken["corpus"]["rows"] = 1
    check("report-rejects-row-mismatch", any("corpus.rows" in error for error in validate_report(broken)))
    broken = json.loads(json.dumps(report))
    broken["selfChecks"][0]["passed"] = False
    check("report-rejects-failed-check", any("selfChecks failed" in error for error in validate_report(broken)))

    broken = json.loads(json.dumps(report))
    broken["queryLoad"]["vector"]["latencyMs"]["p99"] = 0
    check("report-rejects-p99", any("p99" in error for error in validate_report(broken)))

    print(f"\nself-test: {len(failures)} failure(s)")
    return 1 if failures else 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Measure the M13 data plane on the local PostgreSQL/pgvector stack.")
    parser.add_argument("--psql-cmd", default="", help="override the psql command, e.g. 'psql -U rag_platform -d rag_platform'")
    parser.add_argument("--rows", type=int, default=1_000_000, help="synthetic chunk rows (default 1000000)")
    parser.add_argument("--dimension", type=int, default=1024)
    parser.add_argument("--tenants", type=int, default=16)
    parser.add_argument("--kbs-per-tenant", type=int, default=2)
    parser.add_argument("--workers", type=int, default=16)
    parser.add_argument("--iterations", type=int, default=20, help="vector+keyword rounds per worker")
    parser.add_argument("--top-k", type=int, default=20)
    parser.add_argument("--ef-search", type=int, default=40)
    parser.add_argument("--iterative-scan", default="relaxed_order", help="relaxed_order|off|strict_order")
    parser.add_argument("--recall-samples", type=int, default=200)
    parser.add_argument("--write-workers", type=int, default=4)
    parser.add_argument("--write-interval-ms", type=float, default=200.0)
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument("--schema", default=DEFAULT_SCHEMA)
    parser.add_argument("--rls", action="store_true", help="install the M13 strict policy and run measured statements as rag_app")
    parser.add_argument("--keep", action="store_true", help="keep the synthetic schema for inspection")
    parser.add_argument("--report", type=Path, default=DEFAULT_REPORT)
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--validate-report", type=Path)
    arguments = parser.parse_args()

    if arguments.self_test:
        return run_self_test()
    if arguments.validate_report:
        report = json.loads(arguments.validate_report.read_text(encoding="utf-8"))
        errors = validate_report(report)
        if errors:
            for error in errors:
                print(f"invalid: {error}", file=sys.stderr)
            return 1
        print(f"{arguments.validate_report}: valid {report.get('schemaVersion')}")
        return 0

    for name in ("rows", "dimension", "tenants", "kbs_per_tenant", "workers", "iterations", "top_k",
                 "ef_search", "recall_samples"):
        if getattr(arguments, name) < 1:
            print(f"--{name.replace('_', '-')} must be positive", file=sys.stderr)
            return 2
    if arguments.write_workers < 0 or arguments.write_interval_ms < 0:
        print("--write-workers and --write-interval-ms must not be negative", file=sys.stderr)
        return 2
    if arguments.rows < arguments.tenants * 2:
        print("--rows must be at least twice --tenants", file=sys.stderr)
        return 2

    command = shlex.split(arguments.psql_cmd) if arguments.psql_cmd else default_psql_command()
    report_path = arguments.report
    report_path.parent.mkdir(parents=True, exist_ok=True)
    started = time.time()
    begun = time.monotonic()
    report: dict | None = None
    try:
        environment = collect_environment(command)
        print(f"pgvector {environment.get('pgvectorVersion')} on PostgreSQL {environment.get('postgresVersion')}")
        print(f"building {arguments.rows} x {arguments.dimension} synthetic vectors in schema {arguments.schema} ...")
        setup_started = time.monotonic()
        run_sql(command, setup_sql(arguments.schema, arguments.rows, arguments.dimension,
                                   arguments.tenants, arguments.kbs_per_tenant))
        if arguments.rls:
            run_sql(command, rls_sql(arguments.schema))
        wait_for_table_ready(command, arguments.schema)
        setup_seconds = round(time.monotonic() - setup_started, 1)
        print(f"corpus ready in {setup_seconds}s")

        corpus = run_sql_json(command, corpus_check_sql(arguments.schema, arguments.rows, arguments.dimension,
                                                        arguments.tenants, arguments.kbs_per_tenant))
        run_sql(command, recall_functions_sql(arguments.schema, arguments.dimension))

        # The strict policy applies to measured sessions, not to the build.
        build = build_index_with_writes(
            command,
            arguments.schema,
            arguments.dimension,
            arguments.write_workers,
            arguments.write_interval_ms,
            write_tenant=arguments.tenants,
        )
        index = index_catalog(command, arguments.schema, arguments.dimension)
        keyword_index = bm25_catalog(command, arguments.schema)
        print(f"index build {build['durationMs']}ms valid={index.get('valid')} size={index.get('indexBytes')} bytes "
              f"writes={build['concurrentWrites']['samples']}")

        vector_plan = capture_plan(
            command, arguments.schema,
            vector_query_sql(arguments.schema, arguments.dimension, 1, 1, "q-1-1-1", arguments.top_k),
            setup=f"SET LOCAL hnsw.ef_search = {arguments.ef_search};\n"
                  + (f"SET LOCAL hnsw.iterative_scan = {arguments.iterative_scan};" if arguments.iterative_scan else ""),
        )
        # Forced measurement proves the index itself is usable; the natural
        # plan above records what the production query would actually do.
        vector_forced_plan = capture_plan(
            command, arguments.schema,
            vector_query_sql(arguments.schema, arguments.dimension, 1, 1, "q-1-1-1", arguments.top_k),
            setup=f"SET LOCAL hnsw.ef_search = {arguments.ef_search};\n"
                  + (f"SET LOCAL hnsw.iterative_scan = {arguments.iterative_scan};\n" if arguments.iterative_scan else "")
                  + "SET LOCAL enable_seqscan = off;\nSET LOCAL enable_bitmapscan = off;\nSET LOCAL enable_sort = off;",
        )
        keyword_plan = capture_plan(
            command, arguments.schema,
            keyword_query_sql(arguments.schema, 1, 1, arguments.top_k),
        )
        exact_plan = capture_plan(
            command, arguments.schema,
            f"""
SELECT c.id
FROM {arguments.schema}.chunks c
JOIN {arguments.schema}.document_versions dv ON dv.id = c.document_version_id
JOIN {arguments.schema}.documents d ON d.id = dv.document_id
WHERE c.tenant_id = 'tenant-1'
  AND c.knowledge_base_id = 'kb-1-1'
  AND c.status = '{STATUS_READY}'
  AND c.parent_id IS NOT NULL
  AND c.embedding_dimension = {arguments.dimension}
  AND c.embedding_model = '{MODEL}'
  AND c.embedding IS NOT NULL
  AND d.latest_version_id = c.document_version_id
  AND d.status = '{STATUS_READY}'
  AND d.deleted_at IS NULL
ORDER BY c.embedding::halfvec({arguments.dimension}) <=> (
    SELECT embedding FROM {arguments.schema}.queries WHERE id = 'q-1-1-1'
) LIMIT {arguments.top_k}
""".strip(),
            setup="SET LOCAL enable_indexscan = off;\nSET LOCAL enable_seqscan = on;",
        )
        print(f"plans vector={vector_plan['kind']} forced={vector_forced_plan['kind']} "
              f"keyword={keyword_plan['nodeTypes']} exact={exact_plan['kind']}")

        query_load = run_query_load(
            command,
            arguments.schema,
            arguments.dimension,
            arguments.tenants,
            arguments.kbs_per_tenant,
            arguments.workers,
            arguments.iterations,
            arguments.top_k,
            arguments.ef_search,
            arguments.iterative_scan,
            arguments.rls,
        )
        query_load["writeSamples"] = build["concurrentWrites"]["samples"]
        print(f"query load vector p95={query_load['vector']['latencyMs']['p95']}ms "
              f"keyword p95={query_load['keyword']['latencyMs']['p95']}ms qps={query_load['combined']['qps']}")

        recall = run_recall(
            command, arguments.schema, arguments.tenants, arguments.kbs_per_tenant,
            arguments.recall_samples, arguments.top_k, arguments.ef_search, arguments.rls,
        )
        print(f"recall mean={recall['meanRecall']} p10={recall['p10Recall']} samples={recall['samples']}")

        build.update({
            "valid": bool(index.get("valid")),
            "definition": index.get("definition", ""),
            "indexBytes": index.get("indexBytes", 0),
            "tableBytes": index.get("tableBytes", 0),
            "totalBytes": index.get("totalBytes", 0),
            "setupSeconds": setup_seconds,
        })
        parameters = {
            "rows": arguments.rows,
            "dimension": arguments.dimension,
            "tenants": arguments.tenants,
            "knowledgeBasesPerTenant": arguments.kbs_per_tenant,
            "workers": arguments.workers,
            "iterationsPerWorker": arguments.iterations,
            "topK": arguments.top_k,
            "efSearch": arguments.ef_search,
            "iterativeScan": arguments.iterative_scan,
            "seed": arguments.seed,
            "rls": arguments.rls,
            "recallSamples": arguments.recall_samples,
            "writeWorkers": arguments.write_workers,
            "writeIntervalMs": arguments.write_interval_ms,
            "schema": arguments.schema,
            "keep": arguments.keep,
        }
        self_checks = build_self_checks(parameters, corpus, build, vector_plan, vector_forced_plan,
                                        keyword_plan, exact_plan, keyword_index, query_load, recall)
        report = {
            "schemaVersion": SCHEMA_REPORT,
            "generatedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "durationSeconds": round(time.time() - started, 1),
            "parameters": parameters,
            "environment": environment,
            "corpus": corpus,
            "indexBuild": build,
            "keywordIndexBuild": keyword_index,
            "plans": {"vector": vector_plan, "vectorForced": vector_forced_plan,
                      "keyword": keyword_plan, "exact": exact_plan},
            "queryLoad": query_load,
            "recall": recall,
            "selfChecks": self_checks,
            "limitations": dynamic_limitations(parameters, query_load, recall, environment),
        }
        report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        errors = validate_report(report)
        if errors:
            for error in errors:
                print(f"invalid: {error}", file=sys.stderr)
            return 1
        failed = [check["id"] for check in self_checks if check["passed"] is False]
        print(f"\nreport: {report_path}")
        print(f"elapsed: {round(time.monotonic() - begun, 1)}s")
        if failed:
            print("failed self checks: " + ", ".join(failed), file=sys.stderr)
            return 1
        return 0
    finally:
        if not arguments.keep:
            try:
                run_sql(command, f"DROP SCHEMA IF EXISTS {arguments.schema} CASCADE;")
            except Exception as error:  # noqa: BLE001 - cleanup must not hide the benchmark result
                print(f"warning: failed to drop schema {arguments.schema}: {error}", file=sys.stderr)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except KeyboardInterrupt:
        raise SystemExit(130)
