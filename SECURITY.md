# Security Policy

## Supported versions

| Version | Supported |
| --- | --- |
| 0.1.x | Yes |
| < 0.1 | No |

Security fixes are applied to the latest release line. Upgrade to the latest patch
release before reporting an issue that may already be fixed.

## Reporting a vulnerability

Please **do not open a public issue** for suspected vulnerabilities.

Use GitHub's private vulnerability reporting:

1. Open the repository's **Security** tab.
2. Choose **Report a vulnerability**.
3. Include affected version/commit, reproduction steps, impact, and any suggested
   mitigation.

If private reporting is unavailable, contact the maintainer listed in
[CODEOWNERS](.github/CODEOWNERS) through GitHub and ask for a private channel.
Never send production credentials, customer data, or API keys in a report.

## Response targets

| Stage | Target |
| --- | --- |
| Acknowledgement | 3 business days |
| Triage and severity assessment | 7 business days |
| Fix or mitigation for critical/high issues | 30 days where feasible |
| Public disclosure | Coordinated after a fix is available |

These are targets, not a commercial SLA. We will keep you informed of material
changes and credit reporters who wish to be named.

## In scope

- Authentication and session handling, including OIDC, JWT rotation, and API keys.
- Tenant isolation, authorization, quota enforcement, and audit integrity.
- Document upload, parser sandbox escape, archive/format handling, and SSRF.
- Retrieval/citation authorization bypass and cross-tenant data exposure.
- Secret handling, release artifacts, image signing, and supply-chain integrity.
- Injection, XSS, CSRF, and unsafe default configuration in the application.

## Out of scope

- Vulnerabilities in third-party services you deploy separately, unless the
  integration code introduces the issue.
- Findings that require a compromised host, cluster-admin access, or physical access.
- Automated scanner output without a reproducible impact.
- Denial-of-service reports against a deployment you do not own.

## Deployment hardening checklist

The defaults are designed for local development. Before exposing a deployment:

- Set `APP_ENV=production`; startup rejects development identity and placeholder
  credentials in this mode.
- Set `DEV_AUTH_ENABLED=false` and configure OIDC with your identity provider.
- Replace every `change-me-*` and `replace-*` value in `.env`; use managed secrets.
- Put PostgreSQL, Redis, MinIO, and metrics listeners on private networks.
- Use TLS at the ingress and set `AUTH_COOKIE_SECURE=true`.
- Evaluate `TENANT_RLS_MODE=enforce` for strong tenant isolation.
- Enable backups/PITR and object-storage versioning; see
  [docs/operations.md](docs/operations.md).
- Pin release images by digest and verify signatures/SBOMs; see
  [docs/releases.md](docs/releases.md).

## Supply chain

Release images are scanned with Trivy, published with CycloneDX SBOMs, and signed
with cosign keyless signatures. Verification commands are in
[docs/releases.md](docs/releases.md). Third-party components are inventoried in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and [docs/licensing.md](docs/licensing.md).
