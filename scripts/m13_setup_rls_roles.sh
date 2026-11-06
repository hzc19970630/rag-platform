#!/usr/bin/env bash
# Original M13 A1.2 (delivered): create the RLS runtime roles and grant their privileges.
# Idempotent, safe to re-run against an existing database. Passwords come from
# the environment (or .env): RAG_APP_DB_PASSWORD / RAG_MAINTENANCE_DB_PASSWORD.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

if [[ -z "${RAG_APP_DB_PASSWORD:-}" ]]; then
  echo "RAG_APP_DB_PASSWORD is required (rag_app login password)" >&2
  exit 1
fi
if [[ -z "${RAG_MAINTENANCE_DB_PASSWORD:-}" ]]; then
  echo "RAG_MAINTENANCE_DB_PASSWORD is required (rag_maintenance login password)" >&2
  exit 1
fi

compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)

"${compose[@]}" exec -T postgres psql \
  -U "${POSTGRES_USER:-rag_platform}" \
  -d "${POSTGRES_DB:-rag_platform}" \
  -v ON_ERROR_STOP=1 \
  -v db_name="${POSTGRES_DB:-rag_platform}" \
  -v app_password="$RAG_APP_DB_PASSWORD" \
  -v maintenance_password="$RAG_MAINTENANCE_DB_PASSWORD" <<'SQL'
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'rag_app') THEN
        CREATE ROLE rag_app NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'rag_maintenance') THEN
        CREATE ROLE rag_maintenance NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE BYPASSRLS;
    END IF;
END
$$;

-- Passwords never appear in the SQL history: psql interpolates the variables
-- after parsing, and the script itself is not echoed by make.
ALTER ROLE rag_app LOGIN PASSWORD :'app_password' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
ALTER ROLE rag_maintenance LOGIN PASSWORD :'maintenance_password' NOSUPERUSER NOCREATEDB NOCREATEROLE BYPASSRLS;

GRANT CONNECT ON DATABASE :"db_name" TO rag_app, rag_maintenance;
GRANT USAGE ON SCHEMA public TO rag_app, rag_maintenance;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO rag_app, rag_maintenance;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO rag_app, rag_maintenance;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO rag_app, rag_maintenance;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO rag_app, rag_maintenance;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO rag_app, rag_maintenance;
SQL

echo "RLS roles ready: rag_app (NOBYPASSRLS), rag_maintenance (BYPASSRLS)"
