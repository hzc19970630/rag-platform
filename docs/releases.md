# 发布、升级与回滚

本文件是发布工程的操作入口：版本规则、发布产物、签名与 SBOM 校验、升级步骤和回滚边界。日常运维（备份恢复、优雅关闭、故障演练）见 [operations.md](operations.md)。

## 1. 版本规则

- 版本遵循 [Semantic Versioning](https://semver.org/)：`vMAJOR.MINOR.PATCH`，预发布追加 `-rc.N` 等后缀（如 `v0.2.0-rc.1`）。tag 不使用 build metadata（`+...`），因为 Docker 镜像 tag 不允许 `+`。
- tag 必须打在 `main` 上；`main` 受分支保护，发布内容只能通过 PR 合入。
- 每个版本在 [CHANGELOG.md](https://github.com/hzc19970630/rag-platform/blob/main/CHANGELOG.md) 有对应条目；`Unreleased` 只记录尚未发布的变更。
- 破坏性变更、需要人工迁移的变更和回滚限制必须写进 CHANGELOG 与本文第 6 节。
- 版本、提交和构建时间通过 `-ldflags -X` 注入：`api --version`、API/worker 启动日志 `build_info`、镜像 OCI 标签（`org.opencontainers.image.version/revision/created/source`）三处一致。

## 2. 发布产物

| 产物 | 名称 | 说明 |
| --- | --- | --- |
| API 镜像 | `ghcr.io/hzc19970630/rag-platform/api:<version>` | 多二进制镜像：`api`、`worker`、`migrate`、`seed`、`embedctl` |
| 解析器镜像 | `ghcr.io/hzc19970630/rag-platform/docreader:<version>` | 数字文档解析 sidecar |
| MCP 镜像 | `ghcr.io/hzc19970630/rag-platform/mcp:<version>` | stdio MCP Server，非 root，`docker run -i` 使用 |
| MCP 二进制 | `rag-mcp-<version>-<os>-<arch>[.exe]` | linux/darwin/windows × amd64/arm64，六份产物 |
| 前端静态包 | `rag-platform-web-<version>.tar.gz` | `web/dist`，构建时固定 `VITE_API_BASE_URL=/api/v1` |
| SBOM | `rag-platform-api-<version>.cdx.json`、`rag-platform-docreader-<version>.cdx.json`、`rag-platform-mcp-<version>.cdx.json` | CycloneDX |
| 清单 | `release-manifest.txt` | 版本、完整 commit、构建时间、三个镜像的 tag@digest 与 MCP 二进制 |
| 校验和 | `SHA256SUMS`、`SHA256SUMS.cosign.bundle` | 覆盖清单、前端包、三个 SBOM 与 MCP 二进制；bundle 为 cosign 无密钥签名 |
| 镜像签名 | OCI registry 中的 cosign 签名 | 对 `api`/`docreader`/`mcp` 的 digest 签名 |

镜像同时推送 `sha-<12位提交>` 标签，便于按提交追溯。生产环境应固定 digest，不要依赖浮动标签。GHCR 包首次发布后如仓库公开，需在 GitHub Packages 设置里把三个包改为 public，否则匿名 `docker pull` 会失败。

## 3. 触发发布

1. 把 `CHANGELOG.md` 的 `Unreleased` 内容整理为 `## [X.Y.Z] - YYYY-MM-DD`，保留空的 `Unreleased`。
2. 通过 PR 合入 `main`，等待 `ci` 的 `verify`/`integration`/`security` 全部通过。
3. 在 `main` 的最新提交上创建带注释的 tag 并推送（需要发布权限，属于对共享状态的写操作，须经确认）：

```bash
git checkout main && git pull --ff-only
git tag -a v0.1.0 -m "rag-platform v0.1.0"
git push origin v0.1.0
```

4. tag 推送触发 [release.yml](https://github.com/hzc19970630/rag-platform/blob/main/.github/workflows/release.yml)：
   - `verify`：tag 语义化格式、gofmt、checkdeps、OpenAPI 漂移、租户静态门禁、`go test`、`go vet`、`go mod verify`、前端测试与构建。
   - `publish`：构建三个镜像与六份 MCP 二进制 → Trivy HIGH/CRITICAL 门禁（未修复项忽略）→ 生成 SBOM → 推送 GHCR → cosign keyless 签名 digest → 打包前端 → 校验和签名 → 创建 GitHub Release（预发布 tag 自动标记 prerelease）。
   - 镜像先扫描后推送；扫描失败时不会产生签名，也不会创建 Release。
5. 查看进度并在失败时用同一 tag 重跑工作流：

```bash
gh run list --workflow release.yml
gh run watch
```

6. 建议在仓库设置中为 `v*` 增加 tag protection rule，只允许发布者创建/删除发布 tag。

## 4. 校验产物

从 Release 下载全部产物：

```bash
VERSION=v0.1.0
gh release download "$VERSION"
sha256sum -c SHA256SUMS
```

校验镜像签名（digest 取自 `release-manifest.txt`）：

```bash
IMAGE=ghcr.io/hzc19970630/rag-platform/api
DIGEST=sha256:...   # release-manifest.txt 中 @ 之后的部分

cosign verify \
  --certificate-identity "https://github.com/hzc19970630/rag-platform/.github/workflows/release.yml@refs/tags/${VERSION}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  "${IMAGE}@${DIGEST}"
```

校验 `SHA256SUMS` 的 blob 签名：

```bash
cosign verify-blob \
  --bundle SHA256SUMS.cosign.bundle \
  --certificate-identity "https://github.com/hzc19970630/rag-platform/.github/workflows/release.yml@refs/tags/${VERSION}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  SHA256SUMS
```

检查 SBOM 与镜像版本：

```bash
jq '.components | length' rag-platform-api-"$VERSION".cdx.json
docker run --rm "${IMAGE}@${DIGEST}" --version
docker image inspect --format '{{json .Config.Labels}}' "${IMAGE}@${DIGEST}"
```

`--version` 输出与 `release-manifest.txt` 中的 version/commit/built_at 必须一致。cosign keyless 依赖公开的 Fulcio/Rekor；受限网络中的离线部署需另行配置密钥签名，本仓库默认不覆盖该场景。

## 5. 升级

### 5.1 升级前

1. 阅读目标版本 CHANGELOG 的 Added/Changed/Deprecated/Removed/Security 与本文第 6 节。
2. 记录当前版本与 digest，确认可回退：

```bash
docker image inspect --format '{{index .RepoDigests 0}}' ghcr.io/hzc19970630/rag-platform/api:<当前版本>
```

3. 按 [operations.md](operations.md) 第 3 节完成数据库逻辑备份；要求分钟级 RPO 的环境同时确认 WAL/PITR 与对象存储备份可用。
4. 只读检查迁移兼容性（不会修改数据库），预期日志含 `migrations_compatible`：

```bash
export RAG_VERSION=v0.1.0
BASE_ARGS=(-f deploy/docker-compose/docker-compose.yml -f deploy/docker-compose/release.compose.yml)
docker compose --env-file .env "${BASE_ARGS[@]}" run --rm --no-deps migrate -check
```

### 5.2 升级步骤

`release.compose.yml` 用发布镜像替换本地构建，支持 tag 或 digest 两种固定方式。覆盖文件使用 Compose 的 `!reset` 语法（Docker Compose 2.24+）；基础文件里的 `web` 服务仍是开发服务器，生产前端按上面的静态包方式部署。

```bash
export RAG_VERSION=v0.1.0
# 或者硬固定 digest：
# export RAG_API_IMAGE=ghcr.io/hzc19970630/rag-platform/api@sha256:...
# export RAG_DOCREADER_IMAGE=ghcr.io/hzc19970630/rag-platform/docreader@sha256:...

BASE_ARGS=(-f deploy/docker-compose/docker-compose.yml -f deploy/docker-compose/release.compose.yml)
docker compose --env-file .env "${BASE_ARGS[@]}" pull
docker compose --env-file .env "${BASE_ARGS[@]}" run --rm --no-deps migrate
docker compose --env-file .env "${BASE_ARGS[@]}" up -d --no-build docreader
docker compose --env-file .env "${BASE_ARGS[@]}" up -d --no-build worker api
```

- 顺序固定为 **migrate → docreader → worker → api**：先迁移 schema，再让解析器与新协议一致，最后替换消费者和入口。worker 先于 API 升级，避免旧 worker 收到未知的异步任务类型。
- `seed` 是幂等任务，`api` 的 `depends_on` 会以发布镜像自动执行它；不要手工跳过 migrate 直接启动 api。
- Web 不在镜像里。升级时解压前端包到版本目录，校验通过后原子切换软链接，例如：

```bash
mkdir -p /srv/rag-web/releases/"$RAG_VERSION"
tar -xzf rag-platform-web-"$RAG_VERSION".tar.gz -C /srv/rag-web/releases/"$RAG_VERSION"
ln -sfn /srv/rag-web/releases/"$RAG_VERSION" /srv/rag-web/current
```

Nginx 的 `root` 指向 `/srv/rag-web/current`；旧版本目录保留到回滚窗口结束。反向代理需把 `/api/` 转发到 API，静态资源缺失时回退 `index.html`。

### 5.3 升级后验证

```bash
curl -fsS http://localhost:8082/healthz
curl -fsS http://localhost:8082/readyz
docker compose --env-file .env "${BASE_ARGS[@]}" logs --no-color api | grep build_info
docker compose --env-file .env "${BASE_ARGS[@]}" exec api api --version
```

再按发布检查单验证：登录（OIDC/API Key）、知识库列表、文档上传与解析、检索、问答 SSE、审计页与配额页。异步答案升级后确认 worker 能消费新任务且续传事件完整。

## 6. 回滚

先判断迁移是否向后兼容：

- **兼容（推荐路径）**：新版本只做 expand 型变更（加表/加列/加索引），旧二进制仍可运行。直接重新固定上一个镜像 digest，不恢复数据库。
- **不兼容**：迁移删改列/表或改变语义。必须在维护窗口内停止写入、恢复升级前备份和对象存储，再启动旧版本。**不要**把旧二进制直接指向已迁移的库，也不要把 `migrate -down` 当成通用回滚手段：只有带 `*.down.sql` 的迁移可回退（当前仅 `0027_drop_casbin_rule`），其余迁移只向前。

兼容回滚命令：

```bash
export RAG_VERSION=<上一版本>
# 或 export RAG_API_IMAGE=...@sha256:<上一 digest>
#    export RAG_DOCREADER_IMAGE=...@sha256:<上一 digest>
BASE_ARGS=(-f deploy/docker-compose/docker-compose.yml -f deploy/docker-compose/release.compose.yml)
docker compose --env-file .env "${BASE_ARGS[@]}" up -d --no-build worker api docreader
```

前端回滚把 `current` 软链接指回上一个版本目录即可，无需重新构建。

不兼容回滚：

1. 停止 `api` 与 `worker`，阻止新写入；记录停止时间点。
2. 按 [operations.md](operations.md) 第 3.3 节在隔离库恢复备份，核对 `schema_migrations` 与 `authorization_revisions`；对象存储恢复到同一时点。
3. 把数据库指回恢复后的实例，用上一版本镜像启动 `worker`、`api`、`docreader`。
4. 验证 `readyz`、登录、检索、问答与审计；确认无重复/丢失的 outbox 事件后再恢复流量。
5. 在 CHANGELOG/事故记录中登记回滚原因、耗时和数据损失窗口。

## 7. 本地演练

发布流水线本身只在 tag 推送时运行；本地可验证构建注入、镜像内容和编排解析，不替代生产升级窗口的完整演练：

```bash
# 1) 构建带版本信息的镜像
docker build \
  --build-arg VERSION=v0.0.0-drill.1 \
  --build-arg COMMIT=$(git rev-parse --short=12 HEAD) \
  --build-arg BUILD_DATE=$(git log -1 --format=%cI) \
  -t rag-platform:b9-drill .

# 2) 验证版本注入与 OCI 标签
docker run --rm rag-platform:b9-drill --version
docker image inspect --format '{{json .Config.Labels}}' rag-platform:b9-drill

# 3) 验证发布编排能解析 tag 与 digest 两种固定方式
RAG_VERSION=v0.0.0-drill.1 docker compose --env-file .env.example \
  -f deploy/docker-compose/docker-compose.yml \
  -f deploy/docker-compose/release.compose.yml config --quiet
RAG_API_IMAGE=ghcr.io/hzc19970630/rag-platform/api@sha256:0123456789abcdef \
RAG_DOCREADER_IMAGE=ghcr.io/hzc19970630/rag-platform/docreader@sha256:fedcba9876543210 \
  docker compose --env-file .env.example \
  -f deploy/docker-compose/docker-compose.yml \
  -f deploy/docker-compose/release.compose.yml config --quiet

# 4) 在维护窗口演练：拉取真实 Release，执行第 5.2 节升级，再用第 6 节切回上一 digest
```

Windows 上直接构建的基础镜像会把迁移 SQL 以 CRLF 写入镜像，而发布镜像由 Linux CI 以 LF 构建；用前者初始化的本地库切换到发布镜像时，`migrate -check` 会报 `checksum mismatch`。演练前应让构建上下文保持 LF（例如临时设置 `core.autocrlf=false` 后重新检出），不要通过修改校验和绕过生产库检查。

生产升级/回滚演练必须记录：开始与结束时间、源/目标 digest、`migrate -check` 结果、`readyz` 恢复时间、登录/上传/问答抽查结果、回滚是否恢复备份。演练报告归档到运维记录，不提交到仓库。
