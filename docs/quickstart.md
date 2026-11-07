# Quickstart

This guide starts a complete local stack and completes one document question in
about 10–20 minutes, including the first image build.

## Prerequisites

- Docker Engine or Docker Desktop with Compose v2 (`docker compose`).
- Git and `curl`.
- At least 8 GB of free disk space and 4 GB of memory available to Docker.
- Node.js is **not** required for the quickstart: the web UI runs in a container.

Windows users should run the commands in Git Bash. `make` is optional; the
quickstart scripts work without it.

## Start

```bash
git clone https://github.com/hzc19970630/rag-platform.git
cd rag-platform
cp .env.example .env
./scripts/quickstart.sh
```

The script:

1. Builds and starts PostgreSQL, Redis, MinIO, and the document reader.
2. Runs database migrations and the idempotent seed.
3. Starts the API, worker, and web UI.
4. Builds the vector index.
5. Waits for the API and web UI health checks.

First build downloads images and Go/npm dependencies. Later starts are much faster.

## First question

1. Open <http://localhost:5173>.
2. Click **Continue** on the development login panel.
3. Create a knowledge base.
4. Open it and upload `examples/demo/retrieval-handbook.md`.
5. Wait until the document status is `EVIDENCE_READY`.
6. Click **Chat**, create a conversation, and ask:
   `How does hybrid recall combine keyword and vector results?`
7. Confirm the answer contains citations and that the document is listed as the
   source.

Run the automated version of the same flow:

```bash
./scripts/quickstart_check.sh
```

It creates a temporary knowledge base, uploads the demo document, waits for
`EVIDENCE_READY`, checks hybrid search, streams an answer, and deletes the
knowledge base.

## Stop and restart

```bash
./scripts/quickstart.sh --down     # keep database/object-storage volumes
./scripts/quickstart.sh            # start again
```

To delete all local data, stop the stack and remove the named volumes explicitly:

```bash
docker compose --env-file .env \
  -f deploy/docker-compose/docker-compose.yml \
  -f deploy/docker-compose/quickstart.compose.yml down -v
```

**This permanently deletes the local database, Redis data, and object storage.**

## What is running

| Service | Endpoint | Notes |
| --- | --- | --- |
| Web | <http://localhost:5173> | Vite development server |
| API | <http://localhost:8082> | `/healthz`, `/readyz`, `/api/v1` |
| MinIO console | <http://localhost:9001> | Local object storage |
| PostgreSQL | `localhost:5432` | ParadeDB + pgvector |
| Redis | `localhost:6379` | Queue and cache |

Ports can be changed in `.env` (`API_PORT`, `POSTGRES_PORT`, and so on). If you
change `API_PORT`, keep `API_PUBLIC_URL` in sync so the browser calls the right API.

## Local models and identity

The quickstart overlay forces:

- `DEV_AUTH_ENABLED=true` and `OIDC_ENABLED=false` (development login).
- `CHAT_SOURCE=local`: a deterministic extractive adapter that only cites
  sentences containing query terms.
- `EMBEDDING_SOURCE=local`: deterministic hash vectors without semantic meaning.

These settings make the demo reproducible but are **not suitable for production**.
They are rejected by `APP_ENV=production` startup checks.

## Use a real model provider

Edit `.env` (never commit it) and restart the API/worker:

```bash
CHAT_SOURCE=remote
CHAT_BASE_URL=https://your-provider.example/v1
CHAT_API_KEY=...
CHAT_MODEL=your-chat-model

EMBEDDING_SOURCE=remote
EMBEDDING_BASE_URL=https://your-provider.example/v1
EMBEDDING_API_KEY=...
EMBEDDING_MODEL=your-embedding-model
EMBEDDING_DIMENSION=1024
```

Changing `EMBEDDING_DIMENSION` is a managed index change: rebuild the index and
re-embed existing chunks before switching traffic. See
[Operations](operations.md#16-hnsw).

## Production

The quickstart overlay is local-only. For a shared deployment:

- Use the base Compose file without `quickstart.compose.yml`.
- Set `APP_ENV=production`, `DEV_AUTH_ENABLED=false`, and configure OIDC.
- Replace every placeholder credential, use TLS, and keep data services private.
- Back up PostgreSQL and object storage, and verify release signatures/SBOMs.

See [SECURITY.md](https://github.com/hzc19970630/rag-platform/blob/main/SECURITY.md),
[Operations](operations.md), and [Releases](releases.md).

## Troubleshooting

If startup fails, run:

```bash
docker compose --env-file .env \
  -f deploy/docker-compose/docker-compose.yml \
  -f deploy/docker-compose/quickstart.compose.yml ps
docker compose --env-file .env \
  -f deploy/docker-compose/docker-compose.yml \
  -f deploy/docker-compose/quickstart.compose.yml logs --tail=200 api worker web
```

Common causes are listed in [Troubleshooting](troubleshooting.md).
