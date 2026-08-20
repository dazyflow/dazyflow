# Changelog

All notable changes to Dazyflow are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The repository, Go module, and daemon binary are named `dazyflow` / `dzd`.
Versions here correspond to git tags `X.Y.Z` on
[git.sr.ht/~klahr/dazyflow](https://git.sr.ht/~klahr/dazyflow). The running
version is stamped into the binary at build time and surfaced on
`GET /api/v1` (the `build` block) and in the web UI's account menu.

Releasing: write what shipped under `[Unreleased]` as you go, then run
`make patch` (or `minor` / `major`). That target promotes `[Unreleased]`
under a new `[X.Y.Z] - YYYY-MM-DD` heading, leaves a fresh empty
`[Unreleased]`, and commits the changelog together with `./VERSION`
before tagging — so the tag lands on the commit that announces the
version. An empty `[Unreleased]` aborts the release. (`VERSION` matters
because the Docker build reads that file when it isn't handed a `VERSION`
build arg, so it is what a production `docker compose up --build` stamps
into the image.)

## [Unreleased]

## [0.5.0] - 2026-08-20

### Added

- **Undo/redo in the flow editor** — `Cmd/Ctrl+Z`, `Cmd/Ctrl+Shift+Z` and
  `Ctrl+Y`, plus toolbar buttons that stay visible (disabled) so the feature
  and its shortcut are discoverable before you need them. Built on
  whole-document snapshots rather than a command stack: the editor already
  maintains a complete serializer and deserializer that saving and loading
  depend on, so history cannot fall behind the document the way a stack of
  per-action inverses would. Snapshots are applied by reconciling the node and
  edge arrays, preserving object identity for everything unchanged, so undoing
  one step's move re-renders one card instead of the whole canvas. Continuous
  gestures coalesce — a drag or a run of keystrokes in one field is a single
  undo — and the stack is fenced whenever the document is replaced from
  outside the editor, including an edit arriving over the MCP flow-watch, so
  undo can never silently discard an assistant's change.

### Changed

- **"Drop" is now "step" (Swedish: *steg*) everywhere a person can read it.**
  The documentation already said step — 38 uses, a glossary entry, a "Step
  catalog" — while the UI said drop in 106 strings, so this closed a
  docs/UI mismatch rather than choosing new vocabulary. 104 English and 96
  Swedish strings changed, along with the MCP tool descriptions, the operator
  docs, `.env.example`, and the user-visible Go error strings. Swedish gender
  agreement moved with it: *steg* is neuter where *dropp* was common, so 49
  determiners and adjectives shifted (`den här droppen` → `det här steget`).
  Deliberately unchanged, and now the convention: the Go catalog and package
  paths, API routes and JSON field names, MCP tool *names* (`list_drops`,
  `describe_drop`), error codes (`drop_not_found`), audit action names, CSS
  classes, frontend identifiers — the contract every non-human consumer is
  grounded on — and the verb "to drop" ("drop rows", "drop a pin", "drop to
  upload"). `describe_drop`'s description now tells an assistant to say
  "step" to the user, so the split doesn't leak into a conversation.

- **`make patch` / `minor` / `major` promote the changelog themselves.** The
  release recipe moves `[Unreleased]` under the new version heading, leaves a
  fresh empty one, and commits it with `./VERSION` before tagging. This was
  previously a manual step the Makefile only *documented*, and it drifted:
  0.3.0, 0.3.1, 0.3.2 and 0.4.0 were all tagged with no changelog entry
  because nothing checked. An empty `[Unreleased]` now aborts the release,
  and a hand-written version heading is detected and left alone.

- Detail pages share one `BackLink` component instead of five hand-rolled
  copies. Two admin pages had been overriding the shared `.back-link` class
  with a duplicated inline style, putting them at double the bottom margin of
  every other detail page; labels now consistently name the parent
  ("Organizations") rather than mixing that with "Back to runs" and a bare
  "Back", which also reads better to a screen reader.

### Security

- **A store read failure could no longer be mistaken for "this flow doesn't
  exist".** `saveGraph` treated *any* error from the workspace store as the
  new-flow case, and the new-flow path skips the per-flow ownership gate and
  the active-run lock and enforces only the weaker `graph:edit`. A corrupt
  object or a transient I/O fault was therefore enough to let a non-owner
  overwrite an existing private flow. The store now returns a typed
  `ErrGraphNotFound` and every other error fails closed. The same fail-open
  shape in the delete path — which reported success without checking edit
  permission — was fixed with it.

- **Google sign-in is bound to the browser that started it.** The callback
  consumed its state token with no cookie check, so an attacker could complete
  a sign-in in a victim's browser for the attacker's account, and anything the
  victim went on to create or connect landed in the attacker's organization.
  The integrations OAuth flow already had the correct binding pattern; it had
  simply never been applied to the sign-in leg. The binding is mandatory here
  rather than skipped when absent, because sign-in has exactly one start path.

- **Org Google OAuth client secrets are encrypted at rest.**
  `org_auth.google_client_secret` was a plain `TEXT` column while every
  comparable secret in the system was encrypted, so a database dump exposed
  every organization's live client secret. Secrets now live in the per-tenant
  encrypted store under a per-tenant DEK, the column is written empty, and
  rows written before this migrate on first read with no operator step.

- **Secret redaction no longer leaks when one secret contains another.**
  Replacement ran in map-iteration order, so replacing a shorter secret first
  cut it out of the middle of a longer one and the longer secret's tail
  survived into the persisted run record in cleartext — intermittently, which
  is worse than always. Secrets are now replaced longest-first.

- The `DAZYFLOW_DEV_KEY` dev admin token is refused when the deployment
  doesn't look local (a public base URL or a remote database). It mints a
  publicly-known admin bearer token at every boot and was previously guarded
  only by a line in the documentation.

- bcrypt cost raised from 10 to 12, with an opportunistic re-hash on
  successful login — the only moment a correct plaintext is available, so the
  only place an old hash can be strengthened without involving the user.

- The audit trail no longer accepts forged entries. The failed-sign-in path
  records the email as typed — it must, that address is the credential-stuffing
  signal — and nothing had validated it, so a newline forged a second line in
  a compliance-relevant log.

- A Content-Security-Policy is set on the authenticated app surface, and the
  `shell` and `git` steps resolve their working directory through an
  `os.Root` handle instead of cleaning the path as a string, so a symlink
  planted inside the workspace can no longer be followed out of it.

### Fixed

- Postgres and the in-memory job store now agree on `core.ErrConflict` for a
  duplicate enqueue; they had diverged because the conformance test only
  asserted that *some* error came back.

- Error-to-HTTP-status mapping uses typed sentinels instead of matching
  substrings of user-facing messages, so rewording a message can no longer
  change a status code. Authorization failures that wrapped
  `core.ErrUnauthorized` without the exact phrase the matcher looked for had
  been returning 500 instead of 403.

- 27 steps that perform non-idempotent external writes now opt into
  engine-side write dedupe, up from 10 — so an expired-lease reclaim replays
  the recorded result instead of posting, charging or sending twice. Slack,
  Notion, Fortnox, GitHub, Stripe, Drive, calendar, MQTT, email, ntfy and
  webhook sends were all uncovered.

- MCP idempotency keys are hashed over canonical JSON. The key was hashed over
  raw argument bytes on the assumption that a retry sends byte-identical JSON,
  which the protocol doesn't guarantee — so a host that re-serialized its
  arguments produced a new key and a duplicated side effect, in exactly the
  situation the key exists to make safe.

- `on_error` is validated when a graph is saved, so an unrecognized value can
  no longer be accepted and then silently ignored, quietly downgrading a
  fallback edge to abort.

- Panics in drop-spawned goroutines are recovered instead of taking down the
  daemon — notably `for_each`, which runs arbitrary per-item node execution.
  The engine's recover only covers the calling goroutine.

- 31 steps declared no `meta` output port while emitting one, so a third of
  the catalog produced data the editor could not wire and an assistant reading
  the manifest could not see. A new contract sweep mutates one parameter of a
  valid worked example at a time, which reaches the connector code paths the
  previous adversarial sweep could never get past parameter validation.

- Unparseable `DAZYFLOW_*` values log the fallback instead of discarding the
  operator's setting silently; a missing database DSN is reported before a
  malformed SMTP URL can kill the process; and the HTTP listener's drain is
  awaited before exit so in-flight SSE streams and uploads aren't cut.

## [0.4.0] - 2026-08-19

Everything below shipped between 0.2.0 and 0.4.0. The 0.3.0, 0.3.1 and 0.3.2
tags were same-day interim cuts rather than distinct milestones, and none of
the four carried a changelog entry at the time — the release recipe only
documented the step instead of performing it (fixed in `[Unreleased]` above).
They are folded into this one heading rather than split retroactively, because
the boundaries can no longer be reconstructed accurately from the entries.

### Added

- **Support dashboard** (Phase 3 — the Support feature is now complete) — the
  cross-org queue grew the tools a support team actually works from: **assignment**
  (claim a ticket, hand it to a colleague, release it back to the pool — only
  provisioned support agents can be named), **ownership + status filters** on the
  queue (`?assignee=me`, `?unassigned=true`, `?status=`), and **stat tiles**
  counted server-side over the whole queue (`GET /support/tickets/summary`), each
  tile doubling as the filter for what it counts. **Role separation** was tightened
  in both directions: the customer's view of a ticket no longer carries the support
  organisation's internals (who owns it, which individual replied), the requester
  can close or reopen their own ticket but only support can declare it *resolved*,
  and a platform admin still isn't support staff (`platform:admin` does not imply
  `support:agent`). Every assignment is audited into the **org's own** log. The
  feature is now documented for operators: `DAZYFLOW_SUPPORT_ENABLED` in
  `.env.example` and a *Support tickets & consented flow access* section in
  `docs/DEPLOY.md`.
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

- Bumped `github.com/go-git/go-git/v5` to v5.19.2, clearing GO-2026-6214 (path
  traversal via crafted reference names) and GO-2026-6213 (worktree operations
  following symlinks). Both were reachable from our own call graph — the
  reference paths through `workspace.Store.SetRevisionLabel` /
  `PromoteToEnvironment`, and the worktree paths through the `git_checkout` /
  `git_diff` drops — so CI's symbol-granularity `govulncheck` failed on them
  rather than reading them as uncalled.

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

- The web UI no longer reports its version as `vdev` on a production deploy. The
  image version was stamped only when `VERSION` was exported into the
  environment of the `docker compose` call — which the Makefile targets do but
  the documented production command (`docker compose -f docker-compose.yml -f
  docker-compose.prod.yml up -d --build`) does not — and compose's `${VERSION:-dev}`
  default then baked a literal `dev` into the build arg. The build now falls back
  to a committed `./VERSION` file (kept in step with the tag by `make
  patch`/`minor`/`major`), so every build path stamps a real release. This also
  restores the admin System panel's update check for all operators: it reads the
  canonical instance's reported version, which an unstamped canonical build made
  unusable.
- `make upgrade` no longer tears down a production stack. It invoked
  `docker compose` with no `-f` flags, so on a host running the Caddy + docs
  overlay it recreated the stack from `docker-compose.yml` alone — dropping TLS
  termination and the docs site — and it picked the "latest" tag with
  `git tag --sort=-v:refname | head -1`, which sorts any non-version tag (a
  `nightly`) above every release. It now takes a `PROD=1` switch that merges the
  production overlay for every stack target, selects the newest bare `X.Y.Z` tag
  (skipping pre-releases), and stays on the deployed tag instead of returning to
  master. It also refuses to recreate the stack when `caddy` is running but the
  file set it would apply omits it — a check on what is running versus what is
  about to be applied, so a host that configures the overlay through compose's
  own `COMPOSE_FILE` (the recommended setup for a permanent production host) is
  not nagged about a flag it doesn't need. New `make latest` prints the selected
  tag for use in a deploy script.
- The sign-in form's submit button is no longer permanently disabled after a
  session expires. Clearing the token re-ran the identity bootstrap effect, whose
  cleanup could flip its `cancelled` guard before the in-flight `whoami`
  settled — so the `.finally()` that clears `loading` never ran and the button
  stayed gated on a stale `loading: true`, with a full page reload the only way
  back in.

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
