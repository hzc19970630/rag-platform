# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- 知识库详情右侧内嵌问答聊天框：历史会话 Popover、新建会话留在本页，窄屏自动改为抽屉；`/chat` 恢复左侧按知识库分组的会话列表，分组可折叠、悬浮可新建会话、会话不显示时间。
- 文件夹树以知识库名称为根节点，顶层文件夹挂在根下；新建文件夹改为列表内联输入（输入框 + ✓/✗、Enter/Esc），支持在悬浮文件夹下新建子文件夹；移动文档/文件夹的目录选择改为同一套树形下拉。
- 审计动作与资源枚举补齐模型供应商、成员变更、整理重试等取值，审计筛选与表格展示中英文案。

### Changed

- 知识库详情工作区：文件夹栏与文档列表等高，成员与知识整理页签占满可视区域并在卡片内滚动。

### Fixed

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
- Playwright E2E：上传→解析→检索→问答→删除主链路、管理台、可访问性与响应式检查，新增 D3 迁移入口、B14 质量反馈与 B23 预置包回归；接入 CI `e2e` job 与报告产物。

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

- 租户管理台（B4）：租户概览、配额覆盖编辑/恢复、成员角色管理、审计筛选与 CSV 导出。
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
