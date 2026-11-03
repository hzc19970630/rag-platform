一、做得好的部分（作为判断基准）
能力门控做得比多数 RAG 项目扎实：Wiki/Relation/Evidence 独立就绪，任一投影失败不阻断 Evidence RAG（internal/retrieval/capabilities.go、knowledgeprojection/join.go）。
可靠性原语完整：Outbox/Inbox、阶段状态机、幂等键、死信、reconcile、SSE 事件重放（conversation.ReplayEvents）。
安全边界清楚：召回 SQL 内置租户/KB/current-version 过滤，交付前再授权复核；对象存储键不出 API；解析器独立容器 + 沙箱限制。
评估与容量工具已成体系：49 用例门槛、M8 容量报告，可作为架构变更的回归护栏。
二、扩展性评估
1. 模型 / 维度扩展：最大的发布级约束
chunks.embedding 是单列 halfvec，HNSW 是按单一维度建的部分索引（internal/platform/vector/index.go:19-35）。embedding_model 虽然已写入，但检索只按 embedding_dimension 过滤。含义：

换 embedding 模型 = 全量重建索引，无法灰度；
同一 KB 不能绑定不同模型；
回滚只能重建。
建议方向：把向量拆到 chunk_embeddings(chunk_id, model, dim, embedding)，每个活跃模型一个部分索引；先双写 + 影子检索，再切主。这是未来 12 个月最可能撞上的扩展点，优先级高于任何检索后端替换。

2. 检索后端可插拔：端口在，工厂不在
KeywordSearcher / VectorSearcher / ChunkExpander 端口存在，但组合根硬编码 PostgreSQL 适配器（internal/app/application.go），不能按 KB/租户选择实现；DESIGN 所说的单一 Retriever 端口并不存在。结论：现在不换是对的（YAGNI），但“换 OpenSearch/Milvus 只改适配器”目前不成立，需先补一个按配置选择 adapter 的工厂。

3. 图查询 / RelationStore：设计承诺与实现不一致
M8 写的是“迁移必须通过 RelationStore 端口”，但代码里没有这个接口：graph_query.go 的查询、relation_worker.go 的写入都直接走 GORM 的 graph_* 表。要接 Neo4j，改动会扩散到 service/query/worker/join/tests。建议：要么在 M8 立项时补端口并加 ADR，要么从 MILESTONES 删除该承诺，避免后续按文档做迁移时踩空。

4. 连接器扩展：单类型硬编码
datasource 只有 http_manifest，类型校验是单分支（service.go:184），文件扩展名白名单另有一份（worker.go:475）。新增 Git/网盘/IM 需要同时改 service、fetch、worker。建议引入 connector registry（type → fetcher + manifest parser），这是 M8“按渠道需求增加连接器”的前置条件。

5. 多租户与规模
隔离靠应用层 tenant_id 过滤，迁移中没有任何 RLS（grep 命中 0）。面向强合规客户时，加 Postgres RLS 是成本最低的纵深防御。
chunks 无分区，单表同时承载 OLTP、BM25、ANN、图数据。这个“单 PG”决策在首期正确，但容量增长后应按租户或 KB 分区，读路径走副本。
worker 单进程共享 Concurrency，扩容粒度粗；asynq 支持按队列拆进程，等容量报告触发即可。
三、设计冗余（可量化）
冗余	证据	影响
两个完全相同的 Embedder 接口	ingestion/ports.go:15、retrieval/ports.go:61；projection 依赖 ingestion 版本	模型能力变更要改两处
Hit 与 Candidate 重复 12 个字段	retrieval/types.go:57/75	新增字段两处同步
三份 Outbox publisher	ingestion/outbox.go 108 行、datasource/outbox.go 92 行、knowledgeprojection/outbox.go 102 行	退避/重试/SKIP LOCKED 逻辑漂移
参数校验两份	config.go:88 与 retrieval/params.go:122，规则逐条重复；Apply/Overlay 各 13 字段	改一个范围要改两处
格式白名单四处	ingestion/service.go:446、ingestion/worker.go:538、datasource/worker.go:475、config.SupportedFormats	上次新增 xlsx/pptx/html 已需多点同步
CJK bigram / token 估算 / 向量字面量各两份	search/bm25.go:147 vs llm/local.go:89；retrieval/service.go:389 vs llm/local.go:138；vector/search.go:86 vs ingestion/worker.go	分词与预算行为可能不一致
状态字符串散落 SQL	'EVIDENCE_READY' 出现在 bm25.go:62、vector/search.go:39、expand.go:56、reconcile.go 等	改状态名会漏改
错误分类靠字符串前缀	"terminal:" 非测试代码 40 处；worker.go:95 用 HasPrefix 判断，:516 用 SplitN 解析	文档内容或 provider 消息若含该前缀会误判为不可重试，应改 TerminalError + errors.As
配置旋钮多于能力	.env.example 108 个键；OTEL_*、POSTGRES_HOST 等仍无读取方；可观测性是进程内 registry，无 Prometheus/OTel 导出	“假旋钮”与运维盲区
路由按里程碑命名	m1.go…m9.go，cmd/api/main.go:33-40 注册	同一领域逻辑会散落在多个 m*.go，后续新人难定位
四、耦合评估
共享 models + 直接注入 *gorm.DB：所有领域服务直接依赖 GORM 模型，domain → platform 的依赖方向与 DESIGN §6.1 相反。对单库系统这是务实取舍，不建议现在抽 Repository；但应明确“不再新增跨域 raw SQL”的约束。
projection 与 ingestion 通过表和结构体耦合：knowledgeprojection/worker.go:539 直接查 evidence_packages 并反序列化成 ingestion.EvidencePackage。这是跨域契约，应移到 contracts/ 并提供 EvidenceReader 端口，否则 ingestion 改字段会断投影。
knowledgeprojection 是当前最重的包：5,546 行 / 13 文件，service.go 1,021 行，Wiki 与 Relation 两套管道共享一个 Service。建议拆为 projection/wiki 与 projection/relation 两个子包，共享 run/stage 存储层——这是收益最高的一次结构重构。
transport 越界写库：httpapi/m4.go:452 的运维快照直接查 4 张表，属于用例逻辑放在 handler。建议移入 ops/governance 服务，并禁止新 handler 使用 application.DB。
app.Application 是服务定位器：handler 可触达任意服务，模块边界靠自觉。保持显式构造函数是对的，但可收窄每个 RegisterMxRoutes 只接收所需依赖。
配额检查存在 TOCTOU：ingestion/service.go:94 的 COUNT/SUM 在写入事务之外（:202），并发上传可超卖 MaxDocuments。这是正确性问题，不只是扩展性问题；高并发前需改为事务内校验或 DB 约束。
五、方案合理性判断
决策	判断	条件
单 PostgreSQL + pgvector + ParadeDB	合理，首期省掉双写与一致性成本	当 ANN 查询或索引写入与 OLTP 争用超过 M8 阈值时再拆
双索引 + 关系投影 + 按能力门控	优秀，是项目最有价值的设计	保持；不要让图查询进入默认问答计划
Outbox/Inbox + 阶段状态机	合理且必要	代价是 3 套 outbox，应收敛
Casbin + revision + Redis 失效	稳健，正确性不依赖消息投递	到跨租户复杂关系再评估 OpenFGA，且禁止双写
在线同步 embedding + 双路召回	可接受	建议补 query embedding 缓存与超时预算
前后端同仓、显式 DI	合理	不引入 DI 容器，不拆服务
六、优化路线
P0（低风险、先做）

收敛事实源：format registry、CJK/token/vector 工具、统一 Embedder、参数校验单一 spec。
类型化错误（TerminalError）与状态枚举（含 SQL 拼接）。
knowledgeprojection 拆子包；EvidencePackage 移到 contracts/，加 EvidenceReader。
可观测性接线：Prometheus/OTel 导出，清理无读取方的 OTEL_* 等配置（当前 108 个键中仍有死键）。
修 checkQuota 的 TOCTOU。
P1（有明确触发条件再做） 6. 多模型/多维度向量表 + 双写切换方案。 7. Retriever 工厂：按配置选择检索 adapter（OpenSearch/Milvus 仍等 M8 阈值）。 8. RelationStore 端口：接 Neo4j 前必须完成，否则 M8 迁移承诺无效。 9. chunks 分区、读副本、worker 按队列拆池（均以容量报告为门槛）。

P2（产品明确后再做） 10. Chunk/Document 级 ACL + Postgres RLS。 11. Connector registry（Git/网盘/IM）、Agent/MCP、OCR、多模态。 12. 运维快照与审计查询从 handler 迁入 governance 服务。

明确不建议现在做：拆微服务、提前抽象 Repository、引入 DI 框架/消息总线、为了“未来可能”同时维护 OpenSearch + PG 两套检索。

七、需要你决策的两件事
M8 的两个端口承诺：RelationStore 与 Retriever 是“本轮实现”还是“从文档删除”？二者只能选一个，否则后续迁移会按文档做出错误预期。
向量多模型路线：是否接受“一个 KB 一个 embedding 模型”的长期约束？若不能，P1-6 应提前到下一个迭代，因为它决定表结构，越晚改代价越大。
