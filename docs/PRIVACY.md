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

- **Configurable LLM endpoints** — the Claude and OpenAI drops accept a custom
  base URL, so you can route to an EU-region or self-hosted/proxied model
  instead of the US default.
- **Egress allowlist** — `DAZYFLOW_HTTP_EGRESS_ALLOW` restricts which hosts
  flows may reach (exact host, `*.subdomain`, or CIDR). Set it to your
  approved (ideally EU) endpoints so a flow can't silently exfiltrate to an
  un-vetted destination. An always-on SSRF guard additionally blocks
  private/loopback/metadata addresses (`drops/net/egress.go`).
- **Data residency** — run Postgres, backups, the container registry, and any
  OTLP/tracing endpoint in an EU region (e.g. DigitalOcean Amsterdam). This
  keeps the control-plane personal data in the EU; only connector traffic
  leaves, governed by the mechanisms above.

## Data-subject rights

Each right now has a supported endpoint (built 2026-06-15).

| Right (Article) | Product support | How to service it |
|---|---|---|
| **Access (15) / Portability (20)** | **Built in** — `GET /api/v1/me/export` returns a single machine-readable JSON document (profile, memberships, invitations, redacted API keys, flows, run history), served as a download. | Call the export endpoint as the subject. |
| **Rectification (16)** | **Built in** — `POST /api/v1/me/password` (change password) and `POST /api/v1/me/email` (supervised email re-key: re-points memberships + API keys, revokes sessions). Org display-name via `PUT /api/v1/admin/org/profile`. | Use the self-service endpoints. |
| **Erasure (17)** | **Built in** — `DELETE /api/v1/me/account` (self-serve, `?confirm=<email>`), `DELETE /api/v1/admin/users/{email}` (platform admin), `DELETE /api/v1/admin/orgs/{tenant}`. Cascades across users, sessions, api_keys, memberships, invitations, jobs, run_logs, bus_events, org config/profile, and the tenant's `/data`; audit is pseudonymised (user) or deleted (org). | Call the deletion endpoint; member removal (`DELETE …/admin/members/{email}`) still exists for the lighter "remove from org" case. |
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
Rows a flow saves via the *Built-in store · Save* drop accumulate in the
workspace's store until cleared — by the per-board **Clear** action on the
Results page or by deleting the workspace/account (the store lives under the
sandbox subtree, so it rides the erasure cascade; see Erasure). Boards are
user-curated output, not machine exhaust, so treat them as data you keep until
you decide otherwise.

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
