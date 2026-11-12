"""Thin Python client for the RAG Platform HTTP API."""

from __future__ import annotations

import time
from dataclasses import dataclass, field
from typing import Any, Iterator

import httpx

from .operations import OPERATIONS


class RagApiError(Exception):
    def __init__(self, status: int, code: str, message: str = "") -> None:
        self.status = status
        self.code = code
        self.message = message
        super().__init__(f"{code}: {message}" if message else code)


@dataclass
class AskResult:
    session_id: str
    request_id: str
    content: str
    status: str
    citations: list[dict[str, Any]] = field(default_factory=list)
    code: str = ""


class RagClient:
    def __init__(
        self,
        base_url: str,
        api_key: str,
        *,
        timeout: float = 180.0,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self._http = httpx.Client(timeout=timeout, transport=transport)

    def close(self) -> None:
        self._http.close()

    def __enter__(self) -> "RagClient":
        return self

    def __exit__(self, *_: object) -> None:
        self.close()

    def list_knowledge_bases(self) -> dict[str, Any]:
        return self._request("list_knowledge_bases")

    def search(
        self,
        query: str,
        knowledge_base_id: str,
        session_override: str | None = None,
    ) -> dict[str, Any]:
        body: dict[str, Any] = {"query": query, "knowledgeBaseId": knowledge_base_id}
        if session_override:
            body["sessionOverride"] = session_override
        return self._request("search", json=body)

    def get_document_status(self, document_id: str) -> dict[str, Any]:
        return self._request("document_status", document_id=document_id)

    def get_citation(self, chunk_id: str) -> dict[str, Any]:
        return self._request("citation", chunk_id=chunk_id)

    def create_session(self, knowledge_base_id: str, title: str | None = None) -> dict[str, Any]:
        body: dict[str, Any] = {"knowledge_base_id": knowledge_base_id}
        if title:
            body["title"] = title
        return self._request("create_session", json=body)

    def ask(
        self,
        question: str,
        *,
        knowledge_base_id: str | None = None,
        session_id: str | None = None,
        title: str | None = None,
        request_id: str | None = None,
    ) -> AskResult:
        if not session_id:
            if not knowledge_base_id:
                raise ValueError("knowledge_base_id is required when session_id is absent")
            conversation = self.create_session(knowledge_base_id, title)
            session_id = conversation.get("id", "")
            if not session_id:
                raise RuntimeError("session creation returned no id")
        request_id = request_id or f"sdk-{time.time_ns()}"
        result = AskResult(session_id=session_id, request_id=request_id, content="", status="")
        last_sequence = 0
        complete = False
        tail_timeout = False

        def handle(event: dict[str, Any]) -> None:
            nonlocal last_sequence, complete, tail_timeout, result
            sequence = int(event.get("sequence") or 0)
            if sequence > last_sequence:
                last_sequence = sequence
            data = event.get("data") or {}
            event_type = event.get("type")
            if event_type == "answer":
                if isinstance(data.get("content"), str):
                    result.content = data["content"]
                if isinstance(data.get("status"), str):
                    result.status = data["status"]
                citations = data.get("citations")
                if isinstance(citations, list):
                    result.citations = citations
            elif event_type == "complete":
                if isinstance(data.get("status"), str):
                    result.status = data["status"]
                result.code = str(data.get("code") or "")
                complete = True
            elif event_type == "error":
                code = str(data.get("code") or "error")
                if code == "tail_timeout":
                    tail_timeout = True
                    return
                raise RagApiError(0, code, str(data.get("message") or ""))

        self._stream(
            "ask",
            json={"query": question, "requestId": request_id},
            session_id=session_id,
            handle=handle,
        )
        for attempt in range(3):
            if complete:
                break
            if not tail_timeout and last_sequence == 0:
                break
            tail_timeout = False
            time.sleep(0.25 * (attempt + 1))
            self._stream(
                "events",
                params={"stream": "1", "after": str(last_sequence)},
                request_id=request_id,
                handle=handle,
            )
        if not complete:
            raise RuntimeError(f"answer stream ended before a terminal event (request {request_id})")
        if result.status in ("FAILED", "TIMEOUT", "CANCELLED"):
            raise RagApiError(0, result.code or result.status.lower(), f"answer finished with status {result.status}")
        return result

    def _request(
        self,
        operation: str,
        *,
        json: Any = None,
        params: dict[str, str] | None = None,
        **path_args: str,
    ) -> Any:
        method, path = OPERATIONS[operation]
        response = self._http.request(
            method,
            self._url(path.format(**path_args)),
            headers=self._headers("application/json"),
            json=json,
            params=params,
        )
        return self._decode(response)

    def _stream(
        self,
        operation: str,
        *,
        handle: Any,
        json: Any = None,
        params: dict[str, str] | None = None,
        **path_args: str,
    ) -> None:
        method, path = OPERATIONS[operation]
        with self._http.stream(
            method,
            self._url(path.format(**path_args)),
            headers=self._headers("text/event-stream"),
            json=json,
            params=params,
        ) as response:
            if response.status_code >= 300:
                self._decode_error(response)
            for event in _iter_sse(response.iter_lines()):
                handle(event)

    def _headers(self, accept: str) -> dict[str, str]:
        return {
            "Authorization": f"Bearer {self.api_key}",
            "Accept": accept,
            "Content-Type": "application/json",
        }

    def _url(self, path: str) -> str:
        return f"{self.base_url}{path}"

    @staticmethod
    def _decode(response: httpx.Response) -> Any:
        if response.status_code >= 300:
            RagClient._decode_error(response)
        return response.json()

    @staticmethod
    def _decode_error(response: httpx.Response) -> None:
        code = "http_error"
        message = ""
        try:
            payload = response.json()
            code = str(payload.get("error") or code)
            message = str(payload.get("message") or "")
        except ValueError:
            pass
        raise RagApiError(response.status_code, code, message)


def _iter_sse(lines: Iterator[str]) -> Iterator[dict[str, Any]]:
    data: list[str] = []
    for line in lines:
        if line == "":
            if data:
                yield _decode_event("\n".join(data))
                data = []
            continue
        if line.startswith("data:"):
            data.append(line[5:].lstrip())
    if data:
        yield _decode_event("\n".join(data))


def _decode_event(payload: str) -> dict[str, Any]:
    import json

    return json.loads(payload)