# RAG Platform

当前按 `DESIGN.md` 和 `MILESTONES.md` 实施 MVP。M0 已提供容器化工程骨架，M1 已提供仅限开发/测试的身份注入、租户/知识库 RBAC 和授权审计，M2 已实现数字文档 Evidence 入库流水线，M3 已实现有权限约束、可引用的 RAG 问答，M4 已提供企业身份、API Key、文档生命周期和运营可靠性能力，M5 已提供一个外部清单连接器的增量同步能力，M6 已提供 LLM Wiki 投影、Knowledge Index 与 ProjectionJoin，M7 已提供关系投影、可审核图谱快照与有界图查询。

## MVP 范围

- 支持数字 PDF（必须有可提取文本层）、DOCX、XLSX、PPTX、Markdown、TXT、HTML；OOXML 与 HTML 解析只用 Python 标准库，`.xls`/`.ppt` 二进制格式明确拒绝。
- 不支持纯扫描 PDF 或包含扫描页的混合 PDF，统一返回 `unsupported_scanned_pdf`。
- 不支持图片（PNG/JPG 等上传直接 400）。
- 不接入 OCR 云厂商 SDK，不部署 PaddleOCR。
- MVP 本身不包含 Wiki、知识图谱、Agent 和 OpenFGA；M5 以独立里程碑提供 `http_manifest` 连接器，M6 以独立里程碑在 Evidence Package 之上提供 LLM Wiki 与 Knowledge Index，M7 以独立里程碑提供关系投影与有界图查询；三者都复用既有 Evidence Pipeline 和授权端口，不改变 MVP 边界，图谱也不进入默认问答链路。

## 本地启动

需要 Docker、Docker Compose、Node.js。首次启动：

```bash
cp .env.example .env
make dev-up
make api-test
```

本地地址（默认 API 端口为 `8082`，可通过 `.env` 中的 `API_PORT` 覆盖）：

- API health: `http://localhost:8082/healthz`
- API readiness: `http://localhost:8082/readyz`
- MinIO console: `http://localhost:9001`

`make dev-up` 会在依赖就绪后自动执行幂等的 PostgreSQL 基础迁移；也可以单独运行 `make migrate`。

M0 验收命令：

```bash
make test
make lint
./scripts/m0_acceptance.sh
```

验收脚本会验证健康检查、空数据库迁移、幂等种子、`vector`/`pg_search` 扩展、BM25 查询和 HNSW 向量查询。

M1 权限验收命令：

```bash
./scripts/m1_acceptance.sh
```

本地 M1 默认身份为 `dev-user` / `dev-tenant`。测试其他种子身份时，在请求中传入 `X-Dev-User-ID` 和 `X-Dev-Tenant-ID`；该开发身份方式在 `APP_ENV=production` 下会被拒绝。

M2 Evidence 入库验收命令：

```bash
./scripts/m2_acceptance.sh
```

M2 提供 `POST /api/v1/knowledge-bases/:id/documents`、`GET /api/v1/documents/:id` 和 `DELETE /api/v1/documents/:id`。上传使用 multipart 字段 `file`，可选 `document_id` 重处理已有文档，并建议通过 `Idempotency-Key` 保证重复请求不重复入库。响应中的 `pipeline` 和 `stages` 可用于轮询 `EVIDENCE_READY` 或定位失败阶段。

## M3 授权 RAG 与引用交付

M3 验收命令：

```bash
./scripts/m3_acceptance.sh
make m3-eval
```

M3 新增的只读与问答接口：

- `GET /api/v1/knowledge-bases/:id/documents?cursor=&limit=`：`DocumentPageV1`，游标分页列出该知识库下未删除的文档及其当前版本状态（`contentReady` / `previewable`）。状态取自 `latest_version_id` 对应版本，因为 `documents.status` 只在流水线成功时更新。界面据此提供进入详情、用带 `document_id` 的上传替换为新版本、以及删除文档三个入口。
- `GET /api/v1/documents/:id/status`：处理状态、阶段进度和 `contentReady` / `previewable` 标志。
- `GET /api/v1/documents/:id/content`：`DocumentPreviewV1`，仅在当前版本达到 `EVIDENCE_READY` 后返回解析内容。
- `GET /api/v1/documents/:id/chunks?cursor=&limit=`：`ChunkPageV1`，当前版本可检索子块的游标分页；父块不进入列表，按 `:chunk_id` 展开读取。
- `GET /api/v1/chunks/:chunk_id`：单个 Chunk 详情，服务端反查文档、版本和知识库后鉴权；已被替换或删除的版本返回 `409 not_ready`。
- `GET /api/v1/documents/:id/preview`：原始文件受控流式预览，不返回对象存储地址或永久 URL；DOCX/XLSX/PPTX/HTML 以 `attachment` 下载，PDF/Markdown/TXT 内联并带 `nosniff`；HTML 永不被内联。
- `POST /api/v1/search`：只检索不生成。返回 `EvidenceSetV1`，包含 `QueryPlanV1`、融合分数和各通道排名。
- `POST /api/v1/sessions`、`GET /api/v1/sessions`、`GET /api/v1/sessions/:id/messages`：会话与消息历史。
- `POST /api/v1/sessions/:id/messages`：在线问答，SSE 输出 `status`、`references`、`thinking`、`answer`、`complete`、`error` 事件，每条事件带 `event_id`/`request_id`/`sequence`。
- `GET /api/v1/requests/:request_id/events?after=N`（或 `Last-Event-ID`）：断线后按 sequence 续传已持久化的事件。

检索链路为 BM25 与向量并行召回 → RRF 融合（常数 `rrf_k`，默认 60）→ 去重 → 可配置 Rerank → 父块/邻块扩展 → Top-K 与上下文 Token 限制 → 最终权限二次校验。所有召回查询在 SQL 层就带 `tenant_id`、知识库和"当前版本 + `EVIDENCE_READY`"过滤。Rerank 默认关闭，需离线评估后再开启；开启后由 `RERANK_SOURCE` 选择适配器（`none` 直通 / `lexical` 离线词面重叠 / `remote` OpenAI 兼容 `/rerank`），`rerank_threshold` 与 `rerank_top_k` 只在 Rerank 实际运行时生效。父块始终补齐，两个相邻块计数只约束邻居块。接口里返回的 `score` 是 RRF 融合分（量级由 `rrf_k` 决定，默认 60 时靠前的条目落在 0.016-0.033），向量余弦在 `vectorScore`、关键词分在 `keywordScore`。

检索参数采用三级覆盖：`config/config.yaml` 全局默认 → 知识库级（`PATCH /api/v1/knowledge-bases/:id` 的 `retrieval_config`，写入审计）→ 会话/请求级（`sessionOverride`）。三级都通过服务端区间校验：向量阈值 0-1、关键字阈值 ≥ 0（BM25 分数无上界）、Rerank 阈值 -10~10、`*_top_k` 0-200、`rrf_k` 1-200、`expansion_score_factor` 0-1。

Prompt 已外置到 `config/prompt_templates/*.yaml`，启动时校验模板 ID 完整且唯一，Go 代码不含 Prompt 字面量。生成适配器同样是配置：`CHAT_SOURCE=local` 为离线抽取式 Adapter（只引用包含查询词的证据句，无匹配则返回"不足以回答"，生产环境禁止）；`CHAT_SOURCE=remote` 走 OpenAI 兼容 `/chat/completions`。

引用是一等数据：检索阶段为每条证据注册请求级引用 ID，模型只能引用已注册的 ID，落库前再次校验引用的 Chunk 属于本次 EvidenceSet 且仍在授权范围内。越权、未注册或不可定位的引用会被拒绝输出，返回 `citation_rejected`；证据不足返回 `insufficient_evidence` 拒答，不用模型常识填空。

评估集是 `tests/eval/m3_eval_set.json`：17 篇 fixtures、49 个用例，覆盖事实查找（含中文）、跨文档、无答案拒答、权限负向（跨租户与 viewer/editor 角色矩阵）、Prompt 注入和冲突。每个用例声明 `expectedDocuments`（答案应引用的文档）与 `alignedEvidence`（证据应包含的片段），权限用例声明 `actor` 和必须被拒绝的 `probes`。

```bash
python3 scripts/m3_eval.py --base-url http://localhost:8082 \
    --embedding-model text-embedding-3-small --embedding-dimension 1536 \
    --recall-samples 1
```

`--embedding-model` / `--embedding-dimension` 只用于在报告里记录本次使用的向量模型，无法通过 HTTP 观测，不声明就会在报告的 `limitations` 里如实说明指标无法归因。chat 模型从答案事件流读取，自动记录。`--recall-samples`（默认 1）把每条用例的检索调用重复 N 次，`recallAtK` 取均值：默认单次即 0 或 1，N>1 时可能落在 0 到 1 之间，出现分歧的用例列在 `metrics.recallUnstableCases`；这部分不消耗模型调用。

实测结论（同一语料、同一问题连续 25 次检索）：证据集合与顺序**完全一致**，`recallUnstableCases` 为空。也就是说在同一份已建好的索引上，检索对固定查询是确定性的，重复检索取不到新信息；真正的检索波动来自**重新入库**（新建 HNSW 图与写入顺序）或检索期间有并发写入。要衡量后者需要「重新入库 + 重新检索」的整轮重复，而不是本参数。

脚本对**安全、检索与引用质量**做真实判定，违反即退出码非零：权限探测被放行、答案引用了未注册的引用、拒答携带了引用，或任一门槛未达标。当前发布门槛（2026-10-21 由产品/架构负责人确认）：`permissionNegativeDenials`、`refusalRate`、`citationAccuracy`、`groundedness`、`injectionFlagRate`、`recallAtK` 必须 = 1.0，`documentHitRate` ≥ 0.85。`conflictFlagRate` 与 `meanLatencyMs` 仅作诊断、不设发布门槛：前者随模型的引用选择波动、跨轮不可复现，后者受 relay 吞吐影响。报告 `gates` 块显式列出每个门槛的取值和诊断项。偶发的 provider 错误会重试 3 次，仍失败则该用例从聚合中剔除并在 `metrics.excludedCases` 中显式列出，不会被算成质量问题。

M2 接受数字 PDF、DOCX、XLSX、PPTX、Markdown、TXT 和 HTML；OLE 二进制（`.xls`/`.ppt`）与图片不在支持范围，损坏的 OOXML 包以 `unsupported_corrupt_document` 失败，超过解析上限的包以 `document_too_complex` 失败。纯扫描或混合扫描 PDF 会以 `unsupported_scanned_pdf` 失败，不会生成部分 Chunk。Embedding 使用本地确定性 Adapter 以完成离线验收，关键词索引使用 PostgreSQL/ParadeDB BM25 Adapter；真实模型和 RAG 问答属于 M3。

Chunk 切分策略已配置化：Worker 读取 `.env` 中的 `CHUNK_SIZE`、`CHUNK_OVERLAP`、`CHUNK_SPLIT_MARKERS` 和 `CHUNK_MIN_SIZE`。默认采用标记优先、定长窗口回退的策略；调整参数后需对已有文档执行重新处理才会生成新 Chunk。生产环境应通过受管配置发布，不能在代码中写死切分规则。

## M4 企业治理与运营可靠性

M4 验收命令：

```bash
./scripts/m4_acceptance.sh
./scripts/m4_backup_restore.sh
make test && make lint
```

本地 OIDC 联调可使用仓库内的 Keycloak 开发环境。realm、confidential client、`tenant_id` mapper 和测试用户固化在 `deploy/keycloak/rag-realm.json`，其中的凭据仅用于本地开发，生产必须替换为部署方 IdP：

```bash
docker compose --profile oidc --env-file .env -f deploy/docker-compose/docker-compose.yml up -d keycloak
curl -fsS "<KEYCLOAK_HOSTNAME>/realms/rag/.well-known/openid-configuration"
```

启动后设置 `OIDC_ENABLED=true`、`OIDC_ISSUER=$KEYCLOAK_HOSTNAME/realms/rag` 和 realm 文件中的 client id/secret。`KEYCLOAK_HOSTNAME` 必须同时被浏览器和 `api` 容器访问，因此不能使用 `localhost`；本地容器通常使用宿主机 LAN IP。

M4 新增能力：

- 认证：`GET /api/v1/auth/config`、`GET /api/v1/auth/oidc/login`、`GET /api/v1/auth/callback`、`POST /api/v1/auth/dev-login`（仅开发/测试）、`POST /api/v1/auth/refresh`、`POST /api/v1/auth/logout`。OIDC 使用授权码 + PKCE + nonce；会话为服务端 `auth_sessions` 行 + HS256 JWT，支持 `JWT_SIGNING_KEYS` 多 kid 轮换。`OIDC_ENABLED=false` 时登录入口返回 404。
- API Key：`GET/POST/DELETE /api/v1/api-keys`。明文 `rk_<prefix>_<secret>` 只在创建响应返回一次；数据库存储 `sha256(pepper+raw)`，capabilities 为闭集（`knowledge_base:read|write|delete|manage_members`、`chat`），可限制知识库范围与过期时间。Key 只能使用能力范围内的知识库，且不能签发/撤销 Key 或访问运维接口。
- 授权：角色/成员关系是唯一事实源；删除成员或修改角色会提升 `authorization_revisions` 与 `acl_version`，Redis Pub/Sub 只做通知，缓存失效以数据库 revision 为准。撤权后下一次检索即返回 403，`/me` 的 scope 同步移除。
- 文档生命周期：`GET /documents/:id/versions`、`POST /documents/:id/reparse`、`POST /documents/:id/versions/:versionID/activate`、`POST /documents/:id/restore`、`POST /documents/:id/purge`。重新解析会将被替换版本标为 `CANCELLED`、Chunk/证据包标为 `STALE`；回滚复用 STALE 证据包，不重新 Embedding。删除为软删除，`DOCUMENT_RETENTION_DAYS` 到期后由 Worker 硬清理并删除对象存储文件。
- 运维接口（tenant owner/admin 且必须使用 session 凭据）：`GET /ops/tasks/:key`、`GET /ops/dead-letters`、`POST /ops/dead-letters/:id/retry`、`POST /ops/dead-letters/:id/discard`、`POST /ops/pipelines/:id/cancel`、`POST /ops/reconcile`、`GET /ops/metrics`、`GET /ops/metrics/prometheus`。队列按 `ingestion` / `interactive` / `maintenance` 隔离；死信重放走 `interactive`，不排队在批量入库之后。
- 可观测性：`/ops/metrics` 同时返回进程内计数/耗时、三个 Worker 队列深度，以及 24 小时数据库窗口内的阶段计数、解析失败率、模型错误、死信和 Outbox 积压。Prometheus 通过 `METRICS_ADDR` 启用的独立监听器抓取 `GET /metrics`（无租户鉴权，只能绑内网/回环）；`/ops/metrics/prometheus` 保留给带 session 的人工排查。指标口径、备份恢复、故障演练与密钥轮换步骤见 [`docs/operations.md`](docs/operations.md)。

M4 的备份演练会把数据库恢复到一次性 ParadeDB 容器，并逐表比对行数、迁移校验和、授权 revision 与 `chunks_bm25_idx` / `chunks_vector_idx` 索引定义。`pg_dump` 不包含 MinIO/S3 对象，生产恢复必须同时备份对象存储桶。

## M5 外部数据源与增量同步

M5 验收命令：

```bash
./scripts/m5_acceptance.sh
```

M5 实现一个 `http_manifest` 连接器：清单是 JSON `{"items":[{"id","url","path","version","sha256?"}]}`，条目必须是 PDF/DOCX/XLSX/PPTX/Markdown/TXT/HTML（允许清单已同步扩展，m5 验收仍以 Markdown 覆盖同步语义），文件 URL 可相对清单地址。凭据（Bearer 或 Basic）以 AES-256-GCM envelope 存放在 `connectors.credential_ref`，响应只返回 `hasCredential`；清单 URL、重定向和实际拨号地址都经过 SSRF 校验，凭据只发送到清单同源地址，生产环境禁止私网目标。

同步以 `sync_runs` 持久化游标（`items` → `tombstones` → `done`）后才发布 `connector.sync`，因此进程崩溃或取消后可按游标续跑。变更检测使用清单元数据和内容哈希；源端消失的条目转为 tombstone 并软删除文档，重新出现时恢复同一文档。清单是完整快照，`{"items":[]}` 表示源端已无条目，会把全部现存条目转为 tombstone；之后恢复条目时复用原文档 ID 和版本，不产生重复文档。删除可能取消仍在处理的 Pipeline，恢复时从第一个未成功的阶段重新入队并复用原版本；证据包已生成但阶段记录未来得及收尾的 Pipeline 直接收敛为 `EVIDENCE_READY`，embed/index 也支持幂等重放。连接器只写 `Document`/`DocumentVersion`/`PipelineRun`/`StageRun`/Outbox，解析、切分、向量化和索引全部由既有 Pipeline Worker 完成，不直接写索引，也不绕过知识库授权。

接口：

- `POST/GET /api/v1/knowledge-bases/:id/connectors`、`POST /api/v1/knowledge-bases/:id/sync/cancel`
- `GET/PATCH/DELETE /api/v1/connectors/:id`、`POST /api/v1/connectors/:id/sync`、`POST /api/v1/connectors/:id/cancel`
- `GET /api/v1/connectors/:id/runs`、`GET /api/v1/connectors/:id/runs/:runId`、`POST .../retry`、`POST .../cancel`
- `GET /api/v1/connectors/:id/permissions`

连接器变更要求租户 owner/admin 且使用会话凭据（API Key 只能读取）；同步文档继承知识库成员权限，不提供条目级 ACL。当前边界：仅 `http_manifest`；`requestsPerSecond` 的修改在下一次同步即生效；删除连接器保留既有 outbox/审计历史，已同步文档按知识库文档生命周期管理。本机验收需要 `CONNECTOR_ALLOW_PRIVATE_NETWORKS=true`，因为清单 fixture 运行在宿主机；生产环境该开关必须为 false。

## M6 LLM Wiki 与 Knowledge Index

M6 验收命令：

```bash
./scripts/m6_acceptance.sh
```

M6 在知识库的 `IndexingStrategy` 上使用 `wiki_enabled`（默认 false）。开启开关或调用手动重建时，服务端为当前全部 `EvidencePackage` 创建一个投影运行；`required_capabilities` 含 `wiki` 时必须同时开启 `wiki_enabled`，关系投影由 M7 的 `graph_enabled` 独立控制。投影运行使用独立的 `projection` 队列（Asynq 任务 `knowledge.projection.wiki.v1`）和 `COLLECT` → `GENERATE` → `FINALIZE` 三个阶段，失败可独立重试，不会重跑 Evidence Pipeline。

接口：

- `GET /api/v1/knowledge-bases/:id/projection`：`wikiEnabled`、`joinStatus`、页面统计和最近的运行。
- `POST /api/v1/knowledge-bases/:id/wiki/rebuild`、`GET /api/v1/knowledge-bases/:id/wiki/runs`、`GET /api/v1/wiki/runs/:runId`、`POST /api/v1/wiki/runs/:runId/retry`。
- `GET /api/v1/knowledge-bases/:id/wiki/pages`、`GET /api/v1/wiki/pages/:pageId`、`GET /api/v1/wiki/pages/:pageId/versions`。
- `POST /api/v1/wiki/pages/:pageId/versions/:versionId/review`、`POST .../publish`、`POST /api/v1/wiki/pages/:pageId/rollback`。
- `GET /api/v1/knowledge-bases/:id/knowledge/search?q=&limit=`：页面级知识检索。

页面由按标题组织的文档页和跨文档概览页组成。每个内容块保存 `sourceChunkIds`，页面版本保存来源文档版本、生成模型、Prompt 版本、内容哈希和 ACL 版本；页面状态为 `DRAFT` / `PUBLISHED` / `REVIEW_REQUIRED` / `STALE` / `ARCHIVED`，审核状态为 `PENDING` / `APPROVED` / `REJECTED`。读取沿用知识库读权限；重建/重试要求写权限且拒绝 API Key；审核、发布和回滚要求 `manage_members` 且拒绝 API Key，发布只接受已审核版本。

Knowledge Index 是页面级投影（halfvec + BM25），只索引已发布且 ACL 与知识库当前版本一致的页面。成员变更或 `PATCH` 知识库会推进 ACL 版本并立即删除索引行、把派生页面置为 `STALE`；手动重建按新 ACL 生成待审核版本。检索时对每个来源 Chunk 重新校验当前文档版本、`EVIDENCE_READY` 和访问权限，任一来源不可见即不返回该页面。`ProjectionJoin` 在 `wiki_enabled=false` 时为 `EVIDENCE_READY`，最新运行就绪且来源哈希一致时为 `KNOWLEDGE_READY`，运行未完成、失败、来源过期或 ACL 不一致时为 `KNOWLEDGE_PARTIAL`；Wiki 失败不影响 Evidence RAG。

当前边界：Wiki 生成复用平台 Chat 适配器（`CHAT_SOURCE` / `CHAT_MODEL`），本地 `local-extractive` 只用于确定性的离线验收；不提供页面编辑器、自动发布、Wiki 驱动的查询扩展或页面链接图；关系投影在 M7 提供，不在 M6 中实现。

## M7 关系投影与有界图查询

M7 验收命令：

```bash
./scripts/m7_acceptance.sh
make m7-eval        # 图谱查询 vs 纯 Evidence RAG 的对照评估
```

M7 在同一个 `IndexingStrategy` 上使用 `graph_enabled`（默认 false）。开启开关或调用手动重建时，服务端为当前全部 `EvidencePackage` 创建关系投影运行，复用 `projection` 队列（Asynq 任务 `knowledge.projection.relation.v1`）和 `COLLECT` → `EXTRACT` → `FINALIZE` 三个阶段；任一阶段失败只影响关系投影，不会重跑 Evidence Pipeline，也不影响 Wiki。抽取复用平台 Chat 适配器（`CHAT_SOURCE` / `CHAT_MODEL`），输出 `RelationProjectionPackageV1`，并始终合并确定性 `[[subject|predicate|object]]` 三元组，使离线验收可复现；模型给出但无法回指来源 Chunk 的关系会被丢弃。

首期 Relation Store 使用 PostgreSQL 表：`graph_snapshots`、`graph_entities`、`graph_entity_aliases`、`graph_relations`、`graph_relation_sources`。实体按规范化规则合并，所有规范化与合并规则记录在别名行（`declared_alias` / `normalized_merge`），可以追溯到来源键。每条关系必须至少有一个来源 Chunk，且来源必须记录文档、文档版本、Chunk、标题路径和页码；FINALIZE 会拒绝任何无来源关系（`terminal:untraceable_relation`），全部关系无来源时运行以 `terminal:unsourced_relations` 结束，不会生成可用快照。重复抽取幂等：相同来源哈希得到相同实体、关系和来源集合；来源删除或文档版本替换后旧关系随快照失效，重建只引用当前版本。

图谱以快照为发布单位：抽取结果先写为 `PENDING_REVIEW`，审核通过后才成为唯一的 `ACTIVE` 快照，并在同一事务内把旧 `ACTIVE` 快照置为 `SUPERSEDED`；审核会重新校验每条关系的来源，ACL 版本不一致或来源过期时返回冲突。ACL 变更、成员变更或来源哈希变化会把快照置为 `STALE`。只有 `ACTIVE` 且 ACL 与知识库当前版本一致的快照可查询。

接口：

- `GET /api/v1/knowledge-bases/:id/graph/query?q=&depth=&limit=`：以实体名/别名定位种子，深度默认 2（上限 3），节点默认 30（上限 50），超出预算时返回 `truncated: true`。
- `GET /api/v1/knowledge-bases/:id/graph/snapshots`、`GET /api/v1/graph/snapshots/:snapshotId`、`POST /api/v1/graph/snapshots/:snapshotId/review`。
- `POST /api/v1/knowledge-bases/:id/graph/rebuild`、`GET /api/v1/knowledge-bases/:id/graph/runs`、`GET /api/v1/graph/runs/:runId`、`POST /api/v1/graph/runs/:runId/retry`。

查询与快照/运行视图要求知识库读权限；重建和重试要求写权限且拒绝 API Key；快照审核要求 `manage_members` 且拒绝 API Key。图查询以边为可见性单位：只有全部来源 Chunk 仍为当前版本、`EVIDENCE_READY` 且调用者可读的关系参与遍历，节点和路径只能经可见边到达；隐藏节点、隐藏边、共享实体和多跳路径都不会产生侧信道。`ProjectionJoin` 逐能力判定：声明 `wiki` 只检查 Wiki 投影，声明 `relation` / `graph` 只检查关系投影，未声明投影能力的查询不读取投影状态，因此单个投影失败不会阻塞另一投影或 Evidence RAG；图查询是显式能力，不进入默认问答计划。

`make m7-eval` 在本地栈上创建临时知识库，用 `tests/eval/m7_qa_set.json` 逐条比较图谱查询与 Evidence RAG 的命中情况；`make m7-eval-validate` 只校验用例集（fixture 计数、三元组最短跳数、种子解析、答案标记位置），不联系服务栈。V2 用例集为 30 篇文档 / 135 个子块 / 34 个用例（28 个可答案例、20 个多跳、6 个负例，含三条关系链、别名与归一化种子、分叉、近名干扰和 depth-bound 负例）；strict RAG 要求答案短语出现在 `expectedDocument` 的返回块中，marker-anywhere 只作 loose 诊断。报告写入 `tests/eval/m7_eval_report.json`，并给出 `keep-bounded-graph-query` / `simplify` / `inconclusive` 建议。2026-10-27 实跑（远程 chat 抽取连续两轮超时，改用确定性 local-extractive 抽取器）：多跳 graph 100% vs strict RAG 70%（loose 70%），graph-only rescue 6、RAG-only 0，hop exact 100%（20 项），负例 100%（unknown-seed 4/4、depth-bound 2/2），RAG 干扰误报 0/2，均值延迟 graph 20.3ms vs RAG 187.9ms，delta +30%，harness 建议 `keep-bounded-graph-query`。同一用例集在四次独立索引构建上的多跳 strict RAG 为 85%、60%、80%、70%（graph 均为 100%）；报告 limitations 已声明单次索引样本与重建敏感性，发布决策前应重复运行。是否裁剪复杂图查询由产品依据报告决定，脚本不自动改变产品形态。

当前边界：抽取基于 `EvidencePackage`，不引入独立的关系抽取服务或外部图数据库；不提供图可视化、图编辑或图驱动的查询扩展；Agent、OpenFGA 和跨知识库图谱仍属于后续里程碑。

Embedding 维度与模型是索引契约的一部分，切换必须走受管流程，不允许运行中混用两个模型的向量：

```bash
make embed-index     # 按 EMBEDDING_DIMENSION 创建/重建 chunks 的 halfvec(N) HNSW 索引
make embed-rebuild   # 对模型或维度不匹配的活跃 Chunk 全量重新 Embedding
make embed-verify    # 校验无旧维度 Chunk 残留，有残留则非零退出
```

顺序固定为：改配置 → `make embed-index` → `make embed-rebuild` → `make embed-verify` → 再切检索流量并人工确认无旧维度残留。`chunks_vector_idx` 的维度由该命令按配置创建，迁移不再写死维度。端到端验收：

```bash
./scripts/embed_rebuild_acceptance.sh
```

## M8 容量基线

M8 是按证据启动的条件扩展里程碑（OpenSearch/Milvus、Neo4j、Agent 编排、OpenFGA、Kubernetes、MCP/IM/Widget、OCR），`MILESTONES.md` 要求每项候选先有可复现的容量数据或已确认产品需求，外加成本评估、迁移回滚方案和验收测试。本仓库先交付只读取证工具，不启动任何候选扩展：

```bash
make m8-capacity            # 在本地栈上采集容量基线，写入 tests/eval/m8_capacity_report.json
make m8-capacity-validate   # 只校验报告 schema 与内部一致性，不联系服务栈
```

脚本按 `--seed` 生成确定性语料（默认 40 篇 × 10 行，每篇带唯一审计标记和 `[[subject|predicate|object]]` 三元组链），创建临时知识库并测量：上传与入库吞吐（含 `parse`/`normalize`/`chunk`/`embed`/`index` 各阶段耗时与重试次数）、检索 P50/P95 与期望文档命中、关系投影墙钟时间与 depth 1–3 图查询延迟/节点数/截断次数、未知种子负例，以及 PostgreSQL 表体积和行数探针（`--no-storage-probe` 可关闭）。为避免触发平台自身的租户限流（`RATE_LIMIT_PER_MINUTE`，默认 600/分钟），脚本以约 400 次/分钟限速请求、并用文档列表每秒一次轮询就绪状态；限速等待发生在请求之间，不进入单次调用延迟。检索计时前先做一轮暖机，直到全部查询返回非空结果，暖机耗时单独记入 `search.settleMs`（用于避开入库或删除后的索引维护窗口，不计入延迟样本）。报告版本为 `M8CapacityReportV1`，同时写入 7 项候选的证据登记（`startedCandidates` 恒为空）与 `limitations`。

可复现性的前提是本地适配器：与 m6/m7 验收相同，远程 chat/embedding 会引入网络抖动与投影超时，正式基线应在 `.env` 设 `CHAT_SOURCE=local`、`EMBEDDING_SOURCE=local` 并重建 api/worker 后运行，采集完成后恢复原值。脚本结束后 `DELETE` 临时知识库并校验其不可再读；`--keep` 保留现场。报告只作启动门槛的容量证据，不改变任何产品形态，M8 在门槛满足前保持未开始。

关闭环境：

```bash
make dev-down
```

M2 的 `docreader` 提供受限容器中的数字文档解析；支持 OOXML（DOCX/XLSX/PPTX）与 HTML，无新增第三方解析依赖；扫描 PDF 会稳定返回不支持错误，不启用 OCR。
