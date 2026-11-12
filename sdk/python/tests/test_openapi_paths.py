from pathlib import Path

from rag_platform.operations import SPEC_PATHS


def test_operations_exist_in_openapi() -> None:
    repo_root = Path(__file__).resolve().parents[3]
    spec = (repo_root / "api" / "openapi" / "openapi.yaml").read_text()
    for operation, (method, path) in SPEC_PATHS.items():
        assert f"  {path}:" in spec, f"{operation}: {path} is missing from OpenAPI"
        assert f"    {method}:" in spec, f"{operation}: method {method} is missing"