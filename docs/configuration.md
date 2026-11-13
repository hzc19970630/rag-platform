# Business configuration

The platform separates **deployment connection settings** from **business
policy**:

| Tier | Where it lives | Editable by | Examples |
| --- | --- | --- | --- |
| Schema default | `internal/configschema` | code review | safe fallback for every field |
| Deployment | env / `config/config.yaml` | operator, restart required | `RETRIEVAL_CONFIG_FILE`, `CHUNK_*`, model endpoints |
| Tenant | `tenants.retrieval_config` / `tenants.chunking_config` | tenant owner/admin in Administration → Business configuration | default retrieval and chunking for every knowledge base |
| Knowledge base | `knowledge_bases.retrieval_config` | editor of that knowledge base | a single knowledge base overriding `top_k` |
| Session | request `sessionOverride` | caller | an experiment that must not persist |

Resolution order is schema default → deployment → tenant → knowledge base →
session. Every layer is validated against the same schema, so a value cannot
be valid at one tier and invalid at another.

## Single source of truth

`internal/configschema/schema.go` declares each field once:

- key, domain (`retrieval` / `chunking`), group and JSON kind
- safe default, min/max/step and unit
- `applyMode`: `next_retrieval` (next query) or `next_ingest` (new documents)
- sensitivity and i18n label keys

Server validation, the OpenAPI schema document, the workbench form and the
per-field source report all read this declaration. Adding a retrieval parameter
therefore does not require a new hand-written field row in the workbench or a
new OpenAPI property; the engine still has to consume the value. Ranges are
enforced in exactly one place: `configschema.Validate`.

`GET /api/v1/knowledge-bases/{id}/configuration` and
`GET /api/v1/tenant/config` return, for every field:

- `value`: the effective value
- `default`: the safe schema default
- `deployment`: the deployment value when the operator set it
- `source`: `default`, `deployment`, `tenant`, `knowledge_base` or `session`
- `applyMode`: when the change takes effect

Unknown keys are rejected by `PUT /tenant/config`, `PUT
/knowledge-bases/{id}/retrieval-config` and `PATCH /knowledge-bases/{id}`. A
parameter removed from the schema can no longer be written; the configuration
health check reports stored rows that still contain it.

## Chunking

Tenant chunking policy applies to documents chunked after the change. Existing
chunks are not rewritten; reprocess a version to apply the new policy. The
worker resolves the policy per document, so two tenants can run different
chunk sizes on the same deployment.

## Configuration health

`GET /api/v1/tenant/config/health` (tenant owner/admin) returns one check per
risk area:

| Check | What it verifies |
| --- | --- |
| `model.chat`, `model.embedding`, `model.rerank` | deployment model endpoints are reachable |
| `model.tenant_providers` | tenant model provider endpoints are reachable |
| `index.valid` | the chunks HNSW index exists and is valid |
| `embedding.dimension` | `EMBEDDING_DIMENSION`, the serving index and each knowledge-base binding agree |
| `rls.mode` | production is not running RLS off/shadow |
| `backup.age` | the newest file under `BACKUP_DIR` is within `BACKUP_MAX_AGE_HOURS` |
| `license.expiry` | `LICENSE_EXPIRES_AT` is valid and not expired |
| `drift.retrieval_file` | `RETRIEVAL_CONFIG_FILE` has not changed since startup without a restart |
| `drift.stored_config` | tenant/knowledge-base JSON does not contain unknown or out-of-range keys |

Statuses are `ok`, `warning`, `error`, `unknown` and `skipped`. High-risk
warnings (stale backup, shadow RLS, expired license, unreachable model,
index/dimension mismatch, config drift) are counted in `highRisk` and shown in
Administration → Business configuration.

`POST /api/v1/tenant/config/test-connection` probes one capability
(`chat`, `embedding`, `rerank`) with the stored deployment adapter, or a tenant
provider when `providerId` is supplied. Credentials never travel in the request
body. The embedding test also embeds a probe sentence and compares the returned
dimension with `EMBEDDING_DIMENSION`.

## Optional deployment settings

| Variable | Purpose |
| --- | --- |
| `BACKUP_DIR` | directory the backup scripts write to; empty makes the backup check `unknown` |
| `BACKUP_MAX_AGE_HOURS` | staleness threshold for the newest backup (default 24) |
| `LICENSE_EXPIRES_AT` | RFC3339 or `YYYY-MM-DD`; no license issuer exists yet, so empty reports `unknown` |

The API container must see `BACKUP_DIR` for the backup check to be meaningful.
Mount it read-only; the check never writes.

## Workbench

- **Administration → Business configuration**: tenant retrieval/chunking
  overrides, per-field source/apply mode, health panel and connection tests.
- **Knowledge base → Settings → Retrieval & indexing**: knowledge-base
  overrides rendered from the same schema.
- **First-run setup**: on an empty knowledge-base list, the wizard checks that
  chat and embedding models exist, creates the first knowledge base and shows
  sample questions. Model credentials and endpoints remain in Administration →
  Model providers or env/YAML.

## Adding a field

1. Add the declaration to `internal/configschema/schema.go` (default, range,
   group, apply mode, label key).
2. Add the engine field and wire the value where it is consumed.
3. Add i18n text for `labelKey` and `unitKey` if new.
4. Run `go test ./...`, `go run ./cmd/openapi -check`, `npm test` and
   `npm run build`. No OpenAPI DTO or workbench field list needs editing.
