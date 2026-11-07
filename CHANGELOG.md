# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- 对外快速开始：`scripts/quickstart.sh`、`scripts/quickstart_check.sh`、`quickstart.compose.yml` 与示例语料，使用本地确定性模型和开发身份完成部署、入库、检索与问答自检。
- 公开文档：快速开始、故障排查、架构、API、竞品对比、Demo 剧本、文档站（MkDocs Material + Mermaid）与 GitHub Pages 工作流。
- 开源治理：`SECURITY.md`、`CONTRIBUTING.md`、`CODE_OF_CONDUCT.md`、`SUPPORT.md`、`GOVERNANCE.md`、`ROADMAP.md`、`THIRD_PARTY_NOTICES.md`、`CODEOWNERS`、Issue/PR 模板。
- API 与 Web 服务健康检查，quickstart 依赖按健康状态就绪后再启动。
- Playwright E2E：上传→解析→检索→问答→删除主链路、B4 管理台回归、axe WCAG 2.1 AA、375/768/1280 响应式检查，并接入 CI `e2e` job 与报告产物。

### Changed

- `README.md` 重写为公开产品文档；`DESIGN.md` 与公开文档移除内部里程碑编号和本地路径。

### Fixed

- quickstart 在 Windows Git Bash 下可完成首次初始化：脚本与迁移 SQL 固定为 LF，`embedctl` 改为按容器 PATH 解析，避免 CRLF shebang 与路径转换失败。
- 可访问性：侧栏分隔条补齐 `aria-valuemin/aria-valuemax/aria-valuenow`，浅色主题绿色标签对比度达到 WCAG AA。

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
