"""Shared harness for the repository evaluation suites.

The M3 (authorized RAG) and M7 (graph versus RAG) scripts used to carry their
own HTTP client, polling helpers, multipart upload and report writer. They had
drifted apart: one authenticated with development headers, the other with a
login token, and only one validated its dataset before uploading. This package
holds the pieces both suites need so a fix or a new auth mode lands once.
"""

from .auth import (
    AUTH_MODES,
    AuthError,
    Credentials,
    add_auth_arguments,
    parse_actor_keys,
)
from .client import Client, DEV_HEADERS, DEV_TENANT_ID, DEV_USER_ID, dev_headers, parse_sse
from .dataset import DatasetError, load_set, validate_common
from .metrics import mean, percentile, rate
from .probes import PERMISSION_PROBES, run_permission_probes
from .report import base_url_for_report, resolve_report_path, write_report
from .stack import wait_document_ready, wait_for, wait_pipeline_status

__all__ = [
    "AUTH_MODES",
    "AuthError",
    "Client",
    "Credentials",
    "DEV_HEADERS",
    "DEV_TENANT_ID",
    "DEV_USER_ID",
    "DatasetError",
    "PERMISSION_PROBES",
    "add_auth_arguments",
    "base_url_for_report",
    "dev_headers",
    "load_set",
    "mean",
    "parse_actor_keys",
    "parse_sse",
    "percentile",
    "rate",
    "resolve_report_path",
    "run_permission_probes",
    "validate_common",
    "wait_document_ready",
    "wait_for",
    "wait_pipeline_status",
    "write_report",
]
