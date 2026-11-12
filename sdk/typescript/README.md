# TypeScript SDK

Thin client for the RAG Platform HTTP API. Types are generated from
`api/openapi/openapi.yaml`; the client adds authentication, SSE answer
streaming, reconnect and citation lookup.

## Install

```bash
cd sdk/typescript
npm install
```

## Authenticate

Create an API key with `knowledge_base:read` + `chat` capabilities, scoped to
the knowledge bases you want to expose:

```bash
curl -sS http://localhost:8082/api/v1/api-keys \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H 'Content-Type: application/json' \
  -d '{"name":"mcp","capabilities":["knowledge_base:read","chat"],"knowledgeBaseIds":["kb-dev"]}'
```

Use the returned `token` only through `RAG_API_KEY`; never commit it.

## Example

```bash
cd sdk/typescript
RAG_API_URL=http://localhost:8082/api/v1 \
RAG_API_KEY=rk_... \
RAG_KNOWLEDGE_BASE_ID=kb-dev \
npm run example
```

The example runs list → search → ask → citation lookup. `ask` creates a
session when `sessionId` is absent and returns validated citations.

## MCP server

The companion MCP server lives in `cmd/mcp`. Build and run it over stdio:

```bash
go build -o bin/rag-mcp ./cmd/mcp
RAG_API_URL=http://localhost:8082/api/v1 RAG_API_KEY=rk_... ./bin/rag-mcp
```

Example MCP client entry:

```json
{
  "command": "/path/to/rag-mcp",
  "env": {
    "RAG_API_URL": "http://localhost:8082/api/v1",
    "RAG_API_KEY": "rk_..."
  }
}
```

Tools: `list_knowledge_bases`, `search`, `ask`, `get_document_status`,
`get_citation`. Every call reuses the API key's tenant and knowledge-base
scope; there is no second authorization path.

## Codegen drift

```bash
npm run codegen
npm run codegen:check
```

`generated.ts` is produced by `openapi-typescript`; CI fails if it drifts from
`api/openapi/openapi.yaml`.