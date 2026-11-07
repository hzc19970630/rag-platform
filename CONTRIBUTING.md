# Contributing

Thanks for taking the time to improve RAG Platform. This document describes the
development workflow and the checks a change must pass.

## Before you start

- For bug fixes, open an issue with reproduction steps.
- For large features or architectural changes, open a discussion/issue first so the
  design and scope can be agreed before implementation.
- Security issues must follow [SECURITY.md](SECURITY.md), not the public tracker.

## Prerequisites

- Docker Engine/Desktop with Compose v2 (local stack and Go checks)
- Node.js 24 (frontend)
- Python 3.12 (document reader and acceptance scripts)
- Git; `make` is optional, the core workflows are available as scripts

## Local setup

```bash
git clone https://github.com/hzc19970630/rag-platform.git
cd rag-platform
cp .env.example .env
./scripts/quickstart.sh
```

The stack starts at <http://localhost:5173>. Development login is enabled by the
quickstart overlay and must never be used for a shared deployment.

Stop the stack with:

```bash
./scripts/quickstart.sh --down
```

## Checks

Run the checks relevant to your change before opening a pull request.

```bash
# Go
gofmt -w cmd internal tests
go run ./cmd/checkdeps
go run ./cmd/openapi -check
go run ./cmd/checktenancy
go test ./...
go vet ./...

# Frontend
cd web
npm ci
npm run codegen:check
npm test
npm run build
```

If you do not have a Go toolchain on the host, `make test` and `make lint` run the
same checks in the pinned Go container. Acceptance scripts require the local stack
and are listed in [docs/quickstart.md](docs/quickstart.md).

## Coding rules

- Keep domain boundaries: adapters depend on ports, not the other way around.
  `cmd/checkdeps` enforces the dependency direction.
- Every query touching tenant data must be tenant-scoped. `cmd/checktenancy`
  rejects new unscoped GORM paths.
- HTTP DTOs and routes live in `contracts/httpapi`; regenerate OpenAPI with
  `go run ./cmd/openapi` and the frontend types with `npm run codegen`.
- Do not introduce secrets, real customer data, or provider credentials into the
  repository. Use placeholders and `.env` locally.
- Add or update tests for behavior changes. Security-sensitive fixes should first
  add a failing negative test, then the fix.
- Public documentation must not contain internal milestone codes or local paths.

## Commits and pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/) prefixes
  (`feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`).
- Sign off your commits under the [Developer Certificate of Origin](https://developercertificate.org/):

  ```bash
  git commit -s -m "fix: reject cross-tenant document reads"
  ```

- Keep pull requests focused. Describe the problem, the change, the verification
  performed, and any rollout/rollback notes.
- CI must pass (`verify`, `integration`, `security`, and frontend E2E when enabled).
- By contributing, you agree that your contribution is licensed under the
  repository's [MIT License](LICENSE).

## Review and release

Maintainers review pull requests and may ask for tests, documentation, or a smaller
scope. Releases are cut from protected `main` with semantic-version tags; see
[docs/releases.md](docs/releases.md). Governance and maintainer roles are described
in [GOVERNANCE.md](GOVERNANCE.md).
