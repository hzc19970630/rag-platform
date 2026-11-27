#!/usr/bin/env python3
"""T5-2 tool matrix: chat tool selection and answer quality across providers.

Builds one temporary knowledge base from the B40 docx/pptx table fixtures plus
the demo handbook, then runs every question against each configured chat model
twice: with tools (table_qa_enabled=auto) and as a pure-RAG baseline
(table_qa_enabled=off). Metrics come from the live SSE stream: tool_call /
tool_result events, the answer event's citations (table row fields included)
and the complete event's latency and token usage.

Requires the development stack (dev auth) and tool-capable chat models.
Environment:
  TOOL_MATRIX_MODELS   comma list, default "deepseek-flash,gpt-6.1-sol"
  TOOL_MATRIX_REPEATS  runs per question, default 3
  TOOL_MATRIX_ONLY     comma list of question ids to run (debug)
  TOOL_MATRIX_OUT_DIR  report directory, default tests/eval
  TOOL_MATRIX_REPORT   full report path, default <out>/tool_matrix_report.json
  TOOL_MATRIX_API_BASE default http://localhost:8082/api/v1

Scoring: aggregate/lookup questions require correct tool selection and, in
tools mode, a table-row citation for the named sheet. The navigation question
only scores tool selection: list_tables/table_schema are navigation-only and
never answer evidence (B40 invariant c), so a schema-only answer is refused by
the citation validator and is not counted against the model. Baseline runs do
not offer tools, so their tool-selection score is n/a rather than 0.
"""

import importlib.util
import json
import os
import statistics
import sys
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone

BASE = os.getenv("TOOL_MATRIX_API_BASE", os.getenv("B40_API_BASE", "http://localhost:8082/api/v1"))
HEADERS = {
    "X-Dev-User-ID": os.getenv("TOOL_MATRIX_DEV_USER", "dev-user"),
    "X-Dev-Tenant-ID": os.getenv("TOOL_MATRIX_DEV_TENANT", "dev-tenant"),
}
MODELS = [item.strip() for item in os.getenv("TOOL_MATRIX_MODELS", "deepseek-flash,gpt-6.1-sol").split(",") if item.strip()]
REPEATS = int(os.getenv("TOOL_MATRIX_REPEATS", "3"))
ONLY = {item.strip() for item in os.getenv("TOOL_MATRIX_ONLY", "").split(",") if item.strip()}
OUT_DIR = os.getenv("TOOL_MATRIX_OUT_DIR", "tests/eval")
REPORT_PATH = os.getenv("TOOL_MATRIX_REPORT", os.path.join(OUT_DIR, "tool_matrix_report.json"))

TERMINAL = ("COMPLETED", "REFUSED", "FAILED", "CANCELLED", "TIMEOUT", "TIMED_OUT")
TABLE_TOOLS = ("list_tables", "table_schema", "table_query")

QUESTIONS = [
    {
        "id": "docx-sum",
        "category": "aggregate",
        "question": "客户.docx 中「表格 1」的金额总和是多少？",
        "sheets": ["表格 1"],
        "expect_tools": ["table_query"],
        "forbid_tools": [],
        "answer_all": ["300"],
        "answer_any": [],
        "row_union_contains": ["100", "200"],
        "baseline": True,
    },
    {
        "id": "pptx-sum",
        "category": "aggregate",
        "question": "销售.pptx 中「第1页 表格1」的金额总和是多少？",
        "sheets": ["第1页 表格1"],
        "expect_tools": ["table_query"],
        "forbid_tools": [],
        "answer_all": ["300"],
        "answer_any": [],
        "row_union_contains": ["100", "200"],
        "baseline": True,
    },
    {
        "id": "docx-lookup",
        "category": "lookup",
        "question": "客户.docx 的表格里，A公司 在哪个区域？",
        "sheets": ["表格 1"],
        "expect_tools": ["table_query"],
        "forbid_tools": [],
        "answer_all": [],
        "answer_any": ["华东"],
        "row_contains": [["A公司", "华东"]],
        "baseline": True,
    },
    {
        "id": "pptx-lookup",
        "category": "lookup",
        "question": "销售.pptx 的表格里，金额 200 对应哪个区域？",
        "sheets": ["第1页 表格1"],
        "expect_tools": ["table_query"],
        "forbid_tools": [],
        "answer_all": [],
        "answer_any": ["华东"],
        "row_contains": [["200", "华东"]],
        "baseline": True,
    },
    {
        # Navigation probe: list_tables/table_schema are navigation-only and
        # never enter the evidence set (B40 invariant c), so a schema-only
        # answer has no citable evidence and is refused by design. Score tool
        # selection only; answer and grounding are not applicable.
        "id": "table-schema",
        "category": "navigation",
        "question": "这个知识库有哪些表格？各自的列是什么？",
        "sheets": [],
        "expect_tools": ["list_tables"],
        "forbid_tools": [],
        "answer_all": [],
        "answer_any": [],
        "answer_scored": False,
        "grounding_scored": False,
        "baseline": True,
    },
    {
        "id": "handbook-explain",
        "category": "explain",
        "question": "什么是混合检索（hybrid recall）？",
        "sheets": [],
        "expect_tools": [],
        "forbid_tools": list(TABLE_TOOLS),
        "answer_all": ["BM25"],
        "answer_any": ["向量", "vector", "余弦", "融合", "fusion"],
        "baseline": False,
    },
]


def load_fixtures():
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "b40_docreader_tables.py")
    spec = importlib.util.spec_from_file_location("b40fixtures", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def request(method, path, body=None, headers=None, expected=None, timeout=180):
    hdrs = dict(HEADERS)
    if headers:
        hdrs.update(headers)
    req = urllib.request.Request(BASE + path, data=body, headers=hdrs, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    if expected is not None and status != expected:
        raise AssertionError(f"{method} {path}: {status} {raw[:400]!r}")
    return status, raw


def json_request(method, path, payload=None, expected=200):
    body = json.dumps(payload).encode() if payload is not None else None
    headers = {"Content-Type": "application/json"} if body else {}
    status, raw = request(method, path, body, headers, expected)
    return json.loads(raw) if raw else {}


def upload(kb, filename, mime, content):
    boundary = "----toolmatrix" + uuid.uuid4().hex
    body = (
        f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{filename}\"\r\n"
        f"Content-Type: {mime}\r\n\r\n"
    ).encode() + content + f"\r\n--{boundary}--\r\n".encode()
    _, raw = request(
        "POST", f"/knowledge-bases/{kb}/documents", body,
        {"Content-Type": f"multipart/form-data; boundary={boundary}"}, 202,
    )
    return json.loads(raw)["document"]["id"]


def wait_ready(document, timeout=240):
    deadline = time.time() + timeout
    while time.time() < deadline:
        payload = json_request("GET", f"/documents/{document}/status")
        status = payload["version"]["status"]
        if status in ("EVIDENCE_READY", "FAILED", "CANCELLED"):
            return status
        time.sleep(3)
    return "TIMEOUT"


def ask(kb, question):
    session = json_request("POST", "/sessions", {"knowledge_base_id": kb, "title": "tool matrix"}, 201)
    session_id = session["id"]
    body = json.dumps({"query": question, "requestId": str(uuid.uuid4())}).encode()
    req = urllib.request.Request(
        BASE + f"/sessions/{session_id}/messages", data=body,
        headers={**HEADERS, "Content-Type": "application/json", "Accept": "text/event-stream"},
        method="POST",
    )
    events = []
    buffer = ""
    started = time.time()
    with urllib.request.urlopen(req, timeout=300) as response:
        while True:
            chunk = response.read(1024)
            if not chunk:
                break
            buffer += chunk.decode("utf-8", "replace")
            while "\n\n" in buffer:
                block, buffer = buffer.split("\n\n", 1)
                data = [line[5:].strip() for line in block.splitlines() if line.startswith("data:")]
                if data:
                    try:
                        events.append(json.loads("\n".join(data)))
                    except json.JSONDecodeError:
                        pass
    wall_ms = int((time.time() - started) * 1000)
    message = {}
    deadline = time.time() + 60
    while time.time() < deadline:
        payload = json_request("GET", f"/sessions/{session_id}/messages")
        assistants = [
            item.get("message", item) for item in payload.get("items", [])
            if (item.get("message", item)).get("role") == "assistant"
        ]
        if assistants and assistants[-1].get("status") in TERMINAL:
            message = assistants[-1]
            break
        time.sleep(2)
    tool_calls = [event.get("data", {}) for event in events if event.get("type") == "tool_call"]
    tool_results = [event.get("data", {}) for event in events if event.get("type") == "tool_result"]
    answer = next((event.get("data", {}) for event in reversed(events) if event.get("type") == "answer"), {})
    complete = next((event.get("data", {}) for event in reversed(events) if event.get("type") == "complete"), {})
    return {
        "sessionId": session_id,
        "message": message,
        "answer": answer,
        "complete": complete,
        "toolCalls": tool_calls,
        "toolResults": tool_results,
        "wallMs": wall_ms,
    }


def evaluate(run, question, tools_offered):
    tools = [call.get("tool", "") for call in run["toolCalls"]]
    content = run["message"].get("content") or run["answer"].get("content") or ""
    status = run["message"].get("status") or run["answer"].get("status") or ""
    validation = run["answer"].get("validation") or {}
    citations = run["answer"].get("citations") or []
    table_citations = [c for c in citations if c.get("tableAssetId")]
    answer_ok = None
    if question.get("answer_scored", True):
        answer_ok = all(text in content for text in question["answer_all"])
        if question["answer_any"]:
            answer_ok = answer_ok and any(text in content for text in question["answer_any"])
    selection_ok = None
    if tools_offered:
        selection_ok = all(name in tools for name in question["expect_tools"]) and not any(
            name in tools for name in question["forbid_tools"]
        )
    row_ok = None
    if question["sheets"]:
        valid_rows = [
            c for c in table_citations
            if c.get("sheet") in question["sheets"] and int(c.get("rowStart") or 0) >= 1
            and int(c.get("rowEnd") or 0) >= int(c.get("rowStart") or 0)
        ]
        row_ok = bool(valid_rows)
        for group in question.get("row_contains", []):
            row_ok = row_ok and any(all(value in (c.get("quote") or "") for value in group) for c in valid_rows)
        union = question.get("row_union_contains", [])
        if union:
            row_ok = row_ok and all(any(value in (c.get("quote") or "") for c in valid_rows) for value in union)
    grounded = None
    if question.get("grounding_scored", True):
        grounded = bool(validation.get("valid")) and len(citations) > 0
    trace = []
    for index, result in enumerate(run["toolResults"]):
        entry = {
            "tool": result.get("tool", ""), "isError": bool(result.get("isError")),
            "evidence": result.get("evidence", 0), "contentLength": result.get("contentLength", 0),
        }
        if index < len(run["toolCalls"]):
            entry["arguments"] = (run["toolCalls"][index].get("arguments") or "")[:200]
        trace.append(entry)
    return {
        "status": status,
        "tools": tools,
        "toolTrace": trace,
        "toolErrors": sum(1 for item in run["toolResults"] if item.get("isError")),
        "failedTools": [item.get("tool", "") for item in run["toolResults"] if item.get("isError")],
        "toolCalls": len(run["toolCalls"]),
        "answered": status == "COMPLETED",
        "refused": status == "REFUSED",
        "answerOk": answer_ok,
        "toolSelectionOk": selection_ok,
        "tableRowOk": row_ok,
        "citationCount": len(citations),
        "tableCitationCount": len(table_citations),
        "tableCitationQuotes": [(c.get("quote") or "")[:120] for c in table_citations],
        "grounded": grounded,
        "validationReason": validation.get("reason") or "",
        "evidenceCount": len(run["answer"].get("evidenceChunkIds") or []),
        "latencyMs": int(run["complete"].get("latencyMs") or run["message"].get("latencyMs") or 0),
        "wallMs": run["wallMs"],
        "promptTokens": run["complete"].get("promptTokens"),
        "completionTokens": run["complete"].get("completionTokens"),
        "contentPreview": content[:160],
    }


def summarize(runs):
    def ratio(items, key):
        values = [item[key] for item in items if item[key] is not None]
        return round(sum(1 for value in values if value) / len(values), 4) if values else None

    def mean(items, key, skip_zero=True):
        values = [item[key] for item in items if item[key] is not None and not (skip_zero and item[key] == 0)]
        return round(statistics.fmean(values), 1) if values else None

    table_runs = [item for item in runs if item["tableRowOk"] is not None]
    return {
        "runs": len(runs),
        "completed": sum(1 for item in runs if item["answered"]),
        "refused": sum(1 for item in runs if item["refused"]),
        "toolSelectionAccuracy": ratio(runs, "toolSelectionOk"),
        "answerAccuracy": ratio(runs, "answerOk"),
        "tableRowAccuracy": ratio(table_runs, "tableRowOk"),
        "groundedRate": ratio(runs, "grounded"),
        "avgToolCalls": mean(runs, "toolCalls", skip_zero=False),
        "avgLatencyMs": mean(runs, "latencyMs"),
        "avgPromptTokens": mean(runs, "promptTokens"),
        "avgCompletionTokens": mean(runs, "completionTokens"),
        "toolErrors": sum(item["toolErrors"] for item in runs),
    }


def main():
    fixtures = load_fixtures()
    os.makedirs(OUT_DIR, exist_ok=True)
    kb = json_request("POST", "/knowledge-bases", {"name": f"tool-matrix-{int(time.time())}"}, 201)["id"]
    print(f"knowledge base: {kb}")
    runs = []
    try:
        docx = fixtures.zip_bytes({"word/document.xml": fixtures.DOCX, "word/footnotes.xml": fixtures.FOOTNOTES})
        pptx = fixtures.zip_bytes({
            "ppt/presentation.xml": fixtures.PRESENTATION,
            "ppt/_rels/presentation.xml.rels": fixtures.PRESENTATION_RELS,
            "ppt/slides/slide1.xml": fixtures.SLIDE,
        })
        documents = [
            ("客户.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", docx),
            ("销售.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", pptx),
        ]
        with open("examples/demo/retrieval-handbook.md", "rb") as handle:
            documents.append(("retrieval-handbook.md", "text/markdown", handle.read()))
        for filename, mime, content in documents:
            document = upload(kb, filename, mime, content)
            status = wait_ready(document)
            if status != "EVIDENCE_READY":
                raise AssertionError(f"{filename} status={status}")
            print(f"ready: {filename}")

        tables = json_request("GET", f"/knowledge-bases/{kb}/tables").get("items", [])
        if len(tables) < 2:
            raise AssertionError(f"expected two table assets, got {len(tables)}")

        for model in MODELS:
            json_request("PATCH", f"/knowledge-bases/{kb}", {"chat_model": model})
            for mode in ("auto", "off"):
                json_request("PATCH", f"/knowledge-bases/{kb}", {"table_qa_enabled": mode})
                label = "tools" if mode == "auto" else "baseline"
                for question in QUESTIONS:
                    if mode == "off" and not question["baseline"]:
                        continue
                    if ONLY and question["id"] not in ONLY:
                        continue
                    for repeat in range(1, REPEATS + 1):
                        try:
                            run = ask(kb, question["question"])
                            metrics = evaluate(run, question, tools_offered=mode == "auto")
                            error = ""
                        except Exception as failure:  # noqa: BLE001 - record and continue the matrix
                            metrics = {
                                "status": "ERROR", "tools": [], "toolTrace": [], "toolErrors": 0,
                                "failedTools": [], "toolCalls": 0, "answered": False, "refused": False,
                                "answerOk": None, "toolSelectionOk": None, "tableRowOk": None,
                                "citationCount": 0, "tableCitationCount": 0, "tableCitationQuotes": [],
                                "grounded": None,
                                "validationReason": "", "evidenceCount": 0,
                                "latencyMs": 0, "wallMs": 0,
                                "promptTokens": None, "completionTokens": None, "contentPreview": "",
                            }
                            error = str(failure)[:300]
                        record = {
                            "model": model, "mode": label, "questionId": question["id"],
                            "category": question["category"], "repeat": repeat, "error": error, **metrics,
                        }
                        runs.append(record)
                        print(
                            f"{model} {label} {question['id']} #{repeat}: status={record['status']} "
                            f"tools={','.join(record['tools']) or '-'} answerOk={record['answerOk']} "
                            f"rowOk={record['tableRowOk']} calls={record['toolCalls']} "
                            f"failed={','.join(record['failedTools']) or '-'} "
                            f"reason={record['validationReason'] or '-'} {record['latencyMs']}ms"
                        )
    finally:
        json_request("DELETE", f"/knowledge-bases/{kb}", expected=204)

    summary = {}
    for model in MODELS:
        summary[model] = {}
        for label in ("tools", "baseline"):
            scoped = [item for item in runs if item["model"] == model and item["mode"] == label]
            if scoped:
                summary[model][label] = {
                    "overall": summarize(scoped),
                    "byCategory": {
                        category: summarize([item for item in scoped if item["category"] == category])
                        for category in ("aggregate", "lookup", "navigation", "explain")
                        if any(item["category"] == category for item in scoped)
                    },
                    "byQuestion": {
                        question["id"]: summarize([item for item in scoped if item["questionId"] == question["id"]])
                        for question in QUESTIONS
                        if any(item["questionId"] == question["id"] for item in scoped)
                    },
                }

    report = {
        "generatedAt": datetime.now(timezone.utc).isoformat(),
        "apiBase": BASE, "models": MODELS, "repeats": REPEATS,
        "questions": [
            {key: question.get(key) for key in ("id", "category", "question", "expect_tools", "forbid_tools", "answer_scored", "grounding_scored")}
            for question in QUESTIONS
        ],
        "summary": summary,
        "runs": runs,
    }
    json_path = REPORT_PATH
    with open(json_path, "w", encoding="utf-8") as handle:
        json.dump(report, handle, ensure_ascii=False, indent=2)

    print("\n| model | mode | runs | completed | refused | toolSel | answer | tableRow | grounded | avgMs | promptTok | completionTok | calls |")
    print("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
    for model in MODELS:
        for label in ("tools", "baseline"):
            overall = (summary.get(model, {}).get(label) or {}).get("overall")
            if not overall:
                continue
            print(
                f"| {model} | {label} | {overall['runs']} | {overall['completed']} | {overall['refused']} | "
                f"{overall['toolSelectionAccuracy']} | {overall['answerAccuracy']} | {overall['tableRowAccuracy']} | "
                f"{overall['groundedRate']} | {overall['avgLatencyMs']} | {overall['avgPromptTokens']} | "
                f"{overall['avgCompletionTokens']} | {overall['avgToolCalls']} |"
            )
    print("\n| model | question | runs | toolSel | answer | tableRow | grounded | calls | failedTools |")
    print("| --- | --- | --- | --- | --- | --- | --- | --- | --- |")
    for model in MODELS:
        for question in QUESTIONS:
            scoped = [item for item in runs if item["model"] == model and item["mode"] == "tools" and item["questionId"] == question["id"]]
            if not scoped:
                continue
            stats = summarize(scoped)
            failed = sorted({name for item in scoped for name in item["failedTools"]})
            print(
                f"| {model} | {question['id']} | {stats['runs']} | {stats['toolSelectionAccuracy']} | "
                f"{stats['answerAccuracy']} | {stats['tableRowAccuracy']} | {stats['groundedRate']} | "
                f"{stats['avgToolCalls']} | {','.join(failed) or '-'} |"
            )
    print(f"\nreport: {json_path}")


if __name__ == "__main__":
    sys.exit(main())