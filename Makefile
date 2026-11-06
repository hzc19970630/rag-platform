.PHONY: deps dev-up dev-down logs migrate seed api-test web-install web-build test lint nightly acceptance m0-acceptance m1-acceptance m2-acceptance m3-acceptance m4-acceptance m5-acceptance m6-acceptance m7-acceptance m7-eval m8-capacity m8-capacity-validate m13-dataplane m13-dataplane-validate m3-eval vector-recall-probe embed-index embed-check embed-rebuild embed-verify embed-acceptance m13-rls-setup m13-rls-acceptance m13-async-acceptance m13-backup-drill backup-postgres m13-pitr-check m13-object-storage-check

# One-off Go container for test/lint. The module and build caches are named
# volumes so modules are only downloaded once; override GO_PROXY if you are
# not behind the default network.
GO_IMAGE ?= golang:1.26-alpine@sha256:87c65a688975c0b1d4585d204874c45ca78354670e3a5f11368c5a492303d696
GO_PROXY ?= https://goproxy.cn,direct
# Compose reads GO_PROXY as the Docker build arg for the Go image.
export GO_PROXY
GO_RUN = docker run --rm \
	-v "$$(pwd)":/src -w /src \
	-v rag-go-modcache:/go/pkg/mod \
	-v rag-go-buildcache:/root/.cache/go-build \
	-e GOPROXY=$(GO_PROXY) \
	$(GO_IMAGE)

deps:
	$(GO_RUN) go run ./cmd/checkdeps

dev-up:
	@test -f .env || cp .env.example .env
	docker compose --profile oidc --env-file .env -f deploy/docker-compose/docker-compose.yml up --build -d
	$(MAKE) migrate
	$(MAKE) seed
	$(MAKE) embed-index

dev-down:
	docker compose --profile oidc --env-file .env -f deploy/docker-compose/docker-compose.yml down

logs:
	docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml logs -f --tail=100

# The dedicated services carry ADMIN_DATABASE_URL, which enforce mode requires;
# running the binaries inside the api container would use the tenant role.
migrate:
	docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml run --rm --no-deps --build migrate

seed:
	docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml run --rm --no-deps --build seed

api-test:
	curl -fsS http://localhost:$${API_PORT:-8082}/healthz

web-install:
	cd web && npm ci

web-build:
	cd web && npm run build

test:
	$(GO_RUN) sh -c 'test -z "$$(gofmt -d $$(find cmd internal -name "*.go"))" && go run ./cmd/checkdeps && go run ./cmd/checktenancy && go test ./... && go vet ./...'
	cd web && npm test -- --passWithNoTests

lint:
	$(GO_RUN) sh -c 'test -z "$$(gofmt -d $$(find cmd internal tests -name "*.go"))" && go run ./cmd/checkdeps && go run ./cmd/checktenancy && go vet ./...'
	cd web && npm run build

# Nightly-only scale/load checks. TEST_POSTGRES_URL is optional; the graph test
# uses SQLite, the replay load test skips without Postgres.
nightly:
	docker run --rm \
		-v "$$(pwd)":/src -w /src \
		-v rag-go-modcache:/go/pkg/mod \
		-v rag-go-buildcache:/root/.cache/go-build \
		--add-host host.docker.internal:host-gateway \
		-e GOPROXY=$(GO_PROXY) \
		-e TEST_POSTGRES_URL="$${TEST_POSTGRES_URL:-}" \
		$(GO_IMAGE) go test -tags nightly -count=1 ./tests/nightly/...

acceptance: m0-acceptance m1-acceptance m2-acceptance m3-acceptance m4-acceptance m5-acceptance m6-acceptance m7-acceptance embed-acceptance

m0-acceptance:
	./scripts/m0_acceptance.sh

m1-acceptance:
	./scripts/m1_acceptance.sh

m2-acceptance:
	./scripts/m2_acceptance.sh

m3-acceptance:
	./scripts/m3_acceptance.sh

m4-acceptance:
	./scripts/m4_acceptance.sh

m5-acceptance:
	./scripts/m5_acceptance.sh

m6-acceptance:
	./scripts/m6_acceptance.sh

m7-acceptance:
	./scripts/m7_acceptance.sh

# M13 A1.2: runtime roles and the shadow-stage audit. Enforcement is a separate
# deployment decision; see docs/operations.md.
m13-rls-setup:
	./scripts/m13_setup_rls_roles.sh

m13-rls-acceptance:
	./scripts/m13_rls_acceptance.sh

# M13 A3: async answers. The worker-down drill stops the worker, submits a
# question, restarts the worker and requires a terminal SSE event.
m13-async-acceptance:
	./scripts/m13_async_acceptance.sh

m13-backup-drill:
	./scripts/m13_backup_drill.sh

backup-postgres:
	./scripts/backup_postgres.sh

m13-pitr-check:
	./scripts/m13_pitr_check.sh

m13-object-storage-check:
	./scripts/m13_object_storage_check.sh

# Compares bounded graph queries with pure Evidence RAG on the fixed M7 QA set
# and records reachability/path metrics. The keep-or-drop decision for complex
# graph queries is a human call based on the recorded report.
m7-eval:
	python3 scripts/m7_eval.py

# Validates the M7 QA set (fixture counts, triple-graph hops, seed resolution
# and marker placement) without contacting the stack.
m7-eval-validate:
	python3 scripts/m7_eval.py --validate-only

# Measures the M8 capacity baseline on the local stack: ingestion wall time and
# per-stage latency, upload/search/graph-query P50/P95, projection cost and
# PostgreSQL footprint. Writes a schema-versioned report and a gate register;
# it never starts an M8 candidate extension. Use local chat/embedding adapters
# for a reproducible run (see README.md).
m8-capacity:
	python3 scripts/m8_capacity.py

# Validates a recorded M8 capacity report against the report schema without
# contacting the stack.
m8-capacity-validate:
	python3 scripts/m8_capacity.py --validate-report tests/eval/m8_capacity_report.json

# M13 B1: 1M x 1024 synthetic vectors in a throwaway schema, online HNSW build
# with concurrent writes, 16-tenant vector/BM25 query load and ANN recall
# sampling. Writes tests/eval/m13_dataplane_report.json and drops the schema
# unless --keep is passed. This is capacity evidence for M8, not a release gate.
m13-dataplane:
	python3 scripts/m13_dataplane.py

m13-dataplane-validate:
	python3 scripts/m13_dataplane.py --validate-report tests/eval/m13_dataplane_report.json

# Measures vector recall on a synthetic 100:1 tenant split for hnsw.ef_search,
# over-sampling and iterative scan. Uses a throwaway schema and never touches
# the application's chunks/documents tables. Writes tests/eval/vector_recall_report.json.
vector-recall-probe:
	python3 scripts/vector_recall_probe.py

# Records retrieval, citation, refusal and permission metrics for the fixed M3
# evaluation set. Safety and permission invariants fail the target; the release
# threshold for every diagnostic metric is a human decision. See README.md for
# the embedding model flags.
m3-eval:
	python3 scripts/m3_eval.py

# create-index builds the halfvec(N) HNSW index CONCURRENTLY under a versioned
# name and only drops the old index after the replacement is valid. The prompt
# requires --confirm (or EMBED_INDEX_CONFIRM=1) so automation stays explicit.
embed-index:
	docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml run --rm --no-deps --entrypoint /usr/local/bin/embedctl api create-index --confirm

# Read-only index diagnostics: pgvector version, valid/invalid indexes,
# per-dimension chunk counts and space use.
embed-check:
	docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml run --rm --no-deps --entrypoint /usr/local/bin/embedctl api check

embed-rebuild:
	docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml run --rm --no-deps --entrypoint /usr/local/bin/embedctl api reembed

embed-verify:
	docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml run --rm --no-deps --entrypoint /usr/local/bin/embedctl api verify

embed-acceptance:
	./scripts/embed_rebuild_acceptance.sh
