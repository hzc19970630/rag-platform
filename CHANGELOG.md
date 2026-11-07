# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
