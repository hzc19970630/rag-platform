"""Report path resolution and writing."""

from __future__ import annotations

import json
from pathlib import Path


def resolve_report_path(report_dir: Path | str | None, default_dir: Path | str, filename: str) -> Path:
    return (Path(report_dir) if report_dir else Path(default_dir)) / filename


def base_url_for_report(base_url: str, redact: bool) -> str:
    return "redacted" if redact else base_url


def write_report(report: dict, path: Path | str) -> Path:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return path
