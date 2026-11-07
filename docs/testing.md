# 测试

测试分三层：Go 单元/集成测试、前端单元测试、Playwright 浏览器 E2E。E2E 只验证主链路与可访问性，不替代人工验收。

## 1. 前端单元测试

```bash
cd web
npm ci
npm test
```

Vitest + jsdom，覆盖 API 客户端、答案流解析、权限 Hook、聊天引用渲染、管理台配额表单等。`vite.config.ts` 的 `test.include` 只收集 `src/` 下的测试，避免把 Playwright 用例当成单测执行。

## 2. Playwright E2E

E2E 依赖完整 quickstart 栈：PostgreSQL、Redis、MinIO、docreader、API、worker、web。默认使用本地确定性模型和开发身份，不访问外部模型。

```bash
# 终端 1：启动 quickstart 栈
./scripts/quickstart.sh

# 终端 2：首次运行前安装浏览器
cd web
npx playwright install chromium

npm run e2e          # 无头运行
npm run e2e:headed   # 观察浏览器操作
npm run e2e:report   # 打开最近一次 HTML 报告
```

浏览器下载受限时，可用 `E2E_CHANNEL=chrome npm run e2e` 复用本机已安装的 Chrome（或 `msedge`），无需下载 Playwright Chromium；CI 不设置该变量。

`npm run e2e` 前会执行 `e2e/global-setup.ts`：检查 `/readyz`、`/auth/config` 的 `devAuthEnabled`、开发身份 `/me` 和 web 首页。任一不满足会直接报错并提示启动 quickstart 栈。

### 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `E2E_BASE_URL` | `http://localhost:5173` | 浏览器访问的前端地址 |
| `E2E_API_URL` | `http://localhost:8082/api/v1` | 测试辅助请求使用的 API 地址 |
| `E2E_CHANNEL` | 未设置 | 设为 `chrome` 或 `msedge` 复用本机浏览器，跳过 Chromium 下载 |

`API_PORT` 或 `API_PUBLIC_URL` 改动后，需要同步设置 `E2E_API_URL`（以及前端构建时的 `VITE_API_BASE_URL`）。

### 覆盖范围

| 文件 | 内容 |
| --- | --- |
| `e2e/auth.spec.ts` | 开发身份登录进入工作台 |
| `e2e/retrieval-flow.spec.ts` | 创建知识库 → UI 上传 → 轮询 `EVIDENCE_READY` → 检索契约 → 问答 SSE/引用 → 引用打开文档 → 删除文档 |
| `e2e/admin.spec.ts` | B4 管理台回归：概览、配额、成员、审计入口 |
| `e2e/accessibility.spec.ts` | axe WCAG 2.1 A/AA；`Ctrl/Cmd+K` 打开全局搜索、`Esc` 关闭 |
| `e2e/responsive.spec.ts` | 375/768/1280 断点无横向溢出；移动端导航入口 |

主链路用唯一知识库名隔离数据，测试结束通过 API 删除知识库；文档删除在 UI 中完成并校验行消失。上传、问答和引用是真实后端调用，不使用接口 mock（登录页可访问性用例只 mock `/me` 以展示未登录状态）。

### 稳定性约定

- 主链路超时 240 秒，解析轮询 150 秒，评测模型为本地确定性实现。
- 失败时保留 trace 与截图；CI 重试 1 次，本地不重试。
- 用例串行执行（`workers: 1`），避免共享本地栈的模型与索引资源竞争。
- 已知失败先复现再放宽超时，不要用固定 sleep 替代状态轮询。

## 3. CI

`.github/workflows/ci.yml` 的 `e2e` job 会：

1. 安装 web 依赖与 Chromium；
2. 执行 `npm run e2e:typecheck`；
3. 用 `quickstart.compose.yml` 启动完整栈；
4. 执行 `npm run e2e`；
5. 上传 `web/playwright-report`、`web/test-results`（含 trace/截图/视频），并在失败时倾倒 api/worker/web 日志、始终清理栈。

`verify` job 也会执行 `npm run e2e:typecheck`，防止配置和用例类型漂移。

## 4. 限制

- E2E 假设 `DEV_AUTH_ENABLED=true` 且 `APP_ENV` 非 production；不能用发布镜像直接跑。
- 本地模型只验证链路，不验证真实模型质量；检索质量回归使用 `tests/eval` 与 B14 的评测视图。
- 浏览器只覆盖 Chromium；Firefox/WebKit 需要额外安装并增加 CI 时间。
- 可访问性检查是自动化基线，键盘全流程和读屏体验仍需人工抽查。
