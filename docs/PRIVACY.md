# GDPR & data-protection notes

This document describes how `dzd` (the dazyflow daemon) **processes personal
data**, which technical data-protection measures it implements, and what an
operator must do to run it in line with the EU General Data Protection
Regulation (GDPR) and related EU rules. It complements the
[ISO 27001 control mapping](COMPLIANCE.md) and [SECURITY.md](../SECURITY.md);
deployment specifics are in [DEPLOY.md](DEPLOY.md).

**Last reviewed:** 2026-06-15 · **Scope:** the dazyflow product. Not legal
advice.

## What this document is — and is not

GDPR compliance is a property of an **organisation's data-processing
practices**, not of software. A binary has no lawful basis, no Records of
Processing, no Data Processing Agreements, and no Data Protection Officer —
those are things the operating organisation holds. **Dazyflow cannot be
"GDPR compliant" on its own.**

What dazyflow does is **implement the technical and organisational measures
(Art. 24, 25, 32) that a compliant deployment relies on**, and expose the
controls (retention, encryption, access scoping, egress restriction) an
operator needs. This file inventories the personal data the product touches
and maps the product's measures to GDPR obligations, so the operator can
slot it into their own compliance programme. Cite it from — don't substitute
it for — your Record of Processing and DPAs.

**Use this phrasing externally:**

- ✅ "dazyflow **implements technical measures that support GDPR** (encryption,
  access control, configurable retention, egress restriction)."
- ✅ "Operated within our GDPR-compliant processing programme as **data
  controller**, with DPAs in place for each enabled connector."
- ❌ "dazyflow **is GDPR compliant / certified**." — a tool cannot be either.

## Roles

- **You (the operating organisation) are the data controller** (and, where you
  run flows on behalf of your own customers, also a **processor** to them —
  in which case you need a DPA with them and this product sits under it).
- **dazyflow-the-software is the tool you process with**, running on
  infrastructure you control. Anthropic provides the binary, not a managed
  service — Anthropic does not receive your data through dzd itself (see
  [§ Does the product phone home](#does-the-product-phone-home)).
- **Each connector vendor is a sub-processor** of any personal data your flows
  send to it (see [§ Personal data sent to third parties](#personal-data-sent-to-third-parties)).
  You need a lawful basis and a transfer mechanism for each.

## Personal data the product stores

All of the following lives in the operator's own Postgres (or, for graphs,
the operator's `/data` volume) — nothing is sent to the vendor. Tables are in
`auth/` and `daemon/`.

| Data | Where | Notes |
|---|---|---|
| Email, password hash (bcrypt), roles, TOTP secret (AES-GCM encrypted), recovery codes (bcrypt), email-verification token hash | `users` (`auth/postgres.go`) | Email is the primary key. |
| Membership & invitation records (email, inviter email) | `memberships`, `invitations` (`auth/postgres_orgs.go`) | |
| Sessions (subject, tenant, expiry) | `sessions` (`auth/postgres.go`) | Token stored only as SHA-256 hash. |
| API keys (subject, salted SHA-256 hash) | `api_keys` (`auth/postgres.go`) | Secret never stored in clear. |
| Audit events — actor (email), action, target, and **source IP for auth events** | `audit_events` (`daemon/audit.go`) | IP is personal data; retained 90 days by default. |
| Run logs & job payloads / node outputs | `run_logs`, `jobs`, `bus_events` (`daemon/runlog_pg.go`, `engine/jobstore`, `daemon/eventbus_pg.go`) | **May contain arbitrary personal data from the flows themselves** (e.g. emails in a processed CSV). Secrets are redacted (`engine/redact.go`); personal data is **not**. |
| Connector credentials & OAuth tokens (AES-256-GCM, per-tenant DEK) | `encrypted_secrets`, `encrypted_secret_deks` (`daemon/encrypted_secrets_store.go`) | Grant access to a person's third-party account (Gmail, Slack, …). Erased with the org. |
| Integration config — MCP servers, web-API catalogs, git mirrors (remote URL + account + editor email), runner registrations and tokens | `tenant_mcp_servers`, `tenant_web_apis`, `git_mirrors`, `tenant_runners`, `runner_tokens` | Carry admin emails and sealed credentials. Erased with the org. |
| Queued/running runner tasks — script, env, stdin | `runner_tasks` (`daemon/runner_tasks.go`) | **May contain arbitrary personal data**, same as run logs. Swept by `DAZYFLOW_RUNNER_TASK_RETENTION`; erased with the org. |
| Billing pointer (Stripe customer/subscription id, status), entitlement overrides + admin notes, monthly usage counters | `tenant_plans`, `tenant_entitlements`, `usage_counters` | Erased with the org. Invoices themselves live in Stripe under your own retention obligation. |
| Platform-admin and support-agent grants (email + who granted it) | `platform_admins`, `support_agents` | Email is the primary key. Revoked on account erasure; the granter's email is pseudonymised. |
| Blocklist entries (banned email/domain, reason, blocking admin) | `blocked_identities` (`auth/blocklist.go`) | **Retained on erasure** — see below. The blocking admin's email is pseudonymised. |
| Public link tokens + who minted them | `workspace_shares`, `collection_shares` (`daemon/share_pg.go`, `daemon/collectionshare_pg.go`) | The token is a bearer credential stored in the clear so the dialog can re-display it. A `collection_shares` row makes that collection's rows readable without an account — see below. Erased with the org; the minter's email is pseudonymised. |
| Flow graph definitions | git repos under `/data` (`daemon/service.go`) | On disk, not in Postgres. May embed personal data in literals. |

## Personal data sent to third parties

This is the most important point for an automation tool: **flows are designed
to send data to external services, and the built-in connectors are almost all
US-based.** Any flow that passes personal data through one of these performs a
restricted international transfer (GDPR Chapter V).

| Connector | Endpoint | Location |
|---|---|---|
| Claude | `api.anthropic.com` (`drops/claude/claude.go`) | US (base URL configurable) |
| OpenAI / ChatGPT | `api.openai.com` (`drops/openai/openai.go`) | US (base URL configurable) |
| Gmail, Sheets, Forms, Google OAuth | `*.googleapis.com` (`drops/gmail`, `drops/sheets`, …) | US |
| GitHub | `api.github.com` | US |
| Slack | `slack.com/api` | US |
| Notion | `api.notion.com` | US |
| Stripe | `api.stripe.com` | US |
| ntfy, SMTP, generic HTTP / webhook | operator/flow-specified | depends |

**This table is the starting point for your Record of Processing and your
sub-processor list.** For each connector you enable you must establish a
lawful basis, sign a DPA, and put a valid transfer mechanism in place — the
EU-US Data Privacy Framework (check the vendor is certified) or Standard
Contractual Clauses plus a transfer impact assessment. For the LLM connectors
in particular, enable the vendor's **zero-retention / no-training** option for
any prompt that may carry personal data.

### Levers the product gives you

- **Configurable LLM endpoints** — the Claude and OpenAI steps accept a custom
  base URL, so you can route to an EU-region or self-hosted/proxied model
  instead of the US default.
- **Egress allowlist** — `DAZYFLOW_HTTP_EGRESS_ALLOW` restricts which hosts
  flows may reach (exact host, `*.subdomain`, or CIDR). Set it to your
  approved (ideally EU) endpoints so a flow can't silently exfiltrate to an
  un-vetted destination. An always-on SSRF guard additionally blocks
  private/loopback/metadata addresses (`drops/net/egress.go`).
- **Public collection links** — a member with edit rights can publish a
  collection as a login-free read-only table (`/board/<token>`, minted from
  the Collections page). The token is the only credential and the page shows
  the collection's rows **as they are** — every column, no redaction — so
  treat minting one as a disclosure: it needs a lawful basis like any other,
  and a leaked URL is a leaked table. There is no field-level filtering to
  fall back on. Links are listed in the app per workspace
  (`GET /api/v1/me/collection-shares`), revocable individually, audited on
  create and delete (`collection.share.create` / `.delete`), and erased with
  the org. If your deployment should not have the surface at all, leave
  `CollectionShares` unwired — it is nil without Postgres, and every mint
  then returns 501.
- **Data residency** — run Postgres, backups, the container registry, and any
  OTLP/tracing endpoint in an EU region (e.g. DigitalOcean Amsterdam). This
  keeps the control-plane personal data in the EU; only connector traffic
  leaves, governed by the mechanisms above.

## Data-subject rights

Each right now has a supported endpoint (built 2026-06-15).

| Right (Article) | Product support | How to service it |
|---|---|---|
| **Access (15) / Portability (20)** | **Built in** — `GET /api/v1/me/export` returns a single machine-readable JSON document (profile, memberships, invitations, redacted API keys, flows, run history, **support correspondence with full message bodies, the subject's own audit trail including source IPs, Collections boards by name/row-count, and platform roles held**), served as a download. Scoped to the requester: a colleague's tickets and audit events are never included (Art. 15(4)). An `excluded` field names each category deliberately left out and why. | Call the export endpoint as the subject. |
| **Rectification (16)** | **Built in** — `POST /api/v1/me/password` (change password) and `POST /api/v1/me/email` (supervised email re-key: re-points memberships + API keys, revokes sessions). Org display-name via `PUT /api/v1/admin/org/profile`. | Use the self-service endpoints. |
| **Erasure (17)** | **Built in** — `DELETE /api/v1/me/account` (self-serve, `?confirm=<email>`), `DELETE /api/v1/admin/users/{email}` (platform admin), `DELETE /api/v1/admin/orgs/{tenant}`. Cascades across users, sessions, api_keys, memberships, invitations, jobs, run_logs, bus_events, shares (workspace and collection links), support tickets/bundles/grants, encrypted secrets (values **and** the tenant's wrapped DEK, so leftover ciphertext is crypto-shredded), MCP servers, web-API catalogs, git mirrors, runners + registration tokens, runner tasks, per-tenant drop switches, billing/entitlement/usage rows, org config/profile, and the tenant's `/data`. Account erasure additionally revokes platform-admin/support-agent grants and pseudonymises the erased address **everywhere it authored a row an org keeps** — `created_by` / `updated_by` / `invited_by` / `disabled_by` across integrations, runners, mirrors, shares, bundles, memberships and invitations, plus grant records and blocklist entries. This is the shared-org path: the org carries on, so its rows are kept and scrubbed rather than deleted. Every person-naming column is enumerated with its disposition in `daemon/gdpr_coverage_test.go`, which fails the build if an undeclared one is added. Audit is pseudonymised (user) or deleted (org). A `deliberatelyRetained` disposition is required in code for any tenant-scoped table that is *not* erased, and a test fails the build when a new one appears without one (`daemon/gdpr_coverage_test.go`). Erasing an org does **not** cancel a live Stripe subscription — the erase report warns when it removes the pointer to one, and cancelling it is an operator step. | Call the deletion endpoint; member removal (`DELETE …/admin/members/{email}`) still exists for the lighter "remove from org" case. |
| **Restriction (18) / Objection (21)** | Disable the account's sessions/keys to halt processing. | Revoke sessions + API keys; pause the org's flows. |
| **Automated decisions (22)** | dzd runs operator-authored flows; any profiling is in your flow logic, not the platform. | Assess per flow. |

## Retention (Art. 5(1)(e))

Retention sweeps run hourly and are **on by default** (`cmd/dzd/main.go`):

| Data | Env var | Default |
|---|---|---|
| Terminal jobs | `DAZYFLOW_JOB_RETENTION` | 30 days |
| Run logs | `DAZYFLOW_RUN_LOG_RETENTION` | = job retention (30 days) |
| Audit events (incl. IPs) | `DAZYFLOW_AUDIT_RETENTION` | 90 days |
| Bus-event spool | — | 1 hour (fixed) |

A value `<= 0` disables that sweep (retain indefinitely) — only do this with a
documented justification. Set each to the shortest period that meets your
operational and legal needs. Note: user accounts, memberships, API keys and
graphs are **not** swept — they persist until explicitly deleted (see Erasure).

**Results boards (built-in store)** are likewise **not** swept by retention.
Rows a flow saves via the *Built-in store · Save* step accumulate in the
workspace's store until cleared — by the per-board **Clear** action on the
Results page or by deleting the workspace/account (the store lives under the
sandbox subtree, so it rides the erasure cascade; see Erasure). Boards are
user-curated output, not machine exhaust, so treat them as data you keep until
you decide otherwise.

### What the export deliberately leaves out

The export document carries an `excluded` array naming these in-band, so a
recipient can tell an omission from an empty category:

- **Collections board contents.** Boards are listed by name and row count, not
  dumped. Their rows are flow output — leads, form responses, collected
  contacts — and so are usually personal data about **third parties**; returning
  them under one member's access request would disclose other people's data,
  which Art. 15(4) exists to prevent. Row-level export stays on the Results
  page, used by someone acting for the org rather than for themselves. (This
  matches how run history is already treated: ids and status, never payloads.)
- **Anti-abuse blocklist entries.** Disclosing the fact and reason of a ban
  through an automated endpoint would undermine the measure. Service that part
  of an access request manually, with a balancing test.

### What erasure deliberately keeps

Two things survive an erasure request, both on stated grounds:

- **Blocklist entries naming the erased person** (`blocked_identities.value`).
  A ban a person can lift by asking to be forgotten is not a ban. The entry is
  kept under **legitimate interest** (Art. 17(1)(c) / 6(1)(f)) — preventing
  abuse and re-registration by an account the operator already removed. Only
  the *blocking admin's* email on the row is pseudonymised. Record this in your
  Record of Processing; a data subject who objects is entitled to a
  balancing-test answer, not an automatic deletion.
- **Audit events**, pseudonymised rather than deleted (Art. 17(3), Recital 26)
  so the security trail survives without the identifier.

Note also that platform-admin status has a second source the daemon cannot
edit: the `DAZYFLOW_PLATFORM_ADMINS` env allowlist. Erasing such an account
emits a warning in the erase report — remove the address from your deployment
config too, or it silently re-elevates if that person ever signs up again.

## Security of processing (Art. 32)

Implemented by the product; see [SECURITY.md](../SECURITY.md) and
[COMPLIANCE.md](COMPLIANCE.md) for detail.

- **At rest:** AES-256-GCM envelope encryption for secrets/OAuth tokens/TOTP
  (per-tenant DEK wrapped by `DAZYFLOW_MASTER_KEY`); bcrypt passwords;
  hashed session and API-key tokens.
- **In transit:** TLS for the browser (Secure cookies + HSTS behind a TLS
  proxy), optional gRPC mTLS, HTTPS to all connectors, and **enforced TLS to
  Postgres** — `dzd` refuses to start in production unless the DSN sets
  `sslmode=require` / `verify-ca` / `verify-full` (`productionConfigProblems`,
  `cmd/dzd/main.go`).
- **Access control:** per-tenant + per-workspace isolation enforced at the
  authz layer and in SQL (`core/authz.go`); one org cannot read another's data.
- **Master key:** keep a sealed off-cluster backup — losing it makes every
  stored secret undecryptable.

## Cookies / ePrivacy

The only cookie is the session cookie (`dazyflow_session`): `HttpOnly`,
`SameSite=Lax`, `Secure` over HTTPS (`daemon/httpgateway.go`). It is
**strictly necessary** for authentication, so under the ePrivacy Directive it
needs **no consent banner**. There are no analytics, advertising, or
third-party tracking cookies, and the frontend loads no third-party
scripts/fonts. If you later add analytics, this changes and consent is required.

## Does the product phone home?

No automatic telemetry. There is no analytics SDK, crash reporter, or tracker
in the daemon or frontend. The only outbound calls the platform makes on its
own are operator-controlled: an **update check** (only when an admin opens the
System page; URL configurable via `DAZYFLOW_UPDATE_URL`, empty disables) and
**OTLP tracing** (only if `OTEL_EXPORTER_OTLP_ENDPOINT` is set). Everything
else is the flows you build.

## Other EU regimes (brief)

- **Breach notification (Art. 33/34, 72h):** the audit trail aids detection;
  the notification process itself is organisational.
- **DPIA (Art. 35):** likely required if you process special-category data or
  do large-scale monitoring through flows — assess per use case.
- **EU AI Act:** the LLM connectors typically fall under transparency
  obligations — disclose to people when they are interacting with, or shown
  output from, an AI system.
- **NIS2 / DORA:** sector- and size-dependent; organisational.

## Operator checklist

1. Maintain a **Record of Processing**; use the [third-party table](#personal-data-sent-to-third-parties) as your sub-processor list.
2. Sign a **DPA** and confirm a **transfer mechanism (DPF/SCCs)** for every connector you enable; enable LLM zero-retention.
3. Set **retention** values for your legal/operational needs.
4. Keep all infrastructure (Postgres + backups, registry, tracing) in an **EU region**; use `sslmode=require` (now enforced) and back up `DAZYFLOW_MASTER_KEY` separately.
5. Constrain egress with `DAZYFLOW_HTTP_EGRESS_ALLOW`.
6. Point support staff at the **data-subject-rights endpoints** (export / rectification / erasure, below) — they're now built in, not a manual runbook.

## Known gaps

Tracked here and in [COMPLIANCE.md § Known gaps](COMPLIANCE.md):

- **International transfers** still need the *organisational* mechanism per
  connector (DPA + DPF/SCCs + TIA) and LLM zero-retention enabled. The product
  side (egress allowlist + startup advisory, configurable LLM base URLs) is in
  place.
- **EU residency** of Postgres/backups/registry/tracing is an operator
  verification task — `sslmode=require` is enforced and an
  advisory fires on unconstrained egress, but residency itself can't be proven
  in code.
- **Personal data in run logs/payloads is not redacted by default** — only
  secrets are (`engine/redact.go`). Mitigate with a short
  `DAZYFLOW_RUN_LOG_RETENTION`, `DAZYFLOW_LOG_RUN_PAYLOADS=false` (drops content
  lines, keeps the status trail), and per-run deletion via
  `DELETE /api/v1/me/runs/{run_id}/logs`.
- **Very short secret values are not redacted** — secret redaction
  (`engine/redact.go`) ignores plaintext shorter than `minRedactableSecretLen`
  (6 chars), because redacting a value like `"1"` or `"true"` would mangle
  every unrelated output containing that substring. A vendor token shorter
  than 6 characters would therefore pass through run logs/outputs unredacted.
  Two mitigations already cover this: the **save-time linter** flags a flow
  that wires a secret straight into a persistence sink (the
  `secret_to_persistence` rule), and resolved secrets only land in node
  *params*, never auto-copied into a `Result` unless a module deliberately
  echoes them. Operators handling sub-6-char credentials should still prefer
  `DAZYFLOW_LOG_RUN_PAYLOADS=false`.

> **Resolved 2026-06-15:** account/org **erasure** (Art. 17), **data export**
> (Art. 15/20), and **self-service rectification** (Art. 16) are now built-in
> endpoints — see the Data-subject rights table above.
