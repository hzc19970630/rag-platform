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
| **EdgeQuake** | Graph-RAG in Rust implementing the LightRAG algorithm: documents to entity/relation graph, six query modes, one binary plus Postgres | Teams that want LightRAG-style graph retrieval with a production wrapper (auth, SSO, audit, SDKs, MCP) |
| **Auto-Ontology** | Semantic/ontology layer over structured data: NL-to-SQL, answerability checks, SQL validation/repair, MCP/API | Teams querying relational or lakehouse data where metric definitions and SQL provenance matter |
| **Semantica** | Graph-native context and knowledge-graph infrastructure: OWL/SHACL/SKOS, deterministic reasoning, W3C PROV-O, decision intelligence | Regulated teams needing ontology governance, reasoning, and audit over multi-source data |

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

## Graph and ontology projects

Reviewed 2026-11-08 from upstream READMEs and GitHub repository metadata. These
projects are adjacent to, not drop-in replacements for, document RAG. None was
deployed, loaded with this repository's corpus, or benchmarked here. “Not
documented” means the reviewed README does not state the capability; it is not
proof that the capability is absent.

| Dimension | RAG Platform | EdgeQuake | Auto-Ontology | Semantica |
| --- | --- | --- | --- | --- |
| Primary input | Documents: PDF, DOCX, XLSX, PPTX, Markdown, TXT, HTML | Documents; PDF text and vision pipelines documented | Structured data only: 10 relational/analytical engines; no document ingestion documented | Files, web, SQL/lakehouse, streams, Git, email, MCP |
| Knowledge model | Chunks; optional relation projection over evidence | Entity/relation graph (LightRAG) in Postgres with pgvector and Apache AGE | Ontology/catalog over schemas: terms, SQL attributes, relationships | Context Graph and KG; OWL/SHACL/SKOS; bi-temporal facts |
| Query interface | BM25 + vector + RRF, optional rerank; citation validation and refusal | Six modes: naive, local, global, hybrid, mix, bypass; vector + graph traversal | NL-to-SQL; answerability; SQL validation/repair; MCP, REST, CLI | Graph traversal + semantic search; Datalog, Rete, SPARQL; REST, MCP, CLI |
| Answer provenance | Validated citations and audit log; no PROV-O export yet | Answers include sources; answer-level citation validation/refusal not documented | SQL, rows, and reasoning steps returned; answerability refusal | W3C PROV-O on facts; JSON/CSV/RDF export; answer-level citation/refusal not documented |
| Multi-tenancy and auth | Tenant/KB RBAC, API keys, audit, quotas, optional PostgreSQL RLS | Fail-closed workspace isolation, built-in auth, SSO (Keycloak/OIDC), audit log | Better Auth + RBAC, per-user MCP calls; tenant isolation not documented; ports 3001/3002 must stay private (ingestion has no auth) | Single API key documented; tenant/RBAC not documented in README |
| Async ingestion reliability | Outbox/inbox, stage state machine, idempotency, dead letters, reconcile | Batch ingestion documented; outbox/dead-letter primitives not documented | Ingestion worker and semantic compilation; reliability primitives not documented | Pipeline stages; reliability primitives not documented |
| Connectors and integrations | `http_manifest` only | Upload/API, MCP server, SDKs (Python, TypeScript, Rust, Go, and more), OpenAPI/SSE | 10 relational/analytical engines; MCP server, REST API, CLI | Files, web, SQL/lakehouse, streams, Git, email, MCP; Agno, CrewAI, LangChain |
| Deployment | Docker Compose; no Helm | Docker quickstart; repo-only Helm charts (chart 0.1.0, appVersion 0.26.4); single Rust binary; Postgres as primary datastore (optional object storage for originals); multi-arch images | Docker Compose or Helm/Kubernetes; NVIDIA NIM or OpenAI-compatible endpoints | `pip install`; Docker/Kubernetes recommended for production; external RDF/LPG/vector stores |
| License | MIT | Apache-2.0 | Apache-2.0 | MIT |
| Maturity | Early 0.1.x | v0.40.7; active; self-published LightRAG comparison (n=200) | NVIDIA-maintained, active; not accepting external contributions | Active; README publishes v0.5.0 benchmarks and flags a simple Rete alpha-node matcher |
| Tested here | M3/M7 self-evaluation | Not tested | Not tested | Not tested |

### Evidence notes

- **EdgeQuake**: [repository](https://github.com/raphaelmansuy/edgequake),
  README on `edgequake-main`, version banner 0.40.7. Apache-2.0. GitHub API on
  2026-11-08: 2,297 stars, last push 2026-11-08. The LightRAG comparison
  (`Honest numbers`) is self-published (n=200) and was not reproduced here.
- **Auto-Ontology**: [repository](https://github.com/NVIDIA/auto-ontology),
  README on `main`. Apache-2.0; the README states the project is not accepting
  external contributions. GitHub API on 2026-11-08: 9,849 stars, last push
  2026-11-07. The README (`How it works`) warns that ports 3001/3002 must stay
  private: the backend trusts a forwarded user header and the ingestion service
  has no authentication of its own.
- **Semantica**: [repository](https://github.com/semantica-agi/semantica),
  README on `main`. MIT. GitHub API on 2026-11-08: 14,471 stars, last push
  2026-11-08. The README (`semantica.reasoning`) acknowledges that the Rete
  alpha-node matcher is intentionally simple in this release and should be
  validated before production compliance use; its benchmarks are from v0.5.0.
- **Not tested**: no source-code audit, deployment, corpus run, security test, or
  benchmark was performed against any of the three by this project. Rows are
  documentation review only; verify current upstream documentation before
  procurement.

## Public packages and dependencies

Reviewed 2026-11-08 from upstream manifests, lockfiles, and registry metadata.
Nothing was installed, built, or deployed. A lockfile-level SBOM/CVE snapshot
(OSV API) was run on 2026-11-08; see "Dependency vulnerability snapshot" below.
Counts are manifest entries; version correspondence is verified only where
stated.

### Published and repo-only artifacts

| Project | Published artifacts | Repo-only artifacts |
| --- | --- | --- |
| EdgeQuake | GHCR images `ghcr.io/raphaelmansuy/edgequake`, `edgequake-frontend`, `edgequake-postgres` (multi-arch); GitHub release v0.40.11 (2026-11-07, no assets); PyPI `edgequake-sdk` 0.4.1 (2026-10-11); npm `edgequake-sdk` 0.1.0; crates.io `edgequake-sdk` 0.4.1 | Server workspace crates (`edgequake`, `edgequake-core`, `edgequake-api` not found on crates.io); `edgequake-llm` 0.10.10 is a separate repository, not the server; Java/Kotlin/C#/PHP/Ruby/Swift/Go SDK sources; Helm charts `edgequake` and `edgequake-stack`; private web UI (`edgequake_webui`) |
| Auto-Ontology | NGC images `nvcr.io/nvidia/auto-ontology`, `auto-ontology-frontend`, `auto-ontology-mcp` 1.2.0; Helm chart `auto-ontology` 1.2.0 from `https://helm.ngc.nvidia.com/nvidia` | Python server and MCP packages not on PyPI; private Next.js frontend; no GitHub release observed; Compose builds local `auto-ontology:latest` and `auto-ontology-frontend:latest` |
| Semantica | PyPI `semantica` 0.7.0 (2026-09-22; MIT; Python >=3.10,<3.14); console scripts `semantica`, `semantica-server`, `semantica-worker`, `semantica-explorer`, `semantica-mcp` | Knowledge Explorer image built locally; Helm chart `knowledge-explorer` 0.1.0 installed from a local path; private npm package; no published image reference or Helm repository found |

### Manifests and dependency footprint

| Project | Manifests | Dependency footprint | Lockfiles |
| --- | --- | --- | --- |
| EdgeQuake | Cargo workspace with 15 member crates plus the root `edgequake` server crate, version 0.40.7, Rust 1.95; `edgequake_webui/package.json` private, 0.40.7, pnpm 10.13.1 | 15 member crates plus server; web UI 72 runtime and 19 dev | `Cargo.lock` 703 packages |
| Auto-Ontology | Root `pyproject.toml` 1.2.0 (Python >=3.12,<3.14); `mcp/pyproject.toml` 1.2.0 (Python >=3.11); `frontend/package.json` private 1.2.0; Helm chart 1.2.0 | 37 server core dependencies plus optional `kumo`; 4 MCP dependencies; 23 frontend runtime and 17 dev | `uv.lock` 147 packages; `mcp/uv.lock` 78 packages |
| Semantica | `pyproject.toml` 0.8.0 (Python >=3.11,<3.14, MIT); `package.json` private 0.1.0; Helm chart 0.1.0 | 22 core dependencies; 69 extras | `uv.lock` 461 packages; `requirements-ci.txt` about 588 KB |

### Notable dependencies and external services

| Project | Notable runtime dependencies | External services and credentials |
| --- | --- | --- |
| EdgeQuake | API, auth, audit, storage, pipeline, query, PDF, observability, rate-limiter, secrets, tasks crates; Next.js UI with Radix UI, TanStack Query, Sigma | An LLM provider key or a local server (Ollama, LM Studio, llama.cpp); PostgreSQL with pgvector and Apache AGE; optional Langfuse for traces |
| Auto-Ontology | FastAPI, LangChain/LangGraph, SQLAlchemy/Alembic, pandas, DuckDB, SQLGlot, `openai==2.44.0`, `hvac`; Better Auth and Prisma in the frontend | NVIDIA NIM API key by default, or an OpenAI-compatible endpoint; PostgreSQL with pgvector; optional HashiCorp Vault; 10 source database engines; OSSIE converter fetched from a pinned Git commit |
| Semantica | NumPy, pandas, SciPy, scikit-learn, RDFLib, NetworkX, Pydantic, Click/Rich, protobuf/gRPC, Pillow | Deterministic core needs no LLM; optional LLM providers, RDF/LPG stores, vector stores, and enterprise connectors only when the corresponding extras are installed |

### Version and packaging drift

| Artifact | Source tree | Published | Note |
| --- | --- | --- | --- |
| EdgeQuake server | 0.40.7 (`VERSION`, workspace) | GitHub release v0.40.11 (2026-11-07) | Release is ahead of the README and tree |
| EdgeQuake Helm charts | Chart 0.1.0, appVersion 0.26.4 | No published chart repository found | Charts trail the server by many minors |
| EdgeQuake TypeScript SDK | 0.4.1 in `sdks/typescript` | npm 0.1.0 | Published package lags the tree |
| EdgeQuake Python and Rust SDKs | 0.4.1 | PyPI and crates.io 0.4.1 | Aligned |
| EdgeQuake Java and Go SDKs | Java `io.edgequake:edgequake-sdk` 0.4.0; Go module `github.com/edgequake/edgequake-go` | Java artifact not found on Maven Central; Go repository returns 404 | Source-only and not resolvable as published |
| Auto-Ontology | 1.2.0 | NGC images and Helm chart 1.2.0 | Aligned; Python packages remain Git/`uvx`-only |
| Semantica package | 0.8.0, Python >=3.11 | PyPI 0.7.0, Python >=3.10 | Published package lags; Python floor differs |
| Semantica chart | Chart 0.1.0, appVersion 0.5.1 | Local chart only | Chart trails the package; image tag is empty in values |
| Semantica extras | 69 extras | 62 extras on PyPI 0.7.0 | Seven extras are repo-only; Pillow floor differs (>=13.0.0 vs >=12.2.0) |

- **Licences**: EdgeQuake and Auto-Ontology are Apache-2.0; Semantica is MIT.
  Auto-Ontology ships consolidated third-party notices at the repository root,
  `frontend/`, and `mcp/`. The reviewed Semantica tree has `LICENSE` and
  `osv-scanner.toml` but no consolidated third-party notice, so generate one
  from the lockfile before redistribution.
- **Supply chain**: Auto-Ontology pins `apache-ossie-nvidia-auto-ontology` to
  commit `cb4b46b97bcdb9f2685ef2f7023b9134a4426da2` in
  `apache/ossie-converters`; that repository is part of the build graph even
  though the server itself is not published to PyPI.
- **Deployment templates**: EdgeQuake's quickstart Compose defaults image tags
  to `latest`, and its example comments still show
  `EDGEQUAKE_VERSION=0.26.4`; pin image digests for reproducible procurement.
  Semantica's chart leaves the image tag empty, so the image must be supplied
  at install time.
- **Semantica version split**: PyPI 0.7.0 lacks the repo-only extras
  `db-cassandra`, `ingest-dynamics365`, `ingest-looker`, `ingest-servicenow`,
  `ingest-tableau`, `llm-typesafe`, and `nlp-langdetect`; it also relaxes the
  Pillow floor from `>=13.0.0` to `>=12.2.0`. Install the version whose
  metadata matches the intended feature set.
- **Scope**: counts exclude transitive packages except where lockfile totals are
  shown. The OSV scan below is lockfile-level and is not an install, build, or
  deployment audit. Verify the exact published version's metadata, not `main`.

### Dependency vulnerability snapshot

Run 2026-11-08 with a lockfile parser and the OSV API, without installing or
building any target and without a Trivy vulnerability DB. Components are
lockfile entries and include dev/build dependencies. Severity is taken from OSV;
Go, RustSec, and Alpine advisories often omit it, so `UNKNOWN` is not `LOW`.
Fixed versions are the advisory's advertised fixes and may include backports on
other major lines; check compatibility before upgrading.

| Target | Components | Unique advisories | Findings | HIGH/CRITICAL findings | Fixable |
| --- | --- | --- | --- | --- | --- |
| This repository, lockfiles | 360 | 2 | 2 | 0 (OSV `UNKNOWN`) | 2 |
| This repository, api image SBOM (Alpine 3.22.8) | 218 / 84 unique | 3 | 3 | 0 (OSV `UNKNOWN`) | 3 |
| EdgeQuake server (`Cargo.lock`) | 703 | 21 | 28 | 10 | 19 |
| EdgeQuake web UI (pnpm) | 1168 | 25 | 26 | 13 | 26 |
| EdgeQuake Python SDK | 31 | 0 | 0 | 0 | 0 |
| EdgeQuake TypeScript SDK | 204 | 1 | 1 | 0 | 1 |
| EdgeQuake Rust SDK | 222 | 9 | 9 | 2 | 8 |
| Auto-Ontology server | 146 | 39 | 40 | 11 | 40 |
| Auto-Ontology MCP | 77 | 27 | 28 | 9 | 28 |
| Auto-Ontology frontend | 1011 | 41 | 46 | 19 | 45 |
| Semantica package | 460 | 2 | 2 | 1 | 2 |
| Semantica CI requirements | 413 | 1 | 1 | 0 | 1 |

- **This repository**: `klauspost/compress` 1.18.7 (GO-2026-6865 /
  CVE-2026-116591, fixed 1.20.2) and `golang.org/x/crypto` 0.58.0 (GO-2026-5932,
  unmaintained `openpgp`, fixed after 0.58.0). `govulncheck` reports no reachable
  vulnerability, `npm audit` reports zero for `web`, and the image adds Alpine
  `zlib` 1.3.2-r1 (ALPINE-CVE-2026-4738, fixed 1.4-r0). The v0.1.0 release image
  passed the CI Trivy HIGH/CRITICAL gate.
- **EdgeQuake**: `zip` 2.4.2 and 3.0.0 are CRITICAL (CVE-2026-115905,
  RUSTSEC-2026-0369; fixed in 9.0.x), with `quinn-proto` HIGH in the Rust SDK.
  The web UI's 13 HIGH findings are mainly `brace-expansion`, `browserslist`,
  `js-yaml`, and `undici`.
- **Auto-Ontology**: CRITICAL `next` 16.3.8, `better-auth` 1.7.4, and
  `dompurify` 3.4.16 in the frontend; CRITICAL `authlib` 1.7.2 and `certifi`
  in MCP; `certifi` CRITICAL plus HIGH `aiohttp`/`oauthlib`/`pyasn1` on the
  server.
- **Semantica**: HIGH `oauthlib` 4.0.0 (CVE-2026-49272, fixed 4.0.1) in the
  package and one MEDIUM in CI requirements; the lowest advisory count of the
  four projects. Its published PyPI 0.7.0 was not scanned separately.

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
- Choose **EdgeQuake** when LightRAG-style graph retrieval is the core requirement
  and a single-binary Postgres deployment with built-in auth, SSO, and audit matters.
- Choose **Auto-Ontology** when the data is mainly relational or lakehouse and the
  hard problem is metric definitions and correct NL-to-SQL.
- Choose **Semantica** when you need ontology governance, deterministic reasoning,
  and regulator-facing provenance over multi-source knowledge graphs.
- Choose **WeKnora/FastGPT** when their specific knowledge or workflow features match
  your use case and their maturity/roadmap fits your risk profile.

EdgeQuake, Auto-Ontology, and Semantica are adjacent layers, not drop-in
replacements for an evidence-governed document RAG platform.

The projects are not mutually exclusive. A common pattern is to use a document
platform for parsing and this project for governed retrieval, citations, and tenant
operations.

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
The graph/ontology matrix above is a documentation review, not a benchmark; see
its evidence notes for sources, dates, and untested items. Competitor claims
should be verified with your own corpus and acceptance criteria; feature
matrices are not a substitute for a proof of concept.
