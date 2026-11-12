# MCP 与 SDK

B16 提供三个入口，全部复用现有 HTTP API、API Key 鉴权和租户/知识库 scope：

- **MCP Server**（`cmd/mcp`）：stdio 传输，暴露 `list_knowledge_bases`、`list_documents`、`get_document`、`get_document_content`、`search`、`ask`、`get_document_status`、`get_citation`。
- **TypeScript SDK**（`sdk/typescript`）：从 OpenAPI 生成类型，薄客户端支持 SSE 问答、断流续传和引用回查。
- **Python SDK**（`sdk/python`）：`httpx` 薄客户端，同样的能力与示例。

MCP Server 不直接连接数据库，也不新增授权路径；它只是带着调用方的 API Key 调用 `/api/v1/*`。因此跨租户、跨知识库的越权仍由服务端拒绝。

## 1. 创建 API Key

MCP 和 SDK 使用 API Key，不用浏览器会话。Key 至少需要 `knowledge_base:read` 和 `chat`，并建议用 `knowledgeBaseIds` 限定范围：

```bash
curl -sS http://localhost:8082/api/v1/api-keys \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H 'Content-Type: application/json' \
  -d '{"name":"mcp","capabilities":["knowledge_base:read","chat"],"knowledgeBaseIds":["kb-dev"]}'
```

返回的 `token`（`rk_...`）只放在环境变量或密钥管理里，不要提交到仓库。

## 2. 运行 MCP Server

```bash
go build -o bin/rag-mcp ./cmd/mcp
RAG_API_URL=http://localhost:8082/api/v1 RAG_API_KEY=rk_... ./bin/rag-mcp
```

构建出的二进制必须与运行它的系统一致。在 Apple Silicon Mac 上运行 Qoder 时，要构建 `darwin/arm64` 版本；不要直接使用 Linux 容器里编译出的 `bin/rag-mcp`（它是 ELF 文件，macOS 无法执行）：

```bash
# 没有本机 Go 时，可在 Docker 中交叉编译 macOS 版本
docker run --rm -v "$PWD":/src -w /src \
  -e CGO_ENABLED=0 -e GOOS=darwin -e GOARCH=arm64 \
  golang:1.26-alpine go build -trimpath -o bin/rag-mcp-darwin-arm64 ./cmd/mcp
```

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `RAG_API_URL` | `http://localhost:8082/api/v1` | 平台 API 根地址（含 `/api/v1`） |
| `RAG_API_KEY` | 必填 | `rk_` 开头的 API Key |
| `RAG_MCP_TIMEOUT_SECONDS` | `180` | 单次工具调用/问答的总超时 |

MCP 客户端配置示例：

```json
{
  "command": "/path/to/rag-mcp",
  "env": {
    "RAG_API_URL": "http://localhost:8082/api/v1",
    "RAG_API_KEY": "rk_..."
  }
}
```

工具：

| 工具 | 能力 | 说明 |
| --- | --- | --- |
| `list_knowledge_bases` | 只读 | 列出 Key 可见的知识库 |
| `list_documents` | 只读 | 列出知识库下的文档，支持 `cursor`/`limit` 游标分页 |
| `get_document` | 只读 | 文档详情、当前版本、流水线状态与阶段 |
| `get_document_content` | 只读 | 解析后的内容块预览；仅在当前版本达到 `EVIDENCE_READY` 后可用 |
| `search` | 只读 | 混合检索，返回 chunk、文档、引用位置 |
| `ask` | 生成 | 创建/复用会话，流式读取答案与已校验引用 |
| `get_document_status` | 只读 | 文档处理状态、版本与阶段 |
| `get_citation` | 只读 | 按 chunk id 回查来源内容，服务端再次校验文档权限 |

典型调用顺序：`list_knowledge_bases` → `list_documents` → `get_document` / `get_document_content` → `search` → `ask` → `get_citation`。

`get_document_content` 在文档尚未就绪时返回工具错误（API 的 409/404 经 MCP 原样传递），不会返回部分或过期内容。

`ask` 默认创建新会话；传入 `sessionId` 可复用已有会话。答案流在 `tail_timeout` 时会自动按 `/requests/{id}/events?after=` 续传。

## 3. SDK

### TypeScript

```bash
cd sdk/typescript
npm install
RAG_API_URL=http://localhost:8082/api/v1 \
RAG_API_KEY=rk_... RAG_KNOWLEDGE_BASE_ID=kb-dev \
npm run example
```

`src/generated.ts` 由 `openapi-typescript` 从 `api/openapi/openapi.yaml` 生成；`npm run codegen:check` 在 CI 校验漂移。

### Python

```bash
cd sdk/python
python3 -m venv .venv && . .venv/bin/activate
pip install -e .
RAG_API_URL=http://localhost:8082/api/v1 \
RAG_API_KEY=rk_... RAG_KNOWLEDGE_BASE_ID=kb-dev \
python examples/search_ask_cite.py
```

`tests/test_openapi_paths.py` 校验 SDK 使用的路径/方法仍存在于 OpenAPI，作为 Python 侧漂移检查。

## 4. 验收

quickstart 栈运行后：

```bash
./scripts/b16_acceptance.sh
```

它会：

1. 校验 `/readyz` 与开发身份；
2. 创建一个临时知识库（绑定当前运行中的 embedding 模型）和只绑定它的 API Key；
3. 上传 `examples/demo/retrieval-handbook.md` 并等待 `EVIDENCE_READY`；
4. 用真实 MCP 客户端完成 `search → ask → get_citation`；
5. 用同一个 Key 查询一个不在范围内的知识库，要求返回工具错误；
6. 运行 TS/Python SDK 单测；
7. 删除临时知识库并撤销 API Key。

需要同时跑 SDK 示例时加 `B16_RUN_EXAMPLES=1`。

## 5. 边界

- MCP 传输目前只做 stdio；HTTP/SSE 传输、工具写操作（上传/删除）不在 B16 范围。
- SDK 是薄封装，不隐藏服务端的错误码、ACL 版本和引用校验。
- 生产部署仍需按 API Key 最小权限原则发 Key；不要把租户管理员会话交给 MCP 客户端。