# TODO

Open items collected across iterations. Each entry notes the impact and a
hint at what's needed. Items at the top section block real usage; items
further down are quality, observability, and known-unknowns.

## Production readiness — phased action plan (reviewed 2026-05-27)

Sequencing layer to go from "feature-complete alpha" to "deployable paid
SaaS." Most line-items are detailed in the sections further down
(Production blockers, Distribution & HA, T3, Observability); this orders
them and names the one gap those sections don't already call out: **the
daemon binary runs entirely on in-memory stores today, so a restart wipes
runs, sessions, API keys, and secrets.**

Rough state at review: features ~8/10, tests ~8/10 (all green), but
durability ~3/10, HA ~2/10, monetization ~1/10 → about **45% to
production**. (Update 2026-05-27: Phase 0 shipped — durability now ~7/10
with Postgres-backed jobs/keys/sessions/users/secrets.)

### Phase 0 — Durability: stop losing data on restart (DONE · 2026-05-27)

Shipped. `hzd` now persists everything to Postgres behind a single
shared `pgxpool` when `--postgres-dsn` / `$HAZYFLOW_POSTGRES_DSN` is set;
absent the flag it keeps the in-memory/JSON stores (dev only) and logs a
loud "lost on restart" warning.

- [x] **Wire the existing Postgres stores into `cmd/hzd`.** Added
  `--postgres-dsn`. `jobstore.NewPostgresFromPool(ctx, pool)` (new
  shared-pool constructor; `Close()` is a no-op when the pool is
  injected so the JobStore can't yank it from the other stores) and
  `daemon.NewPgSecretsStore` are wired when the DSN is set. Exported
  `daemon.SecretsBackend` (alias of the unexported `secretsStore`) so
  `setupEncryptedSecrets` can hold either backend. Boot log confirms
  "encrypted secret store: postgres-backed (durable)".
- [x] **Built the two missing Postgres stores.** New `auth/postgres.go`:
  `PgKeyStore` (APIKeyStore + AdminKeyStore), `PgSessionStore`,
  `PgUserStore`, plus `EnsurePgAuthSchema`. Roles stored as JSONB.
  Tables: `api_keys`, `sessions`, `users`. The store vars in `main.go`
  are now interface-typed (`auth.AdminKeyStore` / `SessionStore` /
  `UserStore` / `core.JobStore`) so either backend slots in.
- [x] **Users off the JSON file** when a DSN is set (`PgUserStore`);
  `JSONUserStore` remains the no-DSN dev fallback.
- [x] **Real-DB tests + bugs caught.** New `auth/postgres_test.go`
  (gated on `HAZYFLOW_TEST_DB`), all pass against a real PG 16. Running
  the long-gated jobstore path for the first time surfaced two latent
  bugs, now fixed: (1) `schema.sql`'s workqueue partial index used
  `now()` in its predicate — illegal (predicates must be IMMUTABLE);
  narrowed it to `kind='node' AND status IN ('queued','running')` and
  left the time-window filter in the Claim query. (2) the jobstore Pg
  test enqueued a record with no `Kind` (defaults to `graph`, which
  Claim never hands out) — fixed to `JobKindNode`.
- [x] **Exit criteria met.** Verified end-to-end: signed up a user,
  killed `hzd`, booted a fresh process against the same DB, signed in
  with the pre-restart credentials → HTTP 200 with the persisted
  tenant. Data lived only in Postgres.
- [ ] **Follow-ups (not blocking):** wire the `HAZYFLOW_TEST_DB` tests
  into a CI service (Phase 4); a one-time JSON-users → Postgres import
  for existing dev deployments (greenfield deploys don't need it).

### Phase 1 — Security hardening for public exposure (~1–2 wks)

Mostly enumerated under **Production blockers** — this is the ordering.
(Several quick wins shipped 2026-05-27 in the overnight batch.)

- [ ] TLS on the gateway (or an enforced reverse-proxy contract) +
  Secure/SameSite cookies + HSTS.
- [x] **`--dev-key` defaults off** (2026-05-27). Was `true`; now opt-in
  via `--dev-key` or `HAZYFLOW_DEV_KEY=1`.
- [x] **Rate limiting on `/auth/{signin,signup}`** (2026-05-27).
  Self-contained per-IP token bucket (`daemon/ratelimit.go`, no new
  dep); flags `--auth-rate-per-min` (default 20) / `--auth-rate-burst`
  (10); 0 disables. 429 + Retry-After on trip. 3 tests.
- [x] **Fail-loud on port-bind failure** (2026-05-27). Webhook + HTTP
  gateway now bind on the main goroutine (new `ServeListener` methods);
  `hzd` fatals on a port-in-use error instead of silently dropping the
  listener.
- [ ] SSRF egress allowlist for `http_request`; finish secret output
  sanitization.
- [ ] **Quota write race** — attempted in the overnight batch, **left
  for review**: closing it needs a reserve-and-hold-across-write change
  threaded through the module write path (lock held across I/O),
  correctness-critical enough to not land unattended. Snapshot model is
  at `engine/engine.go:~397`.
- [ ] Master-key provisioning + rotation runbook (KMS-ready).

### Phase 2 — HA / horizontal scale (~2–3 wks)

- [ ] Replace `MemoryBus` with Postgres LISTEN/NOTIFY (or NATS) — see
  "Multi-node event bus".
- [ ] Scheduler leader election via `pg_try_advisory_lock` — see
  "Scheduler leader election".
- [ ] Verify behaviour under concurrent multi-node load.

### Phase 3 — Monetization (T3 · ~3–4 wks)

Per-tenant metering → Stripe + plan gates → team features. Specced under
**T3** below.

### Phase 4 — Ops maturity (parallelizable · ~1–2 wks)

- [~] **Dockerfile + CI** (2026-05-27, overnight batch). Multi-stage
  `Dockerfile` (web bundle → static Go build → distroless nonroot,
  serves API + bundle on :8080). `.build.yml` for builds.sr.ht runs
  go build/test/vet + web build, with a Postgres service exporting
  `HAZYFLOW_TEST_DB` so the gated jobstore/auth tests run in CI (closes
  the long-standing "real-DB tests never exercised" gap). Still open:
  k8s/compose deploy manifests; confirm the image build in CI.
- [x] **`/readyz`** (2026-05-27) — pings Postgres when configured.
  Still open: `/metrics` — wire the OTel scaffolding in
  `engine/tracing.go` to an exporter.
- [ ] Postgres backup strategy + connection-pool sizing (see "Known
  unknowns").

**Critical paths:** private beta = Phase 0 + most of Phase 1 (~3–4 wks);
paid GA = 0 → 1 → 3, with 2 landing before the second serious customer
(~2–3 months total).

## Path to product-market fit (Zapier-shaped sell)

Active roadmap. Hazy-flow has the engine; the gap vs. Zapier is the
**integrations × OAuth × templates** flywheel and the **self-serve UX**
around it. These are the things a paying SMB/SaaS-ops customer expects
when they sign up to try the product. Tiered T0 → T3 from "without
this no customer can try" to "needed before paid conversion."

### T0 — Foundation that unblocks everything else

- [x] **Built-in encrypted secret store.** Shipped:
  `daemon/encrypted_secrets.go` (envelope encryption — AES-256-GCM
  KEK in process memory wraps per-tenant DEKs; ciphertexts stored
  via a `secretsStore` interface). Two backends: `MemSecretsStore`
  (dev / single-binary deployments) and `PgSecretsStore`
  (Postgres-backed for multi-replica). CRUD at
  `GET/PUT/DELETE /api/v1/secrets[/{name}]` — no GET-by-name
  (values write-only from outside), permission-gated on
  `secret:read`/`secret:write`. Scheme `tenant://<name>` resolves
  at job time via the new `core.WithTenant` context plumbing —
  tenant isolation enforced both at the store layer (WHERE
  tenant=$1) and the AEAD layer (distinct DEKs). 33 tests
  including tamper detection, DEK caching, race-safe provisioning,
  and end-to-end "PUT via API → resolves in graph params." Wired
  into `hzd` behind `--master-key` / `$HAZYFLOW_MASTER_KEY`; off
  by default. BYO cloud providers (Vault/AWS/GCP) deferred to T3.
- [x] **OAuth 2.0 handshake + per-tenant token store.** Shipped:
  `daemon/oauth.go` + `daemon/httpoauth.go`. Authorization-code
  flow with a per-provider config (URLs hardcoded, client_id /
  secret from env). Endpoints:
  `GET /api/v1/oauth/{provider}/authorize` (auth-required, mints a
  256-bit single-use state token, 302s to the provider) and
  `GET /api/v1/oauth/{provider}/callback` (unauth, validates state,
  exchanges code, stores `{access_token, refresh_token, expires_at,
  scope, extras…}` JSON as `tenant://oauth.<provider>.<account>`,
  redirects to `return_to` with `?oauth=success|error`).
  `GET /api/v1/oauth/providers` lists registered providers + each
  tenant's connected accounts. Defenses: same-origin `return_to`
  only, single-use replay-proof state, 10-min TTL, account-name
  validated through the secret-name validator. `hzd` wires Slack,
  GitHub, Google, Notion from `HAZYFLOW_OAUTH_<NAME>_CLIENT_ID/
  SECRET` env vars — providers without credentials skip silently.
  19 tests covering state machine, exchange (success + non-2xx +
  provider-200-with-error), HTTP flow (happy path, replay, denied
  consent, bad state), and 501 when not configured. Refresh-on-
  expiry deferred; tokens are whatever was last stored.
- [~] **Self-serve signup + first-run wizard.** Shipped:
  `POST /api/v1/auth/signup` (`daemon/httpsignup.go`) creates a
  bcrypt-hashed user with an auto-minted `usr_<hex>` tenant slug
  (keeps email out of URLs/logs), grants `editor` + `tenant_owner`
  roles (graph:run/edit/admin + secret:read/write + tenant:admin),
  and immediately issues a session so the UI lands the user
  in-app without a round trip. Behind `--signup` /
  `$HAZYFLOW_ENABLE_SIGNUP` so production deployments default to
  admin-invite-only. 11 backend tests including
  duplicate-rejection-preserves-original-password (the critical
  security pin), email normalization, 7 bad-email variants,
  tenant-ID uniqueness, perm grants. Frontend: `SignUp.tsx` page
  with confirm-password, link to/from signin, `Welcome.tsx`
  3-step landing wizard, routes wired for both authenticated and
  unauthenticated paths. **Open follow-ups (need product
  decision):** email verification (needs an SMTP / SES /
  SendGrid choice — the operator picks delivery infra),
  rate-limiting + captcha (needs a deployment-policy call —
  global throttling vs per-IP vs CAPTCHA). Neither blocks the
  signup story; production deployments today wire their own
  rate-limit reverse-proxy and admin-approve the invite list.
- [~] **`poll_trigger` primitive.** Interval-anchored firing
  shipped: new `GraphTrigger{Type: "poll", IntervalSeconds: N}`,
  `daemon/scheduler.go` extended (cron + poll share the same
  tracking map, discriminated on `scheduleFn != nil` vs
  `interval != 0`; trigger-index suffix in the tracked key so
  cron + poll on the same graph get separate entries),
  `integrations/trigger/poll_trigger.go` drop emitting `fired_at`
  RFC3339 UTC. 7 tests including the multi-fire interval check
  and the "bad interval is skipped, not tight-looped" defense.
  **Cursor-based dedupe** (the Zapier "fire only on new items"
  shape) is the follow-up: needs a graph-side `secret_set` drop
  + a daemon hook that scrapes a designated output port after
  successful runs. The v1 here is enough for "run my graph every
  N minutes" — Gmail/Sheets connectors will use it directly with
  cursor storage in `tenant://` secrets they manage themselves.

### T1 — The "looks and feels like Zapier" gap

- [~] **Slack launch connector.** Action drops shipped:
  `integrations/slack/slack_send_message.go` (chat.postMessage —
  text, thread_ts, Block Kit, body-port-wins-over-params with
  object-input rejection, Slack-envelope error mapping, retry
  policy) and `slack_list_channels.go` (conversations.list with
  types/limit/exclude_archived). Token resolution via either an
  explicit `token` param OR a `SetTokenLookup` hook that `hzd`
  wires to `OAuthRegistry.GetOAuthToken("slack", account)`.
  Brand asset at `/brands/slack.svg`. 17 tests using an
  httptest fake of the Slack API. **Trigger** (`slack_on_mention`,
  Events API) deferred — needs a separate signing-secret
  verification path + multi-tenant event routing by team_id;
  worth its own T1 entry.
- [~] **`secret_set` drop + cursor-based polling.** Piece (a)
  shipped: new `integrations/secrets/` package with a `secret_set`
  drop. Inputs: `value` (string; overrides params.value). Params:
  `name` (required, validated [A-Za-z0-9_.-], ≤128 chars), `value`
  (optional fallback). Outputs: `name` echo (never the value, so
  a misroute can't leak the secret into a persisted Result).
  Cross-cutting hook: `secrets.SetSecretWriter` mirrors the
  Slack/Gmail `SetTokenLookup` pattern — hzd installs a closure
  over `EncryptedSecrets.Put` next to the existing token-lookup
  wiring. The drop's `Description` and `poll_trigger`'s existing
  description document the explicit cursor pattern: read the
  prior fire's cursor via `${tenant://...}` template
  substitution, write the new one with a downstream `secret_set`
  node. 11 tests cover happy path, input-port-wins-over-param,
  bytes input, structured-input rejection (friendly Message +
  technical Details split), missing tenant/name/value, bad-name
  validator, unwired-hook clear error pointing at `--master-key`,
  write-failure detail surfacing, and tenant isolation.
  **Open follow-up (needs design decision):** piece (b) —
  daemon-side automatic cursor scraping after successful runs —
  would require inventing a "designated cursor port" manifest
  concept. Skipping for now because the explicit pattern via
  `secret_set` works end-to-end (used by gmail-new-email-to-
  slack and notion-poll-to-slack templates); an implicit cursor
  port adds magic without removing user work for the common
  case.
- [x] **`slack_on_mention` trigger.** Shipped:
  `integrations/slack/slack_on_mention.go` — trigger drop with
  outputs `text` / `user` / `channel` / `team` / `ts` / `event`
  (full raw payload for advanced use). Daemon side:
  `daemon/slack_events.go` mounts
  `POST /api/v1/events/slack/{tenant}` (no bearer auth — HMAC is
  the auth), with Slack's documented HMAC-SHA256 scheme
  (`v0:<timestamp>:<body>` over the signing secret) plus a
  ±5-minute replay window, `url_verification` challenge echo,
  and event_callback fanout that iterates every workspace in the
  tenant, loads every graph, and `SubmitGraphWithSeed`s any
  graph with at least one `slack_on_mention` node — same model
  the webhook listener uses for `webhook_input`. Fanout runs in
  a background goroutine so the HTTP ack stays well under
  Slack's 3-second retry budget. Standalone execution returns
  `no_trigger_data` with a friendly Message + technical Details
  (same pattern as `webhook_input`). hzd flag
  `--slack-signing-secret` (default
  `$HAZYFLOW_SLACK_SIGNING_SECRET`); empty leaves the endpoint
  returning 501 so misconfiguration shows clearly. 10 tests:
  URL verification challenge, bad/missing/stale/future
  signatures, 501 when unconfigured, end-to-end app_mention
  fires a subscribed graph (verifies the trigger node's output
  ports), reaction_added is acked-without-dispatch, unknown
  envelope types acked, standalone-run errors with
  `no_trigger_data`. **Channel filter shipped as a follow-up:**
  optional `channel_filter` param on the trigger node — when
  set, the events handler skips dispatch to graphs whose
  filter doesn't match the event's channel ID. Empty/missing
  filter preserves the old "fire for any channel" behavior.
  2 additional tests pin the filter (mismatched-graph skipped,
  unfiltered-graph still fires for any channel). **Still
  open (needs hosted-app decision):** team_id ↔
  connected-OAuth-account verification — the per-tenant URL +
  signing secret is the V1 auth model; layered team_id
  routing is the right add for a hosted shared-Slack-app
  deployment if/when that shape ships.
- [x] **Gmail launch connector.** Three action drops shipped:
  `integrations/gmail/gmail_send_email.go` (RFC822 construction
  with CRLF + header-injection defense, base64-URL-no-pad
  encoding, format=text|html, thread_id for replies),
  `gmail_search_messages.go` (Gmail query syntax + pagination via
  page_token), `gmail_get_message.go` (flattens Gmail's nested
  MIME tree into convenience fields: headers as map, body_text
  and body_html base64-decoded, raw passthrough). Daemon-wide:
  added `AuthorizeExtras` to `OAuthProvider` so Google's
  `access_type=offline` + `prompt=consent` (required to get a
  refresh_token) ride along on the authorize redirect. Brand
  asset at `/brands/gmail.svg`. 17 tests using an httptest fake
  of the Gmail API. "Fire on new email" is composable via
  `poll_trigger → gmail_search_messages (q:newer_than:5m) →
  for_each → gmail_get_message` — no dedicated trigger drop
  needed since the composition is cleaner than another drop with
  its own state.
- [x] **Google Sheets launch connector.** Two action drops shipped:
  `integrations/sheets/sheets_append_row.go` (POST values:append
  with USER_ENTERED parsing so "30" lands as number 30 and
  "=SUM(A:A)" as a formula; INSERT_ROWS vs OVERWRITE; emits
  meta.updated_range for follow-up writes) and
  `sheets_read_range.go` (GET values with FORMATTED_VALUE default,
  UNFORMATTED_VALUE preserves numeric/bool types). Both speak the
  canonical `rows`+`headers` shape — interchangeable with
  excel_read/postgres_query downstream. Shares the "google" OAuth
  app with Gmail (one lookup function in hzd, factored into a
  shared `googleLookup` closure). Brand asset at
  `/brands/sheets.svg`. 16 tests including the empty-rows
  short-circuit (avoids Sheets' 400 on empty appends), short-row
  padding both directions, and a contract test pinning output
  shape compatibility with excel_read. `sheets_on_new_row` trigger
  uses the same poll_trigger composition pattern as gmail's
  on_new_email — no dedicated drop needed.
- [~] **GitHub launch connector.** Three action drops shipped:
  `integrations/github/github_create_issue.go` (title + body +
  labels + assignees + milestone; **object-input bodies become
  fenced JSON code blocks** so a "payload was: …" issue renders
  cleanly in Markdown), `github_list_issues.go` (state /
  labels / assignee / since filters, `since` is what
  poll_trigger composition uses for "fire on new issue"),
  `github_add_comment.go` (works for both issues and PRs since
  GitHub treats them in one number space). Shares OAuth wiring
  with the others — `github.SetTokenLookup` reads from the
  existing OAuth registry's `github` provider. Pins the REST v3
  `Accept: application/vnd.github+json` +
  `X-GitHub-Api-Version: 2022-11-28` headers so a future API
  bump can't silently change behavior. Error extractor handles
  the nested `errors[].message` shape so "Validation Failed:
  title is required" makes it through, not just "Validation
  Failed". Brand asset at `/brands/github.svg`. 17 tests.
  **Webhook triggers**: shipped — `github_on_push` +
  `github_on_new_pr` drops, plus
  `POST /api/v1/events/github/{tenant}` in
  `daemon/github_events.go` (GitHub's `X-Hub-Signature-256`
  HMAC scheme, no replay window — GitHub relies on TLS +
  per-delivery UUIDs). One endpoint serves both triggers,
  dispatched by the `X-GitHub-Event` header. `ping` responds
  "pong" so GitHub's "test delivery" button works.
  `pull_request` only dispatches when `action == "opened"`
  (matching the drop's "new PR" name; other actions ack
  without dispatch — future drops like
  `github_on_pr_merged` can claim those). New hzd flag
  `--github-webhook-secret` (default
  `$HAZYFLOW_GITHUB_WEBHOOK_SECRET`); empty leaves the
  endpoint returning 501. 10 tests cover the matrix
  (ping/bad-sig/missing-sig/unconfigured/end-to-end push +
  PR/non-opened-acks/unknown-event-acks/both-standalone-runs).
- [~] **Notion launch connector.** Shipped V1:
  `integrations/notion/notion_create_page.go` (POST /pages with
  either `parent_database_id` OR `parent_page_id` — exactly one;
  outputs id/url/meta) and
  `integrations/notion/notion_query_database.go` (POST
  /databases/<id>/query with filter + sorts + cursor pagination;
  outputs pages/next_cursor/has_more/meta). Notion-Version pinned
  to 2022-06-28 so a future API bump can't silently change
  behavior. Error extractor decodes Notion's `{code, message}`
  envelope and surfaces the code in user-facing errors
  ("validation_error", "object_not_found", etc.) instead of a
  generic HTTP status. OAuth wiring identical to the other
  connectors: `SetTokenLookup` hook in `helpers.go`, hzd reads
  `HAZYFLOW_OAUTH_NOTION_CLIENT_ID/SECRET` (the registry slot
  was already declared). Brand asset at `/brands/notion.svg`.
  11 tests using an httptest fake of the Notion API.
  **`notion_on_db_change` (the "fire on new database row"
  pattern)** doesn't need a dedicated drop — the existing
  poll_trigger + notion_query_database + secret_set
  cursor-dedupe composition handles it (same pattern Gmail
  uses). A seed template that demonstrates the composition is
  the natural follow-up.
- [~] **Template gallery in the editor.** Shipped: static gallery
  at `/templates`. `web/public/templates/index.json` lists
  available templates; each template's graph lives in its own
  `<id>.json` file fetched lazily on "Use this template" (so the
  gallery page loads fast even with dozens of templates). Fork
  flow: clone the graph payload, generate a `<id>-<suffix>` ID,
  fill in tenant/workspace, PUT through the existing saveGraph
  endpoint, redirect to the new flow editor. No new daemon
  endpoint — just JSON files behind the web app's static asset
  server. Welcome wizard reordered so "Browse templates" is step
  1; FlowList has a "From template" button next to "New flow".
  Fifteen seed templates now cover both breadth (Excel→DB,
  webhook→Slack, daily Postgres digest, Gmail→Sheets,
  Sheet→Postgres upsert sync) AND depth (Gmail→Slack with
  cursor dedupe via `secret_set`, GitHub-issue→Slack webhook,
  Sheet×DB join→upsert exercising `join_rows`, daily group-by
  report exercising `group_aggregate`, webhook→Postgres event
  log, clean-Excel→email pipeline, Notion-poll-to-Slack with
  cursor dedupe, Notion→Postgres mirror, Slack-mention→GitHub-
  issue using slack_on_mention, push-to-main→Slack using
  github_on_push). Each entry carries an `integrations: [...]`
  array of brand slugs that render as small vendor logos on
  the card so users can scan "this one touches Slack +
  Gmail" without reading titles. Real brand logos in place
  (Gmail, GitHub, Notion, Sheets, Slack from Wikimedia/SVG
  Repo; Excel/MySQL/Postgres/SQLite as placeholder stubs).
  **Open follow-up (needs product decision):** an admin UI
  to add custom per-tenant templates beyond the shipped set
  — small backend (CRUD on a tenant://templates blob) but
  the UX shape isn't decided.

### T2 — Retention (failing-quietly is what kills trials)

- [~] **Run-history UI polish.** Shipped: new `/runs/{runID}`
  page (`web/src/pages/RunDetail.tsx`) with a failure banner
  that names the failing node + error code at the top, a run
  summary card (status / started / finished / duration / node
  count breakdown), a per-node timeline (status dot + duration +
  expandable input/output preview), and a Replay button that
  re-fires the same graph. Backend: extended
  `ListNodeRecordsOpts` with a `GraphRunID` filter (memory +
  Postgres both implement it) and added `GET
  /api/v1/jobs/{runID}/nodes` so the page draws the timeline in
  ONE round trip. Live-polls while anything's still in flight,
  same heuristic as RunList. 3 backend tests pin the
  "doesn't-leak-across-runs" + 404 + empty-array contracts. The
  /runs list page now links rows into the detail page first
  (editor link still available). **Inputs alongside outputs
  shipped as a follow-up:** each node body in the timeline
  now renders an "Inputs" section above "Output", reading
  from the JobRecord's serialized Job.Input field — no extra
  round trip since the listRunNodes response already carries
  it. **Still open (need product decisions):**
  side-by-side input/output diffs across reruns (needs a UX
  call: side-by-side vs unified vs hover-to-compare) and
  replay-with-modifications (needs scope decision: which
  fields can be edited at replay time — params only, or also
  wiring?). Both are higher-effort polish; the "what
  happened" surface that landed here was the load-bearing
  retention work.
- [~] **Failure notifications.** Shipped: new
  `core.Graph.FailureNotify{Webhook}` field + daemon dispatcher
  in `daemon/failure_notify.go`. Per-run goroutine subscribes
  to the bus at SubmitGraph time (subscribe-then-spawn so the
  dispatcher can't race past it); on terminal+failed, POSTs a
  compact payload (graph_id, run_id, tenant, workspace,
  error_code, error_message, failed_node, run_url, finished_at)
  to the configured URL. `failed_node` is filled by querying
  `ListNodeRecords` with the new GraphRunID filter when the
  TerminalEvent doesn't carry it. `run_url` deep-links to the
  run-detail page when `PublicBaseURL` is set on the Service.
  Race-recheck handles the "worker finished before subscribe"
  case. SettingsModal gets a new "Notifications" tab with the
  webhook URL field. 11 tests covering happy path, no-fire-on-
  success, no-spawn-when-unconfigured, failed-node lookup,
  race-recheck firing without a bus event, 500-from-webhook
  doesn't panic, payload-shape pinning. Webhook-only v1 covers
  Slack (incoming-webhook URLs), Discord, Teams, PagerDuty,
  custom receivers. **Open follow-up (needs product
  decision):** typed Slack-channel / email pickers — the
  user picks a connected Slack account + channel, the
  daemon dispatches via `slack_send_message` instead of an
  incoming-webhook URL. Needs a UX call on the picker shape
  AND a decision on whether failure notification should
  share token plumbing with the action drops (vs its own
  config). The webhook-URL V1 covers the realistic ops
  receiver set today; the picker UI is sugar on top.
- [~] **Trigger test/preview UX.** Shipped: "Sample this node"
  button in the editor's Inspector that fires a partial run
  ending at the selected node. Backend: new
  `core.UpstreamSubset(target) (Graph, bool)` helper computes the
  target plus every transitive predecessor via BFS over edges in
  reverse, preserving on_error / port metadata; new endpoint
  `POST /api/v1/graphs/{tenant}/{workspace}/{id}/nodes/{nodeID}/sample`
  loads the graph, filters to the subset, submits via the
  existing `SubmitGraph` (so authz, run records, SSE, failure
  notifications all work unchanged). Frontend: `onSample`
  callback on the Inspector that saves the in-flight edits first
  (so unsaved param changes participate in the partial run),
  POSTs to the sample endpoint, then swaps `currentRunID` and
  resubscribes to the existing SSE channel — the existing
  OutputPreview lights up automatically. 7 tests on the helper
  (leaf, mid-node parallel-branch drop, source-singleton, edge
  metadata preservation, missing target, identity/authz field
  carry-through, cyclic-edges no-hang); 4 handler tests (accept
  + subset payload verification, unknown node 404, unknown graph
  404, requires auth). **Open follow-up (needs schema
  decision):** filtering sample vs production runs in the
  RunList. The clean way needs a `Sample bool` field on
  `core.JobRecord` so list endpoints can filter by it without
  decoding GraphPayloads — the alternatives (Graph.Name marker,
  ID prefix hack) all fight other parts of the system. Punted
  until someone decides the migration shape (mem store +
  Postgres column + SubmitGraph signature). **Also out of
  scope:** synthetic webhook bodies (webhook_input nodes in
  the subset fail standalone with `no_trigger_data` — flow
  authors test those by firing the real trigger via curl).

### T3 — Monetization (needed before charging)

- [ ] **Per-tenant metering.** Count graph runs + node executions
  per tenant per month. Surface a usage page in the UI. Persistence
  in Postgres (a new `usage_counters` table). Reset on the tenant's
  billing day. ~4 days.
- [ ] **Stripe integration + plan gates.** Free tier (e.g. 100
  runs/month, no polling triggers, no premium connectors), paid
  tier unlocks polling + premium apps. Stripe Checkout for upgrades,
  webhook on payment events updates tenant plan. ~3 days.
- [ ] **Team features.** Multiple users per tenant, shared
  workflows, basic role split (owner / editor / viewer — the RBAC
  primitives already exist server-side, this is the UX). ~3 days.
- [ ] **BYO cloud secret providers** (Vault / AWS Secrets Manager /
  GCP Secret Manager). Same `core.SecretProvider` interface, new
  schemes (`vault://`, `aws://`, `gcp://`). Enterprise-tier feature
  for customers who insist on holding their own keys in their own
  KMS. ~2-3 days per provider.

### Out of scope for this push (deliberate)

- Mobile app (Zapier has one, almost nobody uses it for setup)
- SSO / SAML (enterprise tier; later)
- White-label / embed (much later)
- Most production-blockers from the section below (mTLS rotation,
  scheduler election, multi-node bus) — they matter at scale, not
  at "first 100 paying SMBs"

## Top of the pile — biggest realism gaps

These showed up while building the AP-invoice demo. Without them the
platform can demonstrate but not actually power a real workflow.

- [x] **Webhook body → graph input.** Shipped: `webhook_input` marker
  module in `modules/trigger/webhook_input.go` (outputs: body, headers).
  `daemon.SubmitGraphWithSeed` accepts a `map[nodeID]Result` and writes
  pre-completed node-records before normal dispatch takes over. Webhook
  handler reads the body (1 MiB cap), parses by `Content-Type`
  (`application/json` → object, `text/*` → string, else bytes), and
  seeds every `webhook_input` node in the graph. Cron triggers could
  reuse the same seeding path later.
- [x] **Template-style secret substitution.** Shipped: `engine.SubstituteString`
  / `engine.SubstituteValue` handle `${scheme:path}` inline anywhere in
  a string, with composition across multiple placeholders in one value.
  Wired into `resolveSecrets` so any string in `Job.Params` / `Job.Env`
  picks it up; the legacy whole-string `env://KEY` form is preserved.
  `for_each` adds `${item:path}` per-iteration on a deep-copy of
  step_params (dot-separated path traversal over maps and lists; empty
  path = whole item; missing-field errors surface in the `errors`
  output keyed by iteration index). E2E proves
  `url: "https://api/${item:id}"` + `Authorization: "Bearer ${env:TOKEN}"`
  routes correctly per item.
- [x] **Upstream-output templating.** Shipped: `${upstream:nodeID.port.path[idx]…}`
  resolves against prior-node results passed into `Engine.RunNode`.
  Dot-then-bracket path syntax over the port's Inline value (maps and
  slices). `resolveSecrets` renamed to `resolveTemplates` and now
  composes upstream + secret substituters in one pass — mixed strings
  like `https://hooks/${upstream:q.meta.id}?token=${env:TOKEN}` resolve
  in a single substitution. Maps/slices stringify as JSON for
  embedding; primitives use `fmt.Sprint`. Unknown nodeID errors out so
  typos don't silently produce empty values landing in DSNs/paths.
- [x] **`await_approval` pause/resume.** Shipped:
  `core.JobStatusAwaiting` (not terminal) + `core.StatusAwaiting` Result
  sentinel; `Memory.Complete` / `Postgres.Complete` accept awaiting and
  the resume path (awaiting → succeeded). The `await_approval` module
  in `modules/flow/` returns awaiting; the worker writes the awaiting
  record and frees the worker without dispatching dependents. Resume
  via `POST /approve/<graphRunID>/<nodeID>?token=<hmac>&decision=approve|reject`
  on `daemon.ApprovalListener`. `daemon.HMACApprovalSigner` mints
  deterministic per-(run, node) URLs so a worker that re-Executes
  after a lease expiry emits the same URL. Output ports: decision,
  approver, comment, approved/rejected control signals, context
  passthrough. Worker + Service share a `daemon.Dispatcher` so the
  edge-classifier (skip/fallback/dormant) applies identically whether
  a node terminates via Execute or via external resume.
  Persistence-across-restart is automatic when running against the
  Postgres jobstore (in-memory store loses awaiting records on
  restart, same as for queued/running). Caveats: token has no TTL
  (out of scope for V1); multi-replica deployments must share the
  HMAC secret out-of-band.

## Production blockers (security + correctness)

- [ ] **mTLS cert rotation.** Certs reload only at hzd restart. Need
  `tls.Config.GetCertificate` callback + filesystem watcher + atomic
  reload. *Otherwise certs expiring mid-flight wedge the cluster.*
- [ ] **CRL / OCSP support.** All certs signed by the configured CA are
  accepted. Revocation can't be enforced. Add CRL fetch (HTTP) or
  OCSP-stapling config to the TLS layer.
- [ ] **Concurrent-write race in quotas.** Snapshot model (`QuotaUsed`
  captured at job-start) lets two concurrent `file_write`s in the same
  tenant briefly exceed the limit. Per-tenant write mutex or OS-level
  quotas (XFS project quotas, ZFS) needed for hard enforcement.
- [ ] **Real secret providers (vault, gcp, aws, azure).** Interface +
  scheme registry + env/builtin providers exist now. The cloud KMS
  / vault implementations are real integrations we haven't done. Spec
  lists all four.
- [~] **Per-tenant ACL on secrets.** Shipped: `Namespaced bool` on
  `EnvProvider` and `BuiltinProvider`. When set (via the new hzd
  `--isolate-shared-secrets` flag, default off for backward
  compat), every `Get(ctx, name)` requires the name to be of the
  form `<tenant>.<key>` matching the caller's tenant from
  `core.TenantFromContext`. Names without a prefix and
  cross-tenant prefixes are both rejected at the provider before
  the underlying env/builtin lookup runs — so tenant A's graph
  can't read `${env:globex.api_key}` even if the env var exists.
  tenant:// was already isolated (per-tenant DEKs in
  `EncryptedSecrets`). 7 tests pin the matrix: matching prefix
  resolves, cross-tenant rejected, unprefixed rejected,
  missing-tenant rejected, backward-compat mode (Namespaced
  false) unchanged, builtin same behavior. **Open follow-up:**
  document the per-tenant env var convention (e.g.
  `HAZYFLOW_TENANT_<UPPER>_<KEY>`) in the README; today the
  provider just looks up the full prefixed name verbatim.
- [ ] **Egress allowlist for `http_request`.** SSRF blocks private IPs
  but a tenant can still reach any public IP. Production needs a
  per-tenant allowlist of permitted domains/IPs above the SSRF layer.
- [~] **Idempotency keys on outbound HTTP.** Shipped:
  `core.Job.IdempotencyKey()` returns `"hazyflow:<job_id>"` — and
  Job.ID is the stable per-node-record identifier (worker
  retries Requeue under the same ID, so a retried call carries
  the same key). Wired into the outbound modules where retry-
  causes-double-effect matters: `slack_send_message`,
  `gmail_send_email`, `github_create_issue`, `github_add_comment`,
  and the generic `webhook_send` (set BEFORE user-supplied
  headers so users can override for endpoints that mishandle
  the convention). Slack and Stripe-shaped APIs honor the
  `Idempotency-Key` header; APIs that don't recognize it ignore
  it harmlessly. Contract test verifies same Job.ID → same key
  across attempts, different Job.IDs → different keys. **Open
  follow-up:** add to `http_request` once we figure out the
  signature-based-auth edge case (some signed-request schemes
  hash all headers, so an unexpected Idempotency-Key could
  break the signature; safer to gate behind a param).
- [x] **Port-bind failures are silent.** FIXED 2026-05-27. Webhook +
  HTTP gateway bind on the main goroutine (new `ServeListener` methods);
  `hzd` calls `log.Fatalf` on a port-in-use bind error so k8s/systemd
  restarts the process instead of it running listener-less.
- [~] **Output sanitization for secrets.** V1 shipped:
  `core.LintGraph(g) []LintIssue` runs at every `PUT /graphs`
  save. The first rule, `secret_to_persistence`, detects nodes
  whose Params or Env reference `${env|tenant|builtin:...}`
  placeholders and have a forward edge path (any number of
  intermediate transforms) into a persistence sink
  (`file_write`, `excel_write`, `*_insert_rows`,
  `*_upsert_rows`, `sheets_append_row`, `secret_set`). Each
  reached pair emits a `LintIssue` with severity `warn` naming
  both nodes and explaining why it matters. Lint is advisory:
  save still succeeds, the response includes
  `lint: []LintIssue`, and the FlowEditor's bottom banner
  surfaces findings with a Dismiss button. External-API sends
  (Slack/Gmail/GitHub/HTTP-post) are intentionally NOT
  persistence sinks — they exchange the secret with the
  service that holds it. 12 core tests pin direct/transitive
  paths, multiple sinks, nested-in-array params, secret in env,
  every secret scheme, non-secret placeholder rejection,
  external-API send not triggering, BFS-stops-at-first-sink.
  2 handler tests pin the save-response shape (lint present +
  empty case). **Open follow-ups:** more rules (e.g. lint when
  `sheets_append_row` is downstream of a secret-bearing call,
  surface in the inspector with per-node markers, allow
  selective dismissal vs dismiss-all).

## API surface — control plane gaps

- [ ] **`job cancel` RPC.** Currently a "not implemented" stub. Needs a
  `JobStore.Cancel` operation + worker-side cooperative cancel via
  context.
- [ ] **`job logs` streaming.** Stub today. Need a structured log surface
  on JobStore (or sidecar log store) + streaming gRPC.
- [ ] **`workspace create/list`.** Stub today. Need a `TenantService`
  RPC, a workspaces table (probably in Postgres) and identity-tied
  ownership.
- [ ] **`module push/pull/search`.** Module registry doesn't exist.
  Decisions: who hosts it (per-org? central?), signing/attestation,
  versioning beyond the manifest's `version` field.

## Distribution & HA

- [ ] **Multi-node event bus.** `MemoryBus` is in-process. Two hzd
  instances can't share progress streams. Replace with Postgres
  LISTEN/NOTIFY (we already have the pgxpool) or NATS. Interface is
  designed for the swap.
- [ ] **Scheduler leader election.** Spec mandates
  `pg_try_advisory_lock`-based leader election so only one hzd fires
  cron triggers and handles expired-lease cleanup. Today each hzd runs
  its own scheduler — fine for single-node, broken for multi-node
  (every cron fires N times).
- [~] **Postgres JobStore real-DB tests.** The jobstore + auth Pg paths
  now pass against a real PG 16 (verified 2026-05-27 against a throwaway
  container; fixed the IMMUTABLE-index and test-kind bugs found in the
  process). Still open: wire a CI service / docker-compose so
  `HAZYFLOW_TEST_DB` is exercised automatically on every push rather than
  by hand (tracked under Phase 4 ops).

## Modules

- [ ] **`shell` module.** Spec lists it under `modules/io/`. Needs
  process-level isolation beyond filesystem sandbox: nsjail, bubblewrap,
  Linux user namespaces, or a per-job container. Resource limits via
  ulimit/cgroups. Output capturing of stdout/stderr/exitcode per spec.
- [~] **`split` module.** Spec lists it under `modules/flow/` as
  "one input → N outputs." Two-way predicate fork shipped as
  `transform/split_rows` (matched / unmatched on a CEL predicate).
  N-way variant (route each row to one of N ports by a lookup map
  or per-port predicate) still open under
  `Modules wishlist → route_rows / N-way split`.
- [x] **`for_each` module.** Shipped in `modules/flow/for_each.go`.
  Runs a configured step module once per item with bounded
  `concurrency` and optional `fail_fast`. Outputs a `results` list
  (one Result per item, in input order) plus an `errors` map keyed by
  failing index. Step module is resolved from `engine.Default`, so it
  must be a native module. Limitation: per-item step params are
  static — real per-item parameterization needs template substitution
  (top of list).
- [ ] **HTTP modules: file upload / streaming download.** `http_request`
  handles small JSON / form bodies via inline. For multipart upload or
  large download streams it'd need a different shape — probably a
  separate `http_upload` / `http_download` to keep `http_request`
  simple.

## OnError + retry refinements

- [ ] **Manifest-driven max-retries.** Currently `MaxRetries` is a
  worker-global setting. A module's author should be able to say "this
  one tolerates 10 retries" or "this is one-shot".
- [x] **Jitter on retry backoff.** DONE 2026-05-27. The default
  `WorkerConfig.RetryBackoff` now multiplies the exponential base by a
  random factor in [0.75, 1.25) (±25%), so sibling nodes failing
  together don't re-synchronize their retries.

## Auth

- [ ] **Real OIDC verifier.** `OIDCAuthenticator` is scaffold —
  `IDTokenVerifier` interface plus a `stubVerifier` for tests. Wire
  `github.com/coreos/go-oidc/v3` for real JWKS-backed validation.
  Microsoft Entra / Okta / Google Workspace are spec-targeted IdPs.
- [ ] **Cert-CN → Principal mapping.** Service accounts currently
  authenticate twice (mTLS + bearer token). Map the cert's CN/SAN to a
  Principal so cert-only auth works.
- [ ] **SPIFFE / SVID support.** URI-SAN validation, trust-domain
  matching.
- [ ] **Service-mesh sidecar mode.** `--tls-disable` (or similar) for
  when Linkerd/Istio handles transport security.
- [ ] **Webhook trigger authentication beyond a per-graph secret.**
  Today the webhook compares one shared secret. HMAC-signed payloads
  (GitHub, Stripe style) need verifying the request signature against
  the body, not a bearer token.

## Reliability & cleanup

- [ ] **`Job.Cleanup` enforcement.** `CleanupPolicy` is on `core.Job`
  but no code consumes it. Need cleanup on node/graph complete.
- [ ] **Streaming writes with unknown size.** Quota check happens
  pre-write with known size. For an HTTP download that streams to disk,
  we'd need a `ReserveAndStream` pattern that aborts mid-write when
  budget exceeded.
- [ ] **`maybeCompleteGraph` is O(N) per completion.** Acceptable for
  typical graphs but scales linearly. Could add a per-graph-run
  outstanding-node counter; only walk when counter hits 0.

## Data flow

- [ ] **Cross-workspace transfer.** Refs are workspace-relative. Two
  workspaces can't share data through the filesystem path. Either a
  URI scheme (`workspace://<src-workspace>/<path>`) or an explicit
  copy primitive.
- [ ] **Per-workspace quotas.** Currently per-tenant total only. Cannot
  say "this workspace gets 80% of the tenant budget".
- [ ] **Richer variadic semantics.** Indexed keys (`items[0]`,
  `items[1]`) work but leak through to module authors. A nested
  list-Ref form would be cleaner but requires reworking `core.Ref`.
- [ ] **Binary inline data wonky over gRPC.** Engine `json.Marshal`s
  `Ref.Inline`, so `[]byte` becomes a base64 string the consumer must
  decode. Text round-trips fine (use `string`). A first-class binary
  path on the protobuf (`bytes` field that bypasses JSON wrapping)
  would clean this up.

## Observability

- [ ] **Disk-usage metrics per tenant.** Expose
  `hazyflow_quota_bytes_used{tenant=...}` and `_limit` via the OTel
  metrics SDK so dashboards can show approaching-limit signals.
- [~] **Worker health / readiness.** `GET /healthz` (liveness) and
  `GET /readyz` (readiness — pings Postgres when configured via
  `gw.ReadyCheck`) both serve on the HTTP gateway as of 2026-05-27.
  Still open: a gRPC health-check service for non-HTTP deployments.
- [ ] **Structured graph-progress audit log.** Right now progress only
  flows over the gRPC stream. Long-running graphs that disconnect
  lose history.
- [ ] **Per-tenant rate limiting beyond disk quota.** Spec mentions
  `max_concurrent_jobs`, `max_graph_nodes`, `max_job_duration_s` —
  none enforced yet.

## Coverage gaps (the honest list)

- [ ] `engine/` at 57% — `LocalCatalog.LoadDir`, `cancelledResult`,
  some error branches in `runProtocol`.
- [ ] `engine/jobstore/` at 36% — Postgres path gated, exercised only
  when `HAZYFLOW_TEST_DB` is set.
- [ ] `cmd/hzctl` / `cmd/hzd` at 0% — no tests.
- [ ] `workspace/` at 59% — most missed branches are error paths in
  Save/Promote.
- [ ] `modules/io/` at 66% — copyFile branch and a few sandbox edge
  cases.

## Architectural debt

- [ ] **`Engine.Run` (layered) coexists with per-node path.** The
  in-process Engine.Run path still exists and has tests, but the
  daemon never calls it. Either keep it as a documented in-process
  mode (for `cmd/hzctl graph run --inline`?) or delete.
- [ ] **No remote descriptor `LoadDir`.** `LocalCatalog.LoadDir` reads
  *.json files from a directory; `RemoteCatalog` requires manual
  `Register` calls. Spec wants symmetry.
- [ ] **Graph-record storage in JobStore.** Graphs live both in Git
  (workspace) and as JobRecord.GraphPayload (job durability). Two
  sources of truth — fine today, drift risk later.

## Known unknowns

These are *what we don't know we don't know*:

- Behaviour under sustained high load — never stress-tested with
  thousands of concurrent graphs.
- Behaviour with very large graphs (>1000 nodes) —
  `maybeCompleteGraph`'s O(N) per completion could become quadratic
  in graph size × concurrency.
- Postgres connection pool tuning — `pgxpool.New` uses default
  settings; production needs sizing.
- Lease durations vs. real-world worker crash recovery — current 30s
  is a guess; should validate against actual restart times.
- Cron scheduler under clock skew — we use `time.Now`; haven't
  reasoned about NTP jumps, daylight-saving transitions, or container
  clock drift.

## UI / gateway gaps (called out at ship time)

Surfaced as "honest gaps" when individual UI features landed — none
blocking, but listed so we don't lose them.

### Admin
- [ ] **Audit log.** Card is stubbed. Needs (a) a persistence model
  for the events themselves, (b) instrumentation across graph saves,
  run submissions, secret accesses, approval decisions, and (c) a list
  endpoint + UI. Biggest of the remaining admin items.
- [ ] **Workspace settings UI.** Card is stubbed. Quotas, sandbox
  roots, retention — daemon already supports them at config time;
  needs a CRUD endpoint and an admin form.
- [ ] **Module registry admin view.** Card is stubbed. The editor's
  catalog already lists modules; this would be the place to approve
  remote / MCP modules once that gate exists.
- [ ] **API key expiry-setting UI.** Data model has `ExpiresAt`; issue
  modal doesn't surface it. One-line add.
- [ ] **Role templates as a backend resource.** Today they're a
  frontend constant in `web/src/components/IssueKeyModal.tsx`.
  Promote to `/api/v1/admin/role-templates` when customer deployments
  start needing per-tenant overrides.
- [ ] **User rename / merge.** Subject is a free-text identifier baked
  into each key at issue time. Renaming or merging two subjects'
  key history isn't supported — would need an alias table or rewrite.

### Editor
- [ ] **Variadic input ports** (e.g. `merge.items`) render as a single
  handle today. React Flow accepts multiple edges into one handle so
  wiring works, but the UI doesn't show `items[0]`, `items[1]`
  distinctly. Either render N handles dynamically as edges connect,
  or surface a port count in the catalog.
- [x] **Cron expression validation** in the trigger settings modal.
  Shipped: new `POST /api/v1/validate/cron` endpoint
  (`daemon/httpcron.go`) reuses the EXACT
  `robfig/cron`-with-`Minute|Hour|Dom|Month|Dow`-flags parser the
  scheduler uses, so a green "valid" hint guarantees the
  scheduler will accept the expression. Returns
  `{valid, error?, next_fires?: [3 RFC3339 UTC timestamps]}`.
  Frontend: new `CronField` component in SettingsModal debounces
  validation (250ms) on every keystroke, draws a red border +
  inline error message when the daemon rejects, and shows a
  "Next: YYYY-MM-DD HH:mm · ..." preview rendered in local time
  when accepted — confirms the cadence without timezone noise.
  5 backend tests (valid + invalid + empty + auth + parser-
  agreement-with-scheduler).
- [ ] **Variadic step_params** in `for_each` — `step_params` is an
  untyped object so the Inspector form falls back to JSON. A
  schema-driven option (e.g. "use this manifest's schema for the
  child step") would close the loop.

### Output preview / runs
- [ ] **Streaming large outputs.** Output preview fetches the whole
  JobRecord as JSON. Fine for inline payloads; once blob storage
  ships, large refs need a separate fetch path.
- [ ] **Show node inputs alongside outputs.** Inspector preview shows
  output ports only. Adding input previews is one fetch per incoming
  edge.
- [ ] **Cursor pagination for runs.** Currently offset-based. For
  large run histories cursor (by enqueued_at) would be steadier under
  concurrent writes.
- [ ] **Date-range filter** on the RunList page.
- [ ] **Per-row live updates in RunHistory dropdown.** Polls the
  first page every 3s while open and any row is non-terminal — fine
  for V1 but a per-run SSE would be tighter.
- [ ] **Cross-workspace runs view for admins.** `/runs` is scoped to
  the principal's workspace. A `tenant:admin` opt-out would let them
  see runs across workspaces in their tenant.

### Approvals
- [ ] **Group + filter the inbox** (by graph, by age). Flat list is
  fine for one workspace's worth; bigger orgs would want it.
- [ ] **Sidebar approval badge** polls every 30s independently from
  the `/approvals` page's 5s poll, so it briefly lags. Could share a
  context provider or push from SSE.

### Browser auth + transport
- [~] **Cookie / CSRF auth.** SameSite=Lax + HttpOnly session
  cookie was already in place from the password sign-in flow.
  Added: `verifyCookieOrigin` middleware that gates every
  cookie-authenticated POST/PUT/PATCH/DELETE on an Origin header
  matching the configured `--web-origin` allowlist. Bearer-auth
  requests pass through unchanged (no cookies = no CSRF surface);
  GET/HEAD/OPTIONS pass through (no state change). Defense-in-
  depth on top of modern browsers' SameSite enforcement —
  catches the older-browser edge cases Lax doesn't fully cover.
  5 tests pin the matrix: bearer-only POST passes, cookie POST
  without Origin → 403, cookie POST with allowed Origin → 200,
  cookie POST from disallowed Origin → 403, cookie GET still
  passes. **Open follow-up:** explicit CSRF tokens
  (double-submit cookie pattern) would add another layer for
  the rare browsers that don't enforce SameSite — not load-
  bearing today but worth a follow-up if customer security
  reviews ask.
- [~] **Rate limiting** on the gateway. Per-IP token bucket now guards
  `/api/v1/auth/{signin,signup}` (2026-05-27, `daemon/ratelimit.go`).
  Still open: extend to other write endpoints + a per-tenant tier;
  honor X-Forwarded-For behind a trusted-proxy allowlist.
- [ ] **Per-tenant origin pinning** for CORS. Currently `*` (or
  configurable globally).

### Modules wishlist
- [x] **Notifier modules** — email, Slack, generic webhook-out.
  Shipped: `notify/email.go` (SMTP), `notify/ntfy.go` (push),
  `notify/webhook_send.go` (generic POST/PUT/PATCH — covers Slack,
  Discord, Teams, PagerDuty, and anything else with an incoming-
  webhook URL). Pairs naturally with `await_approval` for sending
  approval URLs to whichever channel the human reads.
- [x] **Database modules** — Postgres, SQLite, and MySQL shipped
  (`integrations/db/`: `*_insert_rows`, `*_query`, `*_upsert_rows`
  for each, plus pooled connection registries keyed by (tenant, dsn)
  with lazy idle eviction — `pgxpool` for Postgres, `*sql.DB` for
  MySQL, no pool for SQLite since file-open is microseconds).
- [x] **Row-transform module** — `integrations/transform/map_rows.go`.
  Static field operations (select / drop / rename / default /
  filter_eq / filter_neq / filter_in) with fixed application order
  and string-based equality so int 30 matches "30". 19 tests.
  Closes the "Excel column names don't match my DB schema" gap.
- [x] **`compute_rows` module** — `integrations/transform/compute_rows.go`.
  Per-row derived fields and filters via CEL (Google's Common
  Expression Language). Each expression sees `row` as a
  `map<string,dyn>`; `compute` adds/overwrites columns,
  `filter` drops rows. Compiles expressions once before the row
  loop, fails the whole batch on per-row runtime errors (same
  contract as the SQL drops). 18 tests. Closes the gap `map_rows`
  deliberately left open (string concat, arithmetic, conditionals,
  multi-column predicates).
- [x] **`sort_rows` / `dedupe_rows`** — `integrations/transform/`.
  `sort_rows` does stable, multi-key, asc/desc sorts with
  numeric-aware comparison so Excel-string "10" lands after "2".
  `dedupe_rows` drops duplicates by an optional `by` column list
  (default = whole row), `keep: first|last`, preserves input order
  for survivors, and emits a `dropped` count for downstream alerting.
  21 combined tests.
- [ ] **Blob storage** — S3 / GCS / R2 with proper streaming via the
  Ref pointer (not Inline).
- [x] **`join_rows`** — shipped:
  `integrations/transform/join_rows.go`. Two row-input ports
  (`left_rows` + `left_headers`, `right_rows` + `right_headers`),
  `on` param for the join-key mapping (`{left_col: right_col}`,
  multi-column supported), `kind` picks
  `inner`/`left`/`right`/`outer`. Hash join over the right side
  for O(L+R); cartesian-within-key-group on duplicate right
  keys matches SQL behavior. Key-value equality uses
  `fmt.Sprint` coercion (int 30 joins string "30") — same rule
  `map_rows.filter_eq` uses, so Excel-string vs DB-int
  mismatches don't force a pre-cast. The right side's key
  columns are dropped from the output (they equal the left's
  by construction); non-key column collisions get the right
  one suffixed (default `_right`, overridable via
  `right_suffix`). Outer/right joins reconstitute the join-key
  values on unmatched rights under the LEFT's column names, so
  every output row carries the joined key under one stable name
  regardless of which side matched. Unmatched-side columns are
  present-with-nil rather than missing — SQL tuple semantics so
  downstream CEL filters (`row.country != null`) behave
  predictably. 18 tests covering each `kind`, multi-column
  keys, cartesian-within-group, type coercion, header
  collisions (default + custom suffix), shared-key-name
  deduplication, empty-side cases, missing-key-column errors
  on either side, bad params.
- [x] **`group_aggregate`** — shipped:
  `integrations/transform/group_aggregate.go`. `by` param picks
  the group columns ([] = single total group covering all
  rows); `aggregate` maps each output column to `{op, column}`
  with ops `count` / `sum` / `avg` / `min` / `max` / `first` /
  `last` / `collect`. Numeric ops coerce string-numeric values
  via `strconv.ParseFloat` (Excel "30" sums alongside int 30 /
  float 30.0); `min`/`max` falls back to lexical comparison
  when values aren't all numeric. Sums down-cast to int64 when
  the float result is integral (avoids "30.0" cosmetics
  downstream). Groups emitted in first-seen order;
  aggregation-output headers alphabetized so map iteration
  randomness doesn't leak. Numeric extrema reuse a single
  float accumulator alongside the running sum to keep memory
  flat per group. Per-row errors (sum on a non-numeric column)
  surface with code `eval` mentioning the column and row index.
  20 tests: single + multi-column `by`, count, sum/avg with
  string/int coercion, sum-on-non-numeric fails, avg of all-nil
  group → nil, numeric vs lexical min/max, first/last/collect
  preservation, empty `by` total-group, empty input rows,
  first-seen ordering pinned, header sort stability under
  Go's map randomization, every error-path
  (missing/empty/unknown params, missing/unknown columns).
- [~] **`route_rows` (N-way split)** — shipped V1:
  `integrations/transform/route_rows.go`. Eight fixed routing
  slots (`rows_1`..`rows_8`) + `default` catch-all + `headers`
  passthrough; param `routes: [{slot, filter}, ...]` is an
  ordered list of (CEL predicate, target slot) rules with
  first-match-wins semantics. Rows matching no route land on
  the configurable `default_slot` (default `default`).
  Validation rejects unknown slot names, empty filters, and
  collisions between an explicit route and the default slot.
  Dormant slots (declared in manifest but not referenced in
  params) emit no output, so downstream of them is correctly
  skipped by the existing edge-classifier. 13 tests cover
  3-way split, first-match-wins, custom default, empty inputs,
  headers passthrough, dormant-slot absence, and every error
  path. **Open follow-up (the original "variadic by name"
  design):** the editor can't render per-name output handles
  yet, so the user's semantic names (`SE`, `NO`, `UK`) live in
  the downstream node labels rather than on the port itself.
  Same fix that unblocks variadic INPUT port rendering would
  unlock this — both share the "draw N handles from a
  manifest-declared list at runtime" need.
- [ ] **Excel polish on shipped drops** (`integrations/io/excel_*`):
    - `start_cell` on `excel_write` — write the table starting at
      e.g. `B5` instead of A1; templated reports with a banner row
      can't do this today.
    - Multi-sheet `excel_read` mode — output `{sheet_name: rows}`
      for workbooks laid out as one tab per category. The single-
      sheet path stays; new param `all_sheets: true` switches shape.
    - Streaming reader for huge sheets — current path buffers the
      whole workbook in memory. Only matters at 100k+ rows; would
      flip `ExecutionModel` to `ExecutionStream` and use excelize's
      `Rows()` iterator.
- [ ] **DB drop polish**:
    - Inserted-vs-updated split on `postgres_upsert_rows` — emit
      separate counts via the `INSERT ... RETURNING (xmax = 0)`
      trick. Same for MySQL (uses `ROW_COUNT()` semantics: 1 per
      INSERT, 2 per UPDATE). Lets webhook notifications say
      "loaded 245 new + updated 78 existing" instead of "processed
      323."
    - Streaming reads on `*_query` — current path accumulates all
      rows in memory. Adding a streaming variant (cursor on
      Postgres, batched fetch on MySQL/SQLite) matters once a
      single SELECT returns >100k rows.
- [ ] **Reusable sub-graph components in the UI.** `flow/subgraph`
  already runs nested graphs, but there's no UI to save a graph as
  a reusable component, give it inputs/outputs, drop it onto another
  graph from the catalog. Would let users build "this is our
  standard customer-cleanup pipeline, here's the node" — turns
  ad-hoc graphs into a sharable library.
- [x] **`split` module** — shipped as `split_rows`
  (`integrations/transform/split_rows.go`). Forks a row stream by a
  CEL predicate into `matched`/`unmatched` ports plus a shared
  `headers` port. Same expression surface as `compute_rows.filter`
  (reuses `compileOptionalFilter`/`evalFilter`). Real ETL win is
  "route invalid records to a review queue instead of dropping
  them" — previously required `map_rows` twice with opposite
  filters, which walks the input twice. 14 tests.
