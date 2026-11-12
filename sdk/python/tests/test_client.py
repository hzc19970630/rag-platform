import json

import httpx
import pytest

from rag_platform import RagApiError, RagClient

SSE = (
    'event: answer\nid: req-1:1\n'
    'data: {"request_id":"req-1","sequence":1,"type":"answer",'
    '"data":{"content":"answer text","citations":[{"chunkId":"c1"}],"status":"COMPLETED"}}\n\n'
    'event: complete\nid: req-1:2\n'
    'data: {"request_id":"req-1","sequence":2,"type":"complete",'
    '"data":{"status":"COMPLETED"}}\n\n'
).encode()


def handler(request: httpx.Request) -> httpx.Response:
    assert request.headers["authorization"] == "Bearer test-key"
    path = request.url.path
    if path.endswith("/knowledge-bases"):
        return httpx.Response(200, json={"items": [{"id": "kb1"}]})
    if path.endswith("/search"):
        body = json.loads(request.content.decode())
        if body.get("knowledgeBaseId") == "kb-forbidden":
            return httpx.Response(403, json={"error": "forbidden", "message": "outside scope"})
        return httpx.Response(200, json={"items": [{"chunkId": "c1"}]})
    if path.endswith("/sessions"):
        return httpx.Response(201, json={"id": "s1"})
    if path.endswith("/sessions/s1/messages"):
        return httpx.Response(200, content=SSE, headers={"content-type": "text/event-stream"})
    if path.endswith("/chunks/c1"):
        return httpx.Response(200, json={"chunkId": "c1", "content": "hello", "sourcePath": "doc.md"})
    if path.endswith("/documents/d1/status"):
        return httpx.Response(200, json={"contentReady": True})
    return httpx.Response(404, json={"error": "not_found"})


def client() -> RagClient:
    return RagClient("http://test/api/v1", "test-key", transport=httpx.MockTransport(handler))


def test_search_and_citation() -> None:
    with client() as api:
        evidence = api.search("hello", "kb1")
        assert evidence["items"][0]["chunkId"] == "c1"
        chunk = api.get_citation("c1")
        assert chunk["sourcePath"] == "doc.md"
        assert api.get_document_status("d1")["contentReady"] is True


def test_ask_reuses_session_and_streams_citations() -> None:
    with client() as api:
        result = api.ask("hello", session_id="s1", request_id="req-1")
        assert result.content == "answer text"
        assert result.status == "COMPLETED"
        assert result.citations[0]["chunkId"] == "c1"


def test_ask_creates_session_when_absent() -> None:
    with client() as api:
        result = api.ask("hello", knowledge_base_id="kb1", request_id="req-1")
        assert result.session_id == "s1"


def test_forbidden_knowledge_base() -> None:
    with client() as api:
        with pytest.raises(RagApiError) as error:
            api.search("hello", "kb-forbidden")
        assert error.value.status == 403
        assert error.value.code == "forbidden"