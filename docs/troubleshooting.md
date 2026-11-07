# Troubleshooting

Start with service state and logs:

```bash
COMPOSE=(docker compose --env-file .env \
  -f deploy/docker-compose/docker-compose.yml \
  -f deploy/docker-compose/quickstart.compose.yml)
"${COMPOSE[@]}" ps
"${COMPOSE[@]}" logs --tail=200 api worker web
```

For a production overlay, replace `quickstart.compose.yml` with
`release.compose.yml` and add `RAG_VERSION`.

## The stack does not start

### Port already in use

Typical errors mention `bind: address already in use` for 5173, 8082, 5432, 6379,
9000, or 9001. Change the host port in `.env` (`API_PORT`, `POSTGRES_PORT`) or stop
the conflicting process. Keep `API_PUBLIC_URL` aligned with `API_PORT`.

### Docker runs out of memory or disk

The first build compiles the Go API and installs frontend dependencies. Ensure
Docker has at least 4 GB of memory and 8 GB of free disk. Reclaim space with
`docker system df` and, if you accept the impact, `docker system prune`.

### API is unhealthy

```bash
"${COMPOSE[@]}" logs --tail=200 api
curl -i http://localhost:8082/healthz
curl -i http://localhost:8082/readyz
```

- `configuration_invalid`: a production-only startup check rejected placeholder
  credentials or development auth. Fix the environment; do not disable the check.
- Database connection errors: wait for PostgreSQL to become healthy, then rerun
  migrations. Check `DATABASE_URL` and `ADMIN_DATABASE_URL`.
- Migration lock: another migration process is holding the advisory lock. Wait for
  it to finish; do not delete lock rows manually.

### Migration checksum mismatch

`migrate -check` reports `checksum mismatch` when an already-applied migration file
changed. The fix is to restore the original file, not to edit `schema_migrations`.

On Windows, an image built from a CRLF working tree can produce different checksums
than a Linux CI image built from LF. Ensure the build context uses LF, or use the
published Linux image. Never update production checksums to make the check pass.

## Login and identity

### Login page has no OIDC button

`OIDC_ENABLED=false`. Set it to `true` with a valid issuer/client, or use the
quickstart development login.

### Clicking "Continue" returns to the login page

Development identity is disabled or the API is running in production mode.
Check `DEV_AUTH_ENABLED=true` and `APP_ENV=development`. Never enable development
identity in a shared deployment.

### OIDC redirect fails

`OIDC_ISSUER` and `OIDC_REDIRECT_URL` must be reachable from both the browser and
the API container. `localhost` inside the container refers to the container itself.
For the bundled Keycloak fixture, set `KEYCLOAK_HOSTNAME` to a host reachable from
both sides (for example `host.docker.internal:8083`).

## Upload and parsing

### `unsupported_scanned_pdf`

The PDF has no extractable text layer. OCR is not implemented. Use a digital PDF or
convert the document to text first.

### `unsupported_legacy_office`

Legacy `.xls` and `.ppt` binary formats are intentionally rejected. Convert to
`.xlsx` or `.pptx`.

### Document stays in a non-terminal state

Check the worker and document reader:

```bash
"${COMPOSE[@]}" logs --tail=200 worker docreader
curl -fsS http://localhost:8082/api/v1/documents/<document-id> \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant'
```

Look at `pipeline.status` and `stages`. A failed stage usually contains an
`errorCode` and `errorMessage`. `reconcile` repairs expired leases; it does not fix
a deterministic parser or model error.

### Processing is slow

Local deterministic models are fast; remote providers may be slow or rate-limited.
Check the model health snapshot in the operations view and the worker logs for
timeouts. `DOCREADER_TASK_TIMEOUT_SECONDS` and `EMBEDDING_TIMEOUT_SECONDS` bound
individual stages.

## Retrieval and answers

### Search returns nothing

- Confirm the document and current version are `EVIDENCE_READY`.
- Confirm the vector index dimension matches `EMBEDDING_DIMENSION`:
  `docker compose ... run --rm --no-deps --entrypoint /usr/local/bin/embedctl api check`.
- If you changed the embedding model or dimension, run the managed index workflow
  (`create-index` → `reembed` → `verify`) before searching.
- The local hash embedder has no semantic meaning; use exact terms from the
  document or configure a real embedding provider.

### Answer is refused with `insufficient_evidence`

The retrieval set did not contain evidence that passed authorization and citation
checks. Check the search endpoint with the same query, confirm the document is
current, and ask a question that the document actually answers. The model is not
allowed to answer from general knowledge.

### Answer has no citations

Citations are rejected if they cannot be located in the authorized evidence set or
if the chunk became stale during generation. Check the stream `error` event and API
logs. Re-upload or reparse the document if its version was replaced.

### Rerank does not seem to run

Rerank is off by default. It only runs when `rerank_enabled` is true in the
knowledge-base or request configuration; `RERANK_SOURCE=none` is also a pass-through.

## Frontend

### Web UI cannot reach the API

The browser calls `VITE_API_BASE_URL`. For local defaults this is
`http://localhost:8082/api/v1`. If you changed `API_PORT`, update `API_PUBLIC_URL`
and restart the web container. CORS allows the local development origin only.

### Changes do not appear

The web container runs the Vite dev server. After changing source code, rebuild the
web service:

```bash
"${COMPOSE[@]}" up -d --build web
```

## Reset a local stack

Stopping keeps volumes:

```bash
./scripts/quickstart.sh --down
```

Deleting volumes is destructive and removes the database, Redis data, and object
storage:

```bash
"${COMPOSE[@]}" down -v
```

Run this only when you accept permanent data loss.

## Getting help

Open a bug report with version, deployment method, reproduction steps, and relevant
logs. Remove secrets and personal data. Security issues follow
[SECURITY.md](https://github.com/hzc19970630/rag-platform/blob/main/SECURITY.md).
