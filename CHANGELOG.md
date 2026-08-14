# Changelog

All notable changes to Dazyflow are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The repository, Go module, and daemon binary are named `dazyflow` / `dzd`.
Versions here correspond to git tags `X.Y.Z` on
[git.sr.ht/~klahr/dazyflow](https://git.sr.ht/~klahr/dazyflow). The running
version is stamped into the binary at build time and surfaced on
`GET /api/v1` (the `build` block) and in the web UI's account menu.

Releasing: move the entries below from `[Unreleased]` under a new
`[X.Y.Z] - YYYY-MM-DD` heading, commit, then run `make patch` (or
`minor` / `major`) to cut the annotated tag against that commit.

## [Unreleased]

### Added

- **Support tickets + chat** (native, Phase 2 of the Support feature) — an org
  member can file a ticket about a flow and chat with support in-app; support
  agents work a cross-tenant queue and reply/resolve. Filing auto-attaches a
  **redacted** diagnostic bundle for the referenced flow/run (structure + error,
  no secrets or run data), so support can help the common case without a live
  read-only grant. Chat bodies are secret-scrubbed on ingest. New "Report a
  problem" action on the run-failure page and a Support section in the nav.
  Gated by `DAZYFLOW_SUPPORT_ENABLED`; `whoami` exposes `support_tickets_enabled`.
- **Actionable "contact support"** — the operator-configured support contact is
  now a real link beyond the Connections page: on generic errors (flow editor)
  and, prefilled with run diagnostics, on the run-failure page.

- **Fortnox connector** (`drops/fortnox/`) — Sweden's dominant SMB accounting:
  create customer, create invoice, list invoices (paid-invoice poll source),
  and a customer picker. OAuth 2.0 via `client_secret_basic` (new daemon
  support, below).
- **46elks connector** (`drops/elks/`) — send SMS via the Swedish/Nordic 46elks
  API. Static-credential (HTTP Basic) service connection; no daemon changes.
- **Klarna connector** (`drops/klarna/`) — Order Management: get order, capture
  (full/partial), refund (full/partial). Static-credential (HTTP Basic) service
  connection, region-hosted (EU/NA/OC × prod/playground); no daemon changes.
  Money-moving POSTs are retry-off + write-deduped (no upstream idempotency key).
- **nShift connector** (`drops/nshift/`) — Nordic multi-carrier shipping over the
  Unifaun ExtAPI: create a shipment (book), get a shipment (status/tracking), and
  delete an unprinted draft (cancel). Static-credential (Bearer API key) service
  connection, environment-hosted (integration/production, defaulting to the
  sandbox); no daemon changes. Booking/delete are retry-off + write-deduped.
- **Roaring connector** (`drops/roaring/`) — Nordic company-data enrichment:
  company overview (org number → registered name / status / full record) and
  company search (name → candidate matches). Uses Roaring's OAuth2
  client-credentials grant, exchanged for a bearer token by the connector itself
  and cached in-process — so it's a static-credential (Consumer Key + Secret)
  service connection with no daemon OAuth changes.
- **Phone value drop** (`drops/value/`) — validate and normalize a phone number
  to E.164 with a default-region setting (libphonenumber), emitting country,
  national number, and type; the flow editor shows a live country flag beside
  the field for international input. The SMS-input sibling of the `url` drop.
- **OAuth `client_secret_basic` support** in the daemon's OAuth registry
  (`daemon/oauth.go`) — token requests can present client credentials in an
  HTTP Basic header instead of the form body, selected per provider via
  `TokenAuthStyle: "basic"`. Fortnox requires it; all existing providers keep
  the default form-body behavior.
- **Runs date-range filter.** `GET /api/v1/me/runs` and
  `GET /api/v1/me/flows/{flow_id}/runs` accept `since` and `until` query params
  (RFC3339 timestamp or bare `YYYY-MM-DD`) bounding a run's enqueue time —
  `since` inclusive, `until` exclusive. The Runs page gains a From/To date
  picker that resolves a selected day to local-midnight instants, so filtering
  is server-side and paginates correctly instead of narrowing only the rows
  already loaded.

### Changed

- **Twilio, Discord, MQTT, and Stripe now use a first-class service connection**
  (`ConnectionFields`) instead of loose secret references, so each gets a proper
  entry form on the Apps page (the same shape as ntfy / Home Assistant / SMTP)
  and a "connected / needs setup" state — previously you had to create the
  secret by hand in the secrets manager. Credentials are entered once and are no
  longer node params, so they never appear in the graph.

  **BREAKING — re-enter credentials once after upgrading.** The credentials move
  to per-tenant connection storage; the old secret names are no longer read:
  - Twilio: `TWILIO_ACCOUNT_SID` / `TWILIO_AUTH_TOKEN` → `conn.twilio.*`
  - Discord: `DISCORD_WEBHOOK_URL` → `conn.discord.webhook_url`
  - MQTT: `MQTT_USERNAME` / `MQTT_PASSWORD` (and the per-node `broker` param) →
    `conn.mqtt.*` (broker is now part of the connection)
  - Stripe: `STRIPE_API_KEY` → `conn.stripe.api_key` (the webhook triggers'
    `STRIPE_WEBHOOK_SECRET` is unchanged — it's verified server-side)

  Open each integration on the Apps page and enter its credentials once. Flows
  themselves need no edits.
- Realigned the OpenAPI definition of `GET /api/v1/me/runs` to the parameters
  the endpoint actually accepts (`status`, `since`, `until`, `limit`, `offset`,
  `workspace`, `tenant`); it had drifted to aspirational `from`/`to` plus
  `PageToken`/`PageSize`/`Sort` that were never implemented.

### Security

- **Secret references are no longer resolvable out of flow data.** The
  whole-string `secret://NAME` form was matched AFTER `${upstream.…}` /
  `${item.…}` substitution, so data a flow ingested from the outside world — a
  webhook body, an HTTP response, a form field, a spreadsheet cell — was
  re-interpreted as a credential reference. Anyone able to influence that data
  could read any secret in the organization by supplying the literal text
  `secret://NAME`, connection credentials (`conn.<slug>.<field>`) included, and
  through the `vault://` / `aws://` / `gcp://` schemes anything in the tenant's
  cloud secret manager. Redaction did not contain it: the drop received the
  plaintext in its params regardless of what the persisted run detail showed.
  The reference is now matched against the raw parameter only, before any
  substitution runs — mirroring `SubstituteString`, which likewise never
  re-scans its own replacements. Author-written references are unaffected.
- Stored secrets and wrapped DEKs are now bound to their row with AES-GCM
  additional authenticated data (`(tenant, name)` for a secret, `tenant` for a
  DEK). Without the binding, GCM proved only "sealed under this tenant's DEK",
  so an attacker with database write access could relocate a ciphertext — copy
  `conn.stripe.api_key`'s blob into a secret their flow may read — and recover
  the plaintext through an ordinary reference. Existing ciphertext keeps
  decrypting (an unbound open is attempted as a fallback) and upgrades to the
  bound form as values are rewritten; `--rotate-master-key` upgrades every DEK.
- `Vary: Origin` is now sent on every response, not only when the request's
  Origin matched. A shared cache could otherwise store one origin's
  `Access-Control-Allow-Origin` and replay it to a different origin. A
  disallowed origin in credentialed mode now gets no ACAO header at all rather
  than the comma-joined allowlist, which was never a valid header value.
- The auth/webhook rate limiter now reclaims per-IP buckets that were left
  DEPLETED. Token counts are only updated inside `Allow`, so an abandoned
  bucket kept a stale near-zero count and never satisfied the sweep's
  "fully refilled" test — selecting against exactly the buckets worth expiring,
  since a scanner or credential-stuffer leaves its bucket drained and never
  returns. Those entries survived until the map hit its cap and every insert
  started paying an O(n) eviction scan.
- Run quota is now enforced atomically. The monthly run-cap gate was a
  read-then-increment, so concurrent submissions at the limit could all pass
  and exceed the cap; it now reserves a slot in a single atomic step
  (`AddRunIfUnder`) before the run is enqueued.
- Stripe webhook events are recorded as processed only AFTER the plan change is
  applied (was: before), so a transient apply failure can no longer ack a
  retried delivery without ever applying the subscription state change.
- The sign-in page now validates the `return_to` parameter before navigating,
  closing an open-redirect reachable via a crafted `/signin?return_to=` link.
- The public `/form/` endpoints are rate-limited and cap their request body,
  matching the `/trigger/` surface.
- Workspace file download now refuses the internal `.scratch` tree, like the
  other file operations.
- A panic while processing a node (resolve, template rendering over untrusted
  graph data, sandbox setup) or while fanning out a webhook/event trigger no
  longer crashes the whole multi-tenant daemon: the worker recovers and
  force-fails the node, and the detached trigger fan-out recovers and logs.
- Added `X-Frame-Options: DENY` and `Referrer-Policy` to the app surface
  (clickjacking hardening); the embeddable `/form/` surface is exempt.
- Unauthenticated, DB-touching public endpoints (invitation view, SSO/auth
  config, subdomain resolve, TLS-allow, Google sign-in, handoff) are now
  per-IP rate-limited, matching the rest of the public surface.

### Fixed

- A graph run can no longer strand permanently when a worker shuts down. Once a
  node was claimed, some of its bookkeeping still ran on the claim context, so a
  SIGTERM could land between a terminal write and the dispatch of that node's
  dependents — leaving the node terminal, the dependents never enqueued, and the
  run "running" forever, which `ReapStuckGraphRuns` cannot recover because it
  bails on a MISSING node record. The same exposure let a shutdown fail the
  graph/predecessor READS and mark a node failed for no reason. All of a claimed
  job's store I/O now runs on a context detached from the claim loop; lease loss
  remains the only thing that aborts a claimed job.
- The scheduler no longer holds its mutex across the poll-outcome marker read,
  which is a store round-trip in production — a slow or hung secret store stalled
  rescan's map swap, leader re-anchoring, and `TrackedCount` behind it.
- `Engine.Run` keeps the results of a failed node's SIBLINGS. Merging and
  error-checking shared one pass, so nodes ordered after the failing one were
  dropped from `GraphResult.Nodes` — and since a layer is sorted by node ID,
  which results survived was decided alphabetically. Loop bodies read that map,
  so a body with one failing node was silently losing its other nodes' output.
- Graph/node `timeout_seconds` is clamped against int64 overflow — a hostile
  huge value previously wrapped negative and silently disabled the run/node
  timeout instead of capping it.
- Per-item write dedupe (engine) no longer aliases or re-fires across an
  auto-fanned node; the in-memory dedupe store returns isolated copies; the
  TOTP challenge attempt-counter increments atomically.
- Run-list pagination orders by `(enqueued_at, id)` so rows that share a
  timestamp can't duplicate or vanish across pages; added a `jobs(tenant,
  status)` index for the tenant-scoped hot paths.
- A `ListJobsForGraph` scope check no longer admits records with an empty
  tenant.
- The Postgres event bus no longer permanently drops an event whose row
  committed out of `BIGSERIAL` order (a lower id committing after the listener
  advanced past it): the listener re-scans a bounded trailing window and
  dedupes, so live run/node/progress UI events aren't skipped under multi-node
  load. (Run correctness was never affected — the job store is authoritative.)
- The Postgres pool now reserves headroom for the two permanently-held
  connections (event-bus listener + leader lock) so a low `max_conns` can't
  starve the workqueue.

## [0.2.0] - 2026-06-27

### Added

- **Flow duplicate.** `POST /api/v1/me/flows/{flow_id}/duplicate` copies a
  flow under a fresh ID (new trigger URLs, empty run history) and starts it as
  a disabled draft owned by the caller, so a copied cron/webhook can't fire
  before it's reviewed. Exposed as a per-card "Duplicate" action in the flow
  list that opens the copy in the editor.
- Licensed the project under the GNU Affero General Public License v3.0 or
  later (AGPL-3.0-or-later); added `LICENSE` and a README license section.
- SPDX license headers across all Go and TypeScript source files.

### Changed

- **Write dedupe is now Postgres-backed in multi-node deployments.** The
  engine's write-dedupe store (which suppresses a re-fire of a non-idempotent
  external write — Twilio SMS, Gmail/Discord/Sheets/Home Assistant — when a
  lease reclaim or crash recovery re-runs the same job) is now the shared
  `write_dedupe` table instead of a process-local map, so a reclaim by a
  *different* `dzd` replica sees the recorded result instead of sending the
  message twice. `dzd` fails to boot if the table can't be created, matching
  every other Postgres-backed store; the in-memory store remains the
  single-node/test implementation. The contract is unchanged (at-least-once).
- Consolidated 167 scattered coverage test files (`*_cov_test.go`,
  `*_cov2-4_test.go`, `*_coverage_test.go`, `*_extra_test.go`) into their
  per-subject `_test.go` files. No test functions were removed (3306 before
  and after); only the file layout changed.
- Decluttered the repository root: moved reference docs (`DEPLOY.md`,
  `COMPLIANCE.md`, `PRIVACY.md`, `SECURITY-SLA.md`, `TODO.md`) into `docs/`
  and the `Caddyfile` into `deploy/`, updating all cross-references.
  `README.md`, `LICENSE`, `CHANGELOG.md`, and `SECURITY.md` stay at the root
  by convention.

### Removed

- Stale planning docs `GDPR_FIXES.md` and `manual.md` (history retained in
  git); fixed the dangling links in `PRIVACY.md` and `COMPLIANCE.md`.
- Orphaned root `package-lock.json` stub (no root `package.json` exists).
- The dev-only `cmd/email-preview` template-preview generator and its
  generated `email-preview.html` artifact — unreferenced by the build, CI,
  and docs. Email templates are still previewable in the web UI.
- The `scripts/ha_loadtest` multi-node HA load-test harness — never wired
  into CI or the Makefile; leader-election and failover are covered by
  `daemon/leader_test.go`.

## [0.1.0] - 2026-06-08

Initial release.

### Added

- **Flow engine.** Graph-based flows with conditional branching, fan-out
  (`for_each`), reusable subgraphs, and per-node retry policies. Runs are
  persisted and observable end to end.
- **Connectors.** Built-in integrations for HTTP, Postgres, Slack, Gmail,
  GitHub, Git, Notion, Google Sheets, and Excel, plus shell and
  transform/value utility nodes.
- **AI steps.** Claude-backed LLM nodes for generation and transformation
  inside a flow.
- **Triggers.** Start flows from inbound webhooks or timezone-aware cron
  schedules.
- **Web UI.** Visual flow builder and run viewer, with light and dark
  themes.
- **MCP server.** Exposes the connector catalog so an LLM agent can
  discover, compose, and run flows.
- **Control plane.** gRPC API with the `dzctl` CLI, plus a REST surface
  documented by an OpenAPI spec under `/api/v1`.
- **Auth & multi-tenancy.** Organizations, role-based access control, TOTP
  two-factor auth, invitations, and a platform super-admin role.
- **Secrets.** Master-key-encrypted storage for connector credentials.
- **Deployment.** Docker Compose stack (daemon + Postgres) with a boot
  guard that refuses to start on insecure defaults.
- **Versioning.** Version metadata stamped into the binary at build time,
  surfaced on `GET /api/v1` and in the web UI; `make bin`/`major`/`minor`/
  `patch`/`upgrade` release targets.
