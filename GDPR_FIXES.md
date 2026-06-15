# GDPR / EU compliance — prioritized fixes

Tracking list from the data-protection review (2026-06-15) of `hzd` for an
EU (Amsterdam) deployment. Grounded in a code audit; see [PRIVACY.md](PRIVACY.md)
for the full assessment and [COMPLIANCE.md](COMPLIANCE.md) for the ISO 27001
mapping. Priorities: **P0** compliance-blocking, **P1** important, **P2** good
hygiene. Org = organisational/legal task (not code).

---

## P0 — compliance-blocking

### [x] P0.1 — Account & org deletion with cascade (Right to erasure, Art. 17) — **DONE**
**Done:** New endpoints — `DELETE /api/v1/me/account` (self-serve, confirmation-guarded),
`DELETE /api/v1/admin/users/{email}` (platform admin), `DELETE /api/v1/admin/orgs/{tenant}`
(platform admin or org admin of that tenant). The cascade (`daemon/gdpr.go`) erases, via narrow
capability-interface assertions on each store: the `users` row, `sessions`, `api_keys`,
`memberships`, `invitations`, `jobs`, `run_logs`, `bus_events`, org SSO config + profile, and the
tenant's workspace + sandbox directories. Audit policy chosen: **pseudonymise** the actor on user
erasure (`AnonymizeActor` blanks actor + IP-bearing detail, keeping the trail) and **hard-delete**
the audit on org deletion. Account deletion also wipes the personal org's data when the subject is
its sole member. New store methods added across `auth/`, `daemon/`, `engine/jobstore/`. Tests:
`TestEraseUserIdentity_NoResidual`, `TestDeleteOrgData_NoResidual` assert no residual rows/files.

**Why:** No way to delete a user account or an org/tenant today. Member
removal (`DELETE /api/v1/admin/members/{email}`, `daemon/orgs.go`) revokes
sessions and the membership row, but the `users` row, audit events, run logs,
job payloads, and the tenant's `/data` graphs all persist indefinitely. This
is the single biggest functional GDPR gap.
**What:**
- Add a user-account deletion path and an org/tenant deletion path.
- Cascade across: `users`, `memberships`, `invitations`, `sessions`,
  `api_keys`, `audit_events` (or anonymise actor), `run_logs`, `jobs` /
  `bus_events`, and the tenant's `/data` workspace + sandbox dirs.
- Decide audit policy: hard-delete vs. pseudonymise the `actor`/IP so the
  security trail survives erasure without retaining personal data.
**Where:** `auth/postgres.go`, `auth/postgres_orgs.go`, `daemon/orgs.go`,
`daemon/service.go` (graph/workspace removal), `daemon/runlog_pg.go`,
`daemon/audit.go`.
**Interim:** documented, tested manual-deletion runbook covering every store
above (see PRIVACY.md § Data-subject rights).
**Done when:** an operator can fully erase a data subject (and an org) through
a supported path, and a test asserts no residual rows/files remain.

### [~] P0.2 — International-transfer story for connectors (Chapter V) — *Org + product*
**Product/doc: DONE.** Egress allowlist (`HAZYFLOW_HTTP_EGRESS_ALLOW`) is documented + encouraged in
PRIVACY.md § Transfers; hzd now logs a startup **advisory** when it's unset in production
(`cmd/hzd/main.go applyNetworkPolicy`). Configurable LLM base URLs (`drops/claude/claude.go`,
`drops/openai/openai.go`) for EU/self-hosted routing are documented. **Org/legal: REMAINS** — the
Record of Processing, sub-processor DPAs, DPF-certification checks / SCCs + TIAs, and enabling LLM
zero-retention are operator deliverables (no code can satisfy them).

**Why:** Connectors send flow data to mostly US-based vendors (Anthropic,
OpenAI, Google, GitHub, Slack, Notion, Stripe — `drops/*/`). Each is a
restricted transfer needing a lawful mechanism.
**What (org):** Record of Processing; sub-processor list (use the PRIVACY.md
table); DPA + DPF-certification check or SCCs + transfer impact assessment per
connector; enable LLM **zero-retention / no-training**.
**What (product):** make the egress allowlist a documented, encouraged default
scoped to approved (ideally EU) endpoints; document the configurable LLM base
URLs (`drops/claude/claude.go`, `drops/openai/openai.go`) for EU/self-hosted
routing.
**Where:** `drops/net/egress.go` (allowlist), DEPLOY.md, PRIVACY.md.
**Done when:** every enabled connector has a documented transfer mechanism and
egress is constrained by `HAZYFLOW_HTTP_EGRESS_ALLOW`.

---

## P1 — important

### [x] P1.1 — Data export endpoint (Right to access + portability, Art. 15/20) — **DONE**
**Done:** `GET /api/v1/me/export` returns a single machine-readable JSON document (served as a
download) assembling the subject's profile, memberships, invitations, redacted API keys, flows, and
run history (`daemon/gdpr_export.go`). Best-effort per section so a partial deployment still exports
what it can. Authentication is the only gate — the principal binds the subject, so a caller only
exports their own data.

**Why:** No bulk "download my data"; only piecemeal read APIs
(`/api/v1/me`, `…/me/api-keys`, `…/me/runs`, `…/me/flows`).
**What:** an endpoint that assembles a subject's (and/or an org's) personal
data into a single machine-readable export (JSON).
**Where:** `daemon/httpgateway.go` + the `auth`/`daemon` stores.
**Done when:** a user/admin can fetch a complete structured export in one call.

### [x] P1.2 — Self-service rectification (Right to rectification, Art. 16) — **DONE**
**Done:** `POST /api/v1/me/password` (verifies current password, re-hashes, revokes all sessions) and
`POST /api/v1/me/email` — a supervised re-key that creates the row under the new address, re-points
memberships and API keys, revokes the old sessions, and deletes the old row (`daemon/rectify.go`).
Display-name/profile rectification already exists for orgs (`PUT /api/v1/admin/org/profile`); there
is no per-user display-name field. Tests: `TestChangePassword*`, `TestChangeEmail_Rekey`,
`TestChangeEmail_TargetTaken`.

**Why:** Email is the immutable primary key; no profile or password-change
endpoint.
**What:** self-service display-name/profile edit, a change-password flow, and
an email-change path (decouple identity from the email PK, or support a
supervised re-key).
**Where:** `auth/postgres.go` (schema/PK), `auth/password.go`,
`daemon/httpgateway.go`.
**Done when:** a user can correct their own profile, email, and password
without DB surgery.

### [x] P1.3 — Enforce TLS to Postgres (`sslmode=require`) — **DONE**
**Why:** Code didn't validate `sslmode`; `prefer`/unset can silently fall back
to plaintext (Art. 32, encryption in transit).
**Done:** `productionConfigProblems` now rejects any DSN without
`require`/`verify-ca`/`verify-full` (fatal in prod, warning under
`HAZYFLOW_DEV=1`); added `dsnSSLMode()` helper + tests.
**Where:** `cmd/hzd/main.go`, `cmd/hzd/main_test.go`.

### [ ] P1.4 — Confirm EU data residency of all infrastructure — *Org/ops* (cannot be code-completed)
**Note:** purely an operations/verification task — confirm DO Managed Postgres + backups, the
container registry, and any OTLP/tracing endpoint are EU-region (Amsterdam), and that the `/data`
PVC + any object storage are EU. No code change can satisfy this; the operator checklist in
[PRIVACY.md](PRIVACY.md) item 4 tracks it. (hzd now enforces `sslmode=require` — P1.3 — and warns on
unconstrained egress — P0.2 — which support, but don't prove, residency.)

**Why:** Server location supports residency only if every dependency is in-EU.
**What:** verify DO Managed Postgres + its backups, the container registry, and
any OTLP/tracing endpoint are all in an EU region (Amsterdam). Confirm `/data`
PVC and any object storage are EU.
**Done when:** an inventory shows no control-plane personal data leaving the EU.

---

## P2 — hygiene

### [x] P2.1 — Personal data in run logs / payloads — **DONE**
**Done:** per-run log deletion endpoint `DELETE /api/v1/me/runs/{run_id}/logs` (authorized like
reading them; `daemon/me_routes.go` + `Service.DeleteRunLog`), and an opt-out of payload/content
logging via `HAZYFLOW_LOG_RUN_PAYLOADS=false` — drops streamed `progress` content lines while
keeping the status/terminal trail (`daemon/runlog.go RecordingBus.SetLogPayloads`, wired in
`cmd/hzd/main.go`). Store methods `DeleteRun`/`DeleteByTenant` on both Pg and in-memory run-log
stores. Residual risk is documented in PRIVACY.md. Tests: `TestRecordingBus_PayloadOptOut`.

**Why:** Run logs, job payloads, and bus events may carry arbitrary personal
data from flows; only secrets are redacted (`engine/redact.go`), not PII.
Retained 30 days (`HAZYFLOW_RUN_LOG_RETENTION`).
**What:** per-run log deletion endpoint; an opt-out of payload/output logging;
document the residual risk (done in PRIVACY.md). Optionally extend redaction to
configurable PII patterns.
**Where:** `daemon/runlog_pg.go`, `daemon/eventbus_pg.go`, `engine/redact.go`.

### [x] P2.2 — Write PRIVACY.md — **DONE**
**Done:** added [PRIVACY.md](PRIVACY.md) (roles, data inventory, sub-processor
table, data-subject rights, retention, Art. 32 measures, cookies, transfers,
AI Act/NIS2 notes, operator checklist, known gaps).

### [ ] P2.3 — Fix stale retention claim in COMPLIANCE.md
**Why:** COMPLIANCE.md states retention "Default is unset (retain
indefinitely)", but the code defaults to 30 d jobs / 90 d audit / 30 d run logs
(`cmd/hzd/main.go` `startRetentionSweeps`).
**What:** correct the line; cross-reference PRIVACY.md § Retention. Add the new
GDPR items to COMPLIANCE.md § Known gaps.
**Where:** `COMPLIANCE.md`.

---

## Status summary

| ID | Priority | Item | Status |
|----|----------|------|--------|
| P0.1 | P0 | Account/org deletion + cascade (erasure) | ☑ done |
| P0.2 | P0 | Connector transfer mechanisms (DPA/DPF/SCC) + egress default | ◑ product/doc done; org/legal remains |
| P1.1 | P1 | Data export endpoint (access/portability) | ☑ done |
| P1.2 | P1 | Self-service rectification (profile/email/password) | ☑ done |
| P1.3 | P1 | Enforce `sslmode=require` | ☑ done |
| P1.4 | P1 | EU residency of all infra | ☐ ops-only (cannot be code-completed) |
| P2.1 | P2 | PII in run logs (deletion/opt-out/redaction) | ☑ done |
| P2.2 | P2 | PRIVACY.md | ☑ done |
| P2.3 | P2 | Fix COMPLIANCE.md retention line + gaps | ☑ done |
