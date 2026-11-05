#!/usr/bin/env bash
# Runs only when the PostgreSQL data directory is initialized. It creates the
# M13 runtime roles so a brand-new volume has them before migrations run, and
# registers default privileges so tables created by this owner are usable by
# the runtime roles without an extra setup pass. Passwords stay in
# scripts/m13_setup_rls_roles.sh, which can be re-run on an existing volume.
set -euo pipefail

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
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
SQL

# Default privileges apply to objects this owner creates later, which is
# exactly how the migrate service creates the schema.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v db_name="$POSTGRES_DB" <<'SQL'
GRANT CONNECT ON DATABASE :"db_name" TO rag_app, rag_maintenance;
GRANT USAGE ON SCHEMA public TO rag_app, rag_maintenance;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO rag_app, rag_maintenance;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO rag_app, rag_maintenance;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO rag_app, rag_maintenance;
SQL

if [[ -n "${RAG_APP_DB_PASSWORD:-}" ]]; then
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
    -v app_password="$RAG_APP_DB_PASSWORD" <<'SQL'
ALTER ROLE rag_app LOGIN PASSWORD :'app_password';
SQL
fi

if [[ -n "${RAG_MAINTENANCE_DB_PASSWORD:-}" ]]; then
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
    -v maintenance_password="$RAG_MAINTENANCE_DB_PASSWORD" <<'SQL'
ALTER ROLE rag_maintenance LOGIN PASSWORD :'maintenance_password';
SQL
fi

echo "M13 RLS roles created"
