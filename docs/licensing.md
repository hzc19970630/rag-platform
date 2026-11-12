# 许可证盘点（非法务意见）

本文件是**依赖盘点**，用于法务审查和商业分发清单，不是法务结论。正式分发前仍需要法务书面结论，或由产品/架构负责人明确记录“自用豁免”的范围和有效期。

盘点范围：随交付物分发的容器镜像、编译产物、Python 服务与前端静态资源。版本以仓库当前锁定值为准；升级镜像或依赖后必须重新盘点。

**盘点日期：2026-11-12。** 运行版本已在阿里云 ECS（Alibaba Cloud Linux 3，8G/40G）核对：PostgreSQL 17.9、Redis 7.4.12、MinIO `RELEASE.2026-08-04T00-00-00Z`；Keycloak 以镜像 tag `26.0` 为准。

## 1. 高关注组件

| 组件 | 当前版本/引用 | 许可证 | 分发/使用风险 | 可选方案 |
| --- | --- | --- | --- | --- |
| ParadeDB | `paradedb/paradedb:v0.22.6-pg17@sha256:58bf87d2a6f1e72f56590b0f0ae1467e82c3f99ca203dde646672c8ac13148b2`（镜像内 PostgreSQL 17.9） | AGPL-3.0 | 作为数据库镜像随私有化交付；AGPL 的网络使用与源码提供义务需法务判断，修改镜像或扩展时更明显 | ① 向 ParadeDB 购买商业许可；② 内部自用并完成 AGPL 合规评审；③ 改用原生 PostgreSQL + 独立 BM25（属于存储迁移，不在本次范围） |
| MinIO | `pgsty/minio:RELEASE.2026-08-04T00-00-00Z@sha256:b6bfe7239bfc83fb90d31612d9704d86039dd714f7904b3f1ad68f211e602372`（上游 MinIO 的第三方构建） | AGPL-3.0（以镜像内 LICENSE/上游为准） | 对象存储随交付物部署，AGPL 义务同样需判断；第三方构建还需核对镜像 SBOM/来源 | ① 商业许可；② 合规评审；③ 替换为 Apache-2.0/BSD 的 S3 兼容实现（如 SeaweedFS/Ceph，需适配与迁移验证）；④ 改用阿里云 OSS 等托管对象存储（按云厂商商业条款） |
| Redis | `redis:7-alpine@sha256:c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b`；运行版本 **7.4.12** | Redis 7.4+ 为 RSALv2/SSPLv1；更早 7.x 为 BSD-3-Clause | 当前 7.4.12 属于 RSALv2/SSPLv1；若对外提供托管服务，SSPL 有额外义务 | ① 固定到 BSD 许可的 7.2.x；② 改用 Valkey（BSD-3-Clause，协议兼容）；③ 商业许可 |
| Keycloak | `keycloak/keycloak:26.0@sha256:09a381c715ab0b111835b70f2905955274843a219c6f27efb348e4d9f4086858` | Apache-2.0 | 低 | 保留 |
| PostgreSQL | ParadeDB 镜像内置 PostgreSQL 17.9 | PostgreSQL License（宽松） | 低 | 保留 |
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

## 3. 运行版本与可追溯证据（2026-11-12）

在阿里云 ECS 上核对到的实际版本：

- PostgreSQL 17.9（`postgres --version`）
- Redis 7.4.12（`redis-server --version`）
- MinIO `RELEASE.2026-08-04T00-00-00Z`
- Keycloak 以镜像 tag `26.0` 为准（未单独读取运行版本）

可追溯证据：

- 镜像 digest：见第 1 节；compose 文件中的 `image:` 均固定到 digest。
- SBOM：每次 GitHub Release 生成 `api`/`docreader` 的 CycloneDX SBOM；`THIRD_PARTY_NOTICES.md` 记录权威清单与生成方式。
- 项目自研代码：MIT（`LICENSE`）；第三方组件保留各自许可证，MIT 不改变其义务。

## 4. 自用豁免记录（模板，需 owner 确认）

适用范围：个人自用，单台阿里云 ECS，仅本人访问；不向第三方分发镜像或二进制，不对外提供商业服务，不修改 AGPL/SSPL 组件。

声明：本人确认上述范围，并接受以下风险：AGPL/SSPL 组件的义务在对外分发、销售、SaaS 或允许第三方使用时可能触发；本记录不是法务意见。范围变化时必须重新评估并取得法务书面结论。

确认状态：已确认（个人自用豁免）
决策人：产品/架构负责人（本人）
日期：2026-11-12
有效期：至适用范围变化为止；对外分发、销售、SaaS 或允许第三方使用时失效

## 5. 商业分发待确认清单

正式商业分发/对外 SaaS 前，至少确认：

1. 交付形态：私有化镜像、二进制安装包，还是托管服务？
2. 是否修改 ParadeDB/MinIO/Redis，或将其作为网络服务提供给第三方？
3. 是否购买 ParadeDB/MinIO/Redis 商业许可，或采用替代组件？
4. AGPL 源码提供方式、SSPL 服务条款、镜像/SBOM 与 NOTICE 的随附方式。
5. 是否需要法务出具书面结论并纳入发布门禁。

## 6. 待决事项

1. 个人自用：由 owner 确认第 4 节豁免记录后，A2 可按“自用豁免”完成。
2. 商业分发：必须由法务书面确认 ParadeDB/MinIO/Redis 三项；本文件不替代。
3. 任何镜像/依赖升级都要更新本表并重新盘点；AGPL/SSPL 组件的修改、分发方式和是否对外提供网络服务都要重新判断。
