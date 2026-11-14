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

## Embedding spaces and migration

Each knowledge base is pinned to one vector space `(model, dimension)`. An empty
knowledge base may bind any embedding model registered by the deployment or the
tenant catalog; once documents exist the binding only moves through a managed
migration. Retrieval never substitutes another embedding model: a fallback
would silently mix two incomparable vector spaces.

Vectors are stored per chunk and model in `chunk_embeddings`. The legacy
`chunks.embedding` columns remain during the transition and ingestion
dual-writes both locations, so a previous binary can still read the database.
Search reads the variant table, which is why the upgrade sequence in
[releases.md](releases.md) runs `embedctl backfill-embeddings` before the new
API starts.

A migration is one knowledge base at a time:

| Status | Meaning |
| --- | --- |
| `pending` / `building_index` | target index is being created for the target dimension |
| `reembedding` | active chunks are re-embedded in batches; ingestion for this knowledge base is paused |
| `verifying` | every active chunk must have a target-space row |
| `ready` | verified; retrieval still uses the source space until cutover |
| `cutover` | binding switched to the target; the source space is kept for rollback |
| `finalized` | source rows deleted; rollback now requires a new migration |
| `cancelled` / `rolled_back` / `failed` | terminal states |

Endpoints (knowledge-base write permission; viewers are rejected):

```
POST /api/v1/knowledge-bases/{id}/embedding-migration
GET  /api/v1/knowledge-bases/{id}/embedding-migration
POST /api/v1/knowledge-bases/{id}/embedding-migration/{migrationId}/cancel
POST /api/v1/knowledge-bases/{id}/embedding-migration/{migrationId}/cutover
POST /api/v1/knowledge-bases/{id}/embedding-migration/{migrationId}/rollback
POST /api/v1/knowledge-bases/{id}/embedding-migration/{migrationId}/finalize
```

Rollback is available until finalize: the source rows and source index are left
in place, so flipping the binding back is immediate. Migration spends the
tenant's embedding provider calls but does not consume the per-minute request
budget reserved for online traffic.

Cross-knowledge-base queries group targets by `(model, dimension)`, issue one
vector recall per space and fuse the per-space rankings. A query spanning more
than four spaces is rejected instead of comparing incomparable scores.

## Change impact preflight

`POST /api/v1/config/preview` is a read-only check before saving a tenant or
knowledge-base change. It never writes configuration, rebuilds an index or
enqueues ingestion work. The request states `scope` (`tenant` or
`knowledge_base`), the proposed retrieval/chunking documents, and optionally a
proposed embedding model/dimension.

Every field declares `impact`:

| impact | Meaning | Save behaviour |
| --- | --- | --- |
| `hot` | effective on the next retrieval | saved directly |
| `next_ingest` | applies to documents ingested/reprocessed later | saved after confirmation; history rebuild is optional |
| `rebuild` | old data is incompatible until a migration completes | blocked; the report points at the migration |
| `restart` | the process must restart | blocked; restart is required |

The response includes the effective old→new diff, `rebuildRequired`,
`rebuildOptional`, `restartRequired`, blockers, affected knowledge bases /
documents / chunks, duration and cost tiers, and suggested actions. Chunking
changes are `next_ingest` with `rebuildOptional=true`; the workbench confirms
that only new documents change. Embedding model/dimension changes are blocked
with an `embedding_contract_requires_rebuild` blocker and an
`open_embedding_migration` action. The change is applied through the
knowledge-base migration, never through the save path.

## Configuration health

`GET /api/v1/tenant/config/health` (tenant owner/admin) returns one check per
risk area:

| Check | What it verifies |
| --- | --- |
| `model.chat`, `model.embedding`, `model.rerank` | deployment model endpoints are reachable |
| `model.tenant_providers` | tenant model provider endpoints are reachable |
| `index.valid` | the chunks and `chunk_embeddings` HNSW indexes exist and are valid |
| `embedding.dimension` | every knowledge-base binding has a valid variant index for its dimension |
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
