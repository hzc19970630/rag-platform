# Roadmap

This is a public, capability-level roadmap. It is not a commitment to dates or
scope. Items move based on user feedback, security findings, and deployment
evidence. Detailed acceptance criteria live in the issue tracker.

## Now

- Stabilize the 0.1.x release line: security fixes, upgrade/rollback drills, and
  documentation corrections.
- Browser E2E coverage for the core flow: upload → parse → retrieve → answer →
  delete, plus accessibility checks.
- Operational visibility: queue/outbox/projection metrics, trace propagation
  through asynchronous work, and actionable alerts.
- Quickstart and deployment documentation that work on a clean machine.

## Next

- Observability package: Prometheus rules, Grafana dashboards, SLOs, and on-call
  routing for self-hosted deployments.
- Connector breadth beyond `http_manifest` (for example SharePoint, Confluence,
  Feishu/WeCom, and S3), preserving incremental sync, tombstones, and permission
  inheritance.
- Finer-grained permissions: department/folder inheritance and document-level ACL.
- Model and provider management: add providers without code changes, bind
  chat/embedding/rerank per knowledge base, health/degradation, and audit.
- Retrieval quality operations: replayable evaluation sets, rerank on/off
  comparisons, and regression alerts.

## Later

- OCR and richer document parsing for scanned and mixed PDFs, with explicit
  review states.
- Configuration center with layered inheritance, diff, audit, rollback, and
  secret write-only semantics.
- Kubernetes/Helm deployment, multi-model embedding routing, and re-embedding
  orchestration at scale.
- Data-plane extensions only when capacity evidence shows PostgreSQL/pgvector is
  the bottleneck.

## Explicit non-goals for now

- A general-purpose agent sandbox or arbitrary code execution platform.
- Replacing every storage dependency with a pluggable vendor abstraction.
- Billing, packaging, or a hosted SaaS offering before design-partner validation.
- Claiming parity with larger platforms on features that are not implemented and
  tested here.

## How to influence the roadmap

Open an issue describing the deployment scenario, the problem, and the acceptance
criteria. Design partners and reproducible capacity/quality evidence weigh more
than feature counts.
