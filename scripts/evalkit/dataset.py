"""Dataset loading and structural validation.

The common checks cover invariants every suite depends on before it uploads a
single fixture: the file is UTF-8 JSON, the schema version and name are declared,
fixtures have bare unique filenames and non-empty content, and every case has a
unique id, a type and a question. Suite-specific semantics (document roles,
permission probes, graph reachability) stay in the suite validators.
"""

from __future__ import annotations

import json
from pathlib import Path

MAX_FILENAME_LENGTH = 255


class DatasetError(Exception):
    """Raised when a dataset cannot be read as UTF-8 JSON."""


def load_set(path: Path | str) -> dict:
    path = Path(path)
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        raise DatasetError(f"{path}: file not found") from None
    except UnicodeDecodeError as error:
        raise DatasetError(f"{path}: not valid UTF-8 ({error})") from None
    try:
        evaluation = json.loads(text)
    except json.JSONDecodeError as error:
        raise DatasetError(
            f"{path}: invalid JSON at line {error.lineno} column {error.colno}: {error.msg}"
        ) from None
    if not isinstance(evaluation, dict):
        raise DatasetError(f"{path}: the dataset root must be a JSON object")
    return evaluation


def validate_common(evaluation: dict, *, schema_version: str | None = None) -> list[str]:
    errors: list[str] = []
    if not isinstance(evaluation, dict):
        return ["the dataset root must be a JSON object"]

    version = evaluation.get("schemaVersion")
    if not isinstance(version, str) or not version.strip():
        errors.append("schemaVersion must be a non-empty string")
    elif schema_version and version != schema_version:
        errors.append(f"schemaVersion must be {schema_version}")

    name = evaluation.get("name")
    if not isinstance(name, str) or not name.strip():
        errors.append("name must be a non-empty string")

    notes = evaluation.get("notes")
    if notes is not None and (
        not isinstance(notes, list) or not all(isinstance(note, str) for note in notes)
    ):
        errors.append("notes must be a list of strings")

    fixtures = evaluation.get("fixtures")
    if not isinstance(fixtures, list) or not fixtures:
        errors.append("the set contains no fixtures")
        fixtures = []
    cases = evaluation.get("cases")
    if not isinstance(cases, list) or not cases:
        errors.append("the set contains no cases")
        cases = []

    filenames: set[str] = set()
    for index, fixture in enumerate(fixtures, start=1):
        label = f"fixture #{index}"
        if not isinstance(fixture, dict):
            errors.append(f"{label}: must be an object")
            continue
        filename = fixture.get("filename")
        if not isinstance(filename, str) or not filename.strip():
            errors.append(f"{label}: filename must be a non-empty string")
        else:
            if filename != filename.strip():
                errors.append(f"{filename!r}: filename has leading or trailing whitespace")
            if len(filename) > MAX_FILENAME_LENGTH:
                errors.append(f"{filename!r}: filename exceeds {MAX_FILENAME_LENGTH} characters")
            if (
                filename in {".", ".."}
                or "\x00" in filename
                or any(separator in filename for separator in ("/", "\\", ":"))
            ):
                errors.append(f"{filename!r}: filename must be a bare name without a path")
            key = filename.casefold()
            if key in filenames:
                errors.append(f"duplicate fixture filename {filename!r}")
            filenames.add(key)
        role = fixture.get("role")
        if not isinstance(role, str) or not role.strip():
            errors.append(f"{filename or label}: role must be a non-empty string")
        content = fixture.get("content")
        if not isinstance(content, str) or not content.strip():
            errors.append(f"{filename or label}: fixture content is empty")

    case_ids: set[str] = set()
    for index, case in enumerate(cases, start=1):
        label = f"case #{index}"
        if not isinstance(case, dict):
            errors.append(f"{label}: must be an object")
            continue
        case_id = case.get("id")
        if not isinstance(case_id, str) or not case_id.strip():
            errors.append(f"{label}: id must be a non-empty string")
            continue
        if case_id in case_ids:
            errors.append(f"duplicate case id {case_id}")
        case_ids.add(case_id)
        case_type = case.get("type")
        if not isinstance(case_type, str) or not case_type.strip():
            errors.append(f"{case_id}: type must be a non-empty string")
        question = case.get("question")
        if not isinstance(question, str) or not question.strip():
            errors.append(f"{case_id}: question must be a non-empty string")

    return errors
