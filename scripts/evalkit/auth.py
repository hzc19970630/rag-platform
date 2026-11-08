"""Credential modes shared by the evaluation suites.

Two modes are supported:

* ``dev`` sends development identity headers. It only works when the stack
  enables dev auth outside production, and it lets the harness act as any seeded
  identity, which is what the permission probes need.
* ``api-key`` sends a bearer API key. It is the mode that works against a
  deployment with dev auth disabled. One key cannot impersonate another user,
  so every probe identity needs its own key; a missing or mismatched key makes
  those probes unavailable instead of letting a deny that ran as the wrong
  principal look like a pass.

Both modes verify the identity through ``/api/v1/me`` before using it. A probe
denied because its credential was unauthenticated, or because the credential
belongs to somebody else, proves nothing about authorization.
"""

from __future__ import annotations

import argparse
import os
from dataclasses import dataclass

from .client import Client, DEV_TENANT_ID, dev_headers

AUTH_MODES = ("dev", "api-key")


class AuthError(RuntimeError):
    pass


@dataclass(frozen=True)
class Principal:
    subject_id: str
    tenant_id: str
    credential_type: str


def parse_actor_keys(values: list[str]) -> dict[str, str]:
    """Parses repeated ``USER_ID=API_KEY`` options without echoing the key."""
    keys: dict[str, str] = {}
    for value in values:
        user_id, separator, key = value.partition("=")
        user_id = user_id.strip()
        key = key.strip()
        if not separator or not user_id or not key:
            raise AuthError(f"invalid --actor-key {redact(value)!r}: expected USER_ID=API_KEY")
        if user_id in keys:
            raise AuthError(f"duplicate --actor-key for user {user_id!r}")
        keys[user_id] = key
    return keys


def redact(value: str) -> str:
    """Keeps an option value identifiable in an error without leaking the secret."""
    user_id, separator, _ = value.partition("=")
    return f"{user_id}=***" if separator and user_id else "***"


def add_auth_arguments(parser: argparse.ArgumentParser, *, actor_keys: bool = True) -> None:
    parser.add_argument(
        "--auth",
        choices=AUTH_MODES,
        default="dev",
        help="how the harness authenticates: development headers or a bearer API key (default: dev)",
    )
    parser.add_argument(
        "--api-key",
        default=None,
        help="owner API key for --auth api-key (default: RAG_EVAL_API_KEY)",
    )
    if actor_keys:
        parser.add_argument(
            "--actor-key",
            action="append",
            default=[],
            metavar="USER_ID=API_KEY",
            help="API key for a permission-probe identity; repeat once per actor",
        )


class Credentials:
    """Builds authenticated clients and proves each identity is who it claims.

    The owner client drives the run. Probe actors only exist in suites with
    permission negatives; a missing actor credential is deliberately not fatal
    here. The suite records the affected probes as unavailable, which fails the
    permission gate, so a partially credentialed run can still report the rest
    of the metrics without silently skipping the security check.
    """

    def __init__(
        self,
        base_url: str,
        mode: str = "dev",
        api_key: str | None = None,
        actor_keys: dict[str, str] | None = None,
    ) -> None:
        if mode not in AUTH_MODES:
            raise AuthError(f"unknown auth mode {mode!r}: expected one of {', '.join(AUTH_MODES)}")
        if mode == "dev" and api_key:
            raise AuthError("--api-key requires --auth api-key")
        if mode == "dev" and actor_keys:
            raise AuthError("--actor-key requires --auth api-key")
        api_key = (api_key or "").strip()
        if mode == "api-key":
            api_key = api_key or (os.environ.get("RAG_EVAL_API_KEY") or "").strip()
            if not api_key:
                raise AuthError("--auth api-key requires --api-key or RAG_EVAL_API_KEY")
            if not api_key.startswith("rk_"):
                raise AuthError("--api-key must be an API key starting with 'rk_', not a session token")
        self.base_url = base_url
        self.mode = mode
        self._api_key = api_key
        self._actor_keys = dict(actor_keys or {})
        self._owner: Client | None = None
        self._owner_principal: Principal | None = None
        self._actors: dict[tuple[str, str], Client | None] = {}
        self._actor_errors: dict[tuple[str, str], str] = {}

    def owner_client(self) -> Client:
        if self._owner is None:
            headers = dev_headers() if self.mode == "dev" else {"Authorization": f"Bearer {self._api_key}"}
            self._owner = Client(self.base_url, headers)
        return self._owner

    def verify_owner(self) -> Principal:
        principal = self._identify(self.owner_client(), "owner")
        if self.mode == "dev":
            if principal.credential_type != "dev":
                raise AuthError(
                    f"owner is authenticated as {principal.credential_type!r}, not a development identity; "
                    "pass --auth api-key for a deployment with dev auth disabled"
                )
        elif principal.credential_type != "api_key":
            raise AuthError(f"owner is authenticated as {principal.credential_type!r}, not an API key")
        self._owner_principal = principal
        return principal

    def resolve_actor(self, actor: dict[str, str]) -> dict[str, str]:
        """Resolves a dataset actor to the identity the probes must run under.

        The dataset marks an in-tenant actor with the development tenant id. In
        dev mode that is the owner's tenant. In api-key mode the evaluation
        stack's owner tenant can be different, and a key cannot cross tenants,
        so the probe must run in the tenant that owns the evaluation knowledge
        base. Cross-tenant actors keep their declared tenant.
        """
        resolved = {"userId": actor["userId"], "tenantId": actor["tenantId"]}
        if self.mode == "api-key" and resolved["tenantId"] == DEV_TENANT_ID:
            if self._owner_principal is None:
                raise AuthError("resolve_actor requires verify_owner() before mapping in-tenant actors")
            resolved["tenantId"] = self._owner_principal.tenant_id
        return resolved

    def actor_client(self, actor: dict[str, str]) -> Client | None:
        key = (actor["userId"], actor["tenantId"])
        if key in self._actors:
            return self._actors[key]
        if self._owner_principal is not None and is_owner_actor(actor, self._owner_principal):
            self._actors[key] = None
            self._actor_errors[key] = "actor is the knowledge base owner"
            return None
        if self.mode == "dev":
            client = Client(self.base_url, dev_headers(actor["userId"], actor["tenantId"]))
        else:
            api_key = self._actor_keys.get(actor["userId"])
            if not api_key:
                self._actors[key] = None
                self._actor_errors[key] = "no --actor-key supplied"
                return None
            client = Client(self.base_url, {"Authorization": f"Bearer {api_key}"})
        try:
            principal = self._identify(client, f"probe actor {actor['userId']}@{actor['tenantId']}")
        except AuthError as error:
            self._actors[key] = None
            self._actor_errors[key] = str(error)
            return None
        if principal.subject_id != actor["userId"] or principal.tenant_id != actor["tenantId"]:
            self._actors[key] = None
            self._actor_errors[key] = (
                f"credential identifies as {principal.subject_id}@{principal.tenant_id}"
            )
            return None
        if self.mode == "api-key" and principal.credential_type != "api_key":
            self._actors[key] = None
            self._actor_errors[key] = f"credential type is {principal.credential_type!r}, not api_key"
            return None
        self._actors[key] = client
        return client

    def actor_summary(self) -> dict[str, str]:
        """Report-safe state of every probe identity used so far; never the key."""
        summary: dict[str, str] = {}
        for (user_id, tenant_id), client in self._actors.items():
            label = f"{user_id}@{tenant_id}"
            summary[label] = "verified" if client is not None else self._actor_errors.get(
                (user_id, tenant_id), "unavailable"
            )
        return summary

    def actor_error(self, actor: dict[str, str]) -> str | None:
        return self._actor_errors.get((actor["userId"], actor["tenantId"]))

    def actor_errors(self) -> list[str]:
        return [
            f"{user_id}@{tenant_id}: {reason}"
            for (user_id, tenant_id), reason in self._actor_errors.items()
        ]

    def _identify(self, client: Client, label: str) -> Principal:
        status, payload = client.json("GET", "/api/v1/me")
        if status != 200:
            raise AuthError(f"{label} credential was rejected by /api/v1/me with status {status}")
        principal = payload.get("principal") or {}
        subject_id = principal.get("subjectId")
        tenant_id = principal.get("tenantId")
        credential_type = principal.get("credentialType")
        if not all(isinstance(value, str) and value for value in (subject_id, tenant_id, credential_type)):
            raise AuthError(f"{label} credential returned an incomplete /api/v1/me principal")
        return Principal(subject_id=subject_id, tenant_id=tenant_id, credential_type=credential_type)


def is_owner_actor(actor: dict[str, str], owner: Principal) -> bool:
    """True when a probe actor is the knowledge base owner, which can never be
    a valid negative probe: it would legitimately be allowed."""
    return actor.get("userId") == owner.subject_id and actor.get("tenantId") == owner.tenant_id
