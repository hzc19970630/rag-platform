#!/usr/bin/env python3
"""Tests for the one-command evaluation runner.

The runner only assembles and invokes commands. These tests pin suite parsing,
option/suite compatibility, and the rule that printed commands never expose an
API key. Nothing here starts a stack.
"""

from __future__ import annotations

import argparse
import subprocess
import sys
import unittest
from contextlib import redirect_stdout
from io import StringIO
from pathlib import Path
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))

import eval_suite  # noqa: E402


def namespace(**overrides) -> argparse.Namespace:
    values = {
        "base_url": "http://localhost:8082",
        "auth": "dev",
        "api_key": None,
        "actor_key": [],
        "embedding_model": None,
        "embedding_dimension": None,
        "recall_samples": 1,
        "rag_top_k": 0,
        "report_dir": None,
        "redact_base_url": False,
        "keep": False,
        "validate_only": False,
    }
    values.update(overrides)
    return argparse.Namespace(**values)


class SuiteParsingTests(unittest.TestCase):
    def test_all_expands_to_both_suites_in_order(self) -> None:
        self.assertEqual(eval_suite.parse_suites("all"), ["m3", "m7"])

    def test_explicit_suites_keep_their_order(self) -> None:
        self.assertEqual(eval_suite.parse_suites("m7, m3"), ["m7", "m3"])

    def test_unknown_suite_is_rejected(self) -> None:
        with self.assertRaises(ValueError):
            eval_suite.parse_suites("m3,m8")

    def test_duplicate_suite_is_rejected(self) -> None:
        with self.assertRaises(ValueError):
            eval_suite.parse_suites("m3,m3")

    def test_all_cannot_be_combined(self) -> None:
        with self.assertRaises(ValueError):
            eval_suite.parse_suites("all,m3")


class CommandTests(unittest.TestCase):
    def test_m3_command_carries_probe_and_embedding_options(self) -> None:
        arguments = namespace(
            auth="api-key",
            api_key="rk_owner_secret",
            actor_key=["viewer-a=rk_viewer_secret"],
            embedding_model="text-embedding-3-large",
            embedding_dimension=3072,
            recall_samples=3,
            report_dir="out",
        )
        command = eval_suite.build_command("m3", arguments)
        self.assertIn("scripts/m3_eval.py", command[1].replace("\\", "/"))
        for option in ("--api-key", "--actor-key", "--embedding-model", "--embedding-dimension", "--recall-samples"):
            self.assertIn(option, command)
        self.assertEqual(command[command.index("--recall-samples") + 1], "3")

    def test_m7_command_does_not_carry_m3_only_options(self) -> None:
        arguments = namespace(
            actor_key=["viewer-a=rk_viewer_secret"],
            embedding_model="text-embedding-3-large",
            recall_samples=3,
            rag_top_k=5,
        )
        command = eval_suite.build_command("m7", arguments)
        self.assertIn("--rag-top-k", command)
        for option in ("--actor-key", "--embedding-model", "--recall-samples"):
            self.assertNotIn(option, command)

    def test_omitted_report_dir_is_not_forwarded_as_none(self) -> None:
        command = eval_suite.build_command("m7", namespace())
        self.assertNotIn("--report-dir", command)

    def test_printed_command_redacts_every_key(self) -> None:
        arguments = namespace(
            auth="api-key",
            api_key="rk_owner_secret",
            actor_key=["viewer-a=rk_viewer_secret"],
        )
        command = eval_suite.build_command("m3", arguments)
        printed = eval_suite.redacted(command, arguments)
        self.assertNotIn("rk_owner_secret", printed)
        self.assertNotIn("rk_viewer_secret", printed)
        self.assertIn("***", printed)


class ScopeTests(unittest.TestCase):
    def test_m3_only_options_require_the_m3_suite(self) -> None:
        for overrides in (
            {"actor_key": ["viewer-a=rk_viewer_secret"]},
            {"embedding_model": "model"},
            {"embedding_dimension": 1024},
            {"recall_samples": 2},
        ):
            with self.subTest(overrides=overrides):
                with self.assertRaises(ValueError):
                    eval_suite.validate_scope(namespace(**overrides), ["m7"])

    def test_rag_top_k_requires_the_m7_suite(self) -> None:
        with self.assertRaises(ValueError):
            eval_suite.validate_scope(namespace(rag_top_k=5), ["m3"])

    def test_dev_mode_rejects_api_key_options(self) -> None:
        with self.assertRaises(ValueError):
            eval_suite.validate_scope(namespace(api_key="rk_secret"), ["m3"])


class MainTests(unittest.TestCase):
    def test_public_dry_run_redacts_and_points_at_the_public_directory(self) -> None:
        output = StringIO()
        with redirect_stdout(output):
            code = eval_suite.main([
                "--public", "--dry-run",
                "--auth", "api-key", "--api-key", "rk_owner_secret",
                "--actor-key", "viewer-a=rk_viewer_secret",
            ])
        self.assertEqual(code, 0)
        printed = output.getvalue()
        self.assertIn("--redact-base-url", printed)
        self.assertIn("tests/eval/public", printed.replace("\\", "/"))
        self.assertNotIn("rk_owner_secret", printed)
        self.assertNotIn("rk_viewer_secret", printed)

    def test_dry_run_never_invokes_a_subprocess(self) -> None:
        with mock.patch("eval_suite.subprocess.run", side_effect=AssertionError("ran")) as run:
            code = eval_suite.main(["--dry-run", "--validate-only"])
        self.assertEqual(code, 0)
        run.assert_not_called()

    def test_malformed_actor_key_is_rejected_before_running(self) -> None:
        with mock.patch("eval_suite.subprocess.run", side_effect=AssertionError("ran")) as run:
            code = eval_suite.main([
                "--dry-run", "--auth", "api-key", "--api-key", "rk_owner_secret",
                "--actor-key", "rk_orphan_secret",
            ])
        self.assertEqual(code, 2)
        run.assert_not_called()

    def test_suite_failure_is_reported_with_a_non_zero_exit(self) -> None:
        completed = subprocess.CompletedProcess(args=[], returncode=1)
        with mock.patch("eval_suite.subprocess.run", return_value=completed):
            code = eval_suite.main(["--suite", "m3"])
        self.assertEqual(code, 1)


if __name__ == "__main__":
    unittest.main()
