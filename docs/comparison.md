# Comparison

This comparison is based on public project documentation available in November 2026
and on the capabilities implemented in this repository. Other projects change
quickly; verify the current state before procurement. It is not a benchmark.

## Positioning

| Project | Primary focus | Best fit |
| --- | --- | --- |
| **RAG Platform** | Multi-tenant, evidence-grounded enterprise knowledge base with governance and operable ingestion | Teams that need citations, tenant isolation, audit, and a self-hosted Compose deployment |
| **Dify** | LLM application/workflow platform with agents, tools, and model management | Teams building many LLM applications and workflows, not only document Q&A |
| **RAGFlow** | RAG engine with strong document parsing/layout understanding | Teams whose corpus is heavy on complex PDFs and who need deep parsing/OCR |
| **WeKnora** | Knowledge base and agent platform with Wiki/graph-style knowledge capabilities | Teams exploring knowledge-base plus agent/knowledge-graph features |
| **FastGPT** | Knowledge-base Q&A and workflow orchestration | Teams wanting a quick knowledge-base Q&A application with a visual flow builder |

## Capability matrix

| Dimension | RAG Platform | Dify | RAGFlow | WeKnora |
| --- | --- | --- | --- | --- |
| Deployment | Docker Compose (official); no Helm yet | Docker Compose, Kubernetes options | Docker Compose; Kubernetes via community | Docker Compose |
| Document parsing | Digital PDF, DOCX, XLSX, PPTX, Markdown, TXT, HTML | Broad document ingestion | Strong PDF layout/OCR focus | Broad knowledge ingestion |
| OCR / scanned PDFs | Not supported; explicit error | Varies by plugin/provider | Strong | Varies by deployment |
| Hybrid retrieval | BM25 + vector + RRF, optional rerank | Keyword/vector and workflow retrieval | Hybrid retrieval | Hybrid retrieval + knowledge layers |
| Citations / refusal | First-class citation validation; refusal on insufficient evidence | Citations available; behavior depends on workflow | Citations available | Citations available |
| Multi-tenancy | Tenant/KB RBAC, API keys, audit, quotas, optional PostgreSQL RLS | Workspace/tenant model | Team/workspace model | Tenant model |
| Async ingestion reliability | Outbox/inbox, stage state machine, idempotency, dead letters, reconcile | Workflow/task engine | Task pipeline | Task pipeline |
| Observability | Process metrics + OTLP tracing; alerting package not yet delivered | Logging/monitoring integrations | Basic monitoring | Basic monitoring |
| Connectors | `http_manifest` only | Broad integrations/plugins | Multiple data sources | Multiple knowledge sources |
| Agents/workflows | Not a focus | Strong | Limited | Agent/Wiki direction |
| Knowledge graph | Optional relation projection over evidence | Via tools/workflows | Limited | Knowledge-graph direction |
| License | MIT | Apache-2.0 with additional conditions | Apache-2.0 | Check upstream |
| Maturity | Early (0.1.x) | Mature ecosystem | Active ecosystem | Active project |

“Varies” means the capability depends on the version, plugin, or deployment mode;
check the upstream documentation rather than relying on this table.

## Where this project is stronger

- **Evidence discipline**: citation registration, validation, and refusal are part of
  the answer path, not a UI convention.
- **Tenant isolation depth**: application scoping plus optional PostgreSQL RLS,
  static tenancy checks, authorization revisions, and audit.
- **Operable asynchronous ingestion**: explicit stage state, outbox/inbox, leases,
  dead letters, and reconciliation are core primitives.
- **Small, auditable deployment**: one PostgreSQL, Redis, object storage, and a parser
  sidecar; no extra vector database or graph database required.

## Where this project is weaker

- **Parsing breadth**: no OCR, image understanding, or advanced layout extraction.
- **Ecosystem**: few connectors, no agent/workflow builder, no marketplace.
- **Community/maturity**: much smaller than Dify or RAGFlow; APIs and schemas may
  change before 1.0.
- **Deployment options**: Compose only; no official Helm chart or managed service.
- **Operations package**: Prometheus rules, Grafana dashboards, and on-call routing
  are still on the roadmap.
- **Commercial polish**: no billing, packaged SaaS, or formal support offering.

## How to choose

- Choose **Dify** when the main problem is building and operating many LLM
  applications, workflows, and agents.
- Choose **RAGFlow** when complex document parsing and OCR quality dominate.
- Choose **RAG Platform** when the problem is governed enterprise knowledge:
  multi-tenant access, citations, audit, reliable ingestion, and a deployment you can
  inspect and operate yourself.
- Choose **WeKnora/FastGPT** when their specific knowledge or workflow features match
  your use case and their maturity/roadmap fits your risk profile.

The projects are not mutually exclusive. A common pattern is to use a document
platform for parsing and this project for governed retrieval, citations, and tenant
operations.

## Reproducing claims

This repository publishes its evaluation sets and acceptance scripts. Retrieval,
citation, refusal, and permission gates are described in
[tests/eval](https://github.com/hzc19970630/rag-platform/tree/main/tests/eval) and can be run against a local stack:

```bash
./scripts/m3_acceptance.sh
python scripts/m3_eval.py --base-url http://localhost:8082
```

Competitor claims should be verified with your own corpus and acceptance criteria.
Feature matrices are not a substitute for a proof of concept.
