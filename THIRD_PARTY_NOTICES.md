# Third-Party Notices

RAG Platform is licensed under the [MIT License](LICENSE). It depends on and
distributes third-party open-source software; each component remains under its own
license and copyright.

## Authoritative inventory

- Go modules: `go.mod` and `go.sum`.
- Frontend: `web/package.json` and `web/package-lock.json`.
- Document reader: `services/docreader/requirements.txt`.
- Container images and pinned digests: `deploy/docker-compose/docker-compose.yml`,
  `Dockerfile`, and `services/docreader/Dockerfile`.
- Release SBOMs: every GitHub Release includes CycloneDX SBOMs for the `api` and
  `docreader` images. The SBOM is the authoritative component inventory for a
  released artifact and records the image digest it describes.

## High-attention components

The following components have licenses that deserve review before commercial
distribution. This table is an inventory, not a legal conclusion.

| Component | License | Note |
| --- | --- | --- |
| ParadeDB (PostgreSQL image) | AGPL-3.0 | Deployed as a database service; review AGPL obligations for your distribution model |
| MinIO | AGPL-3.0 | Deployed as object storage; review AGPL obligations |
| Redis 7 image | BSD-3-Clause or RSALv2/SSPLv1 depending on version | Confirm the exact image version before distribution |
| Keycloak | Apache-2.0 | Optional local OIDC fixture |
| pgvector | PostgreSQL License | Bundled with the PostgreSQL image |
| Go and npm dependencies | Mostly MIT, BSD, Apache-2.0 | See lockfiles and SBOM |

See [docs/licensing.md](docs/licensing.md) for the current review notes and open
questions. Commercial redistribution requires a legal review of the components
above; this file does not replace that review.

## Source offers and attributions

Do not remove copyright, license, or attribution notices from third-party files.
If you modify an AGPL/SSPL component or offer it as a network service, evaluate the
corresponding source-availability obligations with counsel.

## Reporting an inventory error

Open an issue with the component, version, and the conflicting license text. We will
correct the inventory and, when needed, ship a new SBOM with the next release.
