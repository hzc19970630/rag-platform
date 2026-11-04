# 运维手册（M4）

本手册覆盖 M4 引入的企业治理与可运营性能力：迁移兼容检查、备份恢复、优雅关闭、Redis/Worker 故障、Outbox/Inbox 幂等、死信处理、reconciliation、模型网关故障演练和告警指标。

所有命令默认在仓库根目录执行，并使用本地 Compose 栈：

```bash
cd deploy/docker-compose
docker compose --env-file ../../.env -f docker-compose.yml <command>
```

下文用 `compose` 指代 `docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml`。

## 1. 迁移兼容检查

- 迁移版本与校验和记录在 `schema_migrations(version, checksum)`。
- 部署前执行只读检查；`-check` 不修改数据库，发现缺失/漂移的迁移或校验和不一致时以非零退出：

```bash
compose run --rm --no-deps migrate -check
```

- 预期日志包含 `migrations_compatible`。校验和漂移表示已发布的迁移文件被修改，必须先恢复文件内容，不能直接覆盖数据库记录。
- 迁移只允许向前执行；回滚通过恢复备份或编写补偿迁移完成。

## 2. 优雅关闭

- API 与 Worker 都监听 `SIGINT`/`SIGTERM`，停止接收新请求/任务，等待在途工作结束后退出；超时由 `SHUTDOWN_TIMEOUT_SECONDS` 控制（默认 10 秒）。
- API 正常退出日志：`api_shutdown_complete`。Worker 正常退出日志：`worker_shutting_down`。
- Worker 的 Asynq 队列在关闭时先停止拉取新任务，再由 `server.Shutdown()` 等待在途任务；超过超时时间后强制 `Stop()` 并记录 `worker_shutdown_timeout` 警告。
- Compose 停止时给容器的宽限期应大于 `SHUTDOWN_TIMEOUT_SECONDS`，否则在途任务会被 SIGKILL 并在 Inbox 中留下 `PROCESSING` 租约，由 reconciliation 回收。

验证：

```bash
compose restart api
compose logs --no-color api | grep api_shutdown_complete
compose stop worker && compose start worker
compose logs --no-color worker | grep worker_shutting_down
```

## 3. 备份与恢复演练

`scripts/m4_backup_restore.sh` 会：

1. 用 `pg_dump --format=custom --no-owner --no-privileges` 导出当前开发库。
2. 启动一个一次性 `paradedb/paradedb:v0.22.6-pg17` 容器（随机容器名，不映射宿主机端口），等待镜像完成 initdb 的“初始化→重启”两阶段。
3. 过滤掉目标镜像已内置的 `paradedb` schema 与 `pg_search`/`vector` 扩展条目后 `pg_restore --exit-on-error`。
4. 对比全部 27 张 public 表的行数、`schema_migrations` 的版本与校验和、`authorization_revisions`、`kb-dev` 及其 owner 成员关系。
5. 对比 `chunks` 的全部索引定义，确认 `chunks_bm25_idx` 与 `chunks_vector_idx`（`halfvec(1024)` HNSW）随备份恢复。
6. 结束时删除一次性容器和临时文件；源栈只读。

```bash
./scripts/m4_backup_restore.sh
```

生产恢复步骤（人工确认后执行）：

1. 在隔离环境准备与源库同版本的 PostgreSQL + ParadeDB/pgvector；恢复前不要运行 seed。
2. `pg_restore --no-owner --no-privileges --exit-on-error` 导入备份；目标镜像若预装扩展，按脚本方式过滤扩展/schema 条目。
3. 执行 `migrate -check` 确认迁移一致，再启动 API/Worker。
4. 恢复对象存储：`pg_dump` 不包含 MinIO/S3 中的原始文件与 `normalized.json`。生产必须同时备份对象存储桶；缺对象时预览/内容接口返回 409 `content_unavailable`（不回显对象键），重新解析失败，但已生成 Chunk 的检索仍可用。
5. 验证：`readyz`、`/api/v1/ops/metrics`、任选文档的详情/预览/检索，以及 `authorization_revisions` 与备份前一致。
6. 新环境上线后轮换 `JWT_SIGNING_KEYS`、`API_KEY_PEPPER` 和 OIDC client secret；更换 pepper 会使已签发 API Key 全部失效，需重新签发。

## 4. Redis/Worker 故障

### Redis 重启

- 授权缓存的唯一事实源是数据库中的 `authorization_revisions` 与 Casbin 投影；每次 `Authorize`/`BuildScope` 都会 `ensureFresh` 比对数据库 revision，因此 Redis 丢失不会让撤权回退。
- 队列、Asynq 调度和 Pub/Sub 通知属于可丢失状态：在途任务由 Outbox 重新投递；已撤销角色的缓存会在下一次请求按 revision 重建。
- Redis 恢复后 API/Worker 会自动重连，无需重启进程；`/readyz` 的 `redis` 检查恢复为 `ok` 即可。
- 演练：`compose restart redis`，确认 `/readyz` 恢复，重复执行一次“撤销成员→检索 403→重新添加→检索 200”。

### Worker 停止

- API 只写业务表与 `outbox_events`，不直接投递 Asynq；Outbox Publisher 运行在 Worker 中。Worker 停止期间上传照常返回 202，事件保持 `published_at IS NULL`。
- Worker 恢复后按 `FOR UPDATE SKIP LOCKED` 批量拉取未发布事件；`attempts` 与 `next_attempt_at` 支持退避重试。
- 超过 `OUTBOX_MAX_ATTEMPTS` 的事件不再计入 `outboxBacklog`，其对应任务由错误处理或 reconciliation 记录为死信。

## 5. 重复投递、Inbox 与死信

- 每个阶段任务的 `TaskID` 等于 `idempotency_key`（`<pipeline_run_id>:<stage>`），Asynq 会拒绝重复入队；Worker 的 `task_inbox` 以同一键做二次幂等，`SUCCEEDED` 的任务直接跳过。
- 重复发布同一 Outbox 事件不会产生重复 Chunk/Inbox 行，可用以下查询核对：

```sql
SELECT count(*) FROM chunks WHERE document_version_id = '<version>';
SELECT count(*) FROM task_inbox WHERE idempotency_key LIKE '<pipeline>:%';
```

- 任务失败会按 Asynq 重试预算（最多 5 次）重试；预算耗尽后 Worker 的错误处理器写入 `task_dead_letters`（状态 `PENDING`），同时把 Inbox 置为 `FAILED`。以 `terminal:` 开头的错误（如 `unsupported_scanned_pdf`）跳过重试并直接记录终态。
- 运维接口（要求 session 凭据且为 tenant owner/admin）：

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| GET | `/api/v1/ops/tasks/:key` | TaskInspector：查看 inbox、stage、pipeline 与最近错误 |
| GET | `/api/v1/ops/dead-letters?status=` | 列出死信 |
| POST | `/api/v1/ops/dead-letters/:id/retry` | 清理原队列 archived 后重新投递到 `interactive` 队列，Inbox/Stage 复位，写审计 |
| POST | `/api/v1/ops/dead-letters/:id/discard` | 清理原队列 archived 并放弃死信（不重放），写审计 |
| POST | `/api/v1/ops/pipelines/:id/cancel` | 取消流水线，阻止后续阶段 |
| POST | `/api/v1/ops/reconcile?staleAfterSeconds=` | 手动触发 reconciliation |
| GET | `/api/v1/ops/metrics` | 指标快照（见第 7 节） |

- 人工重试/放弃会先删除该任务在 Asynq 原队列中的 archived 记录，再分别重投或关闭死信；成功后 `queues.*.archived` 应回到 0。`RETRYING` 状态的死信不能再次重放，避免并发重复投递。
- 人工重试后若任务成功，死信状态变为 `RETRYING`；下一次 reconciliation 确认 Inbox 已 `SUCCEEDED` 后置为 `RESOLVED`。也可直接调用 reconcile 立即收敛。
- 死信重放必须保持 payload 原样；不要手工修改 `idempotency_key`，否则会绕过 Inbox 幂等。

## 6. Reconciliation

Worker 每个 `RECONCILE_INTERVAL_SECONDS`（默认 60 秒）执行一次，也可通过接口触发：

- 修复 `pipeline_runs`/`document_versions`/`documents` 之间的状态漂移。
- 回收超过 `staleAfterSeconds` 仍处于 `PROCESSING` 的 Stage/Inbox 租约。
- 对 Inbox 已 `SUCCEEDED` 的死信置为 `RESOLVED`。
- 重新入队停滞阶段；若文档已删除、版本已被替换则取消，若 `attempts >= 5` 则标记失败并写死信。
- reconciliation 是幂等的，可在任意时刻重复执行。

```bash
curl -X POST "http://localhost:8082/api/v1/ops/reconcile?staleAfterSeconds=60" \
  -H "Authorization: Bearer <session-token>"
```

## 7. 告警指标

`GET /api/v1/ops/metrics`（tenant owner/admin）返回：

| 字段 | 含义 | 建议告警 |
| --- | --- | --- |
| `counters` / `timings` | 进程内 HTTP 计数与耗时，重启清零 | 5xx 比例、P95 延迟 |
| `queues.{ingestion,interactive,maintenance}.pending/active/retry/archived` | 各 Worker Pool 深度；未创建的队列按 0 返回 | `pending` 持续增长、`archived > 0` |
| `operations.stages` | 24 小时内按阶段/状态的 `stage_runs` 计数 | `parse.failed` 增长 |
| `operations.parseFailureRate` | `parse.failed / (parse.succeeded + parse.failed)`，24 小时窗口 | > 0.2 持续 10 分钟 |
| `operations.modelErrors24h` | 24 小时内失败的 assistant 消息（模型错误） | 任意增长 |
| `operations.deadLetters` | 按 `PENDING`/`RETRYING` 分组的死信数 | 任意 `PENDING` |
| `operations.outboxBacklog` | `published_at IS NULL AND attempts < OUTBOX_MAX_ATTEMPTS` | 持续 > 0 且不下降 |

- `operations` 来自数据库，跨进程重启保留；`counters`/`timings`/`queues` 是进程级或实时 Redis 状态，API 重启后归零。
- Prometheus 抓取走独立监听器：设置 `METRICS_ADDR`（如 `0.0.0.0:9091`；留空则关闭）后，API 进程在该地址只暴露 `GET /metrics`，返回 `text/plain; version=0.0.4`，把进程内 counters 与 timings 转成 `_count`/`_sum`/`_max`。该监听器**没有租户鉴权**，必须只绑内网或回环地址：Compose 默认不发布该端口，跨容器抓取用 `api:9091`，本机抓取再加 `127.0.0.1:9091:9091` 映射，不要暴露到公网。绑定失败只记录 `metrics_listener_stopped`，不会拖垮 API。
- `/api/v1/ops/metrics/prometheus` 仍保留，但它要求 tenant owner/admin session，仅用于人工排查；标准 Prometheus 无法携带该会话 cookie，不要把它作为抓取路径。
- 独立监听器只包含进程内指标，不包含数据库窗口内的 `operations`，抓取端仍需分别告警；Worker 进程的计数器不会出现在该端口。
- 解析失败率的分母只包含成功与失败，不含进行中的阶段；历史事故会在 24 小时后自动退出窗口。
- 检索耗时可通过 `timings["http_request_duration_ms{method=POST,path=/api/v1/search}"]` 观察。`model_errors_total` 只在各自进程内累加：API 的计数器覆盖检索/问答，Worker 的入库 Embedding 计数器不通过 `/ops/metrics` 暴露，入库失败请看 `operations.stages.embed.failed`。

## 8. 模型网关故障演练

目标：确认解析/Embedding 失败可观测、可重试，并且不会静默产生不完整索引。

1. 选择隔离环境（`rag-eval` 或临时 Compose 项目），不要在生产执行。
2. 将 `EMBEDDING_BASE_URL` 指向不可达地址，或只停止 Worker 到模型网关的出口网络。
3. 上传一个小 Markdown 文档；观察阶段停在 `embed`，5 次重试后死信出现，`/ops/metrics` 的 `stages.embed.failed` 与 `deadLetters.PENDING` 增长（`modelErrors24h` 只统计问答生成失败，本演练中应保持 0）。
4. 恢复网关，调用 `/ops/dead-letters/:id/retry`；文档应达到 `EVIDENCE_READY`，死信经 reconcile 变为 `RESOLVED`。
5. 若只断开 Embedding，本地 `EMBEDDING_TIMEOUT_SECONDS`（默认 60 秒）决定单次失败耗时；演练前确认重试总时长在可接受范围。

注意：纯检索不调用生成模型；`modelErrors24h` 只反映问答生成失败。入库 Embedding 失败体现在 `operations.stages.embed.failed`；Worker 进程内的 `model_errors_total{stage="embed"}` 不通过 `/ops/metrics` 暴露。

## 9. 身份与密钥轮换

- **JWT 会话**：`JWT_SIGNING_KEYS=kid1:secret1,kid2:secret2` + `JWT_ACTIVE_KID=kid2`。新会话用 active kid 签发，旧 kid 继续验签；确认旧会话过期后移除旧 kid。只设置 `JWT_SIGNING_KEY` 时使用该单键。
- **API Key**：数据库只保存 `sha256(pepper + raw)`；明文只在创建响应返回一次。轮换 `API_KEY_PEPPER` 会让全部已签发 key 立即失效，需重新签发并通知调用方。
- **OIDC**：`OIDC_ISSUER` 必须指向可发现（discovery）的 issuer；`OIDC_AUTO_PROVISION=false` 时只允许已存在的 membership 登录。登出会撤销 `auth_sessions` 行并清除 HttpOnly cookie；刷新沿用同一 `token_id`，不延长会话 TTL。
- **撤销**：删除知识库成员会同时提升 `knowledge_bases.acl_version` 与 `authorization_revisions`，后续检索按新 revision 重建 scope；不需要手工清 Redis。

## 10. 文档生命周期与保留

- 删除为软删除：`documents.deleted_at/deleted_by/purge_after` 写入，Chunk 与证据包保留以便恢复；检索、列表、详情和预览全部过滤已删除文档。
- `DOCUMENT_RETENTION_DAYS`（默认 30）决定 `purge_after`。到期后 Worker 维护循环硬删除版本、Chunk、证据包、流水线、Outbox/Inbox 行、死信和对象存储文件。
- 恢复（`POST /documents/:id/restore`）清空删除标记并按最新版本是否完整恢复为 `EVIDENCE_READY` 或 `STORED`；超过保留窗口返回冲突。
- 硬清理（`POST /documents/:id/purge`）只允许针对已软删除文档，立即删除全部派生数据与 S3 对象，不可恢复。
- 版本回滚（`POST /documents/:id/versions/:versionID/activate`）会复用被替换版本的 STALE 证据包与 Chunk，不重新 Embedding；前一版本标记 `CANCELLED`。
