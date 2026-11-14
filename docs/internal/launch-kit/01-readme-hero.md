# README 首屏与视觉资产

## 1. 中文首屏（直接替换 README 顶部）

````markdown
# RAG Platform

[![CI](https://github.com/hzc19970630/rag-platform/actions/workflows/ci.yml/badge.svg)](https://github.com/hzc19970630/rag-platform/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/hzc19970630/rag-platform)](https://github.com/hzc19970630/rag-platform/releases/latest)
[![License](https://img.shields.io/github/license/hzc19970630/rag-platform)](LICENSE)
[![Docs](https://img.shields.io/badge/docs-online-blue)](https://hzc19970630.github.io/rag-platform/)

面向企业内部知识库的多租户 RAG 平台。**答案必须带证据，权限和升级都能被审计。**

一条命令本地跑通（无需模型 API Key，使用本地确定性模型）：

```bash
git clone https://github.com/hzc19970630/rag-platform.git
cd rag-platform && cp .env.example .env && ./scripts/quickstart.sh
```

打开 <http://localhost:5173>，创建知识库、上传 `examples/demo/retrieval-handbook.md`，然后提问：
“How does hybrid recall combine keyword and vector results?”。答案会带引用，证据不足时拒答。

**为什么不是又一个 RAG Demo**

- **证据链是一等公民**：引用注册与校验、越权引用拦截、权限负向用例、无证据拒答都在答案路径里。
- **升级可回滚**：知识库级向量空间迁移（变体表 + 回填 + 切换/回滚），有可复现的 v0.1.0 → v0.2.0 升级演练脚本。
- **治理可评审**：租户/知识库 RBAC、API Key、审计、配额，可选 PostgreSQL RLS（ENABLE + FORCE）。
- **零配置但不锁死**：预置包是数据件；建库复制默认值，租户可克隆/编辑自己的包，内置包只读。
- **可被集成**：stdio MCP（8 个受权工具）与 TypeScript/Python SDK。

**v0.2.0 新增**：检索质量闭环（反馈、引用点击、评估回归与高风险保存门禁）、预置包与租户自定义包、
Per-KB Embedding 迁移与回填、配置变更预检。完整变更见 [CHANGELOG](CHANGELOG.md)。

> 当前版本 [v0.2.0](https://github.com/hzc19970630/rag-platform/releases/tag/v0.2.0)。
> 生产使用前请阅读[能力边界](#能力边界)与[安全策略](SECURITY.md)：暂不支持 OCR、HA/K8s 与多连接器。

[快速开始](docs/quickstart.md) · [在线文档](https://hzc19970630.github.io/rag-platform/) · [Release](https://github.com/hzc19970630/rag-platform/releases/latest) · [竞品对比](docs/comparison.md)
````

> 发布时请把 `v0.2.0` 与 Release 链接替换为最新版本。

## 2. 英文首屏（英文 README 或 README 顶部英文摘要）

````markdown
# RAG Platform

Self-hosted, multi-tenant RAG for enterprise knowledge bases.
**Evidence-grounded answers, auditable permissions, and upgrade paths you can roll back.**

Run locally in one command — no model API key required:

```bash
git clone https://github.com/hzc19970630/rag-platform.git
cd rag-platform && cp .env.example .env && ./scripts/quickstart.sh
```

- **Evidence first**: citation registration/validation, permission-negative tests, and refusal when evidence is insufficient.
- **Safe upgrades**: per-knowledge-base vector-space migration with backfill, cutover, and image-only rollback.
- **Reviewable governance**: tenant/KB RBAC, API keys, audit, quotas, optional PostgreSQL RLS.
- **Zero-config without a fork**: versioned preset packs are data; tenants can clone and edit their own copies.
- **Integrable**: stdio MCP with 8 scoped tools plus TypeScript/Python SDKs.

> Current release: [v0.2.0](https://github.com/hzc19970630/rag-platform/releases/tag/v0.2.0).
> Read the capability boundaries before production: no OCR, no HA/Kubernetes, one connector today.
````

## 3. 首屏 GIF 分镜（24 秒，1280×720，无音频）

| 时间 | 画面 | 操作与字幕 |
| --- | --- | --- |
| 0–2s | 知识库列表 → 新建弹窗 | 字幕：**Select a preset, get sane defaults** |
| 2–5s | 预置包下拉与说明卡 | 选中 `general-enterprise`，展示 top_k/切块说明 |
| 5–9s | 上传两篇 demo 文档 | 状态从 `RECEIVED` 到 `EVIDENCE_READY`，加速播放 |
| 9–14s | 问答流式输出 | 问题：hybrid recall 如何融合；显示引用角标 |
| 14–17s | 点击引用 | 打开证据 Chunk 与文档版本 |
| 17–20s | 有帮助/没帮助 + 引用点击 | 字幕：**Feedback feeds the quality view** |
| 20–24s | `/ops` 检索质量卡片 + `/admin` 预置包表 | 字幕：**Operational, not just a demo**；结尾显示仓库地址 |

录制注意：
- 使用全新数据与中性租户名（不要出现真实客户/个人信息）
- 终端与浏览器字号 ≥ 16px；深色主题与文档站一致
- 上传步骤可 2× 加速，但状态变化要可辨认
- GIF 控制在 6MB 内；同时保留 MP4 供 B 站/YouTube

## 4. 社交预览图（1280×640 PNG）

文案层级（从上到下）：

1. 主标题：**RAG Platform**
2. 副标题：**Evidence-grounded, multi-tenant RAG you can audit and upgrade**
3. 三个短标签：`Citations & refusal` · `Tenant RLS` · `Rollback-safe migration`
4. 底部：`github.com/hzc19970630/rag-platform` + `v0.2.0`

设计约束：深色底、单色强调（与文档站主色一致）、不使用竞品 Logo、不写“企业 GA”。

## 5. README 结构建议（首屏之后）

1. 60 秒架构图（保留现有 mermaid）
2. 快速开始（三条命令 + 首问）
3. 能力表（入库、检索、治理、质量闭环、预置包、MCP）
4. 一次真实升级演练摘要（42 断言、回填 32→0、只切镜像回滚）
5. 能力边界（OCR/HA/连接器/许可）
6. 文档与社区入口

## 6. 口径核对

- “一条命令”指 quickstart 脚本；首次构建通常 10–20 分钟，需在网络与磁盘条件满足时成立。
- “无需 API Key”仅指 quickstart 的本地确定性模型；生产模型仍需配置。
- “可回滚”指 expand-only 迁移且未执行 D3 `finalize`；`finalize` 后只能靠备份恢复。
- 不对外宣称企业 GA；不把自测指标写成第三方结论。
