#!/usr/bin/env python3
"""Client-level tests for the shared harness.

The HTTP calls are mocked: these tests verify how authentication headers,
multipart uploads and SSE events are built, not a running stack.
"""

from __future__ import annotations

import json
import sys
import unittest
import urllib.error
from pathlib import Path
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))

from evalkit import Client, dev_headers, parse_sse  # noqa: E402


class FakeResponse:
    def __init__(self, status: int, payload: bytes) -> None:
        self.status = status
        self.payload = payload

    def read(self) -> bytes:
        return self.payload

    def __enter__(self) -> "FakeResponse":
        return self

    def __exit__(self, *args) -> bool:
        return False


def request_headers(request) -> dict[str, str]:
    return {key.lower(): value for key, value in request.header_items()}


class ClientTests(unittest.TestCase):
    @mock.patch("evalkit.client.urllib.request.urlopen")
    def test_dev_headers_are_sent(self, urlopen) -> None:
        urlopen.return_value = FakeResponse(200, b"{}")
        client = Client("http://api", dev_headers("viewer-a", "tenant-a"))
        client.json("GET", "/healthz")
        headers = request_headers(urlopen.call_args.args[0])
        self.assertEqual(headers["x-dev-user-id"], "viewer-a")
        self.assertEqual(headers["x-dev-tenant-id"], "tenant-a")

    @mock.patch("evalkit.client.urllib.request.urlopen")
    def test_login_switches_to_bearer_token(self, urlopen) -> None:
        urlopen.side_effect = [
            FakeResponse(200, json.dumps({"token": "token-1"}).encode()),
            FakeResponse(200, b"{}"),
        ]
        client = Client("http://api")
        client.login()
        client.json("GET", "/api/v1/knowledge-bases")
        headers = request_headers(urlopen.call_args.args[0])
        self.assertEqual(headers["authorization"], "Bearer token-1")

    @mock.patch("evalkit.client.urllib.request.urlopen")
    def test_upload_posts_multipart(self, urlopen) -> None:
        urlopen.return_value = FakeResponse(
            202, json.dumps({"document": {"id": "doc-1"}, "version": {"id": "ver-1"}}).encode()
        )
        client = Client("http://api")
        payload = client.upload("kb-1", "handbook.md", "hello")
        request = urlopen.call_args.args[0]
        headers = request_headers(request)
        self.assertEqual(request.get_method(), "POST")
        self.assertTrue(headers["content-type"].startswith("multipart/form-data; boundary="))
        self.assertEqual(payload["document"]["id"], "doc-1")

    @mock.patch("evalkit.client.urllib.request.urlopen")
    def test_connection_failure_raises_instead_of_returning_a_status(self, urlopen) -> None:
        urlopen.side_effect = urllib.error.URLError("connection refused")
        client = Client("http://api")
        with self.assertRaises(RuntimeError) as raised:
            client.json("GET", "/healthz")
        self.assertIn("no response", str(raised.exception))

    def test_parse_sse_keeps_event_type_and_payload(self) -> None:
        raw = (
            "event: status\n"
            'data: {"type":"status","state":"GENERATING","model":"local"}\n\n'
            "event: answer\n"
            'data: {"type":"answer","status":"ANSWERED"}\n\n'
        )
        events = parse_sse(raw)
        self.assertEqual(events[0]["model"], "local")
        self.assertEqual(events[1]["status"], "ANSWERED")


if __name__ == "__main__":
    unittest.main()
