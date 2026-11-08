#!/usr/bin/env python3
"""Credential-mode tests for the shared harness.

These tests never contact a stack. They pin the two properties the permission
probes rely on: the harness proves each identity through /api/v1/me, and a
missing or mismatched actor credential is unavailable rather than denied.
"""

from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))

from evalkit import AuthError, Credentials, parse_actor_keys, run_permission_probes  # noqa: E402


def me_response(
    subject_id: str,
    tenant_id: str,
    credential_type: str,
    *,
    development_identity: bool = False,
) -> tuple[int, dict]:
    return 200, {
        "principal": {
            "subjectId": subject_id,
            "tenantId": tenant_id,
            "credentialType": credential_type,
        },
        "developmentIdentity": development_identity,
    }


class FakeClient:
    """Returns a canned /api/v1/me response keyed by the sent credential."""

    instances: list["FakeClient"] = []
    responses: dict[str, tuple[int, dict]] = {}

    def __init__(self, base_url: str, headers: dict[str, str] | None = None, timeout: int = 120) -> None:
        self.base_url = base_url
        self.headers = dict(headers or {})
        self.calls: list[tuple[str, str]] = []
        type(self).instances.append(self)

    def identity(self) -> str:
        authorization = self.headers.get("Authorization", "")
        if authorization:
            return authorization.removeprefix("Bearer ")
        return f"{self.headers.get('X-Dev-User-ID')}@{self.headers.get('X-Dev-Tenant-ID')}"

    def json(self, method: str, path: str, body: dict | None = None) -> tuple[int, dict]:
        self.calls.append((method, path))
        if path != "/api/v1/me":
            return 404, {}
        return type(self).responses.get(self.identity(), (401, {}))


class AuthTests(unittest.TestCase):
    def setUp(self) -> None:
        FakeClient.instances = []
        FakeClient.responses = {}
        patcher = mock.patch("evalkit.auth.Client", FakeClient)
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_parse_actor_keys_accepts_pairs(self) -> None:
        keys = parse_actor_keys(["viewer-a=rk_viewer_secret", "editor-a=rk_editor_secret"])
        self.assertEqual(keys["viewer-a"], "rk_viewer_secret")
        self.assertEqual(keys["editor-a"], "rk_editor_secret")

    def test_parse_actor_keys_rejects_malformed_without_echoing_key(self) -> None:
        for value in ("rk_orphan_secret", "=rk_secret", "viewer-a="):
            with self.subTest(value=value):
                with self.assertRaises(AuthError) as raised:
                    parse_actor_keys([value])
                self.assertNotIn("rk_orphan_secret", str(raised.exception))

    def test_parse_actor_keys_rejects_duplicate_user(self) -> None:
        with self.assertRaises(AuthError):
            parse_actor_keys(["viewer-a=rk_one_secret", "viewer-a=rk_two_secret"])

    def test_api_key_mode_requires_a_key(self) -> None:
        with mock.patch.dict("os.environ", {}, clear=True):
            with self.assertRaises(AuthError):
                Credentials("http://api", "api-key")

    def test_api_key_mode_rejects_a_session_token(self) -> None:
        with self.assertRaises(AuthError):
            Credentials("http://api", "api-key", "session-token")

    def test_api_key_mode_reads_the_environment_fallback(self) -> None:
        with mock.patch.dict("os.environ", {"RAG_EVAL_API_KEY": "rk_env_secret"}):
            credentials = Credentials("http://api", "api-key")
        self.assertEqual(credentials.owner_client().headers["Authorization"], "Bearer rk_env_secret")

    def test_dev_mode_rejects_api_key_options(self) -> None:
        with self.assertRaises(AuthError):
            Credentials("http://api", "dev", "rk_secret")
        with self.assertRaises(AuthError):
            Credentials("http://api", "dev", None, {"viewer-a": "rk_secret"})

    def test_verify_owner_accepts_a_development_identity(self) -> None:
        FakeClient.responses["dev-user@dev-tenant"] = me_response(
            "dev-user", "dev-tenant", "dev", development_identity=True
        )
        credentials = Credentials("http://api", "dev")
        principal = credentials.verify_owner()
        self.assertEqual((principal.subject_id, principal.tenant_id), ("dev-user", "dev-tenant"))
        self.assertEqual(credentials.owner_client().headers["X-Dev-User-ID"], "dev-user")

    def test_verify_owner_rejects_a_non_dev_credential_in_dev_mode(self) -> None:
        FakeClient.responses["dev-user@dev-tenant"] = me_response("dev-user", "dev-tenant", "session")
        with self.assertRaises(AuthError) as raised:
            Credentials("http://api", "dev").verify_owner()
        self.assertIn("development identity", str(raised.exception))

    def test_verify_owner_rejects_a_non_api_key_credential(self) -> None:
        FakeClient.responses["rk_owner_secret"] = me_response("admin", "prod-tenant", "session")
        with self.assertRaises(AuthError) as raised:
            Credentials("http://api", "api-key", "rk_owner_secret").verify_owner()
        self.assertIn("API key", str(raised.exception))

    def test_actor_without_a_key_is_unavailable_not_denied(self) -> None:
        credentials = Credentials("http://api", "api-key", "rk_owner_secret")
        FakeClient.responses["rk_owner_secret"] = me_response("admin", "prod-tenant", "api_key")
        credentials.verify_owner()
        actor = credentials.resolve_actor({"userId": "viewer-a", "tenantId": "dev-tenant"})
        self.assertIsNone(credentials.actor_client(actor))
        self.assertEqual(
            credentials.actor_summary(),
            {"viewer-a@prod-tenant": "no --actor-key supplied"},
        )
        self.assertEqual(FakeClient.instances[0].calls, [("GET", "/api/v1/me")])

    def test_actor_with_a_mismatched_credential_is_unavailable(self) -> None:
        FakeClient.responses["rk_owner_secret"] = me_response("admin", "prod-tenant", "api_key")
        FakeClient.responses["rk_wrong_secret"] = me_response("someone-else", "prod-tenant", "api_key")
        credentials = Credentials(
            "http://api", "api-key", "rk_owner_secret", {"viewer-a": "rk_wrong_secret"}
        )
        credentials.verify_owner()
        actor = credentials.resolve_actor({"userId": "viewer-a", "tenantId": "dev-tenant"})
        self.assertIsNone(credentials.actor_client(actor))
        self.assertIn("someone-else@prod-tenant", credentials.actor_summary()["viewer-a@prod-tenant"])

    def test_actor_with_an_unauthenticated_credential_is_unavailable(self) -> None:
        credentials = Credentials("http://api", "dev")
        FakeClient.responses["viewer-a@dev-tenant"] = (401, {})
        self.assertIsNone(credentials.actor_client({"userId": "viewer-a", "tenantId": "dev-tenant"}))
        self.assertIn("401", credentials.actor_summary()["viewer-a@dev-tenant"])

    def test_in_tenant_actor_maps_to_the_owner_tenant_in_api_key_mode(self) -> None:
        FakeClient.responses["rk_owner_secret"] = me_response("admin", "prod-tenant", "api_key")
        FakeClient.responses["rk_viewer_secret"] = me_response("viewer-a", "prod-tenant", "api_key")
        credentials = Credentials(
            "http://api", "api-key", "rk_owner_secret", {"viewer-a": "rk_viewer_secret"}
        )
        credentials.verify_owner()
        actor = credentials.resolve_actor({"userId": "viewer-a", "tenantId": "dev-tenant"})
        self.assertEqual(actor, {"userId": "viewer-a", "tenantId": "prod-tenant"})
        self.assertIsNotNone(credentials.actor_client(actor))
        self.assertEqual(credentials.actor_summary(), {"viewer-a@prod-tenant": "verified"})

    def test_resolve_actor_requires_the_owner_tenant_in_api_key_mode(self) -> None:
        credentials = Credentials("http://api", "api-key", "rk_owner_secret")
        with self.assertRaises(AuthError):
            credentials.resolve_actor({"userId": "viewer-a", "tenantId": "dev-tenant"})

    def test_owner_identity_is_unavailable_as_a_probe_actor(self) -> None:
        FakeClient.responses["rk_owner_secret"] = me_response("admin", "prod-tenant", "api_key")
        credentials = Credentials("http://api", "api-key", "rk_owner_secret")
        credentials.verify_owner()
        self.assertIsNone(credentials.actor_client({"userId": "admin", "tenantId": "prod-tenant"}))
        self.assertIn(
            "owner",
            credentials.actor_summary()["admin@prod-tenant"],
        )

    def test_actor_summary_never_contains_a_key(self) -> None:
        FakeClient.responses["rk_owner_secret"] = me_response("admin", "prod-tenant", "api_key")
        FakeClient.responses["rk_viewer_secret"] = me_response("viewer-a", "prod-tenant", "api_key")
        credentials = Credentials(
            "http://api", "api-key", "rk_owner_secret", {"viewer-a": "rk_viewer_secret"}
        )
        credentials.verify_owner()
        credentials.actor_client(credentials.resolve_actor({"userId": "viewer-a", "tenantId": "dev-tenant"}))
        serialized = json.dumps(credentials.actor_summary())
        self.assertNotIn("rk_", serialized)


class ProbeAvailabilityTests(unittest.TestCase):
    def test_missing_actor_credential_marks_probes_unavailable_not_denied(self) -> None:
        probes = run_permission_probes(
            {"userId": "viewer-a", "tenantId": "dev-tenant"},
            "kb-1",
            "conv-1",
            "doc-1",
            None,
            None,
            ["search", "upload"],
            client_for_actor=lambda actor: None,
        )
        self.assertEqual([probe["probe"] for probe in probes], ["search", "upload"])
        self.assertTrue(all(probe["unavailable"] for probe in probes))
        self.assertFalse(any(probe["denied"] for probe in probes))
        self.assertTrue(all(probe["status"] is None for probe in probes))

    def test_probe_carries_the_specific_credential_reason(self) -> None:
        probes = run_permission_probes(
            {"userId": "viewer-a", "tenantId": "dev-tenant"},
            "kb-1",
            "conv-1",
            "doc-1",
            None,
            None,
            ["search"],
            client_for_actor=lambda actor: None,
            actor_reason="no --actor-key supplied",
        )
        self.assertEqual(probes[0]["reason"], "no --actor-key supplied")

    def test_missing_target_id_marks_the_probe_unavailable(self) -> None:
        class RefusingClient:
            def json(self, *args, **kwargs):
                raise AssertionError("a probe without a target must not call the API")

            def request(self, *args, **kwargs):
                raise AssertionError("a probe without a target must not call the API")

        probes = run_permission_probes(
            {"userId": "viewer-a", "tenantId": "dev-tenant"},
            "kb-1",
            "conv-1",
            "doc-1",
            None,
            None,
            ["chunk", "events"],
            client_for_actor=lambda actor: RefusingClient(),
        )
        self.assertTrue(all(probe["unavailable"] for probe in probes))
        self.assertIn("target id", probes[0]["reason"])


if __name__ == "__main__":
    unittest.main()
