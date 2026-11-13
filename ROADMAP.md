# Roadmap

This is a public, capability-level roadmap. It is not a commitment to dates or
scope. Items move based on user feedback, security findings, and deployment
evidence. Detailed acceptance criteria live in the issue tracker.

## Direction

Build a governed knowledge engine that is **integrated, reviewable, and
vertically deliverable**, not a broader agent platform.

- **Evidence is enforced**: citations point to authorized evidence, and the
  answer path refuses when evidence is insufficient.
- **Permissions are part of retrieval**: application scoping plus PostgreSQL
  row-level security, static tenancy checks, and immediate revocation.
- **The delivery is reviewable**: one OpenAPI source of truth, SBOM, signed
  images, upgrade/rollback runbooks, and reproducible evaluation.
- **Vertical differences are data, not forks**: preset packs carry parsing,
  retrieval, terminology, permission, masking, and evaluation defaults.

Success is measured by four outcomes rather than feature counts: first security
review pass rate, time to first cited answer in a POC, permission-negative test
pass rate, and delivery-artifact completeness.

Verticals are sequenced: **finance first, healthcare second**.

## Now — H1: zero-configuration and vertical packs

- Zero-configuration substrate: one configuration schema that drives server
  validation, OpenAPI types, and the web forms; test-connection and a
  configuration doctor (model reachability, embedding dimension/index
  validity, RLS mode, backup age, license expiry).
- Finance preset pack first: document types and parsing policy, chunking and
  retrieval defaults, terminology, permission template, masking rules,
  compliance checklist, and an evaluation set. A common-enterprise pack is the
  baseline; the healthcare pack follows.
- First-run experience: setup wizard, sample knowledge base, and a system
  doctor, so a finance POC reaches a cited answer without editing environment
  files.
- Documentation correction: the public comparison and capability pages must
  reflect delivered observability, browser E2E, MCP, and SDK work.

## Next — H2/H3: evidence pack and entry capabilities

- Evidence pack for security review: department/folder/document ACL;
  finance compliance minimum (KMS, tamper-evident audit with export, PII
  masking, model-egress allowlist); answer-to-evidence export; finance
  evaluation set including permission-negative and refusal cases.
- Entry capabilities: OCR for scanned and mixed PDFs (finance corpus first);
  one real finance connector (ECM/document management, OA, mail archive, or a
  private-cloud disk) before broadening to SharePoint/Confluence/Feishu/WeCom.
- Retrieval operations: per-knowledge-base embedding routing and re-embedding,
  plus a replayable retrieval-quality loop with rerank on/off comparison.
- Identity: OIDC group/role claim mapping for the customer's IdP; SCIM only
  when the customer requires automated provisioning and deprovisioning.

## Later — H4: OEM delivery

- OEM delivery: high availability, Helm/Kubernetes, an offline image bundle,
  backup in an independent failure domain, and white-label/license/metering
  boundaries. Billing remains out of scope before design-partner validation.
- Remote MCP transport (HTTP/SSE) for partner backends, alongside the existing
  stdio server and SDKs.
- Healthcare pack: PHI handling, clinical-document OCR, and HIS/EMR integration
  once a healthcare design partner is confirmed.
- A second vertical connector, SCIM, and the full configuration-center
  governance surface (inheritance, diff, rollback, write-only secrets) after
  customer demand is demonstrated.

## Verticals

**Finance first.** Documents are more digital and the scan ratio is lower than
in healthcare, so a POC can prove retrieval quality sooner. The connector list
should follow what financial institutions actually run: ECM/document
management, office automation, mail archives, and private-cloud disks, not only
SharePoint or Confluence. The finance pack also defines the first security
review checklist.

**Healthcare second.** The same engine, but with heavier OCR and PHI
requirements: clinical documents, patient privacy, masking, and HIS/EMR
integration. The healthcare pack reuses the finance work on permissions,
audit, and evidence export rather than forking the engine.

## Explicit non-goals

- A general agent sandbox, skills marketplace, browser automation, or IM
  channel suite. Integrate with such products rather than rebuilding them.
- A Neo4j or GraphRAG storage kernel, or multiple graph databases. Graph
  capabilities remain an optional projection over evidence; external graph or
  ontology engines are integrated through MCP after evaluation.
- NL-to-SQL, a semantic/metrics layer, or business-analytics agents.
- An OWL/SHACL reasoning engine. The project exports provenance (for example
  W3C PROV-O) rather than implementing a general ontology reasoner.
- A zoo of vector databases or storage backends without capacity evidence.
- A general workflow builder.
- Billing, packaging, or a hosted SaaS offering before design-partner
  validation.
- Replacing every storage dependency with a pluggable vendor abstraction.

## How to influence the roadmap

Open an issue describing the deployment scenario, the vertical, the problem,
and the acceptance criteria. Design partners and reproducible
capacity/quality/security evidence weigh more than feature counts.
