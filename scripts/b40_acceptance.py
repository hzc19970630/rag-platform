#!/usr/bin/env python3
"""B40 acceptance: docx/pptx table assets, skipped media and chat tool calls.

Requires the development stack (dev auth) and a tool-capable chat model. Set
B40_SKIP_CHAT=1 to verify parsing/tables only.
"""

import importlib.util
import json
import os
import sys
import time
import urllib.error
import urllib.request
import uuid

BASE = os.getenv("B40_API_BASE", "http://localhost:8082/api/v1")
HEADERS = {
    "X-Dev-User-ID": os.getenv("B40_DEV_USER", "dev-user"),
    "X-Dev-Tenant-ID": os.getenv("B40_DEV_TENANT", "dev-tenant"),
}
SKIP_CHAT = os.getenv("B40_SKIP_CHAT") == "1"


def load_fixtures():
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "b40_docreader_tables.py")
    spec = importlib.util.spec_from_file_location("b40fixtures", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def request(method, path, body=None, headers=None, expected=None):
    hdrs = dict(HEADERS)
    if headers:
        hdrs.update(headers)
    req = urllib.request.Request(BASE + path, data=body, headers=hdrs, method=method)
    try:
        with urllib.request.urlopen(req, timeout=120) as response:
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
    boundary = "----b40accept" + uuid.uuid4().hex
    body = (
        f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{filename}\"\r\n"
        f"Content-Type: {mime}\r\n\r\n"
    ).encode() + content + f"\r\n--{boundary}--\r\n".encode()
    status, raw = request(
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
            return status, payload
        time.sleep(3)
    return "TIMEOUT", {}


def chat_with_tools(kb, question):
    session = json_request("POST", "/sessions", {"knowledge_base_id": kb, "title": "B40 acceptance"}, 201)
    session_id = session["id"]
    request_id = str(uuid.uuid4())
    body = json.dumps({"query": question, "requestId": request_id}).encode()
    req = urllib.request.Request(
        BASE + f"/sessions/{session_id}/messages", data=body,
        headers={**HEADERS, "Content-Type": "application/json", "Accept": "text/event-stream"},
        method="POST",
    )
    events = []
    buffer = ""
    with urllib.request.urlopen(req, timeout=180) as response:
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
    for _ in range(60):
        messages = json_request("GET", f"/sessions/{session_id}/messages")
        assistants = [item.get("message", item) for item in messages.get("items", [])
                      if (item.get("message", item)).get("role") == "assistant"]
        if assistants and assistants[-1].get("status") in ("COMPLETED", "REFUSED", "FAILED", "CANCELLED", "TIMEOUT", "TIMED_OUT"):
            return events, assistants[-1]
        time.sleep(2)
    raise AssertionError("chat answer did not reach a terminal state")


def main():
    fixtures = load_fixtures()
    kb = json_request("POST", "/knowledge-bases", {"name": "B40 acceptance"}, 201)["id"]
    try:
        chat_model = os.getenv("B40_CHAT_MODEL", "").strip()
        if chat_model:
            json_request("PATCH", f"/knowledge-bases/{kb}", {"chat_model": chat_model})
        docx = fixtures.zip_bytes({"word/document.xml": fixtures.DOCX, "word/footnotes.xml": fixtures.FOOTNOTES})
        pptx = fixtures.zip_bytes({
            "ppt/presentation.xml": fixtures.PRESENTATION,
            "ppt/_rels/presentation.xml.rels": fixtures.PRESENTATION_RELS,
            "ppt/slides/slide1.xml": fixtures.SLIDE,
        })
        docx_id = upload(kb, "客户.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", docx)
        pptx_id = upload(kb, "销售.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", pptx)
        for name, document, media in (
            ("docx", docx_id, (1, 1, 1)),
            ("pptx", pptx_id, (1, 1, 0)),
        ):
            status, payload = wait_ready(document)
            assert status == "EVIDENCE_READY", f"{name} status={status} {payload}"
            estimate = json_request("GET", f"/documents/{document}/index-estimate")
            assert estimate["tableCount"] == 1, estimate
            assert (estimate["skippedImages"], estimate["skippedCharts"], estimate["skippedNotes"]) == media, estimate
            print(f"{name}: parsed rows={estimate['parsedRows']} skipped={media}")

        assets = json_request("GET", f"/knowledge-bases/{kb}/tables")["items"]
        sheets = {asset["sheet"] for asset in assets}
        assert sheets == {"表格 1", "第1页 表格1"}, assets
        assert all(asset["materializeStatus"] == "ready" for asset in assets), assets
        for asset in assets:
            answer = json_request("POST", f"/knowledge-bases/{kb}/tables/ask", {
                "assetId": asset["id"], "question": f"{asset['sheet']} 的金额总和是多少",
            })
            assert answer.get("value") == 300, answer
        print("tables: two sheets materialized, sum=300")

        if not SKIP_CHAT:
            events, message = chat_with_tools(kb, "表格 1 的金额总和是多少")
            calls = [event for event in events if event.get("type") == "tool_call"]
            assert any((event.get("data") or {}).get("tool") == "table_query" for event in calls), calls
            assert message.get("status") == "COMPLETED", message
            assert "300" in (message.get("content") or ""), message
            print("chat: tool_call/table_query ->", (message.get("content") or "")[:120])
        print("B40 acceptance: ok")
    finally:
        request("DELETE", f"/knowledge-bases/{kb}", expected=204)


if __name__ == "__main__":
    try:
        main()
    except AssertionError as error:
        print(f"B40 acceptance failed: {error}", file=sys.stderr)
        raise SystemExit(1)