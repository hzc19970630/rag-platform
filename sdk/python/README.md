# Python SDK

Thin client for the RAG Platform HTTP API. It uses `httpx`, streams answer SSE
events, reconnects on `tail_timeout`, and returns validated citations.

## Install

```bash
cd sdk/python
python3 -m venv .venv
. .venv/bin/activate
pip install -e .
```

## Authenticate

Create an API key with `knowledge_base:read` + `chat` capabilities and the
knowledge bases you want to expose:

```bash
curl -sS http://localhost:8082/api/v1/api-keys \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H 'Content-Type: application/json' \
  -d '{"name":"mcp","capabilities":["knowledge_base:read","chat"],"knowledgeBaseIds":["kb-dev"]}'
```

Use the returned `token` only through `RAG_API_KEY`; never commit it.

## Example

```bash
cd sdk/python
RAG_API_URL=http://localhost:8082/api/v1 \
RAG_API_KEY=rk_... \
RAG_KNOWLEDGE_BASE_ID=kb-dev \
python examples/search_ask_cite.py
```

The example runs list → search → ask → citation lookup. `ask` creates a
session when `session_id` is absent.

## MCP server

The companion MCP server lives in `cmd/mcp`; it is language-neutral and uses
the same API key. See `sdk/typescript/README.md` for the stdio client entry.

## Drift

`tests/test_openapi_paths.py` fails when an operation path or method used by
`rag_platform/operations.py` disappears from `api/openapi/openapi.yaml`. The
TypeScript SDK additionally generates full schema types from the same spec.

```bash
pip install -r requirements-dev.txt
pytest -q
```