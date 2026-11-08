"""Permission negative probes.

Each probe runs as a caller that must not be authorized. A 2xx means the caller
reached tenant data or performed a write; a probe whose target id is missing is
recorded as unavailable rather than as a pass, so an unrun probe can never look
like a clean result. The caller supplies the client factory, which keeps the
probe layer independent from how identities are authenticated.
"""

from __future__ import annotations

import uuid
from collections.abc import Callable

from .client import Client, multipart_body

PERMISSION_PROBES = (
    "search",
    "session",
    "messages",
    "document",
    "documentContent",
    "chunk",
    "events",
    "upload",
    "members",
    "memberRemove",
)

# Target of the member write probes. Any existing user works: the point is that
# the probe actor is denied, not who it tries to modify.
PROBE_MEMBER_TARGET = "dev-user"


def run_permission_probes(
    actor: dict[str, str],
    knowledge_base_id: str,
    conversation_id: str,
    document_id: str,
    chunk_id: str | None,
    request_id: str | None,
    probes: list[str],
    *,
    client_for_actor: Callable[[dict[str, str]], Client | None],
    actor_reason: str | None = None,
) -> list[dict]:
    # A caller that cannot prove the actor's identity returns None. The probe
    # is then unavailable, never denied: an unauthenticated request is rejected
    # before authorization runs and says nothing about tenant isolation.
    client = client_for_actor(actor)
    results: list[dict] = []
    for probe in probes:
        if probe in {"chunk", "events"} and (
            chunk_id is None if probe == "chunk" else request_id is None
        ):
            results.append({
                "probe": probe,
                "status": None,
                "denied": False,
                "unavailable": True,
                "reason": "the target id was never produced",
            })
            continue
        if client is None:
            results.append({
                "probe": probe,
                "status": None,
                "denied": False,
                "unavailable": True,
                "reason": actor_reason or "the actor credential is missing or does not match the actor",
            })
            continue
        if probe == "search":
            code, _ = client.json(
                "POST", "/api/v1/search",
                {"query": "hybrid recall", "knowledgeBaseId": knowledge_base_id},
            )
        elif probe == "session":
            code, _ = client.json("POST", "/api/v1/sessions", {"knowledge_base_id": knowledge_base_id})
        elif probe == "messages":
            code, _ = client.json(
                "POST", f"/api/v1/sessions/{conversation_id}/messages",
                {"query": "hybrid recall", "requestId": "probe-" + uuid.uuid4().hex},
            )
        elif probe == "document":
            code, _ = client.json("GET", f"/api/v1/documents/{document_id}")
        elif probe == "documentContent":
            code, _ = client.json("GET", f"/api/v1/documents/{document_id}/content")
        elif probe == "chunk":
            code, _ = client.json("GET", f"/api/v1/chunks/{chunk_id}")
        elif probe == "events":
            code, _ = client.json("GET", f"/api/v1/requests/{request_id}/events")
        elif probe == "upload":
            raw, content_type = multipart_body("probe.md", "# probe\n\nunauthorized upload attempt\n")
            code, _ = client.request(
                "POST",
                f"/api/v1/knowledge-bases/{knowledge_base_id}/documents",
                raw=raw,
                content_type=content_type,
            )
        elif probe == "members":
            code, _ = client.json(
                "POST", f"/api/v1/knowledge-bases/{knowledge_base_id}/members",
                {"user_id": PROBE_MEMBER_TARGET, "role": "viewer"},
            )
        elif probe == "memberRemove":
            code, _ = client.request(
                "DELETE",
                f"/api/v1/knowledge-bases/{knowledge_base_id}/members/{PROBE_MEMBER_TARGET}",
            )
        else:
            raise RuntimeError(f"unknown permission probe {probe!r}")
        results.append({"probe": probe, "status": code, "denied": code >= 400})
    return results
