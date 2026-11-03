.PHONY: dev-up dev-down logs migrate seed api-test web-install web-build test lint acceptance m0-acceptance m1-acceptance m2-acceptance m3-acceptance m4-acceptance m5-acceptance m6-acceptance m7-acceptance m7-eval m8-capacity m8-capacity-validate m3-eval embed-index embed-rebuild embed-verify embed-acceptance

# One-off Go container for test/lint. The module and build caches are named
# volumes so modules are only downloaded once; override GO_PROXY if you are
# not behind the default network.
GO_IMAGE ?= golang:1.26-alpine
GO_PROXY ?= https://goproxy.cn,direct
GO_RUN = docker run --rm \
	-v "$$(pwd)":/src -w /src \
	-v rag-go-modcache:/go/pkg/mod \
	-v rag-go-buildcache:/root/.cache/go-build \
	-e GOPROXY=$(GO_PROXY) \
	$(GO_IMAGE)

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

migrate:
	docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml run --rm --no-deps --entrypoint /usr/local/bin/migrate api

seed:
	docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml run --rm --no-deps --entrypoint /usr/local/bin/seed api

api-test:
	curl -fsS http://localhost:$${API_PORT:-8082}/healthz

web-install:
	cd web && npm install

web-build:
	cd web && npm run build

test:
	$(GO_RUN) sh -c 'test -z "$$(gofmt -d $$(find cmd internal -name "*.go"))" && go test ./... && go vet ./...'
	cd web && npm test -- --passWithNoTests

lint:
	$(GO_RUN) sh -c 'test -z "$$(gofmt -d $$(find cmd internal -name "*.go"))" && go vet ./...'
	cd web && npm run build

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

# Records retrieval, citation, refusal and permission metrics for the fixed M3
# evaluation set. Safety and permission invariants fail the target; the release
# threshold for every diagnostic metric is a human decision. See README.md for
# the embedding model flags.
m3-eval:
	python3 scripts/m3_eval.py

embed-index:
	docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml run --rm --no-deps --entrypoint /usr/local/bin/embedctl api create-index

embed-rebuild:
	docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml run --rm --no-deps --entrypoint /usr/local/bin/embedctl api reembed

embed-verify:
	docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml run --rm --no-deps --entrypoint /usr/local/bin/embedctl api verify

embed-acceptance:
	./scripts/embed_rebuild_acceptance.sh
