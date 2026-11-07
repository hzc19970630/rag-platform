# API

HTTP 契约以仓库中的 [OpenAPI 文档](https://github.com/hzc19970630/rag-platform/blob/main/api/openapi/openapi.yaml)为准。该文件由 `contracts/httpapi` 的路由与 DTO 反射生成，CI 会拒绝未重新生成的契约变更。

## 生成与校验

```bash
go run ./cmd/openapi          # 重新生成 api/openapi/openapi.yaml
go run ./cmd/openapi -check   # 校验文档未漂移
```

前端类型由同一份 OpenAPI 生成：

```bash
cd web
npm run codegen
npm run codegen:check
```

## 在线查看

下面的 Redoc 视图直接加载 `main` 分支的 OpenAPI 文件。生产环境建议把 OpenAPI 文件与文档站一起发布，避免依赖外部 CDN。

<div id="redoc-container"></div>
<script src="https://cdn.redoc.ly/redoc/latest/bundles/redoc.standalone.js"></script>
<script>
  Redoc.init(
    "https://raw.githubusercontent.com/hzc19970630/rag-platform/main/api/openapi/openapi.yaml",
    {},
    document.getElementById("redoc-container")
  );
</script>

## 认证

- 浏览器会话：OIDC 授权码 + PKCE，服务端会话与短期 JWT。
- 程序化访问：`rk_<prefix>_<secret>` API Key，创建时只返回一次明文，数据库保存哈希。
- 开发模式：`X-Dev-User-ID` / `X-Dev-Tenant-ID`，仅在 `DEV_AUTH_ENABLED=true` 且非 production 时生效。

API Key 的能力是闭集，不能签发/撤销 Key，也不能访问运维接口。详细行为见[运维](operations.md)与[安全策略](security.md)。

## 主要接口

| 场景 | 接口 |
| --- | --- |
| 健康检查 | `GET /healthz`、`GET /readyz` |
| 知识库 | `GET/POST /api/v1/knowledge-bases`、`GET/PATCH/DELETE /api/v1/knowledge-bases/:id` |
| 文档 | `POST /api/v1/knowledge-bases/:id/documents`、`GET /api/v1/documents/:id` |
| 预览与 Chunk | `GET /api/v1/documents/:id/content`、`GET /api/v1/documents/:id/chunks` |
| 检索 | `POST /api/v1/search` |
| 会话与问答 | `POST /api/v1/sessions`、`POST /api/v1/sessions/:id/messages`（SSE） |
| 续传 | `GET /api/v1/requests/:request_id/events?after=N` |
| 运维 | `GET /ops/metrics`、`GET /ops/dead-letters`、`POST /ops/reconcile` |

完整路由、请求/响应字段和错误码以 OpenAPI 为准。
