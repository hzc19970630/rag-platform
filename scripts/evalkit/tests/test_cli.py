#!/usr/bin/env python3
"""CLI-boundary tests for the M3 suite.

Invalid numeric options must fail before the dataset is loaded or the stack is
contacted; silently clamping them would hide an operator mistake in a report.
"""

from __future__ import annotations

import io
import sys
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))

import m3_eval  # noqa: E402


class M3CliTests(unittest.TestCase):
    def run_with(self, *args: str) -> tuple[int, str]:
        stderr = io.StringIO()
        with mock.patch.object(sys, "argv", ["m3_eval.py", *args]), redirect_stderr(stderr):
            code = m3_eval.main()
        return code, stderr.getvalue()

    def test_zero_recall_samples_is_rejected(self) -> None:
        code, stderr = self.run_with("--recall-samples", "0")
        self.assertEqual(code, 2)
        self.assertIn("--recall-samples", stderr)

    def test_negative_embedding_dimension_is_rejected(self) -> None:
        code, stderr = self.run_with("--embedding-dimension", "-1")
        self.assertEqual(code, 2)
        self.assertIn("--embedding-dimension", stderr)

    def test_blank_embedding_model_is_rejected(self) -> None:
        code, stderr = self.run_with("--embedding-model", "")
        self.assertEqual(code, 2)
        self.assertIn("--embedding-model", stderr)


if __name__ == "__main__":
    unittest.main()
