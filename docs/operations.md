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
- 迁移进程在独立连接上持有 PostgreSQL session 级 advisory lock（`pg_advisory_lock`），多副本同时启动时串行执行；等待期间日志停在 `migration_lock_acquired` 之前，拿到锁后记录该行。进程退出会释放锁，不会留下需要人工清理的锁记录。
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
- Compose 停止时给容器的宽限期应大于 `SHUTDOWN_TIMEOUT_SECONDS`，否则在途任务会被 SIGKILL 并留下 `PROCESSING` 租约，由 reconciliation 在租约过期后回收。

验证：

```bash
compose restart api
compose logs --no-color api | grep api_shutdown_complete
compose stop worker && compose start worker
compose logs --no-color worker | grep worker_shutting_down
```

## 3. 备份与恢复演练

### 3.1 定时逻辑备份

`scripts/backup_postgres.sh` 提供可调度的 `pg_dump` 备份：必须显式设置 `BACKUP_DIR`（仓库外的运维目录），`BACKUP_RETENTION_DAYS` 默认 7 天；脚本会校验 dump 非空并清理过期文件。示例 cron：

```cron
17 2 * * * cd /opt/rag-platform && BACKUP_DIR=/var/backups/rag ./scripts/backup_postgres.sh >>/var/log/rag-backup.log 2>&1
```

逻辑备份只是基础回退路径；生产 RPO 若要求分钟级，必须启用 WAL/PITR（见 3.2），逻辑备份不能替代 PITR。

### 3.2 WAL/PITR 与对象存储

`deploy/postgres/pitr.conf.example` 给出 `wal_level=replica`、`archive_mode=on`、`archive_command` 与 `archive_timeout=60` 的模板。启用前必须把 WAL 归档目录放到独立、可复制、可恢复的存储，并按目标 RPO 调整 `archive_timeout`/WAL 投递频率。对象存储需启用版本化与跨副本复制（MinIO/S3 的 `mc version enable`、`mc replicate add` 或云厂商等价能力），否则 `pg_dump`/PITR 不包含原始文件与 `normalized.json`。

### 3.3 恢复演练与 RTO 记录

`scripts/m13_backup_drill.sh` 包装现有的逻辑 dump/restore 演练，并强制要求显式提供 `RTO_TARGET_SECONDS` 与 `RPO_TARGET_SECONDS`；缺省直接失败，避免用臆造目标值通过验收。运行后把实测 RTO/RPO 写入 `tests/eval/m13_backup_drill_report.json`，超过目标则退出 1。

```bash
RTO_TARGET_SECONDS=900 RPO_TARGET_SECONDS=300 ./scripts/m13_backup_drill.sh
# 只校验输入和计划，不执行恢复：
RTO_TARGET_SECONDS=900 RPO_TARGET_SECONDS=300 ./scripts/m13_backup_drill.sh --dry-run
```

季度演练仍需运维按计划执行并归档报告；本脚本只保证流程可重复、输入明确、结果可核对。

`scripts/m4_backup_restore.sh` 会：

1. 用 `pg_dump --format=custom --no-owner --no-privileges` 导出当前开发库。
2. 启动一个一次性 `paradedb/paradedb:v0.22.6-pg17` 容器（随机容器名，不映射宿主机端口），等待镜像完成 initdb 的“初始化→重启”两阶段。
3. 过滤掉目标镜像已内置的 `paradedb` schema 与 `pg_search`/`vector` 扩展条目后 `pg_restore --exit-on-error`。
4. 对比全部 42 张 public 表的行数、`schema_migrations` 的版本与校验和、`authorization_revisions`、`kb-dev` 及其 owner 成员关系。
5. 对比 `chunks` 的全部索引定义，确认 `chunks_bm25_idx` 与 `chunks_vector_idx*`（`halfvec(1024)` HNSW，当前实现为版本化名 `chunks_vector_idx_<dim>_v<N>`）随备份恢复。
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

- 授权缓存的唯一事实源是数据库中的 `authorization_revisions` 与成员关系表；Casbin 策略是进程内可重建投影，每次 `Authorize`/`BuildScope` 都会 `ensureFresh` 比对数据库 revision，因此 Redis 丢失不会让撤权回退。
- revision 按 tenant 缓存，只有该租户的 revision 变化时才重建策略；`BumpRevision` 只删除本实例的缓存并发布通知，不再同步全量重建。
- 队列、Asynq 调度和 Pub/Sub 通知属于可丢失状态：在途任务由 Outbox 重新投递；已撤销角色的缓存会在下一次请求按 revision 重建。
- Redis 恢复后 API/Worker 会自动重连，无需重启进程；`/readyz` 的 `redis` 检查恢复为 `ok` 即可。
- 演练：`compose restart redis`，确认 `/readyz` 恢复，重复执行一次“撤销成员→检索 403→重新添加→检索 200”。

### Worker 停止

- API 只写业务表与 `outbox_events`，不直接投递 Asynq；Outbox Publisher 运行在 Worker 中。Worker 停止期间上传照常返回 202，事件保持 `published_at IS NULL`。
- Worker 每个 tick 按 `FOR UPDATE SKIP LOCKED` claim 最多 `OUTBOX_BATCH_SIZE` 条事件，写入 lease 后在事务外发布；发布成功批量标记，失败按 `attempts`/`next_attempt_at` 退避。
- 发布是 at-least-once：进程在发布后、标记前崩溃会重投，由 Asynq `TaskID` 与 `task_inbox` 去重。
- 超过 `OUTBOX_MAX_ATTEMPTS` 的事件不再计入 `outboxBacklog`，其对应任务由错误处理或 reconciliation 记录为死信。

## 5. 重复投递、Inbox 与死信

- 每个阶段任务的 `TaskID` 等于 `idempotency_key`（`<pipeline_run_id>:<stage>`），Asynq 会拒绝重复入队；Worker 的 `task_inbox` 以同一键做二次幂等，`SUCCEEDED` 的任务直接跳过。
- Inbox 认领是条件更新：只有空闲（`PENDING`/`FAILED`）或 `lease_until` 已过期的行才能被认领；执行期间 Worker 每 TTL/3 续租 `lease_until`，因此重复投递的第二个 Worker 无法并发执行同一阶段。执行结束或失败后租约立即释放，Asynq 重试可直接再次认领。
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
| POST | `/api/v1/ops/reconcile?staleAfterSeconds=` | 手动触发当前租户的 reconciliation（只修复该租户的文档状态、inbox 租约、死信与阶段） |
| GET | `/api/v1/ops/metrics` | 指标快照（见第 7 节） |

- 表内操作都以调用者租户为边界：`stage_runs` 与 `task_inbox` 没有租户列，通过所属 `pipeline_runs` 或 `projection_runs` 确认归属；无法确认归属的任务按 404 处理，不会返回其他租户的数据。
- 人工重试/放弃会先删除该任务在 Asynq 原队列中的 archived 记录，再分别重投或关闭死信；成功后 `queues.*.archived` 应回到 0。`RETRYING` 状态的死信不能再次重放，避免并发重复投递。
- 人工重试后若任务成功，死信状态变为 `RETRYING`；下一次 reconciliation 确认 Inbox 已 `SUCCEEDED` 后置为 `RESOLVED`。也可直接调用 reconcile 立即收敛。
- 死信重放必须保持 payload 原样；不要手工修改 `idempotency_key`，否则会绕过 Inbox 幂等。

## 6. Reconciliation

Worker 每个 `RECONCILE_INTERVAL_SECONDS`（默认 60 秒）执行一次，也可通过接口触发：

- 修复 `pipeline_runs`/`document_versions`/`documents` 之间的状态漂移。
- 回收超过 `staleAfterSeconds` 仍处于 `PENDING`/`PROCESSING` 的 Stage；Inbox 只回收 `lease_until` 已过期的 `PROCESSING` 行，正在心跳续租的任务不会被误判为失败。
- 对 Inbox 已 `SUCCEEDED` 的死信置为 `RESOLVED`。
- 重新入队停滞阶段；若文档已删除、版本已被替换则取消，若 `attempts >= 5` 则标记失败并写死信。
- reconciliation 是幂等的，可在任意时刻重复执行。
- 通过 HTTP 触发时只处理当前租户的数据；全平台维护循环由持有 leader 锁的单个 Worker 执行，租户 owner/admin 没有全平台入口。

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
- Prometheus 抓取走独立监听器：设置 `METRICS_ADDR`（开发/测试可用 `0.0.0.0:9091`；留空则关闭）后，API 和 worker 各自在该地址只暴露本进程的 `GET /metrics`，返回 `text/plain; version=0.0.4`。`APP_ENV=production` 时只允许 `localhost` 或回环/RFC1918/ULA/link-local 的字面 IP，`:9091`、`0.0.0.0`、公网 IP 和普通主机名都会让启动校验失败。进程内 counters 输出为 `_total`，timings 输出为固定毫秒桶的 histogram（`_bucket{le="1|5|10|25|50|100|250|500|1000|2500|5000|10000|+Inf"}`、`_count`、`_sum`），另外保留 `<name>_max` gauge 供既有面板使用。P95 用 `histogram_quantile(0.95, sum by (le) (rate(<name>_bucket[5m])))` 计算。两个容器使用同一容器内端口不会冲突，抓取端分别配置 target `api:9091` 与 `worker:9091`。该监听器**没有租户鉴权**，必须只绑内网或回环地址：Compose 默认不发布该端口；本机抓取再加 `127.0.0.1:9091:9091` 映射，不要暴露到公网。绑定失败只记录 `metrics_listener_stopped`，不会拖垮 API 或 worker。
- `/api/v1/ops/metrics/prometheus` 仍保留，但它要求 tenant owner/admin session，仅用于人工排查；标准 Prometheus 无法携带该会话 cookie，不要把它作为抓取路径。
- 独立监听器只包含本进程的指标，不包含数据库窗口内的 `operations`；API 与 worker 的 counters/timings 是两套，抓取端需要分别配置 target 与告警。
- 解析失败率的分母只包含成功与失败，不含进行中的阶段；历史事故会在 24 小时后自动退出窗口。
- 检索耗时可通过 `timings["http_request_duration_ms{method=POST,path=/api/v1/search}"]` 观察。`model_errors_total` 只在各自进程内累加：API 的计数器覆盖检索/问答，worker 的入库 Embedding 计数器只在 worker 的 `/metrics` 暴露；`/ops/metrics` 是 API 路由，看不到 worker 进程计数，入库失败仍可看 `operations.stages.embed.failed`。`rerank_degraded_total{reason=resolve|error|breaker_open}` 记录 Rerank 降级次数：`resolve` 为知识库绑定解析失败，`error` 为供应商调用失败，`breaker_open` 为熔断期间跳过调用；降级时检索仍返回关键词/向量融合结果（`rerankApplied=false`），`model_errors_total` 只统计真实的解析/调用错误。

## 8. 模型网关故障演练

目标：确认解析/Embedding 失败可观测、可重试，并且不会静默产生不完整索引。

1. 选择隔离环境（`rag-eval` 或临时 Compose 项目），不要在生产执行。
2. 将 `EMBEDDING_BASE_URL` 指向不可达地址，或只停止 Worker 到模型网关的出口网络。
3. 上传一个小 Markdown 文档；观察阶段停在 `embed`，5 次重试后死信出现，`/ops/metrics` 的 `stages.embed.failed` 与 `deadLetters.PENDING` 增长（`modelErrors24h` 只统计问答生成失败，本演练中应保持 0）。
4. 恢复网关，调用 `/ops/dead-letters/:id/retry`；文档应达到 `EVIDENCE_READY`，死信经 reconcile 变为 `RESOLVED`。
5. 若只断开 Embedding，本地 `EMBEDDING_TIMEOUT_SECONDS`（默认 60 秒）决定单次失败耗时；演练前确认重试总时长在可接受范围。

注意：纯检索不调用生成模型；`modelErrors24h` 只反映问答生成失败。入库 Embedding 失败体现在 `operations.stages.embed.failed`；Worker 进程内的 `model_errors_total{stage="embed"}` 不通过 `/ops/metrics` 暴露。

### 8.1 Chat / Rerank 模型绑定

- 可路由模型来自部署配置：`LLM_CHAT_MODEL`、`CHAT_MODELS`、`LLM_RERANK_MODEL`、`RERANK_MODELS`。每项可写成 `name` 或 `name@version`；版本只用于审计，不参与供应商请求。`CHAT_MODELS`/`RERANK_MODELS` 只在对应 `*_SOURCE=remote` 时额外登记，离线适配器固定一个模型。改配置后需重启 api（目录在启动时构建）。
- 知识库绑定通过 `PATCH /api/v1/knowledge-bases/{id}` 的 `chat_model`、`rerank_model` 字段设置；空串清除覆盖，回到部署默认。绑定写入前会校验模型已登记且能力匹配，未登记返回 400；每次实际变化都写 `audit_logs`（`resource_type=model_binding`，`reason` 含新/旧模型与版本），可用 `GET /api/v1/audit` 按资源排查。
- 运行路径会做两次保护：绑定模型已不在目录中（例如回滚部署）时自动降级到部署默认；绑定供应商健康探测失败时也降级到默认，探测结果缓存 30 秒。默认模型同样不可用时请求失败（问答记录 `model_error`），不会静默切换。
- Chat 与 Embedding 适配器同样受熔断、租户并发舱壁与有界重试保护：连续 5 次失败熔断 30 秒，期间不调用供应商；每租户每能力最多 4 个并发调用；只对 transport/429/5xx 重试，Chat 最多 2 次尝试，Embedding 每批保持 3 次尝试。租户来自请求或任务上下文（`tenantdb.WithTenant`），缺失时使用进程级兜底舱壁。
- Rerank 是唯一允许降级的模型调用：绑定解析失败、供应商调用失败或熔断打开时，检索退回 rerank 前的关键词/向量融合顺序继续问答（`rerankApplied=false`），只损失重排序，不会让整次问答失败。降级原因通过 `rerank_degraded_total{reason=resolve|error|breaker_open}` 与 `rerank_degraded` 日志观察；`model_errors_total` 仍只统计真实的解析/调用错误。适配器连续失败 5 次后熔断 30 秒，期间直接使用融合顺序；冷却后只放行一个探针，成功即恢复。Chat 生成仍按上一条硬失败，不会静默降级。
- 探测地址默认从各适配器的 base URL 推导：chat 与 embedding 用 `<base>/models`，rerank 用 `<base>/health`；可用 `CHAT_HEALTH_URL`、`EMBEDDING_HEALTH_URL`、`RERANK_HEALTH_URL` 覆盖（留空即用默认）。探测是带 Bearer 凭据的 `GET`，单次超时 3 秒；401/403（凭据被拒）和 5xx 判为不健康，其余状态（含 404）只要求网关可达，避免因供应商没有标准健康路径而误降级。
- 当前健康快照可通过租户 owner/admin 的 `GET /api/v1/ops/metrics` 查看（`modelHealth` 数组，含 `model`、`provider`、`version`、`healthy`、`checkedAt`），也可从同一鉴权路由 `GET /api/v1/ops/metrics/prometheus` 抓取 `model_health{model,provider,version}` gauge（1 健康、0 不健康）。`model_health` 目前只在 API 进程的这条路由上暴露，不在 `METRICS_ADDR` 的无鉴权监听器里；需要自动告警时，让抓取端带 owner/admin 会话访问该路由，或对 `modelHealth` 做巡检。
- `TENANT_MODEL_REQUESTS_PER_MINUTE` 限制每租户、每能力（chat/rerank/embedding）每分钟的模型解析次数，超限返回 `quota_exceeded`。它按进程内固定 1 分钟窗口计数，多副本部署时每个副本各自计数；需要全局硬预算时应在上游网关限流。
- Embedding 绑定不受这套运行时切换影响：知识库有文档后仍不能改模型或维度，需走受管重建。
- 迁移 `migrations/postgres/0020_model_registry.sql` 只新增 `chat_model`、`chat_model_version`、`rerank_model`、`rerank_model_version` 四列，使用 `ADD COLUMN IF NOT EXISTS`，可重复执行；回滚时删除这四列即可（不触碰 Embedding 列）。

## 9. 身份与密钥轮换

- **APP_ENV 枚举**：只接受 `development`、`test`、`production`（忽略首尾空白与大小写）；未知值让所有进程在启动校验阶段失败，避免把拼写错误当成非生产环境绕过 production fail-fast。
- **生产密钥 fail-fast（原 M14 A1，已交付）**：`APP_ENV=production` 时启动校验拒绝仓库默认值。JWT 必须显式设置 `JWT_SIGNING_KEYS`，不能回落到 `JWT_SIGNING_KEY` 的开发单键；`API_KEY_PEPPER`、`S3_ACCESS_KEY`/`S3_SECRET_KEY`（含 MinIO 默认值），以及 `DATABASE_URL`、`MAINTENANCE_DATABASE_URL`、`ADMIN_DATABASE_URL` 中的占位密码都会让进程以 `configuration_invalid` 退出。被拒绝的值包括 `.env.example` 的 `replace-with-a-random-*`、`change-me-*`、`rag-local` 和编译内置的 `local-*-change-me`。非生产环境保留这些默认值；生产允许数据库 URL 不带密码（证书/peer 认证），但不接受已知占位密码。
- **JWT 会话**：`JWT_SIGNING_KEYS=kid1:secret1,kid2:secret2` + `JWT_ACTIVE_KID=kid2`。新会话用 active kid 签发，旧 kid 继续验签；确认旧会话过期后移除旧 kid。只设置 `JWT_SIGNING_KEY` 时使用该单键（仅限非生产）。
- **API Key**：数据库只保存 `sha256(pepper + raw)`；明文只在创建响应返回一次。轮换 `API_KEY_PEPPER` 会让全部已签发 key 立即失效，需重新签发并通知调用方。
- **连接器凭据密钥**：新信封固定为 `v2:<kid>:<base64(nonce||sealed)>`，AAD 绑定 `v2:<kid>`。单键模式继续使用 `CONNECTOR_ENCRYPTION_KEY`（kid 固定为 `legacy`）；轮换时改用 `CONNECTOR_ENCRYPTION_KEYS=kid1:key1,kid2:key2` + `CONNECTOR_ENCRYPTION_ACTIVE_KID=kid2`，新写入用 active kid，旧 kid 保留用于解密历史信封。移除某个 kid 会让引用它的连接器立即不可解密（本轮没有批量重加密），所以轮换只加不删；历史的 `v1:` 信封始终用 `legacy` 键解密。设置 `CONNECTOR_ENCRYPTION_KEYS` 时 `CONNECTOR_ENCRYPTION_ACTIVE_KID` 必填，重复/空 kid、非法 key 和缺失的 active kid 都会让启动失败。
- **OIDC**：`OIDC_ISSUER` 必须指向可发现（discovery）的 issuer；`OIDC_AUTO_PROVISION=false` 时只允许已存在的 membership 登录。登出会撤销 `auth_sessions` 行并清除 HttpOnly cookie；刷新沿用同一 `token_id`，不延长会话 TTL。
- **反向代理与客户端 IP**：`HTTP_TRUSTED_PROXIES` 是逗号分隔的代理 IP/CIDR 列表，只有来自这些地址的 `X-Forwarded-For`/`X-Real-IP` 会被用于审计、限流和配额的客户端 IP。留空表示不信任任何代理，审计 IP 取 TCP 对端；`0.0.0.0/0` 与 `::/0` 会在启动校验被拒绝，避免把客户端 IP 重新交给调用方控制。部署在 Nginx/网关之后时，把实际的代理网段填入该变量，不要使用通配网段。
- **CSRF**：会话 cookie 为 `HttpOnly` + `SameSite=Lax`；此外，携带会话 cookie 的写请求（POST/PUT/PATCH/DELETE）要求 `Origin`（缺失时回退 `Referer`）与请求 Host、`WEB_PUBLIC_URL` 或开发工作台源一致，否则返回 403。Bearer/API Key 请求不依赖 cookie，不受该检查影响。
- **撤销**：删除知识库成员会同时提升 `knowledge_bases.acl_version` 与 `authorization_revisions`，后续检索按新 revision 重建 scope；不需要手工清 Redis。
- **租户成员变更**：修改租户角色或移除租户成员时，在同一事务内按 `tenant_id + user_id` 撤销该用户的 `auth_sessions`；用户的下一次请求直接返回 401，不再等待 JWT 自然过期。仅删除知识库成员不会撤销租户会话，知识库权限仍按 revision 即时生效。

## 10. 文档生命周期与保留

- 删除为软删除：`documents.deleted_at/deleted_by/purge_after` 写入，Chunk 与证据包保留以便恢复；检索、列表、详情和预览全部过滤已删除文档。
- `DOCUMENT_RETENTION_DAYS`（默认 30）决定 `purge_after`。到期后 Worker 维护循环硬删除版本、Chunk、证据包、流水线、Outbox/Inbox 行、死信和对象存储文件。
- `AUDIT_RETENTION_DAYS`（默认 180，0 表示永久保留）只作用于 `audit_logs`。Worker 每小时按 `created_at` 批量删除过期行（每批 1000 条，每轮最多 10 批），并由 `audit_logs_created_at_idx` 支撑。
- `STREAM_EVENT_RETENTION_DAYS`（默认 7，0 表示永久保留）只作用于 `stream_events`。它保存的是 SSE 断线续传缓冲，不是问答历史；回答正文、引用和证据集都在 `messages` / `message_citations` / `evidence_set` 中。Worker 用同一套每小时批量清理（每批 1000 条，每轮最多 10 批），索引为 `stream_events_created_at_idx`。超过保留期后，旧请求的 `ReplayEvents` 返回空事件列表，但消息历史仍可正常读取；不建议从 `messages` 反向重建事件。
- `TASK_HISTORY_RETENTION_DAYS`（默认 90，0 表示永久保留）只作用于入库任务历史：状态为 `EVIDENCE_READY`/`FAILED`/`CANCELLED` 且 `updated_at` 超过保留期的 `evidence` 流水线，以及状态为 `RESOLVED`/`DISCARDED` 且 `resolved_at` 超过保留期的死信。每轮每小时清理，每批 200 条、最多 10 批；删除顺序为 `stage_runs` → `outbox_events`（按流水线 ID 与文档版本 ID 两种聚合）→ `task_inbox`（`<pipelineID>:%`）→ `pipeline_runs`，死信按 `id` 删除。索引为 `pipeline_runs_status_updated_idx` 与 `task_dead_letters_status_resolved_idx`（迁移 0022）。`PENDING`/`RETRYING` 死信、未终态流水线以及文档版本/Chunk/证据包永远不会被这条规则删除；投影运行（`projection_runs`）也不受影响。保留期后若旧 Asynq 任务被重新投递，对应 `task_inbox` 键已不存在，worker 会重新校验版本并幂等重跑。
- SSE 回答流在空闲时每 15 秒写入一行 `: ping` 注释帧。EventSource 与 Web 端的 `parseSSEBlock` 都会忽略注释帧，但反向代理会把它当作流量，不再因等待模型输出期间没有数据而断开连接。
- 恢复（`POST /documents/:id/restore`）清空删除标记并按最新版本是否完整恢复为 `EVIDENCE_READY` 或 `STORED`；超过保留窗口返回冲突。
- 硬清理（`POST /documents/:id/purge`）只允许针对已软删除文档，立即删除全部派生数据与 S3 对象，不可恢复。
- 版本回滚（`POST /documents/:id/versions/:versionID/activate`）会复用被替换版本的 STALE 证据包与 Chunk，不重新 Embedding；前一版本标记 `CANCELLED`。

## 11. 数据库连接池

- API、worker、embedctl 共用同一组连接池参数：`DATABASE_MAX_OPEN_CONNS`（默认 20）、`DATABASE_MAX_IDLE_CONNS`（默认 10）、`DATABASE_CONN_MAX_LIFETIME_SECONDS`（默认 1800）。`0` 空闲连接合法，其余非法值会让配置校验失败。
- 规划容量时按 `副本数 × DATABASE_MAX_OPEN_CONNS < PostgreSQL max_connections` 预留余量；迁移、备份和人工 psql 会话也要计入。
- 连接寿命用于在 PostgreSQL 重启或主从切换后回收失效连接；不要设为 0（0 表示永不回收）。

## 12. Tracing（OTel）

- `OTEL_EXPORTER_OTLP_ENDPOINT` 为空时，api/worker 仍生成 trace 并在响应头 `X-Trace-ID` 与 `http_request` 日志的 `trace_id` 中输出，但不导出；设置 OTLP/HTTP 端点（例如 `http://otel-collector:4318`）后按批导出到 collector，重启进程生效。
- `OTEL_SERVICE_NAME` 默认 `rag-platform`，api 与 worker 共用；需要区分进程时可分别设置。
- HTTP 请求会继续上游 W3C `traceparent`；检索、远程模型调用是同一 trace 的子 span，模型 span 记录 `gen_ai.request.model` 与 token 用量。
- 异步 worker 的 span 记录任务 envelope 的 `trace.id`、`pipeline_run.id`、`stage`，但当前尚未把 HTTP span 作为父 span 跨队列串联；排障时用 `trace_id` 关联 API 日志与 worker 日志。
- 导出器故障只影响 tracing，不阻塞请求；collector 不可用时检查进程日志中的 `tracing_bootstrap_failed`（启动期）与 OTLP 导出错误。

## 13. 投影生成缓存

- Pipeline B 的 Wiki 文档页、概览块和关系抽取结果按 EvidencePackage 缓存在 `projection_generation_cache`。主键为 `(tenant_id, knowledge_base_id, projection_type, artifact, package_id)`；`artifact` 区分 `document`、`overview-block`、`extraction`，同一 package 的多个产物不会互相覆盖。
- 缓存键包含 package 内容摘要（chunk 级）、ACL、模型和 Prompt 版本；任一项变化都会 miss 并重算。package 内容未变时，运行日志出现 `projection_generation` 且 `reused` 增加、模型调用数下降。
- 手动 `POST /knowledge-bases/:id/wiki/rebuild` 与 `POST /knowledge-bases/:id/graph/rebuild` 会先清空该知识库对应类型的缓存，保证“重建”是全量重算，不复用旧结果。
- Worker 维护周期调用 `PurgeGenerationCache`，删除 `evidence_packages` 已不存在的缓存行；当前 package 的缓存长期保留，因此很久未改的文档不会因为缓存过期而在下一次变更时被迫全量重算。
- 回滚开关：`PROJECTION_INCREMENTAL_ENABLED=false` 并重启 api/worker，投影恢复为每次全量生成；缓存表可保留或直接 `TRUNCATE`，二者都不影响业务事实。
- 观测：`projection_cache_hits_total{type="wiki|relation"}` 与 `projection_cache_misses_total{type="wiki|relation"}` 记录每次运行的 package 数；`projection_generation` 日志给出同一组计数。

## 14. 向量召回调参与探针

- `vector_ef_search`（默认 40）、`vector_oversample`（默认 1）、`vector_iterative_scan`（默认 true → pgvector `relaxed_order`）是三级可覆盖的检索参数；`0` 的 ef_search 表示使用 pgvector 服务端默认。
- 本地验证：`make vector-recall-probe` 在临时 schema 中构造 100:1 租户语料，输出 `tests/eval/vector_recall_report.json`，不触碰业务表。报告同时记录自然计划与强制 HNSW 计划的返回数、relevant recall、P95 和执行计划节点。
- Enforce 演练：`python3 scripts/vector_recall_probe.py --rls --report tests/eval/vector_recall_report_rls.json` 在临时 `chunks` 表上安装与 0024 相同的严格策略，并以 `rag_app`（`SET LOCAL ROLE`）执行测量；与不带 `--rls` 的基线报告比较计划节点和 P95，要求仍为 HNSW 且 P95 不超过基线 1.5×。
- 2026-11-05 结论（pgvector 0.8.7）：自然计划选择精确过滤扫描时返回 20/20；强制 HNSW 且 iterative scan=off 时只返回 4–6/20，提高 ef_search 或过采样都无法补齐；`relaxed_order` 补到 20/20、recall 1.0、P95 10–32ms。若生产 pgvector 版本低于 0.8，把 `vector_iterative_scan` 设为 false，否则 `SET LOCAL` 会失败并在检索阶段暴露清晰错误。
- 2026-11-05 真实语料只读复核（1463 条 1024 维；小 KB 选择性约 2.6%–6.1%，以 chunk 自身 embedding 查询、精确扫描 Top-20 为基准并排除自身）：自然计划走 HNSW；`ef_search=40`、过采样 1 且关闭迭代扫描仅返回 7.0–8.7/20，过采样 1/4/10 无改善；`relaxed_order` 在 ef_search=40 恢复 20/20，recall 0.982–1.000，P95 1.3–2.2ms。与合成探针结论一致。
- 监控：`vector_recall_underfill_total{ef_search,oversample}` 非零表示过滤后的 ANN 扫描未填满 TopK；同时检查 `vector_recall_underfill` 日志中的 tenant、KB 数和 requested/returned。该指标出现时，优先确认 `vector_iterative_scan=true`，再评估分区或外部向量库。

## 15. 租户 RLS（原 M13 A1，已交付）

- `TENANT_RLS_MODE=off|shadow|enforce`，默认 `shadow`。`off` 不设置 `app.tenant_id`，可用 `./scripts/m13_rls_mode.sh off` 关闭 RLS；`shadow` 使用允许策略并在租户作用域查询中设置该 GUC，写路径触发器据此审计；`enforce` 使用迁移 0024 的严格策略。代码只提供开关与验收，是否切换是部署决策。
- API 在 enforce 下把每个已认证请求放进一个数据库事务：`tenantdb.BeginRequestTx` 开启事务并执行 `SELECT set_config('app.tenant_id', <tenant>, true)`，普通查询与 `tenantdb.InTx` 服务事务都加入该事务（GORM 的显式 `Transaction` 不经过重定向回调，因此服务层必须用 `InTx`）。handler 返回 5xx、panic 或任一嵌套事务失败时整体回滚；事务级 GUC 随连接归还自动清空。worker 在 enforce 下整体使用 `MAINTENANCE_DATABASE_URL`，跨租户任务不依赖 GUC；API 只对认证前的身份查询使用维护连接。
- 迁移 0023 安装 36 张表的 `ENABLE/FORCE ROW LEVEL SECURITY`、允许策略 `tenant_shadow_allow` 与写审计触发器 `rls_shadow_write`，并为 `task_inbox` 回填 `tenant_id`；0026 新增 `answer_jobs` 后总数为 37 张（33 张租户表 + 4 张子表）。无法按 `<runID>:<stage>` 归属的 inbox 行会写入 `rls_shadow_violations`，enforce 前必须清零（`m13-rls-acceptance` 会检查）。
- 角色：`rag_app`（`NOSUPERUSER NOBYPASSRLS`，API 租户流量）与 `rag_maintenance`（`BYPASSRLS`，worker 与跨租户维护）。enforce 下 API/worker 的 `DATABASE_URL` 必须是 `rag_app`，且 `MAINTENANCE_DATABASE_URL` 必填并必须使用不同角色；migrate/seed/embedctl 使用 `ADMIN_DATABASE_URL`（所有者角色，缺省回退 `DATABASE_URL`）。启动时查询 `pg_roles` 校验：API 运行时角色不得是超级用户或 `BYPASSRLS`，worker 维护角色必须具备 `BYPASSRLS`，否则拒绝启动。密码与授权由 `make m13-rls-setup` 设置，需先导出 `RAG_APP_DB_PASSWORD`、`RAG_MAINTENANCE_DB_PASSWORD`；脚本幂等，可对已有库重复执行。全新数据卷由 `deploy/docker-compose/initdb/01-roles.sh` 自动创建角色。
- `APP_ENV=production` 时 `TENANT_RLS_MODE` 必须是 `enforce`；`shadow`/`off` 或未设置都会让启动校验失败（原 M14 A2，已交付）。CI 使用 `APP_ENV=development` + dev auth，但以 `rag_app`/`rag_maintenance` 整栈运行 enforce；`ENFORCE_LAYOUT=1 make m13-rls-acceptance` 会额外断言容器内 `TENANT_RLS_MODE=enforce`、`DATABASE_URL` 角色为 `rag_app`、维护角色不同，以及 `pg_stat_activity` 中同时存在两种连接。production 的 OIDC 登录不在 CI 中模拟，由配置单测覆盖启动拒绝。
- 影子审计范围：触发器只记录写操作。仅当 `current_user = 'rag_app'` 或 GUC 已设置时写入 `rls_shadow_violations`；迁移、种子和工具以所有者身份运行，不会被记录。读路径的租户验证由 `tenantdb` 的应用 guard、静态门禁 `cmd/checktenancy` 与 `tests/postgres/tenantrls_test.go` 承担，**不承诺对生产读流量做数据库级拦截采样**。
- 审计 SQL：

  ```sql
  SELECT table_name, operation, db_user, count(*)
  FROM rls_shadow_violations
  WHERE observed_at > NOW() - INTERVAL '1 hour'
  GROUP BY 1, 2, 3 ORDER BY 4 DESC;

  SELECT * FROM rls_shadow_violations
  WHERE row_tenant_id IS DISTINCT FROM session_tenant_id
  ORDER BY observed_at DESC LIMIT 50;
  ```

- 切换到 enforce（按顺序）：
  1. `make m13-rls-setup` 创建或更新角色与授权。
  2. 以所有者身份应用 0024：`make migrate`。0024 只替换策略、不改表结构，可重复执行。
  3. 设置 `TENANT_RLS_MODE=enforce`、`DATABASE_URL`（`rag_app`）、`MAINTENANCE_DATABASE_URL`（`rag_maintenance`）与 `ADMIN_DATABASE_URL`（所有者）。容器只在创建时读取环境变量，需要重建 api/worker。
  4. 启动后确认没有角色校验错误，运行 `make m13-rls-acceptance` 与 m1–m4；跨租户负向、直连 SQL 绕过探测、向量/BM25 执行计划与 P95 都通过后才保留 enforce。
  5. 回滚：`./scripts/m13_rls_mode.sh shadow` 恢复允许策略，把 `TENANT_RLS_MODE` 改回 `shadow` 并重建 api/worker；`./scripts/m13_rls_mode.sh off` 会同时关闭 RLS。迁移历史仍记录 0024 已应用，如需再次 enforce，手动重放 `migrations/postgres/0024_tenant_rls_strict.sql`（幂等）。
- Enforce 前置检查（缺一不可）：`SELECT current_user, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user` 中 `current_user = rag_app` 且两项均为 false；`task_inbox` 无 `tenant_id IS NULL` 行；`rls_shadow_violations` 无跨租户（`row_tenant_id` 与 `session_tenant_id` 不一致）记录；m1–m4 以 `rag_app` 通过。任何一项不满足都保持 shadow。
- 验收：`make m13-rls-acceptance` 校验 37 张表的 RLS 状态、触发器、角色属性、inbox 归属与影子审计表，接受 `tenant_shadow_allow`（shadow）或 `tenant_isolation`（enforce）两种策略布局，并在临时表上演练严格策略（跨租户插入被拒、GUC 未设置时 fail-closed）。`ENFORCE_LAYOUT=1` 时只接受严格布局，并增加迁移 0024、容器环境、运行角色与 `pg_stat_activity` 断言。`tests/postgres/rls_runtime_test.go`（需 `TEST_POSTGRES_URL` 与 `make m13-rls-setup`）以 `SET LOCAL ROLE rag_app` 在真实 `audit_logs` 表上验证跨租户读写被拒与无 GUC 时 fail-closed。

## 16. 在线 HNSW 索引变更（原 M13 A2，已交付）

- 索引契约：一个索引名只服务一个 `halfvec(N)` 维度。当前实现使用版本化名 `chunks_vector_idx_<dim>_v<N>`，旧的无版本名 `chunks_vector_idx` 仍被识别并可作为服务索引复用。
- `embedctl create-index` 的执行顺序：读取 `pgvector` 版本（要求 ≥ 0.7.0）、应用会话超时、读取仍有可检索 chunk 的维度、清理 `indisvalid=false` 的残留候选、在独立连接上执行 `CREATE INDEX CONCURRENTLY`、校验 `indisvalid` 与 `halfvec(dim)`/`embedding_dimension = dim`、最后 `DROP INDEX CONCURRENTLY` 删除被替换的旧索引。若其他维度仍有可检索 chunk，其索引会保留，错误的 `EMBEDDING_DIMENSION` 不会删除线上索引；`reembed` 完成后重跑 `create-index` 即可清理旧维度索引。会话超时必须在第一次目录读取之前设置，因为 `pg_get_indexdef` 会持有 `chunks` 的表锁；否则一个已运行的 `ACCESS EXCLUSIVE` 事务会在超时生效前把构建挂住。CONCURRENTLY 不能在事务块内运行，因此不要把它塞进迁移；迁移只负责表结构与策略。
- 失败语义：构建失败会删除本次产生的 INVALID 索引并保留原服务索引；重跑是安全的。若旧索引因锁超时未被删除，新索引已经有效，重跑会继续清理多余索引。
- 观测：`make embed-check`（只读）输出 pgvector 版本、每个 chunks 向量索引的名称/维度/是否有效/大小/定义、每个 `embedding_dimension` 的 chunk 数量、数据库与 `chunks` 堆大小，以及 embedctl 容器根文件系统的剩余空间（不是 PostgreSQL 数据卷的剩余空间，容器内无法直接读取后者）。
- 超时：`EMBED_INDEX_LOCK_TIMEOUT_SECONDS`（默认 10）限制等待表锁的时间，覆盖目录读取与 `CREATE INDEX CONCURRENTLY` 的加锁阶段；被阻塞时命令在超时后失败退出，旧索引保持不变。`EMBED_INDEX_STATEMENT_TIMEOUT_SECONDS`（默认 0，不限制）限制整条 `CREATE INDEX`（含并发扫描）的执行时间；取消后会留下 INVALID 索引，由本次失败清理或下次运行清理。`--dry-run` 同样受锁超时保护。大表构建前先用 `df` 确认数据库卷有约 2× 新索引大小的空闲空间，因为新旧索引会短暂并存。
- 确认：`create-index` 是非日常的受管操作。运行时会提示输入 `y`；非交互调用（stdin 立即 EOF，包括 `docker run` 未加 `-i`）会直接失败，必须传 `--confirm` 或设置 `EMBED_INDEX_CONFIRM=1`；`--dry-run` 只输出“将创建/将删除”的计划，不修改目录。
- 验收：`make embed-acceptance` 在 3↔4 维之间切换，断言新索引生效、旧维度索引已删除、reembed/verify 无残留；`tests/postgres/vectorindex_test.go`（需 `TEST_POSTGRES_URL`）在 20 万行合成表上于构建期间持续写入，要求写 P99 ≤ 2× 基线（低于 5ms 的基线按 5ms 计，避免调度噪声误报），并验证查询计划命中新索引。

## 17. 异步答案生成（原 M13 A3，已交付）

- 默认 `ANSWER_ASYNC_ENABLED=true`：`POST /sessions/:id/messages` 只做预检（会话、scope、配额、模型、历史），在同一事务内写入 user message、`RECEIVED/AUTHORIZED/PLANNED` 三条事件和 `answer_jobs` 行（`QUEUED`），然后把 `answer.generate.v1` 投递到 Asynq 交互队列。检索、生成、引用校验与终态事件都在 worker 执行。API 进程不再承载长耗时的生成。
- 事件序列：`answer_jobs.next_sequence` 由 `UPDATE ... RETURNING` 原子分配，API 预检与 worker 共用同一计数器，因此跨进程仍保持 `1..N` 连续，`ReplayEvents` 与 `Last-Event-ID` 续流不会因序列冲突丢事件。
- 幂等：`(tenant_id, user_id, conversation_id, request_id)` 唯一。重复请求命中已有 job 时不会再次入队，也不会重复写 user message 与事件；已存在答案时直接重放终态。Asynq 重复投递由条件 `UPDATE` claim 拦截，只有 lease 持有者执行。
- 重试退避：非终态失败会把 inbox 标为 FAILED 但保留租约到 TTL，队列的下一次投递在租约到期前只能得到 `inbox lease retained until retry backoff` 并继续退避；终态失败才立即释放租约，交给运维重跑。
- 已知限制：`ANSWER_ASYNC_ENABLED=false` 的同步路径仍会在请求 goroutine 内完成检索与生成，慢客户端会长期占用连接池；生产应保持异步路径。预览/下载在写响应头前提交租户请求事务，不把对象流式读取期间的事务钉在连接上。
- 租约：worker claim 时写入 `lease_owner/lease_until`（默认 300 秒，`ANSWER_ASYNC_LEASE_SECONDS`）。worker 维护循环把租约过期仍为 `RUNNING` 的 job 置 `FAILED`（`worker_lost`）并追加终态 `error` + `complete` 事件，SSE 客户端因此不会无限等待。生成超时受请求超时约束（≤ 60 秒），正常结束远早于 lease 过期。
- SSE：`GET /api/v1/requests/:request_id/events` 默认仍是一次性 JSON 回放；加 `?stream=1` 后每 `ANSWER_ASYNC_POLL_INTERVAL_MS`（默认 250ms）轮询 `ReplayEvents`，直到 `complete`/`error` 或 `ANSWER_TAIL_TIMEOUT_SECONDS`（默认 180）。超时返回 `tail_timeout` 错误事件，客户端可以带 `after`/`Last-Event-ID` 再次续流。同一请求可以有多个并发 tail。
- 回滚：设置 `ANSWER_ASYNC_ENABLED=false` 并重建 api（worker 保留新版本也可）。API 回到同步路径，`answer_jobs` 表保留但不再新增行；已在队列中的异步任务仍会被新 worker 正常执行。
- 升级顺序：**先 worker 后 API**。旧 worker 不认识 `answer.generate.v1`，会在 API 开始入队后把任务记为失败。回滚顺序相反：先 API 回同步，再降级 worker。
- 观测：`answer_job_total{result}` 与 `answer_job_duration_ms` 在 worker 记录；`answer_jobs_expired` 日志给出本轮回收数量；`answer_preflight` 日志记录入队结果。排障 SQL：

  ```sql
  SELECT status, count(*) FROM answer_jobs GROUP BY 1;
  SELECT id, tenant_id, status, attempts, lease_until, error_code
  FROM answer_jobs WHERE status = 'RUNNING' AND lease_until < NOW();
  SELECT id, status, attempts, lease_until, error_code
  FROM answer_jobs WHERE request_id = '<id>';
  ```

  再对照该请求在 `stream_events` 中的 `sequence` 是否连续。
- 验收：`make m13-async-acceptance` 在真实栈上验证正常异步答案（终态 `complete`、job `COMPLETED`、事件连续）与 worker 停机演练（任务保留、worker 恢复后仍得到终态或明确错误）。

## 18. CI 门禁与供应链（原 M14 A3/A4，已交付）

- CI 分三个 job：`verify`（gofmt、checkdeps、checktenancy、`go test`、`-race`、vet、`go mod verify`、前端 `npm ci`/test/build、compose config）、`integration`（整栈 enforce + 迁移 + RLS 负向 + m0/m1/m13 验收）、`security`（govulncheck、npm audit、trivy fs/image、SBOM、镜像 pin 校验）。
- `integration` 使用受版本控制的 `deploy/ci/enforce.env`（全部为一次性 runner 的 dummy 凭据），复制为工作区 `.env`，并以 `COMPOSE_PROJECT_NAME=rag-ci` 隔离。启动顺序固定为：依赖服务 → `make migrate` → `make m13-rls-setup` → `make seed` → `make embed-index` → api/worker。API/worker 在 `APP_ENV=development` 下运行 enforce，因为 production 还要求 OIDC；production 的启动拒绝由配置单测覆盖。`m0/m1` 通过 `ACCEPTANCE_ENV_FILE=.env` 选择 enforce 配置，m0 的空库 migrate/seed 还会覆盖 `ADMIN_DATABASE_URL`。
- `ENFORCE_LAYOUT=1 make m13-rls-acceptance` 是 CI 的防伪断言：除严格策略外，还要求 api/worker 容器内 `TENANT_RLS_MODE=enforce`、`DATABASE_URL` 角色为 `rag_app`、维护角色不同，并且 `pg_stat_activity` 中同时存在 `rag_app` 与 `rag_maintenance` 连接。
- 镜像一律使用 `tag@sha256:<多架构 index digest>`：compose 的 ParadeDB/Redis/MinIO/Keycloak、Dockerfile 的 Go/Alpine/Node/Python 基础镜像以及 Makefile 的 `GO_IMAGE`。`scripts/check_image_pins.sh` 在 CI 中拒绝任何未带 digest 的 `image:`/`FROM`/`GO_IMAGE`。升级镜像时先 `docker buildx imagetools inspect <tag>` 取得新 digest，再替换并提交。
- 前端依赖不再使用 `"latest"`，固定为 lockfile 已解析的精确版本；`make web-install` 使用 `npm ci`，CI 与 Dockerfile 同用 `npm ci`。
- 可复现构建：Go 构建统一带 `-trimpath -buildvcs=false -ldflags=-buildid=`；验收方式是同一输入连续两次 `go build ./cmd/api` 的 SHA-256 相同。镜像层一致性由固定 digest、`go.sum` 与 `package-lock.json` 保证，不承诺跨构建机的镜像字节一致。
- SBOM：CI 用 trivy 对 api 镜像生成 CycloneDX artifact。本地可运行 `trivy image --format cyclonedx --output sbom-api.cdx.json <image>`。
- 漏洞门禁：govulncheck、trivy（HIGH/CRITICAL，忽略未修复）与 `npm audit --audit-level=high` 失败即阻断。确实无法立即修复的条目必须写入 `.trivyignore` 或审计说明，包含 CVE、影响、原因与到期日，并在此节登记。
- `.github/workflows/ci.yml` 的推送可能需要 workflow-scope PAT；不要通过修改 git 凭证绕过。
- `main` 分支保护（2026-11-06 启用）：必须通过 PR 合并，0 审批即可在检查通过后自行合并；必需检查为 `verify`、`integration`、`security` 且要求分支保持最新；对话必须解决，禁止强制推送和删除分支；`enforce_admins=true`，仓库 owner 也不能绕过。CI 工作流本身损坏时需临时关闭保护、修复后恢复。
