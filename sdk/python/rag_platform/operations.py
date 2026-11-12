"""Operation paths used by the client.

The paths mirror ``api/openapi/openapi.yaml``. ``tests/test_openapi_paths.py``
fails if a path or method disappears from the spec, which is the Python-side
drift check; the TypeScript SDK additionally generates full schema types.
"""

OPERATIONS: dict[str, tuple[str, str]] = {
    "list_knowledge_bases": ("GET", "/knowledge-bases"),
    "search": ("POST", "/search"),
    "create_session": ("POST", "/sessions"),
    "ask": ("POST", "/sessions/{session_id}/messages"),
    "events": ("GET", "/requests/{request_id}/events"),
    "document_status": ("GET", "/documents/{document_id}/status"),
    "citation": ("GET", "/chunks/{chunk_id}"),
}

SPEC_PATHS: dict[str, tuple[str, str]] = {
    "list_knowledge_bases": ("get", "/api/v1/knowledge-bases"),
    "search": ("post", "/api/v1/search"),
    "create_session": ("post", "/api/v1/sessions"),
    "ask": ("post", "/api/v1/sessions/{id}/messages"),
    "events": ("get", "/api/v1/requests/{requestID}/events"),
    "document_status": ("get", "/api/v1/documents/{id}/status"),
    "citation": ("get", "/api/v1/chunks/{chunkID}"),
}