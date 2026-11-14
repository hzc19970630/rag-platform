# M8 存储迁移清单

本清单列出把检索/向量存储从 PostgreSQL + pgvector 换到外部引擎时必须实现的端口与步骤。D8 完成后，领域代码只依赖这些端口；迁移不应改动路由、权限、引用或投影逻辑。

## 端口

| 端口 | 位置 | 责任 |
| --- | --- | --- |
| `VectorSearcher` | `internal/contracts/ports` | 按 `(tenant, kb, model, dimension)` 做向量召回，返回带分数的 chunk |
| `KeywordSearcher` | `internal/contracts/ports` | BM25/关键词召回 |
| `EvidenceIndexWriter` | `internal/contracts/ports` | 写入/更新 chunk 向量、删除版本、通知派生索引 |
| `vectorindex.Manager` | `internal/contracts/vectorindex` | 索引创建、替换、维度读取、健康检查、变体索引 |
| `formats.Registry` | `internal/contracts/formats` | 上传格式与 MIME 规范化 |

pgvector 适配器：

- `internal/platform/vector`（召回 + `Manager`）
- `internal/platform/evidenceindex`（写入）
- `internal/platform/search`（BM25）
- `internal/contracts/formats.Static`（内置格式集）

## 迁移步骤

1. **实现召回端口**：至少实现 `SearchVector` 与 `SearchKeyword`；查询必须自己应用租户/知识库/模型/维度过滤，不能把跨空间分数混合。
2. **实现写入端口**：`WriteEmbeddings` 同时维护目标索引与状态；`DeleteVersion` 必须清理派生条目；`IndexChunks` 在 PostgreSQL 是 no-op，外部引擎通常需要真实写入。
3. **实现 `vectorindex.Manager`**：
   - `EnsureChunksIndex` / `EnsureChunkEmbeddingIndex` 返回稳定的索引名与耗时；
   - `CheckChunksIndexes` / `CheckChunkEmbeddingIndexes` 报告有效性与维度直方图；
   - 维度切换必须保留旧索引，直到验证通过。
4. **接入 composition root**：在 `internal/app` 选择适配器；`cmd/embedctl` 的 `check`/`create-index`/`backfill-embeddings` 必须指向同一实现。
5. **迁移数据**：保留 `0031_embedding_variants` 的语义（每个 chunk 每个模型一行）；升级顺序仍为 `migrate → backfill → worker → api`。外部引擎需要等价的回填与断点续跑。
6. **健康检查**：配置体检的 `index.valid` 与 `embedding.dimension` 必须由新 `Manager` 提供，不得保留 pgvector 专用查询。
7. **验收**：
   - `go test ./...`、`checkdeps`、`openapi -check`；
   - m1–m7；
   - `d3_acceptance.sh`（迁移/回滚/清理）；
   - `d3_dataplane.py`（1M × 1024，p95 相对旧路径 ≤ 10% + 5ms，且 EXPLAIN/计划命中目标索引）；
   - 跨租户与越权负向不回归。

## 不应改动

- 检索参数三级覆盖与 `configschema`
- 权限过滤、引用校验、拒答逻辑
- OpenAPI/前端生成类型
- `relationstore` 与对象存储（各自独立端口）

## 已知边界

- 当前 BM25 与向量同库；外部引擎若拆分两类存储，需要保证关键词与向量的 RRF 仍按同一 chunk 标识融合。
- `finalize` 前旧向量空间必须保留；迁移后的回滚窗口与清理策略由部署方确认。
- 外部引擎的索引构建时间、磁盘/内存与备份恢复需要单独演练，不继承 pgvector 的报告。
