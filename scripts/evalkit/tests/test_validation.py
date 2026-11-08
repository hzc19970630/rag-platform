#!/usr/bin/env python3
"""Regression tests for evaluation dataset validation.

These tests do not contact a stack. They guard the rules that run before any
fixture is uploaded: the checked-in sets must stay valid, and tampered copies
must fail with a specific, actionable error.
"""

from __future__ import annotations

import copy
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))

import m3_eval  # noqa: E402
import m7_eval  # noqa: E402
from evalkit import (  # noqa: E402
    DatasetError,
    DEV_TENANT_ID,
    DEV_USER_ID,
    load_set,
    validate_common,
)


def minimal_set() -> dict:
    return {
        "schemaVersion": "TestSetV1",
        "name": "validation fixture",
        "fixtures": [{"role": "handbook", "filename": "handbook.md", "content": "hello"}],
        "cases": [{"id": "case-1", "type": "fact", "question": "what is in the handbook"}],
    }


def m3_fixture() -> dict:
    return {
        "schemaVersion": "EvaluationSetV1",
        "name": "m3 validation fixture",
        "fixtures": [{"role": "handbook", "filename": "handbook.md", "content": "hello"}],
        "cases": [
            {
                "id": "permission",
                "type": "permission_negative",
                "question": "what is in the handbook",
                "actor": {"userId": "other-user", "tenantId": "other-tenant"},
                "probes": ["search"],
            },
            {
                "id": "fact",
                "type": "fact",
                "question": "what is in the handbook",
                "expectRefusal": False,
                "expectedDocuments": ["handbook"],
                "alignedEvidence": ["hello"],
            },
            {"id": "refusal", "type": "no_answer", "question": "unknown", "expectRefusal": True},
            {
                "id": "injection",
                "type": "injection",
                "question": "what is in the handbook",
                "expectRefusal": False,
                "expectInjectionFlag": True,
            },
        ],
    }


class LoadSetTests(unittest.TestCase):
    def test_rejects_non_utf8_file(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "set.json"
            path.write_bytes(b"\xff\xfe\x00")
            with self.assertRaises(DatasetError):
                load_set(path)

    def test_rejects_invalid_json(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "set.json"
            path.write_text("{", encoding="utf-8")
            with self.assertRaises(DatasetError):
                load_set(path)

    def test_rejects_non_object_root(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "set.json"
            path.write_text("[]", encoding="utf-8")
            with self.assertRaises(DatasetError):
                load_set(path)


class CommonValidationTests(unittest.TestCase):
    def test_minimal_set_is_valid(self) -> None:
        self.assertEqual(validate_common(minimal_set()), [])

    def test_schema_mismatch(self) -> None:
        evaluation = minimal_set()
        evaluation["schemaVersion"] = "OtherSetV9"
        self.assertTrue(
            any("schemaVersion must be TestSetV1" in error for error in validate_common(
                evaluation, schema_version="TestSetV1",
            ))
        )

    def test_duplicate_filename_is_case_insensitive(self) -> None:
        evaluation = minimal_set()
        evaluation["fixtures"].append(
            {"role": "copy", "filename": "HANDBOOK.md", "content": "duplicate"}
        )
        errors = validate_common(evaluation)
        self.assertTrue(any("duplicate fixture filename" in error for error in errors))

    def test_filename_must_be_a_bare_name(self) -> None:
        evaluation = minimal_set()
        evaluation["fixtures"][0]["filename"] = "../handbook.md"
        errors = validate_common(evaluation)
        self.assertTrue(any("bare name without a path" in error for error in errors))

    def test_duplicate_case_id(self) -> None:
        evaluation = minimal_set()
        evaluation["cases"].append({"id": "case-1", "type": "fact", "question": "second"})
        errors = validate_common(evaluation)
        self.assertTrue(any("duplicate case id case-1" in error for error in errors))

    def test_case_question_is_required(self) -> None:
        evaluation = minimal_set()
        evaluation["cases"][0]["question"] = "  "
        errors = validate_common(evaluation)
        self.assertTrue(any("question must be a non-empty string" in error for error in errors))

    def test_empty_fixture_content(self) -> None:
        evaluation = minimal_set()
        evaluation["fixtures"][0]["content"] = "\n"
        errors = validate_common(evaluation)
        self.assertTrue(any("fixture content is empty" in error for error in errors))


class M3ValidationTests(unittest.TestCase):
    def test_checked_in_set_is_valid(self) -> None:
        self.assertEqual(m3_eval.validate_set(load_set(m3_eval.EVAL_SET)), [])

    def test_unknown_expected_document_role(self) -> None:
        evaluation = copy.deepcopy(load_set(m3_eval.EVAL_SET))
        case = next(case for case in evaluation["cases"] if case.get("expectedDocuments"))
        case["expectedDocuments"] = ["missing-role"]
        errors = m3_eval.validate_set(evaluation)
        self.assertTrue(any("reference unknown roles" in error for error in errors))

    def test_unknown_probe(self) -> None:
        evaluation = m3_fixture()
        evaluation["cases"][0]["probes"] = ["teleport"]
        errors = m3_eval.validate_set(evaluation)
        self.assertTrue(any("unknown probes" in error for error in errors))

    def test_duplicate_fixture_role(self) -> None:
        evaluation = m3_fixture()
        evaluation["fixtures"].append(
            {"role": "handbook", "filename": "copy.md", "content": "duplicate"}
        )
        errors = m3_eval.validate_set(evaluation)
        self.assertTrue(any("duplicate fixture role" in error for error in errors))

    def test_in_tenant_actor_needs_created_membership(self) -> None:
        evaluation = m3_fixture()
        evaluation["cases"][0]["actor"] = {"userId": "stranger", "tenantId": DEV_TENANT_ID}
        errors = m3_eval.validate_set(evaluation)
        self.assertTrue(any("denied for the wrong reason" in error for error in errors))

    def test_owner_cannot_be_a_permission_actor(self) -> None:
        evaluation = m3_fixture()
        evaluation["cases"][0]["actor"] = {"userId": DEV_USER_ID, "tenantId": DEV_TENANT_ID}
        errors = m3_eval.validate_set(evaluation)
        self.assertTrue(any("must not be the knowledge base owner" in error for error in errors))

    def test_expected_documents_must_stay_within_aligned_documents(self) -> None:
        evaluation = m3_fixture()
        evaluation["cases"][1]["alignedDocuments"] = []
        errors = m3_eval.validate_set(evaluation)
        self.assertTrue(any("must be a subset of alignedDocuments" in error for error in errors))

    def test_chunk_probe_requires_an_earlier_answerable_case(self) -> None:
        evaluation = m3_fixture()
        evaluation["cases"][0]["probes"] = ["chunk"]
        errors = m3_eval.validate_set(evaluation)
        self.assertTrue(any("require an earlier answerable case" in error for error in errors))

    def test_permission_case_requires_handbook_fixture(self) -> None:
        evaluation = m3_fixture()
        evaluation["fixtures"][0]["role"] = "other"
        errors = m3_eval.validate_set(evaluation)
        self.assertTrue(any('role "handbook"' in error for error in errors))

    def test_session_override_must_be_a_json_object(self) -> None:
        evaluation = m3_fixture()
        evaluation["cases"][1]["sessionOverride"] = "[1, 2]"
        errors = m3_eval.validate_set(evaluation)
        self.assertTrue(any("must encode a JSON object" in error for error in errors))

    def test_gate_metric_must_be_measurable(self) -> None:
        evaluation = m3_fixture()
        evaluation["cases"] = [case for case in evaluation["cases"] if case["type"] != "injection"]
        errors = m3_eval.validate_set(evaluation)
        self.assertTrue(any("injectionFlagRate would be unmeasurable" in error for error in errors))


class M7ValidationTests(unittest.TestCase):
    def test_checked_in_set_is_valid(self) -> None:
        self.assertEqual(m7_eval.validate_set(load_set(m7_eval.EVAL_SET)), [])

    def test_negative_case_needs_expect_no_answer(self) -> None:
        evaluation = copy.deepcopy(load_set(m7_eval.EVAL_SET))
        case = next(case for case in evaluation["cases"] if case["type"] == "negative")
        case.pop("expectNoAnswer")
        errors = m7_eval.validate_set(evaluation)
        self.assertTrue(any("negative case must set expectNoAnswer" in error for error in errors))

    def test_expected_node_must_be_declared_by_a_triple(self) -> None:
        evaluation = copy.deepcopy(load_set(m7_eval.EVAL_SET))
        case = next(case for case in evaluation["cases"] if case["type"] != "negative")
        case["expectedNodes"] = ["Not An Entity"]
        errors = m7_eval.validate_set(evaluation)
        self.assertTrue(any("is not declared by any triple" in error for error in errors))


if __name__ == "__main__":
    unittest.main()
