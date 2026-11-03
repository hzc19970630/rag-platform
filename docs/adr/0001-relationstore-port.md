# ADR 0001：RelationStore 端口与 PostgreSQL 适配器

- 状态：已接受
- 日期：2026-11-03
- 相关：`DESIGN.md` §4.2 / §4.3、`MILESTONES.md` M8

## 背景

M8 要求：评估 Neo4j/其他图数据库时，迁移必须通过 `RelationStore` 端口，不泄漏厂商查询类型。此前代码没有该接口，`knowledgeprojection` 直接通过 GORM 读写 `graph_snapshots`、`graph_entities`、`graph_entity_aliases`、`graph_relations`、`graph_relation_sources`，迁移会扩散到查询、投影 worker、生命周期与测试。

## 决策

1. 端口定义在 `internal/relationstore`，只包含图快照与图行的存储契约，不导入任何项目内包。
2. 首期实现为 `internal/platform/relationstore`（PostgreSQL/GORM 适配器）。
3. 领域层继续拥有：种子匹配、深度/节点预算的 BFS、路径拼装、`AuthorizationScope` 复核、证据 current/EVIDENCE_READY 校验、projection run/stage 状态。
4. 图 API 的请求与响应结构不变；端口快照在 projection 层映射回 `models.GraphSnapshot`。
5. 本次不实现 Neo4j/OpenSearch/Milvus 适配器，也不做遍历下推。

## 端口边界

| 能力 | 位置 | 说明 |
| --- | --- | --- |
| 快照读取/列表/按 run 查找 | `RelationStore` | 按 tenant / knowledge base 限定 |
| 实体、别名、关系、来源读取 | `RelationStore.Graph` | 只返回 ACTIVE 关系 |
| 来源引用与无来源关系计数 | `RelationStore.SourceRefs` | 有效性由领域层查证据表判断 |
| 计数与 ACK/STALE 扫描 | `RelationStore.Counts` / `StaleSnapshotIDs` | 漂移 SQL 封装在适配器 |
| 快照写入与替换 | `RelationStore.ReplaceRunSnapshot` | 同一 run 的旧快照删除与新快照插入在一个事务内 |
| 审核/失效 | `Approve` / `UpdateReview` / `Update` | ACTIVE 唯一性由适配器事务保证 |
| BFS、深度/节点预算、ACL | `knowledgeprojection` | 不进入端口 |

`StaleSnapshotIDs` 会读取 `documents`/`document_versions`/`knowledge_bases` 做漂移判断。这是 Relation Store 的完整性扫描，不是查询能力；未来图库适配器可以复用同一证据表检查，或由上层注入同一判断。

## 事务与一致性

- `ReplaceRunSnapshot`、`Approve` 在适配器内部是单事务。
- 图写入与 `projection_runs` 更新、图失效与 wiki 失效不再共享事务。调用顺序固定为“先图后 run/wiki”，失败时由 stage 重试与幂等保证收敛：
  - EXTRACT 重试会删除同一 run 的旧快照并重建；
  - FINALIZE 重试可重复设置 PENDING；
  - INVALIDATE 先让图不可查，避免 ACL 变更后继续返回旧图。
- 新适配器必须复现这些幂等语义，并通过同一组适配器测试。

## 被否决的方案

- **继续直连**：改动最小，但 M8 的迁移承诺无法兑现。
- **端口 + 遍历下推**：把种子匹配/BFS/路径也放入端口，能让 Neo4j 发挥原生遍历性能，但 ACL 复核与证据有效性仍在外部，适配器需要回调领域判断；当前图查询本来就是全量加载后内存 BFS，收益不足。触发条件是 M8 容量数据证明图规模或多跳延迟成为瓶颈。

## 后果

- 正向：图存储成为可替换边界；projection 非测试代码不再出现 graph_* 表名；迁移评估可以只针对适配器。
- 代价：跨存储原子性降低；端口快照与 API 模型之间增加一层映射；新增适配器需要维护 `StaleSnapshotIDs` 等完整性语义。
- 非目标：证据读取端口、projection run 存储、检索索引写入端口。

## 迁移检查清单（仅 M8 触发后执行）

1. 新适配器通过 `internal/platform/relationstore` 的契约测试（快照替换、审核 ACTIVE 唯一、来源引用、计数、失效扫描）。
2. 数据迁移与回滚方案经评审；旧 PostgreSQL 图表保留只读窗口。
3. 图查询 P50/P95、快照写入耗时、来源有效性扫描耗时与容量报告对比。
4. API 与权限负向用例（跨租户、隐藏来源、ACL 漂移）全部复跑。