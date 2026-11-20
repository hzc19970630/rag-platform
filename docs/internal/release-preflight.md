# 发布预检清单

本清单是打 tag 之前的可执行门禁，补充 [docs/releases.md](../releases.md)：那份文档说明
tag 之后如何发布、升级与回滚；这份清单说明 tag 之前必须跑完什么、判定标准是什么、失败后停在哪。

清单口径核对自 2026-11-20 的仓库状态（`v0.2.0` 之后 36 个提交）：

- CI：`.github/workflows/ci.yml`（verify / integration / e2e / eval / observability / security）
- 发布：`.github/workflows/release.yml`（tag 后的 verify 比 CI 窄，不能替代 CI）
- 本地门禁：`Makefile`、`scripts/coverage_gate.sh`、`scripts/m3_eval.py`、各 `*_acceptance.sh`
- 发布规则：[docs/releases.md](../releases.md)

工作流或脚本改动后，先更新本清单，再执行。

## 0. 执行约定

1. 按编号顺序执行，一次一项；不要为了赶时间并行跑重负载门禁（Playwright、`tsc`、
   `vite build`、Go 全量测试同时跑会互相拖垮，jsdom 用例会成片超时）。
2. 每个门禁都记录真实的退出码：`<cmd> > log 2>&1; echo "EXIT=$?"`。
   不要用 `cmd | tail` 的退出码当结论，`tail` 会盖掉真实状态。
3. 任一步不满足就停止本轮，只报观测值（退出码、失败用例、指标）与停止位置；
   不放宽阈值、不补跑到好看为止、不在失败路径上改文件。
4. 需要复跑时先说明原因，并同时保留失败与复跑两份结果。owner 明确同意才继续。
5. push、tag、发布 GitHub Release 都是共享状态写操作，执行前逐次确认。
6. 证据统一写到仓库外，报告类文件不入库（`tests/eval/*.json` 已在 gitignore 的部分不要提交）。

```bash
export RELEASE_VERSION=v0.3.0          # 按 SemVer 决定；0.x 新功能走 minor
export LAST_TAG="$(git describe --tags --abbrev=0)"   # 预期 v0.2.0
mkdir -p /tmp/release-preflight

run() {
  local name="$1"; shift
  local rc=0
  "$@" >"/tmp/release-preflight/${name}.log" 2>&1 || rc=$?
  printf '%s EXIT=%s\n' "$name" "$rc" | tee -a /tmp/release-preflight/summary.txt
  return $rc
}
```

失败时先看 `/tmp/release-preflight/<name>.log` 尾部与 `summary.txt`。

## 1. 前置条件

| 项 | 要求 | 检查 |
| --- | --- | --- |
| Docker / Compose | Docker Desktop 或 Engine + Compose v2 | `docker compose version` |
| Go | 不需要宿主机 Go；所有 Go 门禁走 Docker | `make test` 可用即可 |
| Node | 24.x（CI 口径） | `node --version` |
| Python | 3.12（CI 口径）；本机若为 3.9，先跑 `make eval-validate` 确认脚本可导入 | `python3 --version` |
| 端口 | 8082 / 5173 / 5432 / 18082 等空闲；同一时间只起一套栈 | `docker ps` |
| 磁盘 | 预留给镜像构建与升级演练，建议 ≥ 15 GB | `docker system df` |
| `.env` | 先备份；预检结束后逐字节恢复并验证 | `cp -p .env /tmp/release-preflight/env.backup && shasum .env` |

已知本地限制：

- 共享 Compose 数据库可能因 collation 不一致无法 `CREATE DATABASE`（`m0` 会用到）。
  优先用隔离项目/新卷；不要为了通过预检 `down -v` 掉开发数据。
- 没有宿主机 `go`/`gofmt` 是正常的，Makefile 用固定 digest 的 `golang` 容器。
- `make embed-acceptance` 禁止在开发栈上跑：脚本硬编码 3 维基线，会重绑所有知识库并重建
  1024 维索引。需要在一次性 Postgres/ParadeDB 容器里验证，并单独记录豁免。

## 2. 冻结与版本

- [ ] 工作区干净，且与远端一致（或只领先即将发布的提交）。

```bash
git status --porcelain
git fetch origin --prune
git rev-list --left-right --count origin/main...HEAD
```

通过：`git status` 无输出；`rev-list` 输出 `0 0`，或右侧为非零且确认这些提交就是本次发布内容。

- [ ] 复核自上个发布以来的全部提交，确认没有漏进 CHANGELOG 的用户可见变更。

```bash
git log --oneline "$LAST_TAG"..HEAD
git diff --stat "$LAST_TAG"..HEAD
```

- [ ] 版本号符合 SemVer：新增能力走 minor；只有修复走 patch；预发布用 `-rc.N`；
      不使用 build metadata（`+...`）。本地先过一遍 tag 格式门禁，并确认 tag 不存在：

```bash
[[ "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] \
  && echo "tag format ok" || echo "tag format invalid"
git rev-parse -q --verify "refs/tags/$RELEASE_VERSION" && echo "tag already exists" || echo "tag available"
```

- [ ] `CHANGELOG.md` 把 `Unreleased` 整理为 `## [X.Y.Z] - YYYY-MM-DD`，并保留空的
      `Unreleased`。破坏性变更、需要人工迁移的变更、回滚限制同步写进
      [docs/releases.md](../releases.md) 第 6 节。
- [ ] 从 `v0.2.0` 起只有 `0036_document_folders_tags` 一个迁移；确认 CHANGELOG 与升级说明
      写明它是 expand 型变更，回退可先切回旧镜像 digest。

```bash
git diff --name-only "$LAST_TAG"..HEAD -- migrations | sort
```

- [ ] 空白与冲突标记检查。

```bash
git diff --check
```

失败即停，版本或变更集未定不要进入下一节。

## 3. 静态、单元与构建

这一节覆盖 CI `verify` 的本地等价项，以及 `observability`/`security` 中可本地复现的部分；
全部通过才能进入下一节。

- [ ] Go 格式、依赖方向、OpenAPI 漂移、租户静态门禁、全量单测、`go vet`，以及 Web 单测。

```bash
run P3-make-test make test
```

注意：`make test` 的第一步是 `gofmt` 检查。日志为空且退出码为 1，通常表示有文件未格式化；
把链式步骤拆开单独跑（先 `gofmt -l cmd internal`）再判断。

`make test` 的 Go 结果可能来自构建缓存；发布证据再跑一次 CI 口径的 `-count=1`：

```bash
GO_IMAGE="$(sed -nE 's/^GO_IMAGE \?= ([^ ]+).*/\1/p' Makefile)"
run P3-go-test-fresh docker run --rm -v "$PWD":/src -w /src \
  -v rag-go-modcache:/go/pkg/mod -v rag-go-buildcache:/root/.cache/go-build \
  -e GOPROXY="${GO_PROXY:-https://goproxy.cn,direct}" \
  "$GO_IMAGE" go test -count=1 ./...
```

- [ ] 前端构建与依赖检查。

```bash
run P3-make-lint make lint
```

- [ ] Go race 测试（CI `verify` 覆盖，本地用同一镜像复现）。

```bash
GO_IMAGE="$(sed -nE 's/^GO_IMAGE \?= ([^ ]+).*/\1/p' Makefile)"
run P3-race docker run --rm -v "$PWD":/src -w /src \
  -v rag-go-modcache:/go/pkg/mod -v rag-go-buildcache:/root/.cache/go-build \
  -e GOPROXY="${GO_PROXY:-https://goproxy.cn,direct}" \
  "$GO_IMAGE" sh -c 'apk add --no-cache build-base >/dev/null && \
    CGO_ENABLED=1 go test -count=1 -race ./internal/... ./cmd/...'
```

alpine 镜像默认没有 cgo 工具链，直接 `go test -race` 会报
`-race requires cgo`；上面的 `build-base` 是必要的。CI 在 Ubuntu runner 上跑 race，
本地这次安装只影响容器层，不会改仓库。

- [ ] 模块校验和。

```bash
GO_IMAGE="$(sed -nE 's/^GO_IMAGE \?= ([^ ]+).*/\1/p' Makefile)"
run P3-mod-verify docker run --rm -v "$PWD":/src -w /src "$GO_IMAGE" go mod verify
```

- [ ] Web 类型、生成类型漂移、E2E 类型检查、生产构建。

```bash
cd web
run P3-codegen npm run codegen:check
run P3-e2e-typecheck npm run e2e:typecheck
run P3-web-build npm run build
cd ..
```

`codegen:check` 会先重新生成再 `git diff --exit-code`。如果它退出 1，先确认差异只是
OpenAPI 变更带来的正常再生成，还是工作区脏导致的假失败；不要把重新生成的文件留在未提交状态。

- [ ] TypeScript SDK 与 Python SDK。

```bash
cd sdk/typescript
run P3-sdk-ts-npm npm ci
run P3-sdk-ts-codegen npm run codegen:check
run P3-sdk-ts-test npm test
run P3-sdk-ts-typecheck npm run typecheck
cd ../..

python3 -m venv /tmp/release-preflight/pyenv
/tmp/release-preflight/pyenv/bin/pip install -r sdk/python/requirements-dev.txt
cd sdk/python
PYTHONPATH=. /tmp/release-preflight/pyenv/bin/pytest -q \
  > /tmp/release-preflight/P3-sdk-py.log 2>&1
echo "P3-sdk-py EXIT=$?" | tee -a /tmp/release-preflight/summary.txt
cd ../..
```

- [ ] Compose 基础编排可解析。

```bash
run P3-compose docker compose --env-file .env.example \
  -f deploy/docker-compose/docker-compose.yml config --quiet
```

- [ ] 可观测性包（Prometheus 配置、告警单测、overlay、Grafana JSON）。

```bash
run P3-observability make observability-check
```

- [ ] 评测集与评测框架自检（不连栈）。

```bash
run P3-eval-validate make eval-validate
```

- [ ] 镜像 digest 固定与 npm 审计基线。

```bash
run P3-image-pins ./scripts/check_image_pins.sh
cd web && node ../scripts/npm_audit_gate.mjs > /tmp/release-preflight/P3-npm-audit.log 2>&1
echo "P3-npm-audit EXIT=$?" | tee -a /tmp/release-preflight/summary.txt
cd ..
```

`govulncheck` 与 Trivy 由 CI `security` 执行；本地以 CI 结果为准，不要用未固定 digest 的
临时镜像替代。

## 4. 覆盖率门禁

- [ ] 先跑全量覆盖率，再以**上个发布 tag** 为基准跑 enforce。

```bash
run P4-coverage make coverage
export COVERAGE_BASE="$LAST_TAG" COVERAGE_GATE_MODE=enforce
run P4-coverage-gate make coverage-gate
unset COVERAGE_BASE COVERAGE_GATE_MODE
```

通过：退出码 0，日志里没有 `gate skipped`。`gate skipped` 不是通过——通常表示
`origin/main` 或基准引用无法解析；用 `COVERAGE_BASE="$LAST_TAG"` 重新跑。
记录本次变更涉及的包、最低余量，以及 `scripts/coverage_tiers.txt` 是否有需要同步的基线。

## 5. 快速链路与浏览器 E2E

这一段使用 quickstart 栈，必须先把开发栈停掉，避免端口冲突。

- [ ] 启动 quickstart 栈并完成自检。

```bash
make dev-down || true
export COMPOSE_PROJECT_NAME=rag-rel-e2e
run P5-quickstart ./scripts/quickstart.sh
run P5-quickstart-check ./scripts/quickstart_check.sh
```

- [ ] Playwright E2E 全量通过。

```bash
cd web
npx playwright install chromium || true    # 下载受限时用下面的 E2E_CHANNEL
E2E_CHANNEL=chrome npm run e2e > /tmp/release-preflight/P5-e2e.log 2>&1
echo "P5-e2e EXIT=$?" | tee -a /tmp/release-preflight/summary.txt
cd ..
```

通过：Playwright 退出码 0、0 failed。当前 10 个 spec / 18 个用例（2026-11-20 计数），
数量只作参考，不作为门槛；新增 spec 后更新这一行。
本地浏览器下载受限时用本机 Chrome（`E2E_CHANNEL=chrome`）或 Edge（`msedge`），CI 用 Chromium。

- [ ] MCP / SDK 联调验收（依赖 quickstart 栈与 `sdk/typescript` 依赖）。

```bash
(cd sdk/typescript && npm ci)
PYTHON=/tmp/release-preflight/pyenv/bin/python \
  run P5-b16 ./scripts/b16_acceptance.sh
```

脚本在 Python 缺少 `httpx`/`pytest` 时会打印 `python SDK tests skipped` 并继续；
跳过不算通过。用第 3 节创建的 venv 指定 `PYTHON`，或在记录里明确写“未跑 + 原因”。

- [ ] 关闭 quickstart 栈，再进入验收段。

```bash
./scripts/quickstart.sh --down
unset COMPOSE_PROJECT_NAME
```

## 6. 集成验收

这一段在开发栈上执行。开始前先构建所有 Go 服务镜像，避免 `migrate`/`seed` 还是旧镜像：

```bash
docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml \
  build api worker migrate seed
make dev-up
curl -fsS http://localhost:8082/readyz | grep -q '"status":"ok"'
```

### 6.1 套件与环境矩阵

| 套件 | 命令 | 环境要求 / 注意 |
| --- | --- | --- |
| M0 | `ACCEPTANCE_ENV_FILE=.env make m0-acceptance` | 会建空库并迁移；共享库无法 `CREATE DATABASE` 时换隔离卷/容器 |
| M1 | `ACCEPTANCE_ENV_FILE=.env make m1-acceptance` | 本地必须显式传 `.env`；会重建 api/worker，能验证当前代码 |
| M2 | `make m2-acceptance` | 上传到 `kb-dev`，要求远端 embedding / 1024 维 |
| M3 | `make m3-acceptance` | 远端 embedding / 1024；relay 429 时允许 api+worker 同时 `CHAT_SOURCE=local` 重跑并记录；失败会在 `kb-dev` 留下 `handbook.md`、`scanned.pdf` |
| M4 | `make m4-acceptance` | 需 `OIDC_ENABLED=false` 覆盖，跑完恢复 |
| M5 | `make m5-acceptance` | 需 `CONNECTOR_ALLOW_PRIVATE_NETWORKS=true`；已知清理竞态，见第 11 节 |
| M6 | `make m6-acceptance` | 必须 `CHAT_SOURCE=local`，且 api 与 worker 同时切换 |
| M7 | `make m7-acceptance` | 同上；远端超时先按环境问题处理，不要直接判代码回归 |
| RLS | dev shadow 直接 `make m13-rls-acceptance`；enforce 才先 `make m13-rls-setup` | `m13-rls-setup` 需要 `RAG_APP_DB_PASSWORD`/`RAG_MAINTENANCE_DB_PASSWORD`，dev `.env` 没有；enforce 用 `deploy/ci/enforce.env` 起一次性栈 |
| 异步 | `make m13-async-acceptance` | 会停/启 worker；先用当前源码重建 api/worker |
| B6-a | `make b6a-acceptance` | 文件夹树、标签、递归过滤、批量部分成功 |
| B15-a | `make b15a-acceptance` | 会改 dev/other 租户配置，退出时恢复 |
| B15-c | `make b15c-acceptance` | 只读变更预检，不落库 |
| B23 | `make b23-acceptance` | 预置包 CRUD、内置包只读、跨租户隔离 |
| D3 | `make d3-acceptance` | 需要 18090 stub 端口；build → verify → rollback → cutover → finalize |
| B16 | 用 `./scripts/b16_acceptance.sh`（没有 Makefile 目标） | 已在第 5 节 quickstart 栈上跑过 |
| B3 LDAP | `./scripts/b3_ldap_acceptance.sh` | 仅在本次涉及身份/LDAP 时执行；需 ldap overlay 与 0028 迁移 |

- [ ] 逐项执行并记录退出码。任一项失败即停，不跳到后面的套件。

### 6.2 单键环境覆盖（m4 / m6 / m7）

不要直接改 `.env`。写临时 override 文件，只改容器环境：

```yaml
# /tmp/rel-oidc-off.yml
services:
  api:
    environment:
      OIDC_ENABLED: "false"
  worker:
    environment:
      OIDC_ENABLED: "false"
```

```bash
COMPOSE=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)

"${COMPOSE[@]}" -f /tmp/rel-oidc-off.yml up -d --no-deps --force-recreate api worker
docker inspect "$("${COMPOSE[@]}" ps -q api)" --format '{{range .Config.Env}}{{println .}}{{end}}' | grep OIDC_ENABLED

# m4 after this
make m4-acceptance

"${COMPOSE[@]}" up -d --no-deps --force-recreate api worker
curl -fsS http://localhost:8082/readyz | grep -q '"status":"ok"'
```

m6/m7 用同样的文件形状，把 `OIDC_ENABLED` 换成 `CHAT_SOURCE: "local"`，并确认
**api 与 worker 都在容器里看到同一个值**。只切 worker 会让投影状态永远等不到一致哈希。

### 6.3 M3 评估门禁

- [ ] 先清理 `kb-dev` 里上次验收留下的文档，避免引用版本断言命中旧版本。
      先只读查看，确认哪些是本次/上次验收产物，再通过 API/UI 删除；不要整表清空。

```bash
docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml \
  exec -T postgres psql -U rag_platform -d rag_platform -Atc \
  "select id, name, status from documents where knowledge_base_id = 'kb-dev' order by created_at desc limit 20;"
```

- [ ] 确认栈仍是远端 embedding / 1024 维，chat 可以是远端；relay 持续 429/超时时，
      允许在 api 与 worker 同时切 `CHAT_SOURCE=local` 后重跑，并在记录里注明。

```bash
docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml \
  exec -T api env | grep -E '^(CHAT_SOURCE|EMBEDDING_SOURCE|EMBEDDING_DIMENSION)='

run P6-m3-eval make m3-eval
```

通过标准（`scripts/m3_eval.py` 的 `GATE_THRESHOLDS`）：

| 指标 | 阈值 |
| --- | --- |
| `permissionNegativeDenials` | 1.0 |
| `refusalRate` | 1.0 |
| `citationAccuracy` | 1.0 |
| `groundedness` | 1.0 |
| `injectionFlagRate` | 1.0 |
| `recallAtK` | 1.0 |
| `documentHitRate` | ≥ 0.85 |

`conflictFlagRate` 与 `meanLatencyMs` 是诊断项，不设发布阈值。
记录 `caseCount`、`excludedCaseCount`、`documentHitRate` 的分子/分母、`attempts` 非 1 的用例。
`excludedCaseCount > 0` 按失败处理，不是“部分通过”。

### 6.4 RLS enforce 布局

本地 dev 栈通常是 shadow。发布前必须确认 CI `integration` 的
`ENFORCE_LAYOUT=1 ./scripts/m13_rls_acceptance.sh` 通过；如果要在本地复现，
用 `deploy/ci/enforce.env` 起一次性栈，不要切换正在使用的开发栈。全新库且要跑
enforce 时，`make m13-rls-setup` 必须在 api/worker 启动前执行；shadow 开发栈不受影响。

```bash
cp deploy/ci/enforce.env /tmp/rel-enforce.env
# 用一个独立项目名和独立卷启动，按 CI integration 的步骤跑 migrate/seed
```

本地脚本固定 source `.env`；要在本地复现 enforce，只能临时替换 `.env`（先备份、结束后
逐字节恢复），且必须用一次性卷。默认以 CI 结果为准，不要在开发数据上切换 enforce。

### 6.5 验收结束后的恢复

- [ ] 如果做过 m4/m6/m7 的 override，恢复容器环境并确认 readyz：

```bash
docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml \
  up -d --no-deps --force-recreate api worker
curl -fsS http://localhost:8082/readyz | grep -q '"status":"ok"'
```

- [ ] 确认 `.env` 与预检前逐字节一致：

```bash
cmp -s .env /tmp/release-preflight/env.backup && echo "env restored" || echo "env changed"
```

### 6.6 明确禁止

- 不在开发栈上跑 `make embed-acceptance`。
- 不用 `docker compose down -v` 清理开发数据来“让预检通过”。
- 不在同一个开发库上并行跑两套验收脚本。

## 7. 升级与回滚演练

- [ ] `v0.1.0 → HEAD` 本地双镜像演练通过。

```bash
run P7-upgrade-drill make upgrade-drill
```

通过：退出码 0；`tests/eval/upgrade_drill_report.json` 的回滚结论为通过；回填第二次
`inserted=0`；`readyz` 恢复；升级后检索/SSE 可用。报告不入库。
演练使用 18082/55432/56379/59000/59001 等偏移端口，不要与开发栈或 quickstart 并行。

## 8. 文档与元数据

- [ ] CHANGELOG 已是 `## [X.Y.Z] - YYYY-MM-DD`，`Unreleased` 为空但保留。
- [ ] 面向用户的版本引用同步：
      `README.md` 当前版本与“新增”段、`README.en.md`、`docs/comparison.md` 的版本行。
- [ ] 升级/回滚说明覆盖本次迁移与兼容边界（[docs/releases.md](../releases.md) 第 5、6 节）。
- [ ] 设计/里程碑记录已同步（如适用），不把内部编号写进公开文档。
- [ ] 文档站严格构建通过。

```bash
python3 -m pip install -r docs/requirements.txt
mkdocs build --strict
```

- [ ] 生成类型无漂移（`openapi -check` 已在 `make test`/`make lint` 中；重复确认即可）。
- [ ] `git diff --check` 无空白错误。

## 9. 提交、CI 与 tag

1. 提交发布内容（CHANGELOG、版本引用、必要的文档修正）。
2. push 到 `main`，等待该提交上的 CI 全部通过：`verify`、`integration`、`e2e`、
   `security`、`observability`；`eval` 在缺少远端模型凭据时允许跳过，但必须在记录里写明。
3. 注意：`release.yml` 的 `verify` 只覆盖格式、依赖方向、OpenAPI、租户门禁、Go 测试、
   `go vet`、模块校验和、Web 测试与构建；**不跑** race、覆盖率、integration、E2E、
   security。tag 之后的绿色不能替代第 2 步。
4. 在 `main` 的最新提交上创建带注释的 tag 并推送（先确认）：

```bash
git checkout main
git pull --ff-only
git tag -a "$RELEASE_VERSION" -m "rag-platform $RELEASE_VERSION"
git push origin "$RELEASE_VERSION"
```

5. 观察 `release.yml` 的 `verify` 与 `publish` 两个 job；`publish` 会先扫描再推送镜像，
   扫描失败不会产生签名，也不会创建 Release。

## 10. 发布后校验

按 [docs/releases.md](../releases.md) 第 4 节执行，并记录结果：

```bash
VERSION="$RELEASE_VERSION"
gh release download "$VERSION"
sha256sum -c SHA256SUMS
# macOS 没有 GNU sha256sum 时：
# shasum -a 256 -c SHA256SUMS
```

```bash
IMAGE=ghcr.io/hzc19970630/rag-platform/api
DIGEST=sha256:...        # 取自 release-manifest.txt
cosign verify \
  --certificate-identity "https://github.com/hzc19970630/rag-platform/.github/workflows/release.yml@refs/tags/${VERSION}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  "${IMAGE}@${DIGEST}"
cosign verify-blob \
  --bundle SHA256SUMS.cosign.bundle \
  --certificate-identity "https://github.com/hzc19970630/rag-platform/.github/workflows/release.yml@refs/tags/${VERSION}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  SHA256SUMS
docker run --rm "${IMAGE}@${DIGEST}" --version
```

- [ ] `--version` 的 version / commit / built_at 与 `release-manifest.txt` 一致。
- [ ] 三个镜像 tag@digest 与 manifest 一致；web 包、三个 SBOM、六份 MCP 二进制都在校验和覆盖范围内。
- [ ] GitHub Packages 中 api/docreader/mcp 已设为 public（仓库公开时匿名 pull 才可用）。
- [ ] 本机没有 `gh` 时，用浏览器下载产物或 REST API 完成同样的校验，不要跳过签名校验。

## 11. 失败处置

| 现象 | 处置 |
| --- | --- |
| `make test` 日志为空且退出 1 | 多半是 gofmt 链式步骤吞了输出；拆开跑 `gofmt -l cmd internal` |
| `codegen:check` 退出 1 | 先确认是 OpenAPI 正常再生成，还是工作区脏；不要把生成文件留在未提交状态 |
| 覆盖率日志出现 `gate skipped` | 视为未通过；用 `COVERAGE_BASE="$LAST_TAG"` 重跑 |
| M3 `documentHitRate 0.8333`（30/36） | 失败即停，报缺失文档集合；是否复跑由 owner 决定，复跑也需记录两份结果 |
| M5 在空 manifest 删除与 pipeline finalize 之间出现竞争签名 | 已知竞态；确认签名一致后允许复跑 1 次，记录两次结果 |
| M6/M7 远端 chat 超时或投影停在 `KNOWLEDGE_PARTIAL` | 先确认 api 与 worker 的 `CHAT_SOURCE` 是否一致；必要时两边同时切 local 后重跑 |
| E2E 浏览器下载失败 | `E2E_CHANNEL=chrome` 或 `msedge`；CI 仍用 Chromium |
| 本机 Postgres 无法 `CREATE DATABASE` | 用隔离项目/新卷或一次性容器；不要删开发卷 |
| `make <feature>-acceptance` 报 `Permission denied` | 脚本缺执行位；`chmod +x scripts/<name>.sh` 后重跑 |
| 旧 `migrate`/`seed` 镜像报迁移缺失 | 重建所有 Go 服务镜像，不只重建 api/worker |
| 磁盘/构建缓存不足 | 先清理无用构建缓存，再重跑；不要删正在使用的卷 |

## 12. 执行记录

每轮发布复制一张表到运维记录，至少保留以下字段：

| 步骤 | 命令/套件 | 退出码 | 关键指标 | 证据文件 |
| --- | --- | --- | --- | --- |
| 静态/单测 | `make test` / `make lint` | | | `/tmp/release-preflight/P3-*.log` |
| 覆盖率 | `coverage-gate` | | 最低余量包 / 百分比 | `P4-coverage-gate.log` |
| E2E | `npm run e2e` | | passed/failed | `P5-e2e.log` |
| 验收 | m0–m7、RLS、异步、B6-a、B15、B23、D3 | | 各 suite 结果 | `summary.txt` |
| M3 评估 | `make m3-eval` | | 7 项 gate、documentHit 分子/分母、excluded | 报告 JSON |
| 升级演练 | `make upgrade-drill` | | 回填计数、rollback 结论 | 报告 JSON（不入库） |
| CI | main 提交的 ci | | verify/integration/e2e/security/observability（eval 可跳过） | Actions 链接 |
| 发布 | tag / release.yml | | digest、签名、校验和 | Release 页面 |

发布结论只写实际观测到的事实；没有跑的门禁写“未跑 + 原因”，不要写“应该没问题”。