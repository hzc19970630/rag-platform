# Comparison

This comparison is based on public repository metadata (GitHub API) and README
documentation read on **2026-11-13**. Repository activity and versions change
quickly; verify the current state before procurement. It is **not a benchmark**:
no competitor was deployed, loaded with this repository's corpus, or measured
here. Star counts are a distribution signal, not a quality signal.

## Positioning

| Project | Primary focus | Best fit |
| --- | --- | --- |
| **RAG Platform** | Multi-tenant, evidence-grounded enterprise knowledge base with governance and operable ingestion | Teams that need citations, enforced refusal, tenant isolation, audit, and a self-hosted deployment they can review |
| **WeKnora** | Tencent's open-source knowledge platform: RAG, reasoning agents, and a self-maintaining Wiki | Teams that want knowledge Q&A plus agents, skills, IM channels, and a Wiki in one product |
| **RAGFlow** | Agent-native context infrastructure built on deep document understanding | Teams whose corpus is heavy on complex PDFs, scans, and tables |
| **Dify** | LLM application and agentic workflow platform with model/tool management | Teams building many LLM applications and workflows, not only document Q&A |
| **EdgeQuake** | Graph-RAG in Rust implementing the LightRAG algorithm: documents to entity/relation graph, six query modes, one binary plus Postgres | Teams that want LightRAG-style graph retrieval with a production wrapper (auth, SSO, audit, MCP) |
| **Auto-Ontology** | Semantic/ontology layer over structured data: NL-to-SQL, answerability checks, SQL validation/repair, MCP/API | Teams querying relational or lakehouse data where metric definitions and SQL provenance matter |
| **Semantica** | Graph-native context and knowledge-graph infrastructure: OWL/SHACL/SKOS, deterministic reasoning, W3C PROV-O, decision intelligence | Regulated teams needing ontology governance, reasoning, and audit over multi-source knowledge graphs |

## Project snapshot (2026-11-13)

| Project | Stars | Latest version (source) | License | Last push | Deployment |
| --- | ---: | --- | --- | --- | --- |
| RAG Platform | 1 | 0.1.x | MIT (application); bundled dependencies reviewed separately | 2026-11-13 | Docker Compose |
| WeKnora | 37,364 | v0.9.0 (2026-10-29) | MIT per README; GitHub API reports `NOASSERTION` | 2026-11-09 | Compose, Helm, Lite single binary, desktop |
| RAGFlow | 94,112 | 1.0.0 (2026-11-06) | Apache-2.0 | 2026-11-13 | Compose; source build |
| Dify | 161,081 | Not stated in README | Dify Open Source License (Apache-2.0 with additional conditions) | 2026-11-13 | Compose, cloud |
| EdgeQuake | 2,305 | v0.40.7 (README) | Apache-2.0 | 2026-11-13 | Docker, single Rust binary + Postgres |
| Auto-Ontology | 12,392 | Not stated in README | Apache-2.0 | 2026-11-13 | Compose, Helm |
| Semantica | 14,589 | v0.7.0 published / v0.8.0 referenced (README) | MIT | 2026-11-12 | Docker, Kubernetes, Helm |

Repository metadata: GitHub API on 2026-11-13. Versions and capabilities: each
project's README. "Not stated" means the reviewed README does not state it; it
is not proof the capability is absent.

## Capability matrix

Documentation-level comparison. No competitor was tested against this project's
corpus or acceptance criteria.

| Dimension | RAG Platform | WeKnora | RAGFlow | Dify |
| --- | --- | --- | --- | --- |
| Primary input | Documents: PDF, DOCX, XLSX, PPTX, Markdown, TXT, HTML | Documents, images, web sources, IM; Wiki pages | Documents, scans, tables, web pages | Documents and web content through workflows |
| Knowledge model | Chunks; optional relation projection over evidence | Chunks + Wiki pages + Neo4j knowledge graph (GraphRAG) | Chunks with template-based chunking; agentic multi-step retrieval | Chunks/retrieval nodes inside workflows |
| Answer provenance | Citation registration and validation; refusal when evidence is insufficient; permission-negative evaluation | Citations with answer-to-passage navigation; explicit refusal policy not documented | Citations with chunk visualization; evidence verification in agentic RAG; refusal policy not documented | Not documented |
| Multi-tenancy / auth | Tenant/KB RBAC, API keys, audit, quotas; application scoping plus PostgreSQL RLS, static tenancy checks, and authorization revisions | Workspace RBAC with four roles, per-resource ownership, per-workspace audit; row-level isolation not documented | Multi-tenancy/RBAC/audit not documented in the reviewed README | RBAC and SSO in the enterprise edition; multi-tenancy/audit not documented |
| Async ingestion reliability | Outbox/inbox, stage state machine, idempotency, leases, dead letters, reconcile | Task queue and worker-pool governance; reliability primitives not documented at this depth | Task pipeline and Syncer service | Workflow/task engine |
| Parsing / OCR | Digital documents only; scanned PDFs and images are rejected explicitly | Multimodal parsing, OCR preprocessing, image-vector retrieval, EPUB/MHTML | DeepDoc layout analysis, OCR, and table recognition (CPU inference) | Text extraction from common formats; OCR not documented |
| Connectors | `http_manifest` only | 10 sources: Feishu wiki/Drive, Lark, Confluence, GitLab, Tencent IMA, Notion, Yuque, DingTalk Docs, Seafile, RSS | File sources, sitemap ingestion, BigQuery incremental sync; broader catalog not documented | Tools/MCP and custom APIs; document-connector catalog not documented |
| Agent / workflow | Not a focus | ReAct agent, skills, sandboxes, browser, memory, automations | Agent templates, agentic RAG with thinking modes | Strong visual workflow and agent builder |
| Observability | Prometheus metrics, OTLP tracing, 10 alert rules, Alertmanager routing, Grafana dashboards | Langfuse tracing for agent steps and pipelines | Basic monitoring | Observability integrations |
| Browser E2E | Playwright coverage of upload → parse → retrieve → answer → delete plus accessibility | Not documented | Not documented | Not documented |
| Deployment | Compose; no Helm, no offline bundle | Compose, Helm, Lite single binary, desktop | Compose; no Helm in reviewed README | Compose, cloud |
| License | MIT; ParadeDB/MinIO (AGPL) and Redis 7.4 (SSPL/RSALv2) are distribution decisions | MIT (READM); check bundled components | Apache-2.0 | Custom license with conditions |
| Maturity | Early 0.1.x; public repository created 2026-11-03 | Active, Tencent-backed, large community | Active, very large community | Mature ecosystem |

## Graph and ontology projects

Reviewed 2026-11-13 from GitHub metadata and upstream READMEs. These projects are
adjacent to, not drop-in replacements for, document RAG. None was deployed,
loaded with this repository's corpus, or benchmarked here.

| Dimension | RAG Platform | EdgeQuake | Auto-Ontology | Semantica |
| --- | --- | --- | --- | --- |
| Primary input | Documents | Documents; PDF text and vision pipelines | Structured data only; 10 database engines; no document ingestion documented | Files, web, SQL/lakehouse, streams, Git, email, MCP |
| Knowledge model | Chunks; optional relation projection | Entity/relation graph (LightRAG) in Postgres with pgvector and Apache AGE | Ontology/catalog over schemas: terms, SQL attributes, relationships | RDF and labeled property graphs; OWL/SHACL/SKOS; bi-temporal facts |
| Query interface | BM25 + vector + RRF, optional rerank; citation validation and refusal | Six modes: naive, local, global, hybrid, mix, bypass; vector + graph traversal | NL-to-SQL; answerability; SQL validation/repair; MCP, REST | Graph traversal + semantic search; Datalog, Rete, SPARQL; REST, MCP |
| Answer provenance | Validated citations and audit log; PROV-O export not yet delivered | Sources included; answer-level citation validation/refusal not documented | SQL, rows, and reasoning steps returned; answerability refusal | W3C PROV-O on facts; JSON/CSV/RDF export; answer-level citation/refusal not documented |
| Multi-tenancy and auth | Tenant/KB RBAC, API keys, audit, quotas, PostgreSQL RLS, static tenancy checks | Fail-closed workspace isolation, auth, SSO (Keycloak/OIDC), audit; RBAC/row-level isolation not documented | Better Auth + RBAC; per-user MCP calls; multi-tenancy/audit not documented | Single API key documented; multi-tenant/RBAC not documented |
| Async reliability | Outbox/inbox, stage state machine, leases, dead letters, reconcile | Batch ingestion; reliability primitives not documented | Ingestion worker; semantic compilation; reliability primitives not documented | Pipeline stages; reliability primitives not documented |
| Deployment | Compose | Docker, single binary + Postgres; Helm not documented in README | Compose, Helm | Docker/Kubernetes/Helm recommended for production |
| License | MIT | Apache-2.0 | Apache-2.0 | MIT |
| Maturity | Early 0.1.x | v0.40.7; active; self-published LightRAG comparison (n=200) | NVIDIA-maintained; active; not accepting external contributions | Active; self-published v0.5.0 benchmarks; Rete matcher flagged as simple |
| Tested here | M3/M7 self-evaluation | Not tested | Not tested | Not tested |

### Evidence notes

- **WeKnora**: [repository](https://github.com/Tencent/WeKnora), README on
  `main`, v0.9.0 (2026-10-29). GitHub API on 2026-11-13: 37,364 stars, 5,182
  forks, last push 2026-11-09. MIT per README; the API reports `NOASSERTION`.
  The README documents workspace RBAC, per-workspace audit, multimodal parsing,
  10 data sources, 10 IM channels, Compose/Helm/Lite/desktop deployment, Neo4j
  GraphRAG, skills/sandboxes, and recall + BLEU/ROUGE evaluation.
- **RAGFlow**: [repository](https://github.com/infiniflow/ragflow), README on
  `main`, 1.0.0 released 2026-11-06. GitHub API on 2026-11-13: 94,112 stars,
  11,318 forks, last push 2026-11-13. Apache-2.0. DeepDoc performs layout
  analysis, OCR, and table recognition with CPU inference. The reviewed README
  documents agentic RAG and citations, but not multi-tenancy, RBAC, or audit.
- **Dify**: [repository](https://github.com/langgenius/dify), README on `main`.
  GitHub API on 2026-11-13: 161,081 stars, 25,474 forks, last push 2026-11-13.
  The Dify Open Source License is based on Apache-2.0 with additional
  conditions; review it before redistribution. RBAC/SSO are described for the
  enterprise edition; multi-tenancy, audit, citations, and OCR are not
  documented in the reviewed README.
- **EdgeQuake**: [repository](https://github.com/raphaelmansuy/edgequake), README
  on `edgequake-main`, version banner 0.40.7. GitHub API on 2026-11-13: 2,305
  stars, 276 forks, last push 2026-11-13; repository created 2025-12-21.
  Apache-2.0. Documents six query modes, Apache AGE + pgvector, SSO, audit, and
  fail-closed workspace isolation. No connector catalog or Helm deployment is
  documented in the README. The LightRAG comparison is self-published (n=200)
  and was not reproduced here.
- **Auto-Ontology**: [repository](https://github.com/NVIDIA/auto-ontology),
  README on `main`. GitHub API on 2026-11-13: 12,392 stars, 1,017 forks, last
  push 2026-11-13; repository created 2026-04-27. Apache-2.0. NVIDIA maintains
  the project and does not accept external pull requests. It ingests structured
  data only and documents answerability, SQL validation/repair, MCP, Compose,
  and Helm; multi-tenancy and audit are not documented.
- **Semantica**: [repository](https://github.com/semantica-agi/semantica), README
  on `main`. GitHub API on 2026-11-13: 14,589 stars, 1,723 forks, last push
  2026-11-12. MIT. Documents RDF/LPG, OWL/SHACL/SKOS, Datalog/Rete/SPARQL,
  PROV-O lineage, and many connectors. Multi-tenancy, RBAC, and answer-level
  citation/refusal are not documented. Its benchmarks are self-published
  (v0.5.0), and the README flags the Rete alpha-node matcher as intentionally
  simple.
- **Not tested**: no source-code audit, deployment, corpus run, security test, or
  benchmark was performed against any competitor by this project. Rows are
  documentation review only; verify current upstream documentation before
  procurement.

## Where this project is stronger

- **Evidence discipline**: citation registration, validation, and refusal are part of
  the answer path, not a UI convention.
- **Tenant isolation depth**: application scoping plus PostgreSQL RLS, static
  tenancy checks, authorization revisions, and audit. None of the compared
  projects documents row-level isolation.
- **Operable asynchronous ingestion**: explicit stage state, outbox/inbox, leases,
  dead letters, and reconciliation are core primitives.
- **Reviewable delivery**: one OpenAPI source of truth, generated TypeScript
  types, CycloneDX SBOM, cosign-signed images, upgrade/rollback runbooks,
  reproducible evaluation, and a Prometheus/Grafana/Alertmanager package.
- **Small, auditable deployment**: one PostgreSQL, Redis, object storage, and a parser
  sidecar; no extra vector database or graph database required.

## Where this project is weaker

- **Parsing breadth**: no OCR, image understanding, or advanced layout extraction.
  RAGFlow and WeKnora are substantially ahead here.
- **Connectors and channels**: one connector; no IM channels. WeKnora documents
  10 sources and 10 IM channels.
- **Application breadth**: no agent builder, skills sandbox, or workflow canvas.
  Dify, WeKnora, and RAGFlow cover these.
- **Zero-configuration onboarding**: no setup wizard, sample knowledge base, or
  system doctor; deployment still exposes many environment variables. WeKnora
  offers onboarding, a Lite binary, and a desktop app.
- **Deployment options**: Compose only; no official Helm chart or offline bundle.
- **Community/maturity**: much smaller than WeKnora, RAGFlow, or Dify. APIs and
  schemas may change before 1.0.
- **Distribution licensing**: the application is MIT, but ParadeDB and MinIO
  (AGPL) and Redis 7.4 (SSPL/RSALv2) must be resolved before OEM distribution.

## How to choose

- Choose **Dify** when the main problem is building and operating many LLM
  applications, workflows, and agents.
- Choose **RAGFlow** when complex document parsing, OCR, and table extraction
  dominate.
- Choose **WeKnora** when you want knowledge Q&A plus agents, skills, IM
  channels, and a Wiki in one product, and you value ecosystem breadth and
  day-one onboarding.
- Choose **RAG Platform** when the problem is governed enterprise knowledge:
  multi-tenant access, enforced citations and refusal, audit, reliable
  ingestion, and a deployment you can inspect and operate yourself.
- Choose **EdgeQuake** when LightRAG-style graph retrieval is the core requirement
  and a single-binary Postgres deployment with built-in auth, SSO, and audit matters.
- Choose **Auto-Ontology** when the data is mainly relational or lakehouse and the
  hard problem is metric definitions and correct NL-to-SQL.
- Choose **Semantica** when you need ontology governance, deterministic reasoning,
  and regulator-facing provenance over multi-source knowledge graphs.

For regulated private deployments, the decisive questions are usually not
feature counts but: can the vendor prove row-level isolation, can the answer be
traced to an authorized evidence chunk, can a deletion or permission change take
effect immediately, and can the delivered artifact pass a security review. This
project is optimized for those questions; WeKnora and RAGFlow are optimized for
breadth and document understanding.

EdgeQuake, Auto-Ontology, and Semantica are adjacent layers, not drop-in
replacements for an evidence-governed document RAG platform. They are not
mutually exclusive: a common pattern is to use a document platform for parsing
and this project for governed retrieval, citations, and tenant operations, or to
integrate graph/ontology engines through MCP.

## Reproducing claims

This repository publishes its evaluation sets, harness, and self-measured
reports. Metric definitions, gates, data validation, limitations, and the
one-command workflow are documented in [Evaluation](evaluation.md); the sets
live in [tests/eval](https://github.com/hzc19970630/rag-platform/tree/main/tests/eval).

```bash
make eval-validate   # offline: dataset validation + harness tests
make eval-suite      # m3 then m7 against a quickstart stack
make eval-public     # same run, redacted, reports in tests/eval/public
```

`make eval-validate` needs no stack. The other two need a running quickstart
deployment; a deployment with dev auth disabled also needs `--auth api-key` and
one API key per permission-probe identity. See
[Evaluation](evaluation.md) for the exact flags.

The reported numbers are this project's own measurements on synthetic fixtures.
They are not a third-party benchmark and do not measure competitor products.
The capability matrix above is a documentation review, not a benchmark; see the
evidence notes for sources, dates, and untested items. Competitor claims should
be verified with your own corpus and acceptance criteria; feature matrices are
not a substitute for a proof of concept.