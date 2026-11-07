# 安全策略

完整策略见仓库根目录的 [SECURITY.md](https://github.com/hzc19970630/rag-platform/blob/main/SECURITY.md)。

## 报告漏洞

不要为疑似漏洞创建公开 issue。使用 GitHub 仓库 **Security → Report a vulnerability** 的私有报告流程，并附上受影响版本、复现步骤、影响和建议缓解措施。不要在报告中发送生产凭据、客户数据或 API Key。

## 支持版本

| 版本 | 支持 |
| --- | --- |
| 0.1.x | 是 |
| < 0.1 | 否 |

## 加固清单

- 设置 `APP_ENV=production`，关闭开发身份并配置 OIDC。
- 替换所有 `change-me-*` / `replace-*` 占位值，使用受管密钥。
- PostgreSQL、Redis、MinIO 与指标监听器只放在私网。
- 入口启用 TLS，设置 `AUTH_COOKIE_SECURE=true`。
- 评估 `TENANT_RLS_MODE=enforce`。
- 启用数据库备份/PITR 与对象存储版本化。
- 按 digest 固定发布镜像，并校验签名与 SBOM。

## 供应链

发布镜像经过 Trivy 扫描，附带 CycloneDX SBOM，并用 cosign 无密钥签名。校验命令见[发布与回滚](releases.md)。
