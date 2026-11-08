"""Polling helpers for stack lifecycle state.

``wait_for`` and ``wait_document_ready`` keep the fail-fast semantics the graph
suite relied on: an ingestion that reaches FAILED raises instead of timing out
for five minutes. ``wait_pipeline_status`` keeps the authorized-RAG suite's
reporting semantics, where the caller prints the terminal status and exits 2.
"""

from __future__ import annotations

import time

from .client import Client


def wait_for(description: str, timeout_seconds: int, probe):
    deadline = time.monotonic() + timeout_seconds
    last = None
    while time.monotonic() < deadline:
        result = probe()
        if result is not None:
            return result
        last = time.monotonic()
        time.sleep(1)
    raise RuntimeError(
        f"timed out after {timeout_seconds}s waiting for {description} (last probe at {last})"
    )


def fetch_document(client: Client, document_id: str) -> dict:
    status, payload = client.json("GET", f"/api/v1/documents/{document_id}")
    if status != 200:
        raise RuntimeError(f"document {document_id} detail failed: {status} {payload}")
    return payload


def wait_pipeline_status(client: Client, document_id: str, timeout_seconds: int = 120) -> str:
    deadline = time.monotonic() + timeout_seconds
    status = "UNKNOWN"
    while time.monotonic() < deadline:
        _, payload = client.json("GET", f"/api/v1/documents/{document_id}")
        status = payload.get("pipeline", {}).get("status", "UNKNOWN")
        if status in {"EVIDENCE_READY", "FAILED", "CANCELLED"}:
            return status
        time.sleep(1)
    return status


def wait_document_ready(client: Client, document_id: str, timeout_seconds: int = 300) -> dict:
    def probe():
        payload = fetch_document(client, document_id)
        status = (payload.get("pipeline") or {}).get("status")
        if status == "EVIDENCE_READY":
            return payload
        if status == "FAILED":
            raise RuntimeError(
                f"document {document_id} failed during ingestion: {payload.get('pipeline')}"
            )
        return None

    return wait_for(f"document {document_id} EVIDENCE_READY", timeout_seconds, probe)
