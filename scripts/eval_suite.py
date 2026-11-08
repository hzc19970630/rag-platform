#!/usr/bin/env python3
"""One entry point for the repository evaluation suites.

Runs M3 (authorized RAG) and M7 (graph versus RAG) in sequence, forwarding one
consistent set of credentials and report options. Each suite keeps its own gate
semantics: M3 can fail a release gate, M7 records a keep-or-drop recommendation
without failing the command. This runner returns non-zero when any suite exits
non-zero, so CI can gate on it.

Usage:
    python3 scripts/eval_suite.py [--suite all|m3|m7] [--base-url URL] \
        [--auth dev|api-key] [--api-key KEY] [--actor-key USER_ID=KEY] \
        [--report-dir DIR] [--redact-base-url] [--keep] [--validate-only] \
        [--dry-run] [--public]
    python3 scripts/eval_suite.py --suite m3 --validate-only
    python3 scripts/eval_suite.py --public --dry-run

--public is the reproducible-release mode: it redacts the stack URL and, unless
--report-dir overrides it, writes the reports to tests/eval/public. Do not
publish a report without that redaction.
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path

from evalkit import AUTH_MODES, AuthError, parse_actor_keys

SCRIPTS = Path(__file__).resolve().parent
REPO = SCRIPTS.parent
SUITES = {
    "m3": SCRIPTS / "m3_eval.py",
    "m7": SCRIPTS / "m7_eval.py",
}
PUBLIC_REPORT_DIR = REPO / "tests" / "eval" / "public"


def parse_suites(value: str) -> list[str]:
    requested = [item.strip() for item in value.split(",") if item.strip()]
    if not requested:
        raise ValueError("--suite needs at least one of m3, m7 or all")
    if "all" in requested:
        if len(requested) > 1:
            raise ValueError("--suite all cannot be combined with other suites")
        return list(SUITES)
    unknown = sorted(set(requested) - set(SUITES))
    if unknown:
        raise ValueError(f"unknown suites {unknown}: expected {', '.join(SUITES)} or all")
    duplicates = sorted({item for item in requested if requested.count(item) > 1})
    if duplicates:
        raise ValueError(f"duplicate suites {duplicates}")
    return requested


def build_command(suite: str, arguments: argparse.Namespace) -> list[str]:
    command = [
        sys.executable,
        str(SUITES[suite]),
        "--base-url",
        arguments.base_url,
        "--auth",
        arguments.auth,
    ]
    if arguments.report_dir:
        command += ["--report-dir", arguments.report_dir]
    if arguments.api_key:
        command += ["--api-key", arguments.api_key]
    if suite == "m3":
        for value in arguments.actor_key:
            command += ["--actor-key", value]
        if arguments.embedding_model:
            command += ["--embedding-model", arguments.embedding_model]
        if arguments.embedding_dimension is not None:
            command += ["--embedding-dimension", str(arguments.embedding_dimension)]
        if arguments.recall_samples != 1:
            command += ["--recall-samples", str(arguments.recall_samples)]
    if suite == "m7" and arguments.rag_top_k:
        command += ["--rag-top-k", str(arguments.rag_top_k)]
    if arguments.redact_base_url:
        command.append("--redact-base-url")
    if arguments.keep:
        command.append("--keep")
    if arguments.validate_only:
        command.append("--validate-only")
    return command


def redacted(command: list[str], arguments: argparse.Namespace) -> str:
    secrets = [
        secret for secret in (
            arguments.api_key,
            *(value.partition("=")[2] for value in arguments.actor_key),
        ) if secret
    ]
    display = []
    for item in command:
        for secret in secrets:
            item = item.replace(secret, "***")
        display.append(item)
    return " ".join(display)


def validate_scope(arguments: argparse.Namespace, suites: list[str]) -> None:
    if arguments.actor_key and "m3" not in suites:
        raise ValueError("--actor-key is only used by the m3 suite")
    if (arguments.embedding_model or arguments.embedding_dimension is not None or arguments.recall_samples != 1) and "m3" not in suites:
        raise ValueError("--embedding-model/--embedding-dimension/--recall-samples are only used by the m3 suite")
    if arguments.rag_top_k and "m7" not in suites:
        raise ValueError("--rag-top-k is only used by the m7 suite")
    if arguments.auth == "dev" and (arguments.api_key or arguments.actor_key):
        raise ValueError("--api-key/--actor-key require --auth api-key")
    if arguments.auth == "api-key" and not arguments.api_key and not arguments.validate_only:
        # The suites also read RAG_EVAL_API_KEY; only reject an obviously empty
        # configuration here. The underlying suite owns the final check.
        if not (os.environ.get("RAG_EVAL_API_KEY") or "").strip():
            raise ValueError("--auth api-key requires --api-key or RAG_EVAL_API_KEY")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--suite", default="all", help="comma-separated suites: all, m3, m7 (default: all)")
    parser.add_argument("--base-url", default="http://localhost:8082")
    parser.add_argument("--auth", choices=AUTH_MODES, default="dev")
    parser.add_argument("--api-key", default=None, help="owner API key for --auth api-key")
    parser.add_argument("--actor-key", action="append", default=[], metavar="USER_ID=API_KEY", help="m3 permission-probe API key")
    parser.add_argument("--embedding-model", default=None, help="m3: embedding model name recorded in the report")
    parser.add_argument("--embedding-dimension", type=int, default=None, help="m3: embedding dimension recorded in the report")
    parser.add_argument("--recall-samples", type=int, default=1, help="m3: retrieval draws per case for recallAtK")
    parser.add_argument("--rag-top-k", type=int, default=0, help="m7: diagnostic RAG top_k override")
    parser.add_argument("--report-dir", default=None, help="directory for both reports (default: each suite's tests/eval)")
    parser.add_argument("--redact-base-url", action="store_true", help="write 'redacted' instead of the stack URL")
    parser.add_argument("--keep", action="store_true", help="keep evaluation knowledge bases for inspection")
    parser.add_argument("--validate-only", action="store_true", help="validate both datasets without contacting the stack")
    parser.add_argument("--dry-run", action="store_true", help="print the commands without running them")
    parser.add_argument("--public", action="store_true", help="publish mode: redact the URL and default reports to tests/eval/public")
    arguments = parser.parse_args(argv)

    if arguments.public:
        arguments.redact_base_url = True
        if arguments.report_dir is None:
            arguments.report_dir = str(PUBLIC_REPORT_DIR)

    try:
        suites = parse_suites(arguments.suite)
        parse_actor_keys(arguments.actor_key)
        validate_scope(arguments, suites)
    except (ValueError, AuthError) as error:
        print(f"eval suite: {error}", file=sys.stderr)
        return 2

    if arguments.recall_samples < 1:
        print("eval suite: --recall-samples must be at least 1", file=sys.stderr)
        return 2

    failures: list[tuple[str, int]] = []
    for suite in suites:
        command = build_command(suite, arguments)
        print(f"[{suite}] {redacted(command, arguments)}")
        if arguments.dry_run:
            continue
        completed = subprocess.run(command, check=False)
        if completed.returncode != 0:
            failures.append((suite, completed.returncode))
            print(f"[{suite}] exit {completed.returncode}", file=sys.stderr)

    if arguments.dry_run:
        return 0
    if failures:
        print(
            "eval suite: failed " + ", ".join(f"{suite} (exit {code})" for suite, code in failures),
            file=sys.stderr,
        )
        return 1
    print(f"eval suite: passed {', '.join(suites)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
