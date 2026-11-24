import hashlib
import io
import json
import math
import os
import re
import threading
import time
import uuid
from datetime import date, datetime, time as clock_time, timezone
from decimal import Decimal

import duckdb
import pyarrow as pa
import pyarrow.compute as pc
import pyarrow.dataset as ds
import pyarrow.parquet as pq
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from pyarrow.fs import FileSelector, S3FileSystem
from starlette.concurrency import run_in_threadpool


def _env_int(name, default):
    try:
        return int(os.getenv(name, str(default)))
    except ValueError:
        return default


def _env_bool(name, default):
    return os.getenv(name, str(default)).strip().lower() in ("1", "true", "yes", "on")


def _endpoint_override():
    endpoint = os.getenv("S3_ENDPOINT", "minio:9000").strip()
    if not endpoint:
        endpoint = "minio:9000"
    if "://" not in endpoint:
        scheme = "https" if _env_bool("S3_USE_TLS", False) else "http"
        endpoint = f"{scheme}://{endpoint}"
    return endpoint


BUCKET = os.getenv("S3_BUCKET", "rag-platform-local")
ROOT_PREFIX = os.getenv("TABLEHOUSE_S3_PREFIX", "tablehouse").strip().strip("/") or "tablehouse"
ROW_GROUP_ROWS = max(1000, _env_int("TABLEHOUSE_ROW_GROUP_ROWS", 100000))
QUERY_TIMEOUT_SECONDS = max(1, _env_int("TABLEHOUSE_QUERY_TIMEOUT_SECONDS", 10))
MEMORY_MB = max(128, _env_int("TABLEHOUSE_MEMORY_MB", 1024))
MAX_CONCURRENCY = max(1, _env_int("TABLEHOUSE_MAX_CONCURRENCY", 2))
MAX_SHEET_ROWS = max(1, _env_int("TABLEHOUSE_MAX_SHEET_ROWS", 5000000))
SQL_MAX_ROWS = max(1, _env_int("TABLEHOUSE_SQL_MAX_ROWS", 500))
SQL_MAX_LENGTH = max(200, _env_int("TABLEHOUSE_SQL_MAX_LENGTH", 8000))
SQL_TIMEOUT_SECONDS = max(1, _env_int("TABLEHOUSE_SQL_TIMEOUT_SECONDS", QUERY_TIMEOUT_SECONDS))
SQL_MAX_PARAMS = 32
SCHEMA_VERSION = 1

ROW_NUMBER_FIELD = "__row_number"
IDENTIFIER = re.compile(r"^[A-Za-z0-9_-]{1,128}$")
INT_VALUE = re.compile(r"^[+-]?\d+$")
LEADING_COMMENTS = re.compile(r"^(?:\s+|--[^\n]*(?:\n|$)|/\*.*?\*/)*", re.S)
SELECT_STATEMENT = re.compile(r"^(?i:select|with)\b")

FILESYSTEM = S3FileSystem(
    access_key=os.getenv("S3_ACCESS_KEY", os.getenv("MINIO_ROOT_USER", "rag-local")),
    secret_key=os.getenv("S3_SECRET_KEY", os.getenv("MINIO_ROOT_PASSWORD", "change-me-local-only")),
    endpoint_override=_endpoint_override(),
    region=os.getenv("S3_REGION", "us-east-1"),
)

QUERY_SLOTS = threading.BoundedSemaphore(MAX_CONCURRENCY)
STATE_LOCK = threading.RLock()
SHEET_STATE = {}

app = FastAPI(title="tablehouse", docs_url=None, redoc_url=None, openapi_url=None)


class RequestError(Exception):
    def __init__(self, status, message):
        super().__init__(message)
        self.status = status
        self.message = message


def _identifier(value, name):
    value = (value or "").strip()
    if not IDENTIFIER.match(value):
        raise RequestError(400, f"invalid {name}")
    return value


def _sheet_segment(sheet):
    raw = (sheet or "default").strip() or "default"
    clean = re.sub(r"[^A-Za-z0-9._-]+", "_", raw)[:96] or "sheet"
    digest = hashlib.sha1(raw.encode("utf-8")).hexdigest()[:8]
    return f"{clean}-{digest}"


def _prefix(body):
    tenant = _identifier(body.get("tenant"), "tenant")
    knowledge_base = _identifier(body.get("knowledge_base"), "knowledge_base")
    document = _identifier(body.get("document"), "document")
    version = _identifier(body.get("version"), "version")
    return f"{ROOT_PREFIX}/tenant={tenant}/kb={knowledge_base}/doc={document}/ver={version}"


def _sheet_prefix(prefix, sheet):
    return f"{prefix}/sheet={_sheet_segment(sheet)}"


def _object_path(relative):
    return f"{BUCKET}/{relative}"


def _now():
    return datetime.now(timezone.utc).isoformat()


def _arrow_table(rows, width):
    columns = []
    for index in range(width):
        columns.append(pa.array([row[index] if index < len(row) else "" for row in rows], type=pa.string()))
    return columns


def _write_part(state):
    if not state["rows"]:
        return
    columns = _arrow_table(state["rows"], state["width"])
    arrays = {f"c{index}": column for index, column in enumerate(columns)}
    arrays[ROW_NUMBER_FIELD] = pa.array(state["row_numbers"], type=pa.int64())
    table = pa.table(arrays)
    name = f"part-{state['seq']:06d}-{uuid.uuid4().hex[:8]}.parquet"
    path = _object_path(f"{state['path']}/{name}")
    pq.write_table(table, path, filesystem=FILESYSTEM, compression="zstd", row_group_size=ROW_GROUP_ROWS)
    info = FILESYSTEM.get_file_info(path)
    state["files"].append({"name": name, "rows": len(state["rows"]), "bytes": info.size or 0})
    state["bytes"] += info.size or 0
    state["rows"] = []
    state["row_numbers"] = []
    state["seq"] += 1


def _state_for(prefix, sheet, header, column_types):
    path = _sheet_prefix(prefix, sheet)
    with STATE_LOCK:
        state = SHEET_STATE.get(path)
        if state is None:
            state = {
                "path": path, "header": list(header), "column_types": list(column_types),
                "width": len(header), "rows": [], "row_numbers": [], "seq": 0,
                "rows_total": 0, "bytes": 0, "files": [], "truncated": False,
            }
            SHEET_STATE[path] = state
        return state


def _finish_sheet(state, body):
    _write_part(state)
    manifest = {
        "schema_version": SCHEMA_VERSION,
        "sheet": body.get("sheet", ""),
        "header": state["header"],
        "column_types": state["column_types"],
        "rows": state["rows_total"],
        "parts": state["files"],
        "bytes": state["bytes"],
        "content_hash": body.get("content_hash", ""),
        "truncated": bool(body.get("truncated")) or state["truncated"],
        "created_at": _now(),
    }
    payload = json.dumps(manifest, ensure_ascii=False).encode("utf-8")
    path = _object_path(f"{state['path']}/_manifest.json")
    with FILESYSTEM.open_output_stream(path) as stream:
        stream.write(payload)
    return len(payload)


def _read_manifest(prefix, sheet):
    path = _object_path(f"{_sheet_prefix(prefix, sheet)}/_manifest.json")
    try:
        with FILESYSTEM.open_input_stream(path) as stream:
            return json.loads(stream.read().decode("utf-8"))
    except (FileNotFoundError, OSError):
        return None


def _filter_expression(field, op, value, column_type):
    op = (op or "").strip().lower()
    value = (value or "").strip()
    if op == "eq":
        return field == value
    if op == "ne":
        return field != value
    if op == "contains":
        return pc.match_substring(field, value, ignore_case=True)
    if op not in ("gt", "gte", "lt", "lte"):
        raise RequestError(400, f"unsupported filter op: {op}")
    numeric = column_type in ("int", "float") and _is_number(value)
    if numeric:
        left = pc.cast(field, pa.float64(), safe=False)
        right = float(value)
    else:
        left = pc.utf8_lower(field)
        right = value.lower()
    if op == "gt":
        return left > right
    if op == "gte":
        return left >= right
    if op == "lt":
        return left < right
    return left <= right


def _is_number(value):
    try:
        float(value)
        return True
    except (TypeError, ValueError):
        return False


def _quote(identifier):
    return '"' + identifier.replace('"', '""') + '"'


def _query(body):
    prefix = _prefix(body)
    sheet = body.get("sheet") or "default"
    manifest = _read_manifest(prefix, sheet)
    if manifest is None:
        raise RequestError(404, "table parquet not materialized")
    header = manifest["header"]
    column_types = manifest["column_types"]
    index_by_name = {name: index for index, name in enumerate(header)}
    if not manifest.get("parts"):
        return {"header": header, "totalRows": manifest.get("rows", 0), "matchedRows": 0, "rows": []}
    dataset = ds.dataset(
        _object_path(_sheet_prefix(prefix, sheet)),
        filesystem=FILESYSTEM, format="parquet", exclude_invalid_files=True,
    )
    filters = body.get("filters") or []
    expression = None
    for item in filters:
        column = item.get("column", "")
        if column not in index_by_name:
            raise RequestError(400, f"unknown filter column: {column}")
        index = index_by_name[column]
        condition = _filter_expression(ds.field(f"c{index}"), item.get("op"), str(item.get("value", "")), column_types[index])
        expression = condition if expression is None else expression & condition
    limit = body.get("limit") or 50
    try:
        limit = int(limit)
    except (TypeError, ValueError):
        limit = 50
    limit = max(1, min(limit, 500))
    matched = dataset.count_rows(filter=expression)
    if matched == 0:
        return {"header": header, "totalRows": manifest.get("rows", 0), "matchedRows": 0, "rows": []}
    order_by = (body.get("order_by") or "").strip()
    columns = [f"c{index}" for index in range(len(header))] + [ROW_NUMBER_FIELD]
    if not order_by:
        table = dataset.scanner(columns=columns, filter=expression).head(limit)
        return _rows_response(header, manifest, matched, table)
    if order_by not in index_by_name:
        raise RequestError(400, f"unknown order column: {order_by}")
    order_index = index_by_name[order_by]
    table = dataset.scanner(columns=columns, filter=expression).to_table()
    direction = "DESC" if body.get("desc") else "ASC"
    if column_types[order_index] in ("int", "float"):
        order = f"TRY_CAST({_quote(f'c{order_index}')} AS DOUBLE) {direction} NULLS LAST"
    else:
        order = f"lower({_quote(f'c{order_index}')}) {direction} NULLS LAST"
    connection = duckdb.connect()
    try:
        connection.execute(f"SET memory_limit='{MEMORY_MB}MB'")
        connection.execute("SET threads=2")
        connection.execute("SET temp_directory='/tmp'")
        timer = threading.Timer(QUERY_TIMEOUT_SECONDS, connection.interrupt)
        timer.start()
        try:
            connection.register("rows", table)
            sorted_table = connection.execute(
                f"SELECT * FROM rows ORDER BY {order} LIMIT {limit}"
            ).arrow()
        finally:
            timer.cancel()
    except duckdb.InterruptException as error:
        raise RequestError(504, "query timeout") from error
    finally:
        connection.close()
    return _rows_response(header, manifest, matched, sorted_table)


def _rows_response(header, manifest, matched, table):
    rows = []
    width = len(header)
    row_numbers = table.column(ROW_NUMBER_FIELD).to_pylist()
    columns = [table.column(f"c{index}").to_pylist() for index in range(width)]
    for position, row_number in enumerate(row_numbers):
        cells = {}
        for index, name in enumerate(header):
            value = columns[index][position]
            cells[name] = "" if value is None else value
        rows.append({"rowNumber": int(row_number or 0), "cells": cells})
    return {"header": header, "totalRows": manifest.get("rows", 0), "matchedRows": matched, "rows": rows}


def _sql_columns(header):
    # The same rule runs in Go to expose sqlColumns on the catalog API, so the
    # SQL-facing names stay stable even when a sheet repeats a header cell.
    names = []
    seen = set()
    for index, raw in enumerate(header):
        name = str(raw).strip() or f"column_{index + 1}"
        candidate = name
        suffix = 2
        while candidate.lower() in seen or candidate.lower() == ROW_NUMBER_FIELD:
            candidate = f"{name}_{suffix}"
            suffix += 1
        seen.add(candidate.lower())
        names.append(candidate)
    return names


def _json_value(value):
    if value is None or isinstance(value, (str, bool, int)):
        return value
    if isinstance(value, float):
        return None if math.isnan(value) or math.isinf(value) else value
    if isinstance(value, Decimal):
        return float(value) if value.is_finite() else str(value)
    if isinstance(value, (datetime, date, clock_time)):
        return value.isoformat()
    if isinstance(value, (bytes, bytearray)):
        return bytes(value).hex()
    return str(value)


def _validate_sql(raw):
    sql = (raw or "").strip()
    if not sql:
        raise RequestError(400, "sql is required")
    if len(sql) > SQL_MAX_LENGTH:
        raise RequestError(400, "sql is too long")
    try:
        statements = duckdb.extract_statements(sql)
    except Exception as error:  # noqa: BLE001 - parser errors are caller errors
        raise RequestError(400, f"invalid sql: {str(error)[:160]}") from error
    if len(statements) != 1:
        raise RequestError(400, "exactly one statement is allowed")
    statement = statements[0]
    if statement.type != duckdb.StatementType.SELECT:
        raise RequestError(400, "only SELECT statements are allowed")
    # DuckDB rewrites PRAGMA into a SELECT over a pragma_* table function, so
    # the leading keyword must be checked on the original text.
    if not SELECT_STATEMENT.match(LEADING_COMMENTS.sub("", sql)):
        raise RequestError(400, "only SELECT statements are allowed")
    return statement.query.strip().rstrip(";").strip()


def _sql_params(body):
    params = body.get("params") or []
    if not isinstance(params, list):
        raise RequestError(400, "params must be a list")
    if len(params) > SQL_MAX_PARAMS:
        raise RequestError(400, f"at most {SQL_MAX_PARAMS} parameters are allowed")
    for value in params:
        if isinstance(value, (dict, list)):
            raise RequestError(400, "parameter values must be scalars")
    return params


def _sql_connection(manifest, dataset):
    columns = _sql_columns(manifest.get("header") or [])
    selects = [f"{_quote(f'c{index}')} AS {_quote(name)}" for index, name in enumerate(columns)]
    selects.append(_quote(ROW_NUMBER_FIELD))
    connection = duckdb.connect()
    connection.register("__sheet", dataset)
    connection.execute(f"CREATE VIEW t AS SELECT {', '.join(selects)} FROM __sheet")
    connection.execute(f"SET memory_limit='{MEMORY_MB}MB'")
    connection.execute("SET threads=2")
    connection.execute("SET temp_directory='/tmp'")
    connection.execute("SET autoinstall_known_extensions=false")
    connection.execute("SET autoload_known_extensions=false")
    connection.execute("SET disabled_filesystems='LocalFileSystem,HTTPFileSystem,S3FileSystem'")
    connection.execute("SET enable_external_access=false")
    return connection


def _run_sql(body):
    prefix = _prefix(body)
    sheet = body.get("sheet") or "default"
    manifest = _read_manifest(prefix, sheet)
    if manifest is None or not manifest.get("parts"):
        raise RequestError(404, "table parquet not materialized")
    sql = _validate_sql(body.get("sql"))
    params = _sql_params(body)
    preview = bool(body.get("preview"))
    try:
        limit = int(body.get("limit") or 100)
    except (TypeError, ValueError):
        limit = 100
    limit = max(1, min(limit, SQL_MAX_ROWS))
    dataset = ds.dataset(
        _object_path(_sheet_prefix(prefix, sheet)),
        filesystem=FILESYSTEM, format="parquet", exclude_invalid_files=True,
    )
    connection = _sql_connection(manifest, dataset)
    started = time.monotonic()
    timer = threading.Timer(SQL_TIMEOUT_SECONDS, connection.interrupt)
    timer.start()
    try:
        cursor = connection.execute(f"SELECT * FROM ({sql}) AS __q LIMIT 0", params)
        columns = [{"name": item[0], "type": str(item[1] or "").upper()} for item in cursor.description or []]
        total = int(connection.execute(f"SELECT count(*) FROM ({sql}) AS __q", params).fetchone()[0] or 0)
        rows = []
        if not preview:
            result = connection.execute(f"SELECT * FROM ({sql}) AS __q LIMIT {limit}", params)
            rows = [[_json_value(value) for value in row] for row in result.fetchall()]
        return {
            "columns": columns, "rows": rows, "preview": preview,
            "rowCount": total, "returnedRows": len(rows), "truncated": total > limit,
            "elapsedMs": int((time.monotonic() - started) * 1000),
        }
    except duckdb.InterruptException as error:
        raise RequestError(504, "query timeout") from error
    except duckdb.Error as error:
        raise RequestError(400, f"invalid sql: {str(error)[:200]}") from error
    finally:
        timer.cancel()
        connection.close()


@app.get("/healthz")
def healthz():
    return {"status": "ok"}


@app.get("/readyz")
def readyz():
    try:
        info = FILESYSTEM.get_file_info(BUCKET)
        if info.type.name == "NotFound":
            return JSONResponse(status_code=503, content={"status": "unavailable", "error": "bucket not found"})
        return {"status": "ok", "bucket": BUCKET}
    except Exception as error:  # noqa: BLE001 - readiness reports the failure
        return JSONResponse(status_code=503, content={"status": "unavailable", "error": str(error)[:200]})


def _materialize_batch(body):
    prefix = _prefix(body)
    sheet = body.get("sheet") or "default"
    header = [str(item) for item in body.get("header") or []]
    column_types = [str(item) for item in body.get("column_types") or []]
    rows = body.get("rows") or []
    done = bool(body.get("sheet_done"))
    if not header or (not rows and not done):
        raise RequestError(400, "header and rows are required")
    if len(column_types) != len(header):
        column_types = ["string"] * len(header)
    start = int(body.get("row_start") or 0)
    with STATE_LOCK:
        state = _state_for(prefix, sheet, header, column_types)
        if state["rows_total"] + len(rows) > MAX_SHEET_ROWS:
            state["truncated"] = True
            raise RequestError(413, "sheet row limit exceeded")
        for offset, row in enumerate(rows):
            state["rows"].append([str(cell) for cell in row])
            state["row_numbers"].append(start + offset)
        state["rows_total"] += len(rows)
        if len(state["rows"]) >= ROW_GROUP_ROWS:
            _write_part(state)
        if not done:
            return {"status": "accepted", "rows": state["rows_total"], "path": state["path"]}
        SHEET_STATE.pop(state["path"], None)
    size = _finish_sheet(state, body)
    return {"status": "done", "rows": state["rows_total"], "bytes": state["bytes"], "path": state["path"], "manifestBytes": size}


def _finish_existing(body):
    prefix = _prefix(body)
    sheet = body.get("sheet") or "default"
    with STATE_LOCK:
        state = SHEET_STATE.pop(_sheet_prefix(prefix, sheet), None)
    if state is None:
        raise RequestError(404, "no open sheet")
    size = _finish_sheet(state, body)
    return {"status": "done", "rows": state["rows_total"], "bytes": state["bytes"], "path": state["path"], "manifestBytes": size}


def _clear_version(body):
    prefix = _prefix(body)
    with STATE_LOCK:
        for key in [key for key in SHEET_STATE if key.startswith(prefix + "/")]:
            SHEET_STATE.pop(key, None)
    selector = _object_path(prefix) + "/"
    try:
        listing = FILESYSTEM.get_file_info(FileSelector(selector, recursive=True))
    except OSError:
        listing = []
    removed = 0
    if isinstance(listing, list):
        removed = sum(1 for item in listing if item.type.name == "File")
    # Deleting files one by one leaves the zero-byte directory markers that
    # pyarrow creates alongside each sheet; a recursive delete removes the whole
    # version prefix so repeated rebuilds do not accumulate objects.
    try:
        FILESYSTEM.delete_dir(selector)
    except FileNotFoundError:
        pass
    return {"status": "cleared", "removed": removed}


def _translate(error):
    if isinstance(error, RequestError):
        return JSONResponse(status_code=error.status, content={"error": error.message})
    return JSONResponse(status_code=500, content={"error": str(error)[:200]})


@app.post("/materialize/rows")
async def materialize_rows(request: Request):
    try:
        body = await request.json()
        return await run_in_threadpool(_materialize_batch, body)
    except Exception as error:  # noqa: BLE001 - internal failures must not leak details
        return _translate(error)


@app.post("/materialize/finish")
async def materialize_finish(request: Request):
    try:
        body = await request.json()
        return await run_in_threadpool(_finish_existing, body)
    except Exception as error:  # noqa: BLE001
        return _translate(error)


@app.post("/materialize/version/clear")
async def materialize_clear(request: Request):
    try:
        body = await request.json()
        return await run_in_threadpool(_clear_version, body)
    except Exception as error:  # noqa: BLE001
        return _translate(error)


@app.post("/query")
def query(body: dict):
    try:
        if not QUERY_SLOTS.acquire(blocking=False):
            return JSONResponse(status_code=429, content={"error": "tablehouse busy"})
        try:
            return _query(body)
        finally:
            QUERY_SLOTS.release()
    except RequestError as error:
        return JSONResponse(status_code=error.status, content={"error": error.message})
    except Exception as error:  # noqa: BLE001
        return JSONResponse(status_code=500, content={"error": f"query failed: {str(error)[:200]}"})


@app.post("/sql")
def sql_query(body: dict):
    try:
        if not QUERY_SLOTS.acquire(blocking=False):
            return JSONResponse(status_code=429, content={"error": "tablehouse busy"})
        try:
            return _run_sql(body)
        finally:
            QUERY_SLOTS.release()
    except RequestError as error:
        return JSONResponse(status_code=error.status, content={"error": error.message})
    except Exception as error:  # noqa: BLE001
        return JSONResponse(status_code=500, content={"error": f"query failed: {str(error)[:200]}"})


@app.get("/store/stats")
def store_stats(tenant: str, knowledge_base: str, document: str, version: str):
    try:
        prefix = _prefix({"tenant": tenant, "knowledge_base": knowledge_base, "document": document, "version": version})
    except RequestError as error:
        return JSONResponse(status_code=error.status, content={"error": error.message})
    listing = FILESYSTEM.get_file_info(FileSelector(_object_path(prefix), recursive=True))
    files = [{"path": item.path, "bytes": item.size} for item in listing if item.type.name == "File"]
    return {"prefix": prefix, "files": len(files), "bytes": sum(item["bytes"] for item in files)}


if __name__ == "__main__":
    import uvicorn

    port = _env_int("TABLEHOUSE_PORT", 8088)
    started = time.time()
    uvicorn.run(app, host="0.0.0.0", port=port, log_level=os.getenv("TABLEHOUSE_LOG_LEVEL", "info"))
    print(f"tablehouse stopped after {time.time() - started:.0f}s", flush=True)