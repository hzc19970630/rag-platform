# 许可证盘点（非法务意见）

本文件是**依赖盘点**，用于法务审查和商业分发清单，不是法务结论。正式分发前仍需要法务书面结论，或由产品/架构负责人明确记录“自用豁免”的范围和有效期。

盘点范围：随交付物分发的容器镜像、编译产物、Python 服务与前端静态资源。版本以仓库当前锁定值为准；升级镜像或依赖后必须重新盘点。

## 1. 高关注组件

| 组件 | 当前版本/引用 | 许可证 | 分发/使用风险 | 可选方案 |
| --- | --- | --- | --- | --- |
| ParadeDB | `paradedb/paradedb:v0.22.6-pg17`（compose） | AGPL-3.0 | 作为数据库镜像随私有化交付；AGPL 的网络使用与源码提供义务需法务判断，修改镜像或扩展时更明显 | ① 向 ParadeDB 购买商业许可；② 内部自用并完成 AGPL 合规评审；③ 改用原生 PostgreSQL + 独立 BM25（属于存储迁移，不在本次范围） |
| MinIO | `minio/minio`（compose） | AGPL-3.0 | 对象存储随交付物部署，AGPL 义务同样需判断 | ① 商业许可；② 合规评审；③ 替换为 Apache-2.0/BSD 的 S3 兼容实现（需适配与迁移验证） |
| Redis | `redis:7-alpine`（digest 锁定；具体 7.x 版本以镜像为准） | Redis 7.4+ 为 RSALv2/SSPLv1；更早 7.x 为 BSD-3-Clause | 若镜像实际为 7.4+，SSPL 对“作为服务提供”有额外义务；需按实际镜像版本确认 | ① 固定到 BSD 许可的 7.2.x；② 改用 Valkey（BSD-3-Clause，协议兼容）；③ 商业许可 |
| Keycloak | `keycloak/keycloak:26.0` | Apache-2.0 | 低 | 保留 |
| PostgreSQL | ParadeDB 镜像内置 PG17 | PostgreSQL License（宽松） | 低 | 保留 |
| pgvector | 镜像内置 | PostgreSQL License | 低 | 保留 |

## 2. 应用依赖（直接依赖抽样）

| 生态 | 组件 | 许可证 |
| --- | --- | --- |
| Go | Gin、GORM、Asynq、go-redis、minio-go、OpenTelemetry | MIT / MIT / MIT / BSD-2-Clause / Apache-2.0 / Apache-2.0 |
| Go | Casbin | Apache-2.0 |
| Python | grpcio、grpcio-tools、protobuf | Apache-2.0 / Apache-2.0 / BSD-3-Clause |
| Python | pypdf | BSD-3-Clause |
| 前端 | React、Ant Design、TanStack Query、Zustand、i18next、Vite | MIT（Vite 为 MIT） |

完整清单以锁定文件为准：

```bash
# Go 模块（含间接依赖）
go list -m all
# 前端（含传递依赖）
cd web && npm ls --all
# Python 服务
python3 -m pip freeze -r services/docreader/requirements.txt
# 容器镜像 digest
grep -R "image:" deploy/docker-compose/docker-compose.yml Dockerfile services/docreader/Dockerfile
```

## 3. 待决事项

1. ParadeDB、MinIO、Redis 三项的许可证结论必须由法务书面确认；本文件不替代。
2. 若选择“自用上线”，由产品/架构负责人记录豁免范围和有效期；商业分发前必须重新评估。
3. 任何镜像/依赖升级都要更新本表并重新跑一次盘点命令；AGPL/SSPL 组件的修改、分发方式和是否对外提供网络服务都要重新判断。
4. 建议在发布流水线中保留 SBOM（现有 CI 已生成 CycloneDX）与镜像 digest，作为“商业分发清单可追溯”的证据。
