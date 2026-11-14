# 发布文案与常见问题回复

所有文案默认不带夸大措辞；发布前按实际版本替换链接与版本号。

## GitHub Release 公告（v0.2.0）

**标题**：v0.2.0 — 检索质量闭环、预置包与可回滚的 Embedding 迁移

**正文**

> 这个版本把“能回答”推进到“能运营、能升级”：
>
> - **检索质量闭环**：回答有帮助/没帮助、引用点击、评估报告导入；指标下降超过阈值会标记回归并告警，高风险检索变更保存前必须有近期评估。
> - **预置包**：内置通用企业包，建库时复制默认值；租户可克隆/导入/编辑自己的包，内置包只读。
> - **Per-KB Embedding 迁移**：变体表 + 回填 + 切换/回滚，升级顺序为 migrate → backfill → docreader → worker → api。
> - **配置变更预检**：保存前展示影响范围，切块/embedding 变更不会被静默保存。
>
> 产物包含 api/docreader/mcp 的 SBOM、web 静态包、`SHA256SUMS` 与 cosign bundle，以及 6 平台 `rag-mcp` 二进制。
> 升级/回滚见 `docs/releases.md`；能力边界见 README。生产使用前请完成备份与许可评估。

## V2EX（分享节点）

**标题**：[分享] 自托管多租户 RAG：证据链、RLS 与可回滚的向量迁移

**正文**

> 最近把内部知识库项目做到了 0.2.0，开源在 GitHub。它不是又一个聊天壳子，主要解决三件事：
>
> 1）答案必须引用本次授权证据，证据不足拒答，权限负向用例进评测；
> 2）租户/知识库 RBAC + 审计 + 可选 PostgreSQL RLS；
> 3）换 Embedding 模型时用变体表做可回滚迁移，升级演练覆盖回填与只切镜像回滚。
>
> 一条命令本地跑通，不需要模型 API Key：`git clone ... && cp .env.example .env && ./scripts/quickstart.sh`。
> 已知边界：没有 OCR、没有 HA/K8s、连接器只有一个，暂不宣称企业 GA。
> 文档和 Release 在仓库里，欢迎拍砖，尤其想听“你们生产会卡在哪”。

## 掘金文章摘要

> 大多数 RAG Demo 都会在回答下面放引用，但审计要问的是：引用是不是本次授权证据？撤权后还能不能回查？没有证据时会不会编？
> 这篇文章拆解我们把引用注册、越权拦截与拒答做进答案路径的过程，并给出可复现的验收脚本。
> 顺带说明我们为什么没有把“零幻觉”写进 README。

## 知乎（回答/文章引言）

> 如果问题是“企业内部知识库能不能用 RAG”，答案早就不是“能”。真正难的是三件事：
> 答案能不能被审计、权限能不能被验证、模型/索引升级能不能回滚。
> 我们把这三点做成了一个可自托管的开源项目 v0.2.0，文章里只讲机制与失败边界，不堆功能清单。

## Hacker News（Show HN）

**标题**：Show HN: Self-hosted multi-tenant RAG with citation validation and rollback-safe migrations

**正文**

> I built a self-hosted RAG platform for internal knowledge bases. The parts I care about:
>
> - citations are registered and validated against the authorized evidence set; refusal is a product behavior, not an error;
> - tenant/KB RBAC, audit, quotas, optional PostgreSQL RLS (ENABLE + FORCE);
> - per-KB embedding-space migration with a variant table, idempotent backfill, and image-only rollback.
>
> One command runs it locally with deterministic models, no API key. It is early (0.2.0), no OCR, no HA/Kubernetes, one connector. I would especially value feedback on the migration/rollback design and the citation validation path.
>
> Repo/docs in the first comment.

## Reddit（r/selfhosted）

**标题**：Self-hosted multi-tenant RAG with citation checks and a tested upgrade/rollback path

**正文**

> Stack: Go API/worker, PostgreSQL + pgvector/ParadeDB, Redis/Asynq, MinIO, React admin UI, Python parser sidecar. Docker Compose is the only official deployment today.
>
> What is different: answers are grounded in a validated evidence set, permissions are part of retrieval, and embedding migrations have a state machine plus a drill that switches back to the old image.
>
> Not a drop-in replacement for RAGFlow/Dify: no OCR, no agent workflow, no HA. Happy to answer questions about the trade-offs.

## B 站视频简介

> 8 分钟看一个可审计的私有化 RAG：证据链、租户隔离、质量闭环和可回滚的向量迁移。
> 一条命令本地跑通，无需模型 API Key。仓库与文档见评论区。
> 已知边界：暂不支持 OCR、HA/K8s 与多连接器。

## 公众号导语

> 企业知识库的 RAG 不缺“能回答”，缺“能解释、能管住、能升级”。
> 这篇拆解我们把证据链、权限和迁移做进系统的过程，附可复现脚本与失败边界。

---

## 常见问题回复模板

**Q：和 Dify / RAGFlow 有什么区别？**
> 定位不同：它们覆盖更广的解析、Agent 与工作流；我们专注可评审的证据链、租户隔离和可回滚升级。对比只写到文档级差异，没有做同语料基准测试，详见 `docs/comparison.md`。

**Q：支持扫描件/OCR 吗？**
> 暂不支持；扫描 PDF 会返回明确错误。OCR 在路线图中，由真实扫描件语料触发。

**Q：能上生产吗？**
> 可以私有化试点，但不宣称企业 GA。上生产前至少完成备份、OIDC/关闭开发身份、许可评估；HA/K8s 尚未交付。

**Q：换 Embedding 模型要停机吗？**
> 不需要全量停机：变体表保留旧空间，迁移按 build → verify → cutover 执行，回滚是镜像切换。注意 `finalize` 后旧向量被清理，只能靠备份恢复。

**Q：为什么不用 LangChain / LlamaIndex？**
> 核心链路是显式状态机和 SQL，便于审计与回滚；适配层集中在少数端口，不引入额外运行时。

**Q：许可可以商用/OEM 吗？**
> 应用是 MIT；但 ParadeDB、MinIO（AGPL）与 Redis 的许可需要单独评估。商业分发前请先完成许可结论，仓库不提供法律意见。

**Q：数据会被发到外部吗？**
> 默认 quickstart 使用本地确定性模型，不需要外部 API；生产可配置自托管或兼容端点，模型 egress 由部署方控制。仓库没有遥测。

**Q：为什么 quickstart 这么久？**
> 首次构建会下载镜像与依赖，通常 10–20 分钟；后续启动更快。若失败请附 `docker compose ps` 与日志片段提 issue。

**Q：有 SaaS/在线试用吗？**
> 目前没有托管服务；本地 quickstart 是官方体验路径。若你需要托管试用，可以提 issue 说明场景。

**Q：怎么参与？**
> 从 `good first issue`、文档修正、示例语料或 SDK 示例开始；提交前跑 `make test` 与 `make lint`，见 CONTRIBUTING。
