#!/usr/bin/env bash
# Start or stop the local quickstart stack. This forces development identity and
# offline deterministic models; do not use it for a shared or production deployment.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  cat <<'EOF'
Usage: ./scripts/quickstart.sh [--down]

  (no option)  build and start the local stack, migrate, seed, and build the vector index
  --down       stop the stack; named volumes are kept

The stack uses .env (created from .env.example when missing) and is reachable at
http://localhost:5173. Development login is enabled by the quickstart overlay.
EOF
  exit 0
fi

command -v docker >/dev/null 2>&1 || {
  echo "docker is required; install Docker Engine or Docker Desktop" >&2
  exit 1
}
docker compose version >/dev/null 2>&1 || {
  echo "Docker Compose v2 is required (docker compose)" >&2
  exit 1
}

if [[ ! -f .env ]]; then
  cp .env.example .env
  echo "created .env from .env.example"
fi

compose=(docker compose --env-file .env \
  -f deploy/docker-compose/docker-compose.yml \
  -f deploy/docker-compose/quickstart.compose.yml)

if [[ "${1:-}" == "--down" ]]; then
  "${compose[@]}" down
  exit 0
fi

if [[ -n "${1:-}" ]]; then
  echo "unknown option: $1 (try --help)" >&2
  exit 2
fi

echo "starting dependencies..."
"${compose[@]}" up -d --build --wait postgres redis minio docreader

echo "running migrations and seed..."
"${compose[@]}" run --rm --no-deps --build migrate
"${compose[@]}" run --rm --no-deps --build seed

echo "starting api, worker, and web..."
"${compose[@]}" up -d --build api worker web

echo "building the vector index..."
"${compose[@]}" run --rm --no-deps --entrypoint embedctl api create-index --confirm

api_port="$("${compose[@]}" port api 8080 | awk -F: '{print $NF}')"
if [[ -z "$api_port" ]]; then
  api_port=8082
fi

echo "waiting for the API..."
for _ in $(seq 1 60); do
  if curl -fsS "http://localhost:${api_port}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -fsS "http://localhost:${api_port}/healthz" >/dev/null

echo "waiting for the web UI..."
for _ in $(seq 1 60); do
  if curl -fsS "http://localhost:5173/" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -fsS "http://localhost:5173/" >/dev/null

cat <<EOF

Quickstart is ready.

  Web UI:  http://localhost:5173
  API:     http://localhost:${api_port}
  Login:   click "Continue" (development mode)
  Demo:    examples/demo/retrieval-handbook.md
  Check:   ./scripts/quickstart_check.sh
  Stop:    ./scripts/quickstart.sh --down

The local stack uses deterministic offline models and development identity.
For a real deployment, configure OIDC and a model provider; see docs/quickstart.md.
EOF
