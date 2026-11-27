# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- 内置问答工具层与表格结构化（B40）：常规问答新增只读工具 `search_knowledge`、`list_documents`、`list_tables`、`table_schema`、`table_query`，按知识库 `table_qa_enabled=auto|on|off` 与部署开关 `CHAT_TOOLS_ENABLED` 启用；绑定模型通过 provider 目录 `supportsTools` 声明能力，有界工具轮（`CHAT_TOOL_MAX_ROUNDS`/`CHAT_TOOL_MAX_CALLS`/`CHAT_TOOL_TIMEOUT_SECONDS`）与 16KB 输出上限，`tool_call`/`tool_result` SSE 事件可重放。docx `w:tbl` 与 pptx `a:tbl` 转为表资产与行批次；图片/图表/脚注批注/备注页计入 `skipped_images/skipped_charts/skipped_notes`（迁移 0044），`PARSER_VERSION=docreader-b40-v1`。`table_query` 复用 B39 列式副本与只读 SQL 护栏，表行引用带 asset/sheet/row；无 function-calling 的模型走确定性表格兜底。MCP 新增同源 `list_tables`/`table_schema`/`table_query`；Provider 管理与知识库设置分别增加工具调用开关；迁移 0045 增加 `model_catalog_entries.supports_tools` 与 `knowledge_bases.table_qa_enabled`。新增 `scripts/b40_acceptance.py` 与 `make b40-acceptance`。
- 表格只读 SQL 与审计（B39-P2）：新增 `POST /knowledge-bases/:id/tables/sql`，对单个已物化 sheet 执行单条只读 `SELECT`。tablehouse 在隔离的 DuckDB 连接中注册关系 `t`（列名取 `sqlColumns`，`__row_number` 为源行号），关闭外部文件访问/扩展自动安装，限制内存、线程、行数（默认 100、上限 500）、语句长度与超时；拒绝写操作、多语句、`PRAGMA`、未知列和文件函数，错误以 400 `table_sql_rejected` 返回。请求支持位置参数 `params` 与 `preview`（只校验并统计结果行数，不返回行），响应返回列名/类型、行、总行数、是否截断与耗时；每次查询（含 preview 与被拒）写 `table_query`/`table_asset` 审计并新增 `ingestion_table_sql_query_*` 指标。`GET /knowledge-bases/:id/tables` 增加 `sqlColumns`/`columnTypes`，供生成 SQL 与 `TRY_CAST` 聚合使用。新增 `TABLEHOUSE_SQL_MAX_ROWS`、`TABLEHOUSE_SQL_MAX_LENGTH`、`TABLEHOUSE_SQL_TIMEOUT_SECONDS` 配置。
- 表格列式副本覆盖与重建（B39-P1）：`GET /documents/:id/index-estimate` 与 `GET /knowledge-bases/:id/tables` 返回每个 sheet 的 `materializeStatus/materializeError/materializedRows/byteSize/materializedAt`，`index-estimate` 另汇总 `materializeReady/Partial/Failed/MissingSheets` 与 `materializeRows/materializeBytes`；文档页“索引覆盖”展示列式副本就绪/部分/失败/待重建状态、物化行数与体积，失败时列出原因。新增 `POST /documents/:id/tables/rebuild`：源对象不变时强制新建解析/物化流水线（同一目标版本原地重置，worker 先清空版本前缀），用于失败 sheet 重试与 B39 之前旧资产的补建；进行中的重复请求合并，完成后旧 run 标记 `CANCELLED`。`scripts/b39_acceptance.sh` 增加失败 sheet 重试、旧资产补建与重建不重复写分片的用例。
- 表格列式副本与独立查询服务（B39-P0）：全量表格行不再写入 PostgreSQL `table_rows`，改由新服务 `tablehouse`（Python + pyarrow + duckdb）按 sheet 写入对象存储 Parquet（`tablehouse/tenant=<tid>/kb=<kbid>/doc=<docid>/ver=<verid>/sheet=<sanitized>/part-*.parquet` + `_manifest.json`）。docreader 在 `ParseDocumentBlocks` 中按 sheet 全量扫描并通过新增 `TableRowBatch` 事件流式发送（`DOCREADER_TABLE_ROW_BATCH_ROWS`，默认 5000），worker 转发并按 sheet 记录 `ready/partial/failed`；`POST /knowledge-bases/:id/tables/query` 改由 Parquet 后端执行，不再返回 `table_rows_not_indexed`，B/C 档 sheet 可直接过滤查询。升级前已解析的资产返回 `table_needs_reparse`。迁移 0043 为 `table_assets` 增加 `storage_uri/storage_format/schema_json/materialized_rows/byte_size/materialized_at/materialize_status/materialize_error`；新增 `TABLEHOUSE_URL`、`TABLEHOUSE_S3_PREFIX`、`TABLEHOUSE_MEMORY_MB`、`TABLEHOUSE_QUERY_TIMEOUT_SECONDS`、`TABLEHOUSE_MAX_CONCURRENCY`、`TABLEHOUSE_ROW_GROUP_ROWS` 配置，`DOCREADER_MAX_TABLE_ROWS` 安全阀默认提升到 200 万，`PARSER_VERSION` 升级为 `docreader-b39-v1`。文档 purge 会按 `doc=<id>/` 前缀清理 Parquet。新增 `scripts/b39_acceptance.sh` 与 `make b39-acceptance`。
- 多 sheet 覆盖与三档容量策略（B38）：docreader 为每个 sheet 返回目录/表头/声明行数/实际解析行数/截断状态，解析预算按 sheet 隔离，单个超大 sheet 不再耗尽全局 block 配额；表格行组默认 50 行（可配 10–50）并重复表头。新增 bidi/stream 摘要字段 `sheets`、`storage_mode`，迁移 0042 为 `document_versions` 增加 `storage_mode/parse_status/parse_truncated/parsed_sheets/parsed_rows/keyword_rows/coverage_known`，为 `table_assets` 增加 `storage_mode/parse_status/truncated/total_rows/parsed_rows/header_row`。A 档（默认 ≤20 万行）全量入库，B 档（20 万–500 万行）只入 schema/统计/样本，C 档（>500 万行）仅元数据；B/C 的行查询返回 `table_rows_not_indexed`，文档页展示解析覆盖、语义覆盖、sheet 明细与覆盖缺口警告。120MB/65-sheet 样例现在显示 65/65 sheet 处理（B 档 storage=sampled），不再出现 9/65 或 9/9 误报。新增 `scripts/b38_acceptance.sh` 与 `scripts/generate_xlsx_fixture.py`。
- 大表选择性索引（B36）：表格默认不再整表向量化。按规模自动选择 `deep`（≤2,000 行全量）、`standard`（摘要/列/样本向量 + 原始行仅关键字）或 `lexical`（仅关键字/结构化）；新增 `table_assets`（按 sheet 聚合表头/列/行范围/样本）、`table_rows`（原始单元格）、`chunks.index_role` 与文档覆盖字段。新增 `GET /documents/:id/index-estimate`、`POST /documents/:id/index-mode`、`GET /knowledge-bases/:id/tables`、`POST /knowledge-bases/:id/tables/query`。embedding 增加 token 组批、TPM 配速、429 `Retry-After` 与独立阶段超时，已 embedded chunk 可断点续跑；文档页新增“索引覆盖”卡片。
- 投影批次一致性与增量同步（B35）：新增 `projection_changes` 变更日志与 epoch、`wiki_generations`/`wiki_generation_pages` 整库 generation；证据发布不再逐文件触发 run，改为静默窗口（`PROJECTION_BATCH_QUIET_SECONDS`，默认 60s）/最大等待（`PROJECTION_BATCH_MAX_WAIT_SECONDS`，默认 1800s）后的批次 run。run 记录 `base_epoch/target_epoch`，过期 run 标记 `SUPERSEDED` 且不发布；Wiki 在全部页面审核完成后原子切换 generation（carried forward、拒页 exception、回滚），图谱快照审核要求 epoch 匹配；新增 `POST /knowledge-bases/:id/wiki/generations/:generationId/rollback` 整库回滚。文档删除/ACL 变化立即写 delete/acl 变更、隐藏页面与图谱快照并关闭 wiki/graph 能力；恢复保持 stale 直到重建。迁移 0039 从现有已发布页面建立 generation 0，不改变可见内容。
- 可续传与批量上传（B30）：新增上传会话 API（`POST /knowledge-bases/:id/uploads`、`GET/DELETE /uploads/:id`、`POST /uploads/:id/urls`、`PUT /uploads/:id/parts/:part`、`POST /uploads/:id/complete`）。分片协议在 direct（`S3_PUBLIC_ENDPOINT` 预签名直传）与 proxy（API 流式转发）两种 transport 下一致，均支持断线只补缺失分片；前端上传队列支持多选批量、并发 2、单文件进度/取消/重试，刷新后重选同名文件按会话续传。配额在会话创建时预占、complete 时在注册事务内结算；活跃会话上限（默认租户 20 / 知识库 10）、过期 sweeper 与 `upload_sessions_active`/`upload_sessions_reserved_bytes` 指标已接入 worker；审计新增 `upload_session` 资源。旧单文件上传端点保留给 CLI/MCP 与兼容调用。新增配置 `S3_PUBLIC_ENDPOINT`、`MINIO_API_CORS_ALLOW_ORIGIN`、`UPLOAD_PART_SIZE_MB`、`UPLOAD_PRESIGN_TTL_SECONDS`、`UPLOAD_SESSION_TTL_SECONDS`、`UPLOAD_MAX_ACTIVE_PER_TENANT`、`UPLOAD_MAX_ACTIVE_PER_KB`、`HTTP_READ_TIMEOUT_SECONDS`。
- 大表格与流式解析（B34）：worker→docreader 改为 client-streaming，按 2MiB 分帧发送，docreader 使用 384MiB tmpfs spool（并发 2）并统一 unary/stream 解析源；新增 CSV 格式（UTF-8/GB18030、分隔符嗅探）与 `kind=table` 表格块（首行表头、管道行、行窗口、截断标记），入库切块每个 chunk 重复表头。xlsx 改为 `iterparse` 逐行解析，默认 parser 升级为 `docreader-m2-v3`；新增 `DOCREADER_STREAM_CHUNK_KB`、`DOCREADER_SPOOL_MB`、`DOCREADER_PARSE_TIMEOUT_SECONDS`、`DOCREADER_MAX_ARCHIVE_BYTES`、`DOCREADER_MAX_PART_BYTES`、`DOCREADER_MAX_READ_BYTES`、`DOCREADER_MAX_TABLE_*`、`DOCREADER_TABLE_WINDOW_ROWS` 预算。119MB 样例已可完成解析与切块（embedding 受远程配额限制另行处理）。

### Fixed

- 运行时镜像升级 openssl 到 3.5.9-r2，修复 `CVE-2026-124709`（api 与 mcp 镜像）；Trivy 漏洞库更新后 `security` 的镜像扫描转红。
- MCP 验收缺少表格夹具：`cmd/mcp/e2e_test.go` 的 `list_tables/table_schema/table_query` 断言要求知识库内存在 docx 表格，但 `scripts/b16_acceptance.sh` 只上传 markdown 手册，CI `e2e` 稳定失败；验收脚本现复用 B40 的 docx 夹具并等待两个文档就绪。
- 表格行引用无法落库导致回答流不终止：`message_citations` 的 `chunk_id/document_version_id` 为 NOT NULL 外键，而工具生成的表行引用只有资产/行号。迁移 0046 允许这两列为空并新增 `table_asset_id/sheet/row_start/row_end`；`finishAnswer` 在引用持久化失败时补发 `error` 终态，SSE 不再悬挂；消息重放保留表行位置。
- 工具轮预算耗尽时返回空答案：模型在最后一轮仍发起工具调用会返回空文本并被引用校验判为 `ungrounded_answer`。现于预算耗尽后追加一次不带工具的收尾回答；引用重试会显式列出可用证据序号。
- SSE 回答流被 60s 请求 deadline 提前切断：`routeDeadline` 的豁免模板仍是旧路径 `/api/v1/conversations/sessions/:id/messages`，与注册的 `/api/v1/sessions/:id/messages` 不一致，导致工具多轮或慢供应商的回答在 60.0s 断流（答案本身仍会落库）。注册与豁免现共用同一组路由常量，`deadline_test` 同步真实路径。
- 推理模型拒绝强制工具选择：表格问题首轮下发 `tool_choice=required` 时，DeepSeek thinking mode 等供应商返回 400，现自动降级为 `auto` 重试一次再继续工具轮，不再让整题失败。
- `scripts/b40_acceptance.py` 轮询终态缺 `TIMEOUT`：后端超时终态为 `TIMEOUT`，脚本只认 `TIMED_OUT`，会把已结束的超时回答误报为“未到达终态”；同时新增 `B40_CHAT_MODEL` 用于绑定临时知识库的 chat 模型。
- 大文件上传与解析上限统一：`DOCREADER_MAX_FILE_MB`（默认 150MB）现在同时约束 API 上传、worker 尺寸声明与 docreader 接收/解析；此前 compose 插值默认 100MB，导致 120MB 文件上传约 100MB 后被 parser 拒绝。docreader 仍只接收 `DOCREADER_*` 白名单变量（不加载包含模型/数据库凭据的完整 `.env`），启动栈需带 `--env-file .env`。docreader 启动时校验 `DOCREADER_SPOOL_MB ≥ DOCREADER_MAX_FILE_MB × DOCREADER_SPOOL_CONCURRENCY`，不满足直接启动失败；`/healthz` 暴露 `max_file_mb`/`spool_mb`/`spool_concurrency`。bidi `ParseDocumentBlocks` 在上传中途被拒时会读取服务端摘要并透传 `file_too_large` 等错误码，不再显示裸 EOF；`file_too_large` 归为终态失败并在文档页显示可操作提示。
- B30/B34 风险收口：CSV 改为逐行增量解析（不再整文件读入内存）；docreader 启动校验 spool 可写并在 `/healthz` 暴露 `spool_ok`；unary 恢复 128MiB 消息上限，流式帧保持 2MiB；并发解析等待 spool 槽位而不是立即拒绝；表格截断在文档页显示警告。新增 embedding chunk 预警/硬上限（`EMBEDDING_CHUNK_WARNING_THRESHOLD`、`EMBEDDING_MAX_CHUNKS_PER_DOCUMENT`）、`UPLOAD_SESSION_RETENTION_DAYS` 会话回收、`scripts/reparse_kb.sh` 批量重解析工具，以及 parse/truncated/rows/spool/embedding 指标。worker 改用 bidi `ParseDocumentBlocks`，blocks 按 `DOCREADER_BLOCK_BATCH`（默认 200）分批返回，解除大表格单条 gRPC 响应上限。
- 图谱快照审核通过时补写 `activated_at`，并在审核响应中返回；此前 ACTIVE 快照的激活时间为空。
- 图谱快照写库改为分批（实体/别名/关系/来源各 500 行/批），避免大知识库触发 PostgreSQL `extended protocol limited to 65535 parameters`，100 文档 / 10k chunk 的合成库可正常产出快照。
- 重建知识页/图谱在已有活跃 run 时返回 `started:0` 并提示“已有构建正在进行”，不再伪装成已排入新任务；运维重试改为 `FAILED → RETRY` 条件更新，并发点击只有一个请求生效，避免重复重置与重投单元。

### Changed

- 工具问答引用兜底：工具轮结束后若模型答案缺少引用或引用了不存在的序号，且这次回答有工具证据（如 `table_query` 行），追加一次不带工具的强制引用重写（最多一次）；重写仍未引用时按原规则拒答，不再把漏标 `[n]` 的正确表格结果直接判为 `ungrounded_answer`。
- 表格行存储：`table_rows` 从 B39-P0 起停止写入与读取（表保留用于回滚），行级过滤/TopN 统一走 tablehouse 列式副本；`parsed_rows` 改为实际扫描/落库行数，向量预算与 B38 分档不变（仍按 sheet 一条摘要）。
- 请求级超时：API handler 现在带 context deadline（`HTTP_REQUEST_TIMEOUT_SECONDS`，默认 60s），依赖卡死时返回 504 `{"error":"timeout"}` 并回滚租户事务；SSE 回答流豁免，上传 5 分钟，预览/下载、审计导出与 `ops/reconcile` 2 分钟。前端 `api()` 同步分组超时（JSON 默认 30s、上传 5 分钟、长操作 2 分钟），超时抛 `request_timeout` 并收起按钮 loading，不再无限等待。
- 前端构建分包：业务页面按路由懒加载，antd 与 rc-* 依赖按子系统拆分为 vendor chunk；入口 chunk 由 1.69 MB 降至 113 kB，最大 chunk 459 kB，消除 Vite 的 >500 kB chunk 告警。
- 检索证据去重：同一文档内重复文本（重复页、复制块）在进入证据集前折叠，仅保留排序最高的一条；新增 `max_per_document` 配额（默认 3，0 关闭）限制单个文档占用的证据位。跨文档相同文本仍保留，以维持多来源引用。
- 关系投影改为工作单元：抽取按证据包批次（`PROJECTION_UNIT_MAX_CHUNKS`，默认 32）拆成持久任务，单元独立计时（`PROJECTION_UNIT_TIMEOUT_SECONDS`，默认 600s）、并发执行、失败只重跑该单元；FINALIZE 合并单元结果不再调用模型。运行记录新增 `unitTotal/unitSucceeded/unitFailed`，图谱面板显示构建进度。单元失败时运行显示 `RETRY_RELATION`，队列重试预算耗尽后才转为 `FAILED_RELATION`，不再在仍有单元运行时提前报“构建失败”。运行操作栏对活跃 run 提供“取消运行”（`POST /graph|wiki/runs/:runId/cancel`），并修复关系图失败 run 不显示“重试”按钮的问题（原判断只匹配 `FAILED`）。关系抽取在生成工作单元前按包内 `content_hash` 去重，并按字符预算组批（`PROJECTION_RELATION_CHARS_PER_CALL`，默认 1500，单次最多 8 个 chunk），避免重复页 PDF 产生数千字符的单次请求而超时；同时修复模型返回证据块序号（`"1"`）时未映射回 chunk ID、导致 `unsourced_relations` 落快照失败的问题。

## [0.3.0] - 2026-11-20

### Added

- 容量触发指标与告警：`sse_tails_active`、`graph_snapshot_entities`/`graph_snapshot_relations`、`db_pool_in_use`/`db_pool_max_open`/`db_pool_wait_count`/`db_pool_wait_seconds`，并新增 4 条 ticket 级告警规则与 promtool 用例。
- 知识库详情右侧内嵌问答聊天框：历史会话 Popover、新建会话留在本页，窄屏自动改为抽屉；`/chat` 恢复左侧按知识库分组的会话列表，分组可折叠、悬浮可新建会话、会话不显示时间。
- 文件夹树以知识库名称为根节点，顶层文件夹挂在根下；新建文件夹改为列表内联输入（输入框 + ✓/✗、Enter/Esc），支持在悬浮文件夹下新建子文件夹；移动文档/文件夹的目录选择改为同一套树形下拉。
- 审计动作与资源枚举补齐模型供应商、成员变更、整理重试等取值，审计筛选与表格展示中英文案。

### Changed

- 资源级授权接缝：新增 `authz.ResourceTarget` 与 `AuthorizeResource`，知识库检查统一经单一入口；移除领域包直接调用通用 `Authorize` 与手写策略路径的通道，并新增 `archtest` 授权静态门禁。
- CI 覆盖率门禁由 warn 改为 enforce；`scripts/coverage_tiers.txt` 按 2026-11-19 实测基线校准（无单测的适配器包归零并注明集成验收覆盖，`httpapi` 69%、`search` 21%、`storage` 20%）。
- 知识库详情工作区：文件夹栏与文档列表等高，成员与知识整理页签占满可视区域并在卡片内滚动。

### Fixed

- 运行时镜像升级 zlib 到 1.4-r0，修复 `ALPINE-CVE-2026-4738`（api 与 mcp 镜像）。
- 构建工具链升级到 Go 1.26.11，修复 `GO-2026-6859`（HTTP/1 MaxBytesReader 头竞争，api 与 mcp 二进制）。
- 文档版本写入栅栏：被替换版本进入 `CANCELLED` 后，进行中的 parse/normalize/chunk/embed/index 不会把版本写回 `CHUNKED`/`EMBEDDED`/`EVIDENCE_READY`，不重建 chunks/证据包也不触发投影；worker 将 superseded 视为已确认并关闭对应 stage/pipeline。
- 知识空间写入并发：文件夹移动/递归删除、批量移动与知识库删除接入 KB 级 PostgreSQL advisory 锁（3s 等待超时返回 409），校验与写入进入同一临界区；SQLite 单测保持 no-op。
- 迟到投递不再在父运行被清理后创建无租户的 `task_inbox` 行：claim 返回 `stale delivery` 并由 worker 直接确认；reconcile 新增无租约 `PROCESSING` 行回收（15 分钟宽限），`/ops/reconcile` 报告新增 `inboxesAbandoned`。
- 知识整理运行表固定列宽并在窄屏横向滚动，避免列挤压。
- 窄屏审计筛选不再出现未翻译的裸枚举（如 `model_provider_deleted`）。

## [0.2.0] - 2026-11-14

### Added

- 检索质量闭环：助手消息有帮助/没帮助、引用点击上报，`/ops/quality` 与运维「检索质量」页展示反馈、最近评估与回归状态；评估报告导入后按 6 项指标与上一份对比，下降 >0.02 标记 `regressed` 并触发告警；`rerank_enabled` 等高风险变更保存前必须有 7 天内的评估。
- 零配置预置包：`EnterprisePresetV1` manifest、内置 `general-enterprise@1.0.0`、`preset validate|import|export|diff` 工具链，建库时选包并复制默认值；租户可克隆/导入/编辑/删除自己的包，内置包只读，租户包按租户隔离。
- Per-KB Embedding 迁移：按知识库绑定模型/维度，向量写入 `(chunk, model)` 变体表并保留 `chunks.embedding` 兼容列；迁移作业支持 build → verify → cutover → rollback → finalize，`embedctl backfill-embeddings` 负责升级回填，1M 行数据面 p95 98.5ms。
- 向量/格式端口化：`vectorindex.Manager` 与 `formats.Registry` 收敛 pgvector 与文档格式能力，为替换实现提供端口边界。
- 模型供应商目录：租户级 provider CRUD、API Key 加密、健康探测/降级/限流，按知识库绑定 chat/rerank/embedding。
- 配置单一事实源与变更预检：schema 驱动服务端校验、OpenAPI 与前端表单；`POST /api/v1/config/preview` 返回 old→new diff、影响计数、阻塞项与动作建议，切块/embedding 变更不会被静默保存。
- LDAP/AD 登录、组到角色映射与离目录撤权；MCP 多知识库连接、自检、配置模板、Key 过期与六平台发布；Prometheus/Alertmanager/Grafana 可观测性包。
- 对外快速开始：`scripts/quickstart.sh`、`scripts/quickstart_check.sh`、`quickstart.compose.yml` 与示例语料，使用本地确定性模型和开发身份完成部署、入库、检索与问答自检。
- 公开文档：快速开始、故障排查、架构、API、竞品对比、Demo 剧本、文档站（MkDocs Material + Mermaid）与 GitHub Pages 工作流。
- 开源治理：`SECURITY.md`、`CONTRIBUTING.md`、`CODE_OF_CONDUCT.md`、`SUPPORT.md`、`GOVERNANCE.md`、`ROADMAP.md`、`THIRD_PARTY_NOTICES.md`、`CODEOWNERS`、Issue/PR 模板。
- API 与 Web 服务健康检查，quickstart 依赖按健康状态就绪后再启动。
- Playwright E2E：上传→解析→检索→问答→删除主链路、管理台、可访问性与响应式检查，新增 embedding 迁移入口、检索质量反馈与预置包回归；接入 CI `e2e` job 与报告产物。

### Changed

- 升级顺序固定为 `migrate → backfill-embeddings → docreader → worker → api`；v0.1.0 → 当前版本的升级/回滚由本地双镜像演练（`scripts/upgrade_drill.sh`）覆盖，报告不提交仓库。
- `README.md` 重写为公开产品文档；`DESIGN.md` 与公开文档移除内部里程碑编号和本地路径。
- 回滚口径修正：`0028`–`0035` 均有 down 文件，但只允许在演练/空库使用；生产回滚优先备份恢复 + 向前修复。

### Fixed

- quickstart 在 Windows Git Bash 下可完成首次初始化：脚本与迁移 SQL 固定为 LF，`embedctl` 改为按容器 PATH 解析，避免 CRLF shebang 与路径转换失败。
- 可访问性：侧栏分隔条补齐 `aria-valuemin/aria-valuemax/aria-valuenow`，浅色主题绿色标签对比度达到 WCAG AA。
- 知识库配置响应补齐 `presetName/presetVersion`，避免预置包绑定在配置页丢失。

## [0.1.0] - 2026-11-07

### Added

- 租户管理台：租户概览、配额覆盖编辑/恢复、成员角色管理、审计筛选与 CSV 导出。
- 租户配额覆盖动态接入文档入库与问答链路，配额变更与审计在同一事务内提交。
- 发布构建注入版本、提交与构建时间：`api --version`、启动日志 `build_info` 和 OCI 镜像标签。
- 发布工作流：tag 触发镜像构建、SBOM 生成、cosign 无密钥签名与 GitHub Release 产物。
- 发布编排覆盖文件，支持按 tag 或 digest 固定 api/docreader 镜像；新增升级、校验与回滚运行手册。

### Changed

- Go 模块路径由占位符 `github.com/example/rag-platform` 迁移为 `github.com/hzc19970630/rag-platform`。
- 管理台导航与页面标题统一为「租户管理」。

### Security

- 审计 CSV 导出对公式注入前缀做转义；发布镜像附带 SBOM 并提供签名校验步骤。
- 升级 `golang.org/x/net` 至 0.62.0，修复 HTML 解析拒绝服务漏洞（CVE-2026-78661），发布镜像 Trivy 门禁通过。
