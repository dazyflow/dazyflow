# Vulnerability remediation SLA

This document defines how quickly a known vulnerability in Dazyflow (or its
dependencies) is triaged and fixed once detected. It exists so operators,
auditors, and downstream users have a concrete, citable policy — it satisfies
the "documented remediation SLA" gap called out in
[COMPLIANCE.md §3](COMPLIANCE.md#3-known-gaps-and-remediation) (control
**A.8.8 — Management of technical vulnerabilities**).

> This is the **project's** default policy. A deployment operating under its
> own infosec program may adopt stricter windows; treat the numbers below as
> the ceiling, not the floor.

## How vulnerabilities are detected

| Source | Mechanism | Cadence |
| --- | --- | --- |
| Dependency CVEs reachable from called code | `govulncheck ./...` (`.github/workflows/ci.yml`, `security` job) | Every CI build; fails the build on any reachable vulnerability |
| Go toolchain / stdlib advisories | `govulncheck` (same task) tracks the Go version in `go.mod` | Every CI build |
| Reports from users / researchers | Private disclosure (see **Reporting** below) | On receipt |

Because the `security` job **fails the build** on a reachable vulnerability, a
fix (or a documented, justified suppression) must land before the affected
change can ship — the SLA below governs how fast that fix is produced.

## Severity → remediation window

Severity follows the CVSS v3.1 base score of the advisory (or the maintainer's
assessment for a privately-reported issue). The clock starts when the issue is
**confirmed reachable / exploitable** in a supported configuration.

| Severity | CVSS | Triage (acknowledge + assess) | Fix released |
| --- | --- | --- | --- |
| **Critical** | 9.0–10.0 | 24 hours | **7 days** |
| **High** | 7.0–8.9 | 2 business days | **7 days** |
| **Medium** | 4.0–6.9 | 5 business days | **30 days** |
| **Low** | 0.1–3.9 | 10 business days | **90 days** |

"Fix released" means a tagged release (or pushed `master` for continuous
deployers) with the upgraded dependency or code change, **and** a green
`govulncheck` run.

### Not reachable / not exploitable

`govulncheck` only fails on vulnerabilities reachable from called code, so most
"noise" never reaches this table. If an advisory is flagged but assessed as
**not reachable** (or not exploitable in any supported configuration), it is
recorded with that justification and tracked to the next routine dependency
bump rather than the windows above. The assessment is re-checked whenever
calling code changes.

## Reporting a vulnerability

Report suspected vulnerabilities privately — **do not** open a public issue.
Use GitHub's Private Vulnerability Reporting (the repository's **Security** tab
→ **Report a vulnerability**); [SECURITY.md § Reporting a
vulnerability](../SECURITY.md#reporting-a-vulnerability) has the full
instructions, and the rest of that file is the operator's master-key and
incident runbook. Include affected version/commit, a reproduction, and the
impact you observed; we acknowledge within the triage window above.

## Supply-chain hardening (related)

- **SBOM**: a CycloneDX/SPDX SBOM is produced in CI (the `Generate SBOM` step
  of the `security` job in `.github/workflows/ci.yml`, retained as the `sbom`
  build artifact) so downstream consumers can run their own CVE matching
  against the exact dependency set.
- Dependencies are standard Go modules pinned in `go.mod` / `go.sum`; every
  bump that `govulncheck` forces triggers a re-review (COMPLIANCE.md §3).
