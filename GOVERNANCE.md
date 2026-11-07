# Governance

## Current model

RAG Platform is maintainer-led. The maintainer sets roadmap priorities, reviews
changes, cuts releases, and is responsible for security response. Decisions are
made in public issues or discussions whenever the topic is not security-sensitive.

## Roles

| Role | Responsibilities | How it is granted |
| --- | --- | --- |
| User | Uses the software, reports bugs, participates in discussions | — |
| Contributor | Submits issues, docs, tests, or code | Contribution accepted |
| Reviewer | Reviews pull requests in an area | Invited by maintainers |
| Maintainer | Merge rights, release and security responsibility, roadmap | Invited by existing maintainers |

## Decision making

- Routine changes: lazy consensus in the pull request; one maintainer approval is
  sufficient.
- Cross-cutting changes (contracts, storage schema, security model, licensing):
  require an issue or ADR, a public comment period of at least 7 days for
  non-urgent work, and maintainer approval.
- Security fixes may be prepared privately and disclosed after a fix is available.
- If consensus cannot be reached, the maintainer makes the final decision and
  records the rationale in the issue or ADR.

## Becoming a maintainer

Sustained, high-quality contributions and constructive reviews are the main
criteria. A candidate should demonstrate:

- A track record of merged changes in more than one area.
- Good judgment on security, compatibility, and scope.
- Willingness to review, respond to issues, and follow this governance model.

New maintainers are invited by existing maintainers. The current list is kept in
[CODEOWNERS](.github/CODEOWNERS).

## Releases

Releases follow [docs/releases.md](docs/releases.md). Only maintainers with release
permission may create version tags or publish artifacts. `main` is protected and
changes land through pull requests.

## Changes to this document

Governance changes are proposed as a pull request, left open for at least 7 days,
and require maintainer approval. The rationale is recorded in the pull request.
