"""HTTP client shared by the evaluation suites.

Authentication is explicit rather than baked in: a suite can send development
headers, log in for a bearer token, or pass API credentials through ``headers``.
That keeps the permission probes usable with a second principal and leaves room
for a real-deployment profile without rewriting every call site.
"""

from __future__ import annotations

import json
import re
import urllib.error
import urllib.request
import uuid

DEV_USER_ID = "dev-user"
DEV_TENANT_ID = "dev-tenant"


def dev_headers(user_id: str = DEV_USER_ID, tenant_id: str = DEV_TENANT_ID) -> dict[str, str]:
    return {"X-Dev-User-ID": user_id, "X-Dev-Tenant-ID": tenant_id}


DEV_HEADERS = dev_headers()


def multipart_body(
    filename: str,
    content: str,
    content_type: str = "text/markdown",
    boundary_prefix: str = "evalkit",
) -> tuple[bytes, str]:
    boundary = f"----{boundary_prefix}{uuid.uuid4().hex}"
    body = (
        f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{filename}\"\r\n"
        f"Content-Type: {content_type}\r\n\r\n{content}\r\n--{boundary}--\r\n"
    ).encode()
    return body, f"multipart/form-data; boundary={boundary}"


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


class Client:
    def __init__(
        self,
        base_url: str,
        headers: dict[str, str] | None = None,
        timeout: int = 120,
    ) -> None:
        self.base = base_url.rstrip("/")
        self.timeout = timeout
        self.headers: dict[str, str] = {"Accept": "application/json"}
        if headers:
            self.headers.update(headers)
        self.token = ""

    def request(
        self,
        method: str,
        path: str,
        body: dict | None = None,
        raw: bytes | None = None,
        content_type: str | None = None,
        timeout: int | None = None,
    ) -> tuple[int, bytes]:
        data = raw
        headers = dict(self.headers)
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        if content_type:
            headers["Content-Type"] = content_type
        request = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=timeout or self.timeout) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.read()
        except urllib.error.URLError as error:
            # A connection failure is not an authorization result. Raising keeps
            # callers from reading status 0 as "denied" or "allowed".
            raise RuntimeError(f"{method} {path} got no response: {error.reason}") from error

    def json(self, method: str, path: str, body: dict | None = None) -> tuple[int, dict]:
        status, payload = self.request(method, path, body=body)
        try:
            return status, json.loads(payload or b"{}")
        except json.JSONDecodeError:
            return status, {}

    def login(self, user_id: str = DEV_USER_ID, tenant_id: str = DEV_TENANT_ID) -> None:
        status, payload = self.json(
            "POST", "/api/v1/auth/dev-login", {"userId": user_id, "tenantId": tenant_id},
        )
        if status != 200 or not payload.get("token"):
            raise RuntimeError(f"dev login failed: {status} {payload}")
        self.token = payload["token"]
        self.headers["Authorization"] = f"Bearer {self.token}"

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
        return {
            "answer": answer.get("data", {}),
            "complete": complete.get("data", {}),
            "events": events,
            "requestId": body["requestId"],
        }
