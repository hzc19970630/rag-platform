# 公开评测基线（2026-11-08）

这组报告是**自测证据**，不是第三方基准。数据集为仓库内合成语料，运行环境为
本地 compose 栈（dev 身份），栈地址已脱敏。

## 生成环境

| 项目 | 值 |
| --- | --- |
| 代码 | `ac5b96c` 加生成时工作区的评测层改动（16 个路径，已随本提交入库） |
| API 镜像 | `sha256:71cdb6bbbe1f3b87049fbd5e00204b4c00be5d143f3b1018546b92d7829f0f21` |
| Chat | `gpt-6.1-sol`（remote） |
| Embedding | `qwen3.7-text-embedding`，1024 维（remote） |
| Docreader 超时 | 1800s |
| M3 召回采样 | 每用例 3 次 |
| 生成时间 | M3 `2026-11-08T08:14:35Z`；M7 `2026-11-08T07:57:24Z` |

## 结果

### M3（17 fixtures / 49 cases）

| 门禁 | 值 | 阈值 |
| --- | --- | --- |
| `permissionNegativeDenials` | 1.0 | 1.0 |
| `refusalRate` | 1.0 | 1.0 |
| `citationAccuracy` | 1.0 | 1.0 |
| `groundedness` | 1.0 | 1.0 |
| `injectionFlagRate` | 1.0 | 1.0 |
| `recallAtK` | 1.0（3 次采样，无分歧用例） | 1.0 |
| `documentHitRate` | 0.8889 | 0.85 |

7 项门禁全部通过，无排除用例；`meanLatencyMs=20375`（诊断项）。

### M7（30 fixtures / 34 cases / 135 chunks）

| 指标 | 值 |
| --- | --- |
| `multiHopGraphHitRate` | 1.0 |
| `multiHopRagStrictHitRate` | 0.7 |
| 图查询相对严格 RAG | +30%，6 个 graph-only rescue |
| `multiHopHopExactRate` | 1.0（20 个用例） |
| `negativePassRate` | 1.0（6 个负向用例） |
| `ragDistractorFalsePositiveCount` | 0 / 2 |
| RAG / 图查询均值延迟 | 155.9ms / 11.8ms |
| 建议 | `keep-bounded-graph-query` |

## 失败案例

首次 M3 运行（`2026-11-08T07:46:53Z`）中，可回答用例 `fact-rerank-default`
返回 `ungrounded_answer` 被拒答，`groundedness=0.9565`（22/23）未达 1.0 门禁。
失败报告保留为 `m3_eval_report.failed-20261108T074653Z.json`；同配置重跑通过。
这说明 groundedness 存在模型运行间波动，单次绿色运行不构成稳定保证，发布决策前
应重复运行。M7 的关系抽取依赖模型，且 RAG 侧每次重建索引可能改变 top-k，本次
同样是一次样本。

## 文件

- `m3_eval_report.json`：M3 通过报告。
- `m3_eval_report.failed-20261108T074653Z.json`：首次失败报告，保留作失败案例。
- `m7_eval_report.json`：M7 报告。

报告只保留栈地址（`redacted`）、临时知识库/会话/文档 id 和用例结果；不包含
API Key、对象存储键或文档正文。数据集与报告已人工检查，未发现本机路径、
内网地址或凭据。

## 复现

```bash
make eval-validate
python3 scripts/eval_suite.py --public \
  --embedding-model qwen3.7-text-embedding --embedding-dimension 1024 \
  --recall-samples 3
```

需要已配置远程模型与 dev auth 的栈。quickstart 的本地 chat 无法通过 M3 的
`refusalRate` 门禁；方法与限制见 `docs/evaluation.md`。
