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

Delivery sequencing is demand-led: self-verifiable product gaps come first;
commercial packaging, licensing, pricing, and hosted operations start only after
a paid POC or design partner is signed.

## Now — retrieval depth, quality, and a zero-configuration baseline

- Retrieval depth: per-knowledge-base embedding routing with staged
  re-embedding, verification, and rollback; storage and format capabilities
  move behind adapter contracts without changing current behavior.
- Retrieval-quality loop: a replayable evaluation path for retrieval
  configuration changes, rerank on/off comparison, and online feedback
  (ratings, citation clicks) in one regression view.
- Preset-pack tooling: a common-enterprise baseline pack plus
  `validate/import/export/diff` commands. Vertical packs are data, added when a
  named customer needs one; no industry-specific engine branches.
- Documentation correction: the public comparison and capability pages must
  reflect delivered observability, browser E2E, MCP, and SDK work.

The configuration substrate—one schema driving server validation, OpenAPI
types, and web forms, plus test-connection, a configuration doctor, change
preview, and the first-run wizard—is already delivered and is the base the
preset packs build on.

## Next — POC entry capabilities (customer-triggered)

- Evidence pack for security review: department/folder/document ACL;
  finance compliance minimum (KMS, tamper-evident audit with export, PII
  masking, model-egress allowlist); answer-to-evidence export; finance
  evaluation set including permission-negative and refusal cases.
- First vertical pack (finance first, healthcare second) once a named customer
  provides documents and acceptance criteria: document types and parsing
  policy, terminology, permission template, masking rules, compliance
  checklist, and an evaluation set.
- Entry capabilities: OCR for scanned and mixed PDFs (finance corpus first);
  one real finance connector (ECM/document management, OA, mail archive, or a
  private-cloud disk) before broadening to SharePoint/Confluence/Feishu/WeCom.
- Identity: OIDC group/role claim mapping for the customer's IdP; SCIM only
  when the customer requires automated provisioning and deprovisioning.

## Later — GA and OEM delivery (signed customer required)

- OEM delivery: high availability, Helm/Kubernetes, an offline image bundle,
  backup in an independent failure domain, and white-label/license/metering
  boundaries—started when a signed customer's deployment form requires them.
  Billing remains out of scope before design-partner validation.
- Commercial redistribution review: resolve or license the AGPL/SSPL
  dependencies (ParadeDB, MinIO, Redis) before any commercial distribution or
  hosted service.
- Remote MCP transport (HTTP/SSE) for partner backends, alongside the existing
  stdio server and SDKs.
- Healthcare pack: PHI handling, clinical-document OCR, and HIS/EMR integration
  once a healthcare design partner is confirmed.
- A second vertical connector and the full configuration-center governance
  surface (inheritance, diff, rollback, write-only secrets) after customer
  demand is demonstrated.

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
