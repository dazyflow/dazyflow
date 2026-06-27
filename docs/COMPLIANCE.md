# ISO/IEC 27001:2022 control mapping

This document maps **dazyflow's product-level technical controls** to the
relevant Annex A controls of ISO/IEC 27001:2022. It is a reference for
operators running `dzd` inside an ISO 27001-certified organisation, and
for auditors assessing the product as a component of an Information
Security Management System (ISMS).

**Last reviewed:** 2026-06-04 · **Standard:** ISO/IEC 27001:2022 (Annex A,
93 controls across 4 themes).

## What this document is — and is not

ISO/IEC 27001 certifies an **organisation's ISMS** (clauses 4–10: scope,
risk assessment and treatment, Statement of Applicability, management
review, internal audit, continual improvement). **Software cannot be
"ISO 27001 certified" on its own** — certification is awarded to the
operating organisation's processes, not to a binary.

What dazyflow does is **implement the technical controls an ISMS relies
on**. This mapping covers the Annex A controls that are realisable in the
product (primarily the Technological controls, A.8.x, plus the
technically-enforced Organizational controls). Controls that are purely
organisational — policy, HR, physical security, supplier management,
incident-response process — are listed in
[§4 Organisational controls](#4-organisational-controls-out-of-product-scope)
as out of product scope, with a note on how the product supports them
where applicable.

This file should be cited from, not substituted for, the operator's
Statement of Applicability (SoA).

## Compliance posture — how to describe this externally

ISO 27001 compliance is a property of an **organisation's certified ISMS**,
not of software. There is no configuration, dependency, or amount of code
in this repository that makes dazyflow "ISO 27001 compliant" — a binary has
no risk register, management review, or internal audit, which are the
things the standard certifies. Certification is awarded to the operating
organisation by an **accredited certification body** after a Stage 1 + Stage
2 audit of its ISMS (clauses 4–10) and applicable Annex A controls.

What dazyflow does is **implement the technical Annex A controls, and produce
the evidence**, that let an organisation place the product inside an
ISO 27001-certified ISMS. That is a necessary input to certification — not
certification itself.

**Use this phrasing externally** (customer security questionnaires, RFPs,
trust-centre pages):

- ✅ "dazyflow is **built to support ISO/IEC 27001 control objectives**;
  see its control mapping." — accurate whether or not a certificate exists.
- ✅ "Deployed within our **ISO 27001-certified ISMS** (certificate no. …,
  scope …)." — only when your *organisation* holds a current certificate
  and this deployment is in its scope.
- ❌ "dazyflow is **ISO 27001 compliant / certified**." — a binary cannot be
  either. Asserting it without an in-scope organisational certificate is
  itself an audit finding (and a misrepresentation risk in a contract).

The distinction also bounds responsibility: the [§1](#1-technological-controls-a8x)/[§2](#2-organizational-controls-realised-in-the-product-a5x)
**Met** controls are what the product carries; the [§4](#4-organisational-controls-out-of-product-scope)
controls and ISMS clauses are what the operating organisation must hold to
turn "supports the controls" into "certified."

## How to read the status column

| Status | Meaning |
|---|---|
| **Met** | The product implements the control; the operator inherits it by deploying per [`DEPLOY.md`](DEPLOY.md). |
| **Configurable** | The control is available but must be switched on or tuned by the operator (an env var, a deployment choice). Not on by default, or default needs review. |
| **Shared** | The product provides the mechanism; the operator must supply policy, process, or surrounding infrastructure to complete it. |
| **Gap** | Known shortfall in the product. Tracked in [§3 Known gaps](#3-known-gaps-and-remediation). |
| **Org** | Organisational control, out of product scope. See [§4](#4-organisational-controls-out-of-product-scope). |

---

## 1. Technological controls (A.8.x)

| Control | Status | Implementation & evidence |
|---|---|---|
| **A.8.2 Privileged access rights** | Met | Permission-based RBAC separates a per-tenant super-admin (`tenant:admin`) from a cross-tenant platform super-admin (`platform:admin`). Permissions are explicit and checked uniformly for sessions and API keys. `core/rbac.go`, `core/authz.go`, `daemon/platform_admin_test.go`. |
| **A.8.3 Information access restriction** | Met | Every gateway action is authorised against the caller's principal and tenant; cross-tenant access requires `platform:admin`. Multi-tenant isolation is enforced at the data layer (tenant-scoped queries). `core/authz.go`, `daemon/visibility_test.go`, `daemon/subdomain_test.go`. |
| **A.8.4 Access to source code** | Org | Repository access control is a source-hosting/organisational concern. |
| **A.8.5 Secure authentication** | Met | Passwords verified with bcrypt and a constant-time compare; uniform error prevents account enumeration (`auth/password.go`). Optional TOTP 2FA with an AES-256-GCM-encrypted seed and bcrypt-hashed single-use recovery codes (`auth/totp.go`). OIDC / Google SSO with per-org config (`auth/oidc.go`, `daemon/google_signin.go`). Auth endpoints rate-limited 20/min/IP, burst 10 (`daemon/ratelimit.go`). |
| **A.8.6 Capacity management** | Met | Prometheus metrics expose queue depth, worker backlog (`oldest_queued_seconds`), and Postgres pool saturation; per-tenant disk quotas. `daemon/metrics.go`, `core/quota.go`, [`DEPLOY.md` §Observability](DEPLOY.md). |
| **A.8.7 Protection against malware** | Org | Host/container malware protection is operator-owned. The product reduces attack surface: no plugin/marketplace install path — the drop catalog is fixed at build time. |
| **A.8.8 Management of technical vulnerabilities** | Met | CI runs `govulncheck` against the full module graph on every build, failing on any vulnerability reachable from called code (`.builds/archlinux.yml`, `vuln` task). Dependencies are standard Go modules pinned in `go.mod`/`go.sum`. A default remediation SLA is documented in [SECURITY-SLA.md](SECURITY-SLA.md) — see also [§3](#3-known-gaps-and-remediation). |
| **A.8.9 Configuration management** | Met | All configuration is documented `DAZYFLOW_*` env (`.env.example`). A fail-closed boot guard refuses to start on insecure defaults (default DB password, empty master key) and names the offending value (`DEPLOY.md` §Fail-closed config guard). `DAZYFLOW_DEV=1` downgrades the guard for local trials only. |
| **A.8.10 Information deletion** | Configurable | Retention sweeps run by default: jobs **30 days** (`DAZYFLOW_JOB_RETENTION`), audit events **90 days** (`DAZYFLOW_AUDIT_RETENTION`), and run logs **30 days** (`DAZYFLOW_RUN_LOG_RETENTION`, defaults to the job window). Set a value ≤ 0 to disable a sweep (retain indefinitely). Tune to your policy; cross-reference [PRIVACY.md § Retention](PRIVACY.md). `.env.example`. |
| **A.8.11 Data masking** | Met | The secret store UI is write-only (values never read back); engine output is redacted to keep secret values out of logs and run records. `engine/redact.go`, [`DEPLOY.md` §Secrets](DEPLOY.md). |
| **A.8.12 Data leakage prevention** | Met | Secrets are never returned in plaintext after write; the secret-manager `GET` returns a redacted view; metrics that reveal tenant names are off by default (`DAZYFLOW_ENABLE_METRICS`). `engine/redact.go`, `daemon/httpsecretmanager.go`. |
| **A.8.13 Information backup** | Shared | Product makes all durable state recoverable from one Postgres DB; `DEPLOY.md` documents logical backup and PITR. The master key must be backed up separately. Backup schedule, off-site storage, and restore testing are operator process. [`DEPLOY.md` §Backup & restore](DEPLOY.md). |
| **A.8.14 Redundancy of facilities** | Met | Multi-replica deployment works out of the box (Postgres event bus + advisory-lock leader); graceful drain and a crash reaper prevent stranded runs; health/readiness probes. `daemon/leader.go`, `daemon/reaper_test.go`, `daemon/health.go`. |
| **A.8.15 Logging** | Met | Append-only, per-tenant audit trail records administrative actions (`graph.save/run/delete`, `secret.put/delete`, `secret_manager.put/delete`, `apikey.issue/revoke`, `invitation.create/revoke`, `org_auth.update/delete`, `org_profile.update`) **and authentication-lifecycle events** (`auth.signin`, `auth.signin_failed`, `auth.signout`, `auth.signup`, `auth.mfa_challenge`, with the sign-in method and source IP in detail). Failed sign-ins record under the platform-level tenant so credential-stuffing is visible without revealing account existence. `core/audit.go`, `daemon/audit.go` (`auditAuth`), `daemon/audit_auth_test.go`. |
| **A.8.16 Monitoring activities** | Met | Prometheus RED metrics (HTTP + per-node), OTLP tracing via standard `OTEL_*` env, health probes, and the audit trail give detection signal. Suggested alert thresholds are documented. [`DEPLOY.md` §Observability](DEPLOY.md), `daemon/tracing.go`. |
| **A.8.17 Clock synchronisation** | Org | Host/container NTP is operator-owned. Audit and job timestamps use the host clock. |
| **A.8.18 Use of privileged utility programs** | Configurable | The `shell` drop is host RCE and is **off by default** (`DAZYFLOW_ENABLE_SHELL`); enable only on single-tenant/CI deployments. `DEPLOY.md` §Security knobs. |
| **A.8.20 Networks security** | Met | TLS terminated at a documented reverse-proxy contract; HSTS and `X-Content-Type-Options: nosniff` on forwarded-HTTPS; CORS allowlist + CSRF origin check. `daemon/httpgateway.go`, [`DEPLOY.md` §TLS](DEPLOY.md). |
| **A.8.21 Security of network services** | Met | `sslmode=require` for Postgres; gRPC control plane on a separate port with health service; per-org subdomain isolation keeps session cookies host-only. `daemon/tls.go`, `DEPLOY.md`. |
| **A.8.22 Segregation of networks** | Configurable | An always-on SSRF guard blocks outbound flow traffic to private/loopback/cloud-metadata addresses; `DAZYFLOW_HTTP_EGRESS_ALLOW` pins outbound drops to an allowlist; `DAZYFLOW_ALLOW_PRIVATE_EGRESS` (keep off on multi-tenant) scopes tenant reach. `SECURITY.md`, `daemon/httplimits.go`. |
| **A.8.23 Web filtering** | Configurable | Outbound HTTP egress allowlist (`DAZYFLOW_HTTP_EGRESS_ALLOW`) plus the SSRF guard constrain where flows may reach. |
| **A.8.24 Use of cryptography** | Met | Envelope encryption: a 32-byte AES-256 master key (KEK), held only in process memory, wraps a per-tenant data key (DEK); each secret is sealed AES-256-GCM under its tenant's DEK. Tokens use CSPRNG. Documented, re-runnable key rotation via re-wrap (`dzd --rotate-master-key`). Rotation *cadence* is operator policy. [`SECURITY.md`](../SECURITY.md), `daemon/encrypted_secrets_store.go`. |
| **A.8.25 Secure development life cycle** | Shared | `-race` tests, fuzz tests, `go vet`, and `govulncheck` gate every build; Postgres-backed integration tests run in CI. SDLC governance (review policy, threat modelling) is organisational. `.build.yml`, `daemon/api_fuzz_test.go`. |
| **A.8.26 Application security requirements** | Met | Input validation and graph linting reject malformed/unsafe definitions before execution; idempotency keys; HMAC-verified approval links. `core/validate.go`, `core/lint.go`, `daemon/idempotency.go`, `daemon/approval.go`. |
| **A.8.27 Secure system architecture** | Met | Defence-in-depth: fail-closed config, hashed-at-rest credentials, memory-only KEK, default-off dangerous features, SSRF guard always on. Documented in `SECURITY.md` / `DEPLOY.md`. |
| **A.8.28 Secure coding** | Met | Constant-time credential comparison, no account enumeration, atomic writes, CSPRNG tokens, bounded metric cardinality. Race + fuzz + vet + vuln in CI. `auth/*.go`, `.build.yml`. |
| **A.8.29 Security testing in development** | Shared | Fuzz and integration tests plus `govulncheck` run in CI. Independent penetration testing is an operator/organisational activity. |
| **A.8.30 Outsourced development** | Org | Not applicable to the product itself. |
| **A.8.31 Separation of environments** | Shared | `DAZYFLOW_DEV` cleanly separates a throwaway local trial from production behaviour; deployment separation (dev/stage/prod instances) is operator-owned. |
| **A.8.32 Change management** | Shared | Graph workspaces are git/filesystem-backed (versioned); audit trail records `graph.save`. Change-approval process around deploys is organisational. |
| **A.8.33 Test information** | Shared | Tests use synthetic data and a throwaway Postgres; no production data in the suite. `.build.yml`. |
| **A.8.34 Protection during audit testing** | Org | Audit scheduling/scoping is organisational. |

## 2. Organizational controls realised in the product (A.5.x)

| Control | Status | Implementation & evidence |
|---|---|---|
| **A.5.10 Acceptable use of information** | Shared | Default-off dangerous features (`shell`, private egress, dev key) make the secure configuration the default; acceptable-use policy is organisational. |
| **A.5.14 Information transfer** | Met | TLS in transit (reverse proxy), `sslmode=require` to Postgres, HMAC-signed unauthenticated approval links, single-use short-lived (2 min) subdomain handoff tokens. `DEPLOY.md`. |
| **A.5.15 Access control** | Met | See A.8.2/A.8.3 — RBAC with explicit permissions, per-tenant and platform scopes. `core/rbac.go`. |
| **A.5.16 Identity management** | Met | Email identities, invitations, per-org SSO, platform-admin bootstrap via `DAZYFLOW_PLATFORM_ADMINS`. `auth/invitation.go`, `auth/orgauthconfig.go`. |
| **A.5.17 Authentication information** | Met | bcrypt passwords; API-key secrets stored SHA-256 + per-key salt; session tokens stored as SHA-256 of a 256-bit CSPRNG value (store leak yields hashes, not live credentials); cleartext credentials shown exactly once. `auth/apikey.go`, `auth/session.go`. |
| **A.5.18 Access rights (provisioning/revocation)** | Met | API keys and sessions support expiry and immediate server-side revocation; sign-out and key revoke take effect at once. `auth/apikey.go`, `auth/session.go`, `daemon/admin.go`. |
| **A.5.23 Cloud services security** | Shared | Product runs on managed Postgres and behind a managed ingress; the cloud provider's own attestations cover the underlying platform. Master-key sourcing from AWS/GCP/Vault secret managers is documented. `SECURITY.md`. |
| **A.5.30 ICT readiness for continuity** | Shared | HA mechanics and graceful shutdown are built in (A.8.14); tested BCDR with RTO/RPO is organisational. |
| **A.5.33 Protection of records** | Configurable | Append-only audit trail; retention configurable, defaulting to 90 days (A.8.10). |

## 3. Known gaps and remediation

| # | Control | Gap | Remediation |
|---|---|---|---|
| 1 | **A.8.10 / A.5.33** | Retention sweeps default to jobs 30 d / audit 90 d / run logs 30 d — defined, but tune to your policy (and a value ≤ 0 disables a sweep, retaining indefinitely). | Operator: confirm the windows match your policy. See [PRIVACY.md § Retention](PRIVACY.md). |
| 2 | **A.8.8** | Detection (scanning) is in CI. A default **remediation SLA** is now documented in [SECURITY-SLA.md](SECURITY-SLA.md) (critical/high 7 d, medium 30 d, low 90 d). | Operator: confirm the windows match your infosec program, or adopt stricter ones. Process, not code. |
| 3 | **A.8.24** | Master-key rotation is fully supported but operator-triggered; no defined cryptoperiod. | Operator: define and document a rotation schedule. The `--rotate-master-key` mechanism already supports it (`SECURITY.md`). |
| 4 | **A.8.25 / A.8.29 (supply chain)** | `govulncheck` covers known-vuln detection and CI now produces an SBOM (`syft`, SPDX + CycloneDX — `.builds/archlinux.yml`, `sbom` task). No automated dependency-update bot is wired up yet. | Optional hardening: adopt a dependency-update bot; attach the generated SBOM to releases. |
| 5 | **A.5.34 (privacy / GDPR)** | EU/GDPR data-subject-rights and international-transfer items are tracked separately. | See [PRIVACY.md](PRIVACY.md): erasure + data export + rectification are implemented; connector transfer mechanisms (DPA/DPF/SCC) and EU-residency verification remain operator/legal tasks. |

> **Resolved 2026-06-04:** authentication-event logging (formerly gap #1, A.8.15/A.8.16) — sign-in success/failure, sign-out, signup, and the MFA challenge leg now emit audit events via `daemon/audit.go`'s `auditAuth`, covered by `daemon/audit_auth_test.go`.

## 4. Organisational controls (out of product scope)

These Annex A controls are satisfied by the operating organisation's ISMS,
not by `dzd`. They are listed so a reader does not assume the product
covers them. The product *supports* several (noted), but the policy,
process, and records are organisational deliverables.

- **A.5.1–A.5.8** — Information security policies, roles and
  responsibilities, segregation of duties, management responsibilities,
  contact with authorities, threat intelligence, security in project
  management.
- **A.5.9–A.5.13** — Inventory and acceptable use of assets, return of
  assets, classification and labelling of information. *Product handles
  all secrets uniformly; classification is policy-driven.*
- **A.5.19–A.5.23** — Supplier relationships, supplier agreements, ICT
  supply chain, and cloud-services security. *Relevant to your hosting
  provider; rely on their ISO 27001 / SOC 2 attestations.*
- **A.5.24–A.5.28** — Information security incident management
  (planning, assessment, response, learning, evidence collection).
  *Product's audit trail + metrics feed this; the process is yours.*
- **A.5.29–A.5.30** — Continuity during disruption / ICT readiness.
  *Product provides HA and backup mechanics (A.8.13/A.8.14); the tested
  plan with RTO/RPO is yours.*
- **A.5.31–A.5.37** — Legal/regulatory requirements, IP, protection of
  records, privacy/PII, documented operating procedures.
- **A.6.1–A.6.8** — People controls: screening, terms of employment,
  awareness and training, disciplinary process, post-employment
  responsibilities, confidentiality agreements, remote working, event
  reporting.
- **A.7.1–A.7.14** — Physical controls: secure areas, equipment, cabling,
  maintenance, secure disposal, clear desk/screen. *Inherited from your
  cloud/data-centre provider.*

## 5. Maintaining this document

- Re-review on every dependency bump that `govulncheck` forces, on any
  change to the auth/crypto/audit packages, and at minimum annually.
- The control statuses above are claims about the **product**. Your SoA
  must record, per control, the **decision** (applicable/excluded), the
  **justification**, and the **implementation status** for your
  organisation — citing this file where the product carries the control.
- Evidence is anchored to source paths; if a path moves, update the
  citation rather than the claim.
