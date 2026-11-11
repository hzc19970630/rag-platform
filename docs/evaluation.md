# 评测方法与复现

本仓库的评测是**自测证据**，不是第三方基准。它回答两个具体问题：

1. M3：在固定合成语料上，检索、引用、拒答和权限负向是否达到发布门槛？
2. M7：有界多跳图查询相比纯 Evidence RAG 是否带来可复现的收益，是否值得保留？

M7 可以在 quickstart 栈上重新生成；M3 的拒答门禁需要真实生成模型，quickstart 的本地抽取适配器只能复现其余指标（见 4.1）。数据集、脚本和报告都在仓库内，口径以本页和脚本为准；`docs/comparison.md` 中的竞品信息不来自这些数字。

## 1. 数据集

| 评测 | 文件 | Schema | 规模 | 用例构成 |
| --- | --- | --- | --- | --- |
| M3 | `tests/eval/m3_eval_set.json` | `EvaluationSetV1` | 17 fixtures / 49 cases | fact 18、cross_document 8、no_answer 6、injection 5、conflict 5、permission_negative 7 |
| M7 | `tests/eval/m7_qa_set.json` | `M7EvaluationSetV2` | 30 fixtures / 34 cases / 135 chunks | multi-hop 20、single-hop 6、branch 2、negative 6 |

语料是手工编写的运维/产品说明，角色覆盖手册、运行手册、身份、摄取、队列、存储、可观测性、投影、安全等主题，并包含用于注入与冲突检测的不可信段落。语料全部是合成文本，不包含客户数据。

M3 的用例通过 `expectedDocuments`、`alignedEvidence`、`alignedDocuments`、`sessionOverride`、`actor`、`probes` 等字段声明期望；M7 的用例声明 `seed`、`depth`、`expectedNodes`、`forbiddenNodes`、`expectedHops`、`expectedDocument` 等字段。字段语义以两个 JSON 文件的 `notes` 和脚本校验为准。

### 数据校验

两个脚本在**上传任何文档之前**都会校验数据集；`--validate-only` 只做校验，不发起网络请求。

公共校验（`evalkit/dataset.py`）：

- `schemaVersion` 与脚本声明的版本一致，`name` 非空，`notes` 是字符串列表。
- fixture 的 `filename` 非空、无首尾空白、不超过 255 字符、不含路径分隔符或 NUL、不接受 `.`/`..`；文件名大小写不敏感唯一；`role` 与 `content` 非空。
- case 的 `id` 非空且唯一，`type` 与 `question` 非空。

M3 追加校验：

- `role` 唯一；`expectedDocuments`/`alignedDocuments` 必须引用真实角色，且 `expectedDocuments` 是 `alignedDocuments` 的子集。
- `sessionOverride` 必须是 JSON 对象字符串；布尔标志必须是布尔值。
- 权限用例必须使用已知探针、无重复探针，且 actor 不能是知识库 owner；同租户 actor 必须是 harness 真正创建成员关系的 `viewer-a`/`editor-a`，否则“无成员被拒”会造成假阳性。
- `chunk`/`events` 探针必须有一个更早的可回答案例提供目标 id。
- 每个门禁指标至少有一个用例可以测量它。

M7 追加校验：

- fixture 中的三元组 BFS 跳数与 `expectedHops` 一致，seed/alias 可解析，marker 位于声明的文档中。
- 负向用例必须声明语义：未知 seed 要求空图且有活动快照；depth-bound 要求 forbidden node 留在查询深度之外。
- 所有字段都有类型守卫；非法类型在校验阶段报错，而不是在运行中抛 `TypeError`。

## 2. M3 指标

M3 报告为 `EvaluationReportV1`，包含 `metrics`、`gates`、`cases`、`limitations`、`auth` 和 `documents`。

| 指标 | 定义 | 门禁 |
| --- | --- | --- |
| `recallAtK` | 声明了 `alignedEvidence` 的用例中，至少一个返回 chunk 命中 marker 的抽样比例均值。默认每个用例抽样 1 次，因此取值为 0 或 1；`--recall-samples N` 可测抽样稳定性，分歧用例记录在 `recallUnstableCases`。 | ≥ 1.0 |
| `documentHitRate` | 声明了 `expectedDocuments` 的用例中，所有期望文档都被引用的比例。 | ≥ 0.85 |
| `citationAccuracy` | 声明了 `alignedEvidence` 的用例中，全部引用 chunk 属于该答案的证据集，且引用文档在 `alignedDocuments`（缺省为全部文档）内的比例。 | ≥ 1.0 |
| `groundedness` | 答案校验结果 `validation.valid` 为真的比例。 | ≥ 1.0 |
| `refusalRate` | `expectRefusal` 用例中答案状态为 `REFUSED` 的比例。 | ≥ 1.0 |
| `permissionNegativeDenials` | 权限负向用例中没有探针被允许的比例。探针 `unavailable` **不算**拒绝，会直接触发门禁失败。 | ≥ 1.0 |
| `injectionFlagRate` | `expectInjectionFlag` 用例中检索证据触发注入标记的比例。 | ≥ 1.0 |
| `conflictFlagRate` | `expectConflictFlag` 用例中触发冲突标记的比例。诊断项，不设门槛。 | 无 |
| `meanLatencyMs` | 问答调用的平均墙钟时间（含重试）。诊断项。 | 无 |

门禁阈值在 `scripts/m3_eval.py` 的 `GATE_THRESHOLDS` 中，报告会原样写出。任一门禁不满足，脚本退出码为 1；数据集非法或 harness 无法运行，退出码为 2。

补充规则：

- 上游模型 `model_error`/`timeout` 最多重试 3 次；仍失败时用例从聚合中排除，并在 `excludedCaseCount`、`excludedCases` 和 `limitations` 中记录。排除不是通过：报告会明确列出。
- 注入与冲突标记来自检索证据的规则检测，不依赖模型是否“听话”。
- 权限探针的 actor 凭据必须通过 `/api/v1/me` 证明身份；缺失或主体不符时探针记为 `unavailable`，报告写入 `auth.probeCredentials` 和每个探针的 `reason`。
- 报告默认删除评测知识库；`--keep` 保留现场。`--redact-base-url` 将栈地址写成 `redacted`。

## 3. M7 指标

M7 报告为 `M7EvaluationReportV2`。图查询和 Evidence RAG 使用同一批问题与人工答案，比较的是**可达性**，不是聊天模型的文笔。

| 指标 | 定义 |
| --- | --- |
| `multiHopRagStrictHitRate` | 多跳用例中，所有 marker 都出现在**期望文档**的返回 chunk 里，且未命中禁止证据的比例。 |
| `multiHopRagLooseHitRate` | 多跳用例中，marker 出现在任意返回 chunk 中的比例（诊断口径）。 |
| `multiHopGraphHitRate` | 多跳用例中，快照存在、所有 `expectedNodes` 出现、`forbiddenNodes` 未出现，且声明了 `expectedHops` 时跳数完全一致的比例。 |
| `multiHopHopExactRate` | 声明了 `expectedHops` 的多跳用例中，最短跳数完全一致的比例。 |
| `graphRescueCount` / `ragOnlyCount` / `bothHitCount` / `neitherCount` | 两个通道在同一个多跳用例上的命中组合。 |
| `negativePassRate` | 负向用例通过比例：未知 seed 返回空图，或 depth-bound 的 forbidden node 未越界。 |
| `ragDistractorFalsePositiveCount` | 声明 `forbiddenEvidence` 的可回答案例中，RAG 返回了禁止证据的用例数。 |
| `mean/p95RagLatencyMs`、`mean/p95GraphLatencyMs` | 两个通道的每次查询延迟。 |

`decision` 给出 `keep-bounded-graph-query`、`drop-bounded-graph-query` 或 `inconclusive`，并有最小用例数护栏（≥ 10 个多跳用例、≥ 20 个可回答案例）。这是**建议**，不是发布门禁；M7 脚本只在 harness 本身失败时退出非零。

M7 的关键限制：

- 关系由 fixture 中的确定性 `[[subject|predicate|object]]` 注解播种，抽取质量不在测量范围内。
- seed 由问题人工给出，不评估自由文本的自动实体链接。
- 语料是合成的，不能代表生产文档长度、格式或查询分布。
- RAG 侧每次重建索引都可能改变 top-k 成员；图侧因种子三元组而确定，因此 delta 可能在两次运行间变化。报告是一次样本，发布决策前应重复运行。
- `--rag-top-k` 是诊断覆盖，使用后 headline 对比不再是生产默认检索设置。

## 4. 一条命令复现

前置条件：

- 离线校验和 M7 可以只用 quickstart 栈（默认 `http://localhost:8082`，开发身份可用）；M3 门禁需要真实生成模型，见 4.1。
- 使用仓库内的 Python 环境；脚本只依赖标准库。

```bash
# 离线校验：数据集 + harness 回归测试，不访问网络
make eval-validate

# 运行两个评测：m3 先跑，m7 后跑
make eval-suite

# 公开发布模式：先校验，脱敏栈地址，报告写到 tests/eval/public
make eval-public
```

等价的直接命令：

```bash
python3 scripts/eval_suite.py --suite all
python3 scripts/eval_suite.py --suite m3,m7 --base-url http://localhost:8082
python3 scripts/eval_suite.py --dry-run          # 只打印将要执行的命令
python3 scripts/eval_suite.py --suite m3 --embedding-model <model> --embedding-dimension 1024
```

- `--suite` 接受 `all`、`m3`、`m7` 或逗号分隔的组合；m3 专属参数（`--actor-key`、`--embedding-*`、`--recall-samples`）在未选择 m3 时会被拒绝，不会被静默丢弃。
- `--auth`、`--api-key`、`--actor-key` 原样传给对应脚本；`--dry-run` 打印的命令会遮蔽密钥。
- 报告默认写到 `tests/eval/`；`--report-dir` 可改写，`--public` 默认写到 `tests/eval/public/`。

### 4.1 模型与超时前提

M3 的 `refusalRate` 门禁要求 chat 模型能判断检索证据不足并拒答。quickstart 的 `CHAT_SOURCE=local` 是确定性抽取适配器：只要证据中有词面重叠就引用，不会判断“答不了”，因此 no-answer 用例不会稳定拒答。2026-11-08 的 quickstart 自测中 6 个 no-answer 用例有 3 个未拒答，`refusalRate=0.5`；这是适配器能力限制，不是评测或权限逻辑的缺陷。**不要用 quickstart 的本地 chat 跑 M3 门禁。**

一套可复现远程模型结果的配置：

```bash
CHAT_SOURCE=remote
CHAT_BASE_URL=https://...
CHAT_API_KEY=...
LLM_CHAT_MODEL=...
EMBEDDING_SOURCE=remote
EMBEDDING_BASE_URL=https://...
EMBEDDING_API_KEY=...
LLM_EMBEDDING_MODEL=...
EMBEDDING_DIMENSION=1024
DOCREADER_TASK_TIMEOUT_SECONDS=1800
```

- `LLM_CHAT_MODEL`/`LLM_EMBEDDING_MODEL` 是配置读取的变量名；只设置 `CHAT_MODEL`/`EMBEDDING_MODEL` 不会生效。
- 修改后重启 api/worker。quickstart overlay 强制 `CHAT_SOURCE=local`/`EMBEDDING_SOURCE=local`，需要改用基座 compose 或自行覆盖。
- `DOCREADER_TASK_TIMEOUT_SECONDS` 同时是关系投影任务的超时（默认 120s）。远程模型抽取 M7 的 135 个 chunk 可能超过它，表现为 `FAILED_RELATION: context deadline exceeded`；本地实测 1800s 可通过。M7 客户端最多等待 20 分钟。
- 只复现 M7 且不关心模型口径时，quickstart 的本地模型可以跑通；报告会记录 `local-extractive`，不要与远程模型结果直接比较。

### 4.2 CI 接入

`.github/workflows/ci.yml` 的 `eval` job：

- 总是运行 `make eval-validate`（数据集校验 + harness 回归测试），不访问网络。
- 配置以下仓库 Secrets/Variables 后，job 会启动一次性 compose 栈，跑 M3 与 M7：

| 类型 | 名称 |
| --- | --- |
| Secret | `EVAL_CHAT_API_KEY` |
| Secret | `EVAL_EMBEDDING_API_KEY` |
| Variable | `EVAL_CHAT_BASE_URL` |
| Variable | `EVAL_CHAT_MODEL` |
| Variable | `EVAL_EMBEDDING_BASE_URL` |
| Variable | `EVAL_EMBEDDING_MODEL` |
| Variable | `EVAL_EMBEDDING_DIMENSION`（可选，默认 1024） |

- 缺少任一必填项时，live 步骤跳过并输出 notice；CI 不会用本地抽取模型伪造 M3 门禁。Fork PR 拿不到 secrets，因此只跑离线校验。
- live profile 由 `deploy/ci/eval.env`（CI 专用基础设施凭据与模型槽位）和 `deploy/ci/eval.compose.yml`（dev auth、远程模型源、1800s 投影超时）组成；workflow 在运行前把 `EVAL_*` 值追加进 `.env`。
- M3 是硬门禁：`eval_suite.py` 以非零退出时 job 失败。M7 是建议：只有 harness 失败才非零，报告仍会生成。
- 报告写到 `tests/eval/ci/`，以 `eval-reports` artifact 保留 7 天；栈结束时执行 `down -v` 清理。
- 模型调用会消耗供应商配额；如只想在特定分支运行 live 门禁，可在仓库设置中按分支/环境保护 secrets，或调整 workflow 的触发条件。
- workflow 支持 `workflow_dispatch`：配置凭据后可在 Actions 页面手动运行 `ci`，用于验证 live 门禁而无需额外推送。

### 使用 API Key 鉴权

quickstart 栈默认启用开发身份；若部署关闭了 dev auth，使用 API Key 模式：

```bash
export RAG_EVAL_API_KEY=rk_owner...
python3 scripts/eval_suite.py --auth api-key \
  --actor-key "viewer-a=$VIEWER_KEY" \
  --actor-key "editor-a=$EDITOR_KEY" \
  --actor-key "other-user=$OTHER_KEY"
```

要求：

- owner key 能创建/删除评测知识库并添加成员。
- 数据集中的 `viewer-a`/`editor-a`/`other-user` 必须先作为租户成员存在（quickstart seed 已提供），否则添加知识库成员会失败。
- 每个权限探针身份都需要自己的 key；API Key 不能模拟其他用户。
- harness 会调用 `/api/v1/me` 校验 `subjectId`/`tenantId`。缺 key、主体不符或凭据类型不对的探针记为 `unavailable`，`permissionNegativeDenials` 门禁失败；不会把“用错身份被拒”记成安全通过。
- 数据集用 `dev-tenant` 标记“同租户”actor。在 API Key 模式下，这些 actor 会映射到 owner key 所在租户；跨租户 actor 保持声明的租户。

## 5. 报告与基线

- M3 最新自测：`tests/eval/m3_eval_report.json`（2026-11-08，`gpt-6.1-sol`，49 用例，7 项门禁通过，均值延迟 11.9s）。
- M7 最新自测：`tests/eval/m7_eval_report.json`（2026-11-08，`gpt-6.1-sol`，多跳图 100% vs 严格 RAG 75%，建议保留有界图查询）。

这些文件是生成时的快照。模型、语料、检索参数或代码变化后必须重跑；不要把它们当作当前版本的保证。报告不写入 API Key 或对象存储键，但包含问题、期望、文档 id、用例结果和栈地址；发布前仍应使用 `--public` 或 `--redact-base-url`，并人工检查 `cases` 与 `limitations`。

`tests/eval/public/` 已生成 2026-11-08 公开基线，环境、指标与失败案例见该目录的 `README.md`：

- M3：`gpt-6.1-sol` + `qwen3.7-text-embedding`（1024 维），3 次召回采样，7 项门禁通过，`documentHitRate=0.8889`。
- M7：多跳图 100% vs 严格 RAG 70%（+30%，6 个 graph-only rescue），建议保留有界图查询。
- 失败案例：首次 M3 运行因 `fact-rerank-default` 返回 `ungrounded_answer`，`groundedness=0.9565` 未过门禁；失败报告保留为 `m3_eval_report.failed-20261108T074653Z.json`，同配置重跑通过。这说明单次绿色运行不代表稳定，发布决策前应重复运行。
- 已完成一次人工评审：栈地址脱敏，模型与维度、`limitations`、失败报告均保留，未发现凭据或本机路径。外部独立复现仍未完成，不能把本基线当作第三方结论；后续每次更新基线仍需同样评审。
