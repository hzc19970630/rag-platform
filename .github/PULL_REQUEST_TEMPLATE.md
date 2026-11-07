## Problem

<!-- What fails, is missing, or is risky today? Link the issue if one exists. -->

## Change

<!-- Describe the approach and the boundaries of this change. -->

## Verification

<!-- Commands run, tests added, manual checks, screenshots for UI changes. -->

- [ ] `gofmt` / `go vet` / `go test ./...` (or `make test`)
- [ ] `go run ./cmd/checkdeps`, `go run ./cmd/openapi -check`, `go run ./cmd/checktenancy`
- [ ] `npm test`, `npm run build`, `npm run codegen:check`
- [ ] Acceptance or E2E tests relevant to the change
- [ ] No secrets, customer data, or local absolute paths
- [ ] Documentation updated for user-visible behavior

## Rollout / rollback

<!-- Migrations, configuration changes, compatibility, and how to roll back. -->

## Checklist

- [ ] The change is focused and does not include unrelated refactors.
- [ ] New behavior has tests; security fixes have a failing negative test first.
- [ ] Tenant scoping and authorization were considered for every new query/route.
- [ ] Public docs contain no internal milestone codes or local paths.
