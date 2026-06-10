# TODO

Open items collected across iterations. Each entry notes the impact and a
hint at what's needed. Items at the top section block real usage; items
further down are quality, observability, and known-unknowns.

## Production readiness — phased action plan (reviewed 2026-05-27)

Sequencing layer to go from "feature-complete alpha" to "deployable paid
SaaS." Most line-items are detailed in the sections further down
(Production blockers, Distribution & HA, T3, Observability); this orders
them.

### Where we are (refreshed 2026-05-27, end of stability/hardening push)

Scorecard — baseline review → now:

| Dimension     | Baseline | Now  | What moved it |
|---------------|----------|------|---------------|
| Features      | ~8/10    | ~9.7/10 | + `http_download`/`http_upload` + admin UI (audit log, module registry, workspace limits) |
| Tests         | ~8/10    | ~10/10 | broad suite, `-race` in CI, **named coverage gaps closed** (engine 72.7→90.7%, engine/jobstore 23.5→92.9%, workspace 76.3→83.3%, integrations/io 76.6→87.9%). pg-gated suite passes against real PG 16. Remaining: `cmd/*` low (cobra wiring, integration-test territory). |
| Durability    | ~3/10    | ~9/10 | Phase 0 — Postgres-backed everything; + backup/restore runbook + JSON→Postgres user import |
| HA / correctness | ~2/10 | ~9/10 | Phase 2 (PgBus + leader election, load-tested) + race/lease fixes + **compose/k8s deploy manifests** |
| Observability | ~2/10    | ~9/10 | `/healthz` + `/readyz` + `/metrics` (disk + job gauges) + gRPC health + **OTLP trace export** |
| Monetization  | ~1/10    | ~1/10 | untouched — **Phase 3 is the gate** |

**Overall: ~45% → ~75% to production.** Phases 0–2 shipped; the dominant
remaining work is **Phase 3 (monetization)** — metering, Stripe + plan
gates, team features. Private beta (Phases 0–1) is met; paid GA is gated
on Phase 3.

### Since the last review (this session's stability/hardening)

- **Correctness/concurrency:** fixed a `workspace.Store` data race (CI now
  runs `-race`); fenced node execution **and** completion on lease loss
  (`OwnedCompleter`) — closes a double-execution / result-clobber hazard.
- **Scale:** `maybeCompleteGraph` O(N²)→O(N) (batch read); `--pg-max-conns`
  / `--pg-min-conns` pool sizing.
- **Multi-tenant safety:** `--max-graph-nodes`, `--max-graph-timeout`,
  `--max-concurrent-jobs` (per-tenant fairness throttle).
- **Secrets:** runtime output redaction; `--rotate-master-key` KEK re-wrap.
- **Lifecycle:** `Job.Cleanup` via per-run `scratch://` reclamation.
- **Observability:** `/metrics` (per-tenant disk + job-status gauges),
  gRPC health service.
- **Retry:** manifest-driven max-retries.
- TODO trimmed (1197→~1090 lines) and stale entries reconciled.

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
- [x] **Follow-ups:** `HAZYFLOW_TEST_DB` tests now run in CI (`.build.yml`
  Postgres service). JSON-users → Postgres import shipped 2026-05-27:
  `auth.ImportUsers` + `hzd --import-users-from-json <path>` (idempotent,
  skips existing); see `DEPLOY.md`.

### Phase 1 — Security hardening for public exposure (DONE · 2026-05-27)

All shipped. Details in **Production blockers** below; one-line summary:

- [x] TLS via reverse-proxy contract — `--trust-proxy-headers` (Secure cookies + HSTS); nginx example in `DEPLOY.md`.
- [x] `--dev-key` defaults off (opt-in).
- [x] Auth rate limiting on `/auth/{signin,signup}` — per-IP token bucket (`daemon/ratelimit.go`).
- [x] Fail-loud on port-bind failure (webhook + HTTP gateway bind on the main goroutine).
- [x] SSRF egress allowlist (`--http-egress-allow`) + runtime secret-output redaction (`engine/redact.go` scrubs resolved secrets from persisted Results, backing the save-time lint).
- [x] Quota write race closed — reservation + in-flight model (`core.QuotaReserver` / `FSQuota.Reserve`, `io.SetQuotaReserver`).
- [x] Master-key rotation re-wrap — `hzd --rotate-master-key` (`EncryptedSecrets.RewrapDEKs`); runbook in `SECURITY.md`.

### Phase 2 — HA / horizontal scale (~2–3 wks)

- [x] Replace `MemoryBus` with Postgres LISTEN/NOTIFY — `daemon/eventbus_pg.go`
  (`PgBus`, `bus_events` table + `pg_notify`). Wired in cmd/hzd when
  `--postgres-dsn` is set. Tests in `daemon/eventbus_pg_test.go`.
- [x] Scheduler leader election via `pg_try_advisory_lock` —
  `daemon/leader.go` (`PgLeader`), gated through `Scheduler.SetLeader`.
  Followers rescan and take over on leader failure. Tests in
  `daemon/leader_test.go` (single-holder + failover).
- [x] Verify behaviour under concurrent multi-node load — `scripts/ha_loadtest.sh`
  runs two real hzd processes against one Postgres and asserts (a) exactly
  one leader, (b) no double-fire of a 1s poll trigger, (c) failover: kill
  the leader, a follower acquires the lock and keeps firing. Seeds a
  node-less poll graph via `scripts/ha_loadtest/seed.go` (one job row per
  fire). Passing as of 2026-05-27.

### Phase 3 — Monetization (T3 · ~3–4 wks)

Per-tenant metering → Stripe + plan gates → team features. Specced under
**T3** below.

### Phase 4 — Ops maturity (parallelizable · ~1–2 wks)

- [~] **Dockerfile + CI** (2026-05-27, overnight batch). Multi-stage
  `Dockerfile` (web bundle → static Go build → distroless nonroot,
  serves API + bundle on :8080). `.build.yml` for builds.sr.ht runs
  go build/test/vet + web build, with a Postgres service exporting
  `HAZYFLOW_TEST_DB` so the gated jobstore/auth tests run in CI (closes
  the long-standing "real-DB tests never exercised" gap). **Deploy
  manifests shipped 2026-05-27** — `deploy/docker-compose.yml` (hzd +
  Postgres, durable, metrics on) and `deploy/k8s/hazyflow.yaml`
  (Deployment ×2 + Service + Secret, liveness `/healthz` + readiness
  `/readyz` probes), with `deploy/README.md`. Image build in CI shipped 2026-06-10: `.builds/archlinux.yml`
  gained a rootless `podman build` task (podman handles the
  Dockerfile's BuildKit cache mounts; local docker without buildx
  cannot — that's a builder limitation, not a Dockerfile bug).
  Not pushed from CI; releases tag and push by hand.
- [x] **`/healthz` + `/readyz` + `/metrics`** (2026-05-27) — liveness,
  readiness (pings Postgres when configured), and a Prometheus `/metrics`
  endpoint behind `--metrics` with per-tenant disk gauges
  (`daemon/metrics.go`). gRPC health service for non-HTTP deployments
  shipped (see "Worker health / readiness"). Open: more gauges.
- [x] **Connection-pool sizing** (2026-05-27) — `--pg-max-conns` /
  `--pg-min-conns`.
- [x] **Postgres backup/restore runbook** (2026-05-27) — `DEPLOY.md`
  covers pg_dump + PITR, the master-key break-glass caveat, and what's
  safe to lose (bus_events spool, derived sandbox dirs).

**Critical paths:** private beta (Phases 0–1) ✅ done; paid GA gated on
**Phase 3 (monetization)**. Phase 2 (HA) already landed.

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
  in-app without a round trip. Gated on `HAZYFLOW_ENABLE_SIGNUP=1` so
  production deployments default to
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
  prior fire's cursor via `${secret.//...}` template
  substitution, write the new one with a downstream `secret_set`
  node. 11 tests cover happy path, input-port-wins-over-param,
  bytes input, structured-input rejection (friendly Message +
  technical Details split), missing tenant/name/value, bad-name
  validator, unwired-hook clear error pointing at `HAZYFLOW_MASTER_KEY`,
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
  (same pattern as `webhook_input`). Gated on
  `HAZYFLOW_SLACK_SIGNING_SECRET`; empty leaves the endpoint
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
  `github_on_pr_merged` can claim those). Gated on
  `HAZYFLOW_GITHUB_WEBHOOK_SECRET`; empty leaves the endpoint
  returning 501. 10 tests cover the matrix
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
- [~] **Stripe launch connector.** Shipped (2026-06-10), five drops in
  `drops/stripe/`: `stripe_create_customer` (email/name/description
  pins, customer_id out), `stripe_create_payment_link` (price + wired
  quantity → hosted checkout `url` pin — the "order row → payment link
  → email/Slack it" flow), `stripe_create_refund` (payment_intent pin,
  partial amount + reason; pairs with await_approval upstream),
  `stripe_list_events` (types filter; `after_id` cursor in →
  `ending_before`, `last_id` out — echoes the cursor on empty so a
  Set-secret step never clobbers it; the poll_trigger + secret_set
  composition covers "on new payment / failed invoice / new
  subscription" without per-event trigger drops) and
  `stripe_search_customers` (dashboard search syntax, customers +
  count out). Auth: api_key param defaulting to
  ${secret.STRIPE_API_KEY} (x_advanced, so the card stays clean —
  ${vault./aws./gcp.…} work too), `RequiresConnections` secret so the
  connection gate fires. All POSTs carry the job's Idempotency-Key
  (Stripe honors it → retries can't double-charge); base_url test
  seam goes through the SSRF guard like the other connectors.
  Official Blurple wordmark at /brands/stripe.svg (from Stripe's
  logo kit). 9 httptest-backed tests + the repo-wide adversarial/
  invariants harnesses; verified end-to-end in the browser (catalog +
  palette show all five branded; a real run resolved the secret,
  called a fake Stripe with the right key + idempotency header, and
  the customer_id pin carried the created id). **Open:** invoice
  creation (multi-call: items → invoice → finalize — add when asked);
  a signed `stripe_on_event` webhook trigger if polling latency ever
  matters; ~~a seed template demonstrating the events-cursor
  composition~~ (shipped 2026-06-10: stripe-payment-to-slack —
  poll → list_events with the cursor secret wired into After id →
  for_each Slack message per event → secret_set saves Last id;
  list_events now treats non-evt_ cursors as "no cursor" so the
  documented "-" placeholder works on the first run).
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
  custom receivers. **Email channel shipped 2026-06-10:**
  `FailureNotify.Email` + a daemon `Mailer` (one operator SMTP
  account via HAZYFLOW_SMTP_URL/_FROM, smtp/smtps/starttls/none,
  hand-rolled ~150 lines) sends a plain-text failure summary;
  the Settings → Notifications tab gained the email field. The
  same mailer delivers invitation links (createInvitation emails
  the accept URL when configured + the URL is absolute, response
  carries email_sent). **Open follow-up (needs product
  decision):** typed Slack-channel picker — the
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

- [~] **Per-tenant metering.** Shipped (2026-06-10): `daemon.UsageStore`
  (Mem + `PgUsageStore` over a new `usage_counters` table, one row per
  tenant-month, atomic ON CONFLICT upserts so multi-replica workers
  never lose counts). Runs counted in `SubmitGraphWithSeed` (the single
  choke point all entry points funnel through — manual Run, scheduler,
  webhook/form/Slack/GitHub; nested sub-graph runs deliberately NOT
  counted as runs to avoid double-billing one user action). Node
  executions counted in the worker next to the latency metric — every
  executed attempt meters (retries included), skipped/disabled nodes
  don't. Best-effort by contract: metering failure logs, never fails a
  run. Surface: `GET /api/v1/me/usage` (scope-checked, current month
  always present, `?months=` history) + a Usage page in the sidebar
  (headline current-month runs/step-executions + monthly history,
  en+sv). Tests: store contract (mem + pg-gated), e2e through real
  SubmitGraph/worker, endpoint authz; verified in the browser against
  a real Postgres. **Open:** billing-day alignment — buckets are UTC
  calendar months on purpose; layer the tenant's billing anchor at
  query time when Stripe plans land (the writes don't change). The
  Usage page shows no plan limit yet (no plans exist).
- [~] **Stripe integration + plan gates.** Shipped (2026-06-10), the
  run-limit slice: `daemon.PlanStore` (Mem + `PgPlanStore` over a new
  `tenant_plans` table; no row = free). Gate: `Service.checkRunQuota`
  in `SubmitGraphWithSeed` rejects a free tenant over
  `HAZYFLOW_FREE_RUNS_PER_MONTH` runs/month (read from the metering
  counters) with `core.ErrPlanLimit` → HTTP 402 + an upgrade-worded
  message; pro unlimited; limit unset (default) = no enforcement, so
  self-hosted installs never gate. Fails OPEN on plan/usage store
  errors (availability over one free run). Sub-graph children bypass
  (parent already admitted). Stripe: hand-rolled minimal client
  (`daemon/stripe.go` — Checkout session, billing-portal session,
  webhook signature verify with replay window + rotation support; no
  stripe-go dep, same trade as /metrics). Endpoints:
  `GET /api/v1/me/billing` (plan/cap/used/can_upgrade/can_manage),
  `POST /me/billing/checkout` + `/portal` (Stripe-hosted URLs;
  tenant rides as client_reference_id AND subscription metadata so
  every event maps back without a reverse index),
  `POST /api/v1/events/stripe` (signature is the auth, mirroring the
  GitHub events endpoint; checkout.session.completed → pro,
  subscription deleted/canceled/unpaid → free, past_due stays pro
  while Stripe retries the charge; store errors 500 so Stripe
  retries). Env (all default-off, documented in `.env.example`):
  `HAZYFLOW_STRIPE_SECRET_KEY` / `_PRICE_ID` / `_WEBHOOK_SECRET`,
  `HAZYFLOW_FREE_RUNS_PER_MONTH`. UI: Usage page gained the plan
  card — plan badge, used/limit progress bar (red at the cap),
  Upgrade (Checkout redirect) + Manage billing (portal) buttons,
  en+sv. Tests: plan-store contract (mem + pg-gated), gate matrix
  (capped/pro/disabled, via real submissions), Stripe client +
  signature vectors, webhook handler matrix (upgrade/downgrade/
  past_due/forged-sig/unconfigured/unknown-event), full
  capped→webhook-upgrade→uncapped loop; verified end-to-end in the
  browser (402 with friendly message at the cap, signed webhook
  flips the page to Pro and the run goes through). **Follow-ups shipped 2026-06-10:** polling gate
  (HAZYFLOW_FREE_POLLING_TRIGGERS=0 → the scheduler skips cron/poll
  fires for free tenants, fire-time enforcement, fail-open, surfaced
  as polling_allowed on /me/billing + Usage-page copy); webhook
  event-ID dedupe (stripe_webhook_events table, INSERT ON CONFLICT
  is the atomic first-time test, replays ack without re-applying,
  fail-open); pro-pitch copy on the Usage page. **Still open:**
  premium-connector gate (needs a product call on which connectors);
  billing-day alignment (see metering entry).
- [~] **Team features.** Shipped (2026-06-10), the role-split slice —
  multi-user-per-tenant + shared workflows already worked (memberships,
  invitations, switchOrg); what was missing was the viewer role and the
  role-change UX. Canonical catalog: `core.TeamRoleViewer/Editor/Admin`
  (+ `TeamRoleByName`) — one source of truth now used by signup's
  default editor role, the invite default, and the UI presets (the
  catalog used to be ad-hoc per call site: "viewer" existed for API
  keys/OIDC but not invites). Viewer = graph:run only; the FlowEditor
  already renders read-only without graph:edit. New endpoint:
  `PATCH /api/v1/admin/members/{email}` `{roles:[…]}` — change a
  member's roles in place (PutMembership is already an upsert, no
  store change). Guards: organization:admin required, role grants
  capped to the caller's own permissions via the new shared
  `capRolesToCaller` (extracted from createInvitation so the two grant
  paths can't drift), home owner 409s (their roles live on the User
  record, so the org always keeps its owner-admin), empty roles 400
  (removal is DELETE's job), audited as `member.roles.update`. UI:
  invite modal offers Viewer / Editor / Admin with the permission
  preview; member cards gained a role dropdown (catalog roles +
  read-only "Custom" for hand-rolled sets) calling the PATCH endpoint;
  en+sv. Tests: handler matrix (demote/404/400/over-scoped/
  platform:admin/non-admin) over a fake membership store; verified
  end-to-end in the browser (invite a viewer → accept → dropdown
  shows viewer → switch to editor → membership perms really change).
  **Follow-up shipped 2026-06-10:** membership role changes and
  removals now sweep the member's live sessions
  (auth.SessionRevoker on mem/pg/caching stores; best-effort from
  the PATCH/DELETE member handlers) so demotion is immediate.
  **Open:** per-workspace
  roles (one Membership row carries one role set for the whole org);
  org-ownership transfer (removing/editing the home owner is blocked);
  invite email delivery shipped 2026-06-10 (operator mailer, see below); signup email verification shipped 2026-06-11: auto-active when
  HAZYFLOW_SMTP_URL + PUBLIC_BASE_URL are set — token (sha256 + 48h
  expiry) rides on the User record (mem/JSON/Pg, migration ALTERs),
  signup emails the link, POST /auth/verify-email consumes it
  (constant-time compare, enumeration-safe error shape, idempotent on
  verified accounts), /me/verification/resend re-mints, whoami carries
  email_verified/verification_pending, the shell shows a resend banner,
  and /verify-email lands the link signed-in or not. Gating is soft by
  design (runs + sign-in stay open; instant-try funnel intact) with ONE
  hard gate: unverified accounts can't send invitations through the
  operator's mailer. Verified end-to-end in the browser through the
  real signup form + a real SMTP exchange. Open: hard-gating more
  surfaces (outbound-email drops?) if abuse shows up; cleanup sweep for
  never-verified stale accounts.
- [x] **BYO cloud secret providers** (Vault / AWS Secrets Manager /
  GCP Secret Manager). Shipped (2026-06-10) — Vault/OpenBao already
  existed; AWS + GCP added on the same model (per-tenant config in the
  encrypted store under `cfg:secret-manager-aws`/`-gcp`, tenant-scoped
  `Get` from `core.TenantFromContext`, 60s TTL cache, verify-on-save,
  independent slots so all three schemes coexist). `aws://NAME[#field]`
  → GetSecretValue with a hand-rolled SigV4 signer (pinned against an
  independently-computed golden vector; no aws-sdk dep — same trade as
  /metrics and the Stripe client) using static access keys.
  `gcp://NAME[#field]` → versions/latest:access with service-account
  RS256 JWT → OAuth token exchange (stdlib crypto; token cached to
  expiry; the test fake actually verifies the assertion signature).
  `#field` plucks a key from a JSON secret value. Endpoints:
  `/api/v1/secret-manager/aws` + `/gcp` (GET redacted / PUT
  connection-tested / DELETE), same permission gates as the vault one.
  UI: the Admin secret-manager page is now three sections (Vault form,
  AWS region+key form, GCP project+pasted-key-file form), en+sv.
  Verified end-to-end in the browser: configs saved against fake
  AWS/GCP servers, page shows redacted views (secret key / private key
  never rendered), and a real flow run resolved `${aws.apikey}` through
  the engine (the fake recorded the GetSecretValue call). **Open:**
  AWS IAM-role / STS assume-role and GCP workload-identity auth (V1 is
  static keys — the universal denominator); Azure Key Vault if a
  customer asks; per-secret version pinning (always latest/AWSCURRENT
  today).

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
  / `engine.SubstituteValue` handle `${scheme.path}` inline anywhere in
  a string, with composition across multiple placeholders in one value.
  Wired into `resolveSecrets` so any string in `Job.Params` / `Job.Env`
  picks it up; the legacy whole-string `env://KEY` form is preserved.
  `for_each` adds `${item.path}` per-iteration on a deep-copy of
  step_params (dot-separated path traversal over maps and lists; empty
  path = whole item; missing-field errors surface in the `errors`
  output keyed by iteration index). E2E proves
  `url: "https://api/${item.id}"` + `Authorization: "Bearer ${env.TOKEN}"`
  routes correctly per item.
- [x] **Upstream-output templating.** Shipped: `${upstream.nodeID.port.path[idx]…}`
  resolves against prior-node results passed into `Engine.RunNode`.
  Dot-then-bracket path syntax over the port's Inline value (maps and
  slices). `resolveSecrets` renamed to `resolveTemplates` and now
  composes upstream + secret substituters in one pass — mixed strings
  like `https://hooks/${upstream.q.meta.id}?token=${env.TOKEN}` resolve
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

- [x] **Concurrent-write race in quotas** (2026-05-27) — closed via a
  reservation + in-flight model (`core.QuotaReserver` / `FSQuota.Reserve`,
  `io.SetQuotaReserver`); see Phase 1. OS-level quotas remain the backstop
  for out-of-process writers.
- [ ] **Real secret providers (vault, gcp, aws, azure).** Interface +
  scheme registry + env/builtin providers exist now. The cloud KMS
  / vault implementations are real integrations we haven't done. Spec
  lists all four.
- [~] **Per-tenant ACL on secrets.** Shipped: `Namespaced bool` on
  `EnvProvider` and `BuiltinProvider`. When set (via
  `HAZYFLOW_ISOLATE_SHARED_SECRETS=1`, default off for backward
  compat), every `Get(ctx, name)` requires the name to be of the
  form `<tenant>.<key>` matching the caller's tenant from
  `core.TenantFromContext`. Names without a prefix and
  cross-tenant prefixes are both rejected at the provider before
  the underlying env/builtin lookup runs — so tenant A's graph
  can't read `${env.globex.api_key}` even if the env var exists.
  tenant:// was already isolated (per-tenant DEKs in
  `EncryptedSecrets`). 7 tests pin the matrix: matching prefix
  resolves, cross-tenant rejected, unprefixed rejected,
  missing-tenant rejected, backward-compat mode (Namespaced
  false) unchanged, builtin same behavior. **Open follow-up:**
  document the per-tenant env var convention (e.g.
  `HAZYFLOW_TENANT_<UPPER>_<KEY>`) in the README; today the
  provider just looks up the full prefixed name verbatim.
- [~] **Egress allowlist for `http_request`.** DONE (operator-global)
  2026-05-27: `integrations/net/egress.go` — opt-in allowlist of exact
  hosts, `*.wildcards`, and CIDR/IPs, checked at request time above the
  IP SSRF guard. Wired via `HAZYFLOW_HTTP_EGRESS_ALLOW` / `SetEgressAllowlist`.
  Empty = allow-all (backward compatible). 4 tests. **Still open:** make
  it *per-tenant* (today it's one operator-wide list) — needs tenant
  read from ctx + per-tenant policy storage.
- [x] **`webhook_send` SSRF guard.** FIXED 2026-05-27. Now uses the
  shared `net.SafeHTTPClient` (private-IP dial block) + `net.EgressAllowed`
  (same operator allowlist as http_request), with an
  `allow_private_networks` opt-in param for intentional local targets.
  Exported `SafeHTTPClient` / `EgressAllowed` / `IsSSRFError` from the net
  package so the two drops share one implementation. Tests updated +
  a new "blocked by default" case (169.254.169.254 → ssrf_blocked).
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
- [x] **Output sanitization for secrets** — two layers. (1) Save-time
  lint (`core.LintGraph`): `secret_to_persistence` flags a
  secret-referencing node with an edge path into a persistence sink, and
  `hardcoded_secret` flags literal credentials pasted into params/env;
  advisory (warns, surfaced in the FlowEditor banner + per-node markers).
  (2) Runtime redaction (Phase 1, `engine/redact.go`) scrubs resolved
  secret plaintext from persisted Results. 20 core + handler tests.
  **Open:** selective lint dismissal; more rules as patterns emerge.

## API surface — control plane gaps

- [x] **`job cancel` RPC.** Shipped. `JobService.CancelJob` (new RPC,
  `api/proto/control.proto`) routes through the existing
  `Service.CancelGraphRun` — graceful: every non-terminal node-record +
  the graph-record flip to `Cancelled`, mid-run nodes finish naturally
  (the dispatcher's terminal-status guard prevents further dispatch).
  `hzctl job cancel JOB_ID [--reason ...]` wired. New gRPC test
  `TestGRPC_CancelJob` covers happy path, already-terminal
  (FailedPrecondition), and missing-run (NotFound). The "worker-side
  cooperative cancel via context" line in the original TODO is the
  follow-up: today an HTTP `sleep(60s)` node keeps running until it
  finishes naturally; nothing downstream advances, but the node itself
  isn't interrupted.
- [ ] **`job logs` streaming.** Stub today. Need a structured log surface
  on JobStore (or sidecar log store) + streaming gRPC.
- [ ] **`workspace create/list`.** Stub today. Need a `TenantService`
  RPC, a workspaces table (probably in Postgres) and identity-tied
  ownership.
- [ ] **`module push/pull/search`.** Module registry doesn't exist.
  Decisions: who hosts it (per-org? central?), signing/attestation,
  versioning beyond the manifest's `version` field.

## Distribution & HA

- [x] **Multi-node event bus.** `PgBus` (`daemon/eventbus_pg.go`) shares
  progress streams across hzd instances via a `bus_events` table +
  `pg_notify`. Selected automatically when `--postgres-dsn` is set;
  `MemoryBus` remains the single-node default.
- [x] **Scheduler leader election.** `PgLeader` (`daemon/leader.go`)
  holds a session-scoped `pg_try_advisory_lock`; only the holder fires
  crons (`Scheduler.SetLeader`). Lock auto-releases on connection loss,
  so a follower takes over on leader death. Wired in cmd/hzd.
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
- [x] **HTTP modules: file upload / streaming download** (2026-05-27).
  `http_download` (`integrations/io/http_download.go`): streams a URL to a
  sandbox path (workspace or `scratch://`) in 64 KiB chunks, enforces
  `max_bytes` + the tenant quota as it writes (reserve-and-stream), aborts
  mid-stream with the partial file removed. `http_upload`
  (`http_upload.go`): streams a sandbox file out — raw PUT (presigned-URL
  style, default) or multipart/form-data POST (via an `io.Pipe`, so large
  files don't sit in memory); takes the file from the `in` ref or
  `params.path`. Both reuse the `net` SSRF guard + egress allowlist. 12
  httptest-backed tests.

## OnError + retry refinements

- [x] **Manifest-driven max-retries** (2026-05-27). `core.Manifest.MaxRetries`
  overrides the worker-global attempt cap in `Worker.maybeScheduleRetry`
  (>0 wins, else the default) — a flaky module can tolerate more, a costly
  one can cap at 1. Two tests (raises / lowers the cap).
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

- [x] **HMAC approval-link flow wired** (found + fixed 2026-05-27 QA).
  The unauthenticated HMAC email/Slack-link approval path was built +
  tested but dormant (no `engine.ApprovalSigner` set, listener never
  served). Now wired in `cmd/hzd` behind `--approval-listen` +
  `--approval-hmac-secret` (+ `--public-base-url`): the engine mints
  signed `/approve/<run>/<node>` URLs and `ApprovalListener` serves the
  HMAC-verified endpoint (given a fail-loud `ServeListener`). Off by
  default. Happy-path test added; the authenticated inbox path
  (`approveAuthed`) is unchanged.
- [x] **QA pass: dead code eliminated** (2026-05-27). Removed the
  unreachable `HTTPGateway.Serve` / `WebhookListener.Serve` wrappers
  (superseded by the fail-loud `ServeListener` refactor) and the
  superseded `NewRandomHMACSigner`. `go vet`, `staticcheck`, and
  `deadcode -test ./...` are all **clean** (zero dead code repo-wide).
  (`golangci-lint` is unusable on the go1.26 toolchain — its bundled
  type-checker can't read the export data; not a code issue.)
- [x] **Fence node execution + completion on lease loss** (2026-05-27).
  `renewLease` used to only log a failed `Renew`, so a worker whose lease
  expired and was reclaimed kept executing *and* wrote its result,
  clobbering the new owner (double-execution). Two-layer fence: (1) a
  `Renew` `ErrConflict`/`ErrNotFound` (lost ownership) cancels the
  execution context and the worker abandons — no terminal write, no
  retry/dispatch; (2) the terminal/awaiting write goes through a new
  optional `core.OwnedCompleter.CompleteOwned(jobID, worker, …)` (mem +
  pg) that only lands if the worker still owns the record, closing the
  narrow window where a worker finishes and `Complete`s before its renew
  tick detects the loss. Transient renew errors still log + retry. Tests:
  `daemon/lease_test.go` (fence-on-loss / no-fence-on-transient) +
  `CompleteOwned` ownership tests (mem + gated pg).
- [x] **Data race in `workspace.Store` under concurrent access**
  (2026-05-27). go-git storers aren't concurrency-safe and `Store` had no
  mutex, so scheduler rescans (`ListGraphs`/`Load`) raced the gateway's
  `Save` — caught by `-race` in `scheduler_poll_test.go`. Fixed with a
  `sync.Mutex` guarding all repo access (full mutex, **not** RWMutex: the
  FS object LRU cache mutates on reads). `go test -race ./...` green; CI
  now runs `-race`.
- [x] **`Job.Cleanup` enforcement — per-run scratch via `scratch://`**
  (2026-05-27). Cleanup needed a real target since `WorkspaceRoot` is
  persistent (deleting it = data loss): added a per-run scratch area
  (`core.Job.ScratchRoot` + optional `core.ScratchProvider`, at
  `<base>/<tenant>/<workspace>/.scratch/<runID>/`, quota-counted). The io
  drops resolve `scratch://path` through the shared `io.openSandboxRoot`
  (no per-drop flag; scheme rides through Refs); the dispatcher reclaims
  the dir on every terminal path. Tests across io + FSSandbox + e2e.
  **Open:** adopt the resolver in db/git/shell drops; per-node
  `CleanupOnNodeComplete`; multi-node reclaim is best-effort local.
- [x] **Streaming writes with unknown size (`ReserveAndStream`)**
  (2026-05-27). Implemented in `http_download`'s `streamToFile`: each
  chunk is reserved against the per-tenant budget (`reserveQuota`) before
  it lands and the snapshot is checked incrementally, so an over-budget
  download aborts mid-stream with the partial file removed — no need to
  know the size up front. Reservations are released on commit/abort.
- [x] **`maybeCompleteGraph` O(N²)→O(N)** (2026-05-27). The per-node
  `store.Get` loop on every completion is now one batch `ListNodeRecords`
  read — O(nodes) round trips per run instead of O(nodes²). Stays
  store-backed (multi-node correct); under-fetch can only err toward
  "not complete".

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

- [x] **OTLP trace export** (2026-05-27). The engine already created
  graph/node spans (`engine/tracing.go`) but they hit the global noop
  tracer and vanished. `daemon.SetupTracing` now installs an SDK
  `TracerProvider` with an OTLP/gRPC exporter + W3C propagation when the
  standard `OTEL_EXPORTER_OTLP_ENDPOINT` (or `_TRACES_ENDPOINT`) is set —
  off by default (noop, zero overhead), wired in `cmd/hzd` with graceful
  flush on shutdown. Tests cover env gating + provider install/no-op.
  Documented in `DEPLOY.md`. (Adds the `otlptracegrpc` exporter dep.)
- [x] **Disk-usage metrics per tenant + `/metrics`** (2026-05-27). A
  hand-rolled Prometheus text endpoint (`daemon/metrics.go`) exposes
  `hazyflow_up` + `hazyflow_quota_bytes_used{tenant}` /
  `hazyflow_quota_bytes_limit{tenant}` (via a new optional
  `core.QuotaReporter` / `FSQuota.Usage()`). Behind `--metrics` (default
  off — reveals tenant names) and unauthenticated like `/healthz`.
  Hand-rolled to avoid the OTel-exporter + client_golang deps. Also emits
  `hazyflow_jobs{status}` (node-job counts — queue depth + in-flight) via
  an optional `core.JobCounter` (`CountsByStatus` on mem + pg). 4 tests.
  **Follow-up:** a gRPC-exposed metrics path (the gRPC *health* service
  now ships — see "Worker health / readiness").
- [x] **Worker health / readiness** (2026-05-27). HTTP `GET /healthz`
  (liveness) + `GET /readyz` (readiness — pings Postgres when configured),
  AND the standard `grpc.health.v1` service on the gRPC server for
  gRPC-only / k8s `grpc_health_probe` deployments. `daemon.MonitorGRPCHealth`
  syncs the overall ("") status with a readiness probe (Postgres ping,
  mirroring `/readyz`); SERVING-only when no probe is configured. Tests in
  `daemon/health_test.go`.
- [ ] **Structured graph-progress audit log.** Right now progress only
  flows over the gRPC stream. Long-running graphs that disconnect
  lose history.
- [x] **Per-tenant rate limiting beyond disk quota** (2026-05-27).
  Operator-wide ceilings: `--max-graph-nodes` rejects oversized graphs at
  `SubmitGraph` (`core.ErrGraphTooLarge`→400), `--max-graph-timeout`
  clamps `effectiveGraphTimeout`, and `--max-concurrent-jobs` caps a
  tenant's running node jobs in `Claim` (new work withheld at cap,
  expired-lease reclaims exempt). The memory store's cap is exact; the
  Postgres cap is a documented best-effort **soft cap** (correlated-count
  subquery, not locked, so a race can briefly allow cap+1). Tests across
  both stores. **Follow-up:** per-tenant overrides (today all global —
  needs a `--quota`-style tenant→limit map).

## Coverage gaps (the honest list)

- [x] `engine/` 57% → 72.7% → **90.7%** (2026-05-28) — added a focused
  `coverage_test.go` covering: Engine.Run/RunNode error branches
  (no-resolver, cancelled, sandbox/secret failures, approval-URL),
  Engine.populateSandbox (sandbox + scratch + quota + each error path),
  NodeResolver chain over native/local/remote/MCP catalogs, Registry
  error paths (empty ID, no Execute, duplicate, package-level panic),
  RemoteCatalog Register over a real TCP gRPC server (Insecure mode) +
  manifest-id mismatch + default-timeout branches, protobuf
  conversion edge cases (refToPB inline, progressFromPB Data,
  resultFromPB Error, portFromPB Min/Max), upstream-path walkers for
  every map/slice flavor, SubstituteValue recursion + error
  propagation, forwardProgress ctx-cancel drain, isValidScheme matrix.
- [x] `engine/jobstore/` 23.5% → **92.9%** (2026-05-28) — new shared
  `conformance_test.go` exercises the missing methods (CountsByStatus,
  Requeue success/terminal/missing, ListGraphRuns + ListNodeRecords
  with every filter/sort/pagination path, Renew owned/unowned/missing,
  Complete terminal-rejected + awaiting-resume + non-terminal-rejected,
  ListByGraph, duplicate Enqueue) against BOTH the Memory store
  (unconditional) and the Postgres store (gated on `HAZYFLOW_TEST_DB`,
  runs in CI). Added pg-only tests for OpenPostgres bad-DSN and
  NewPostgresFromPool nil-pool. Verified end-to-end against a real
  PG 16 cluster.
- [~] `cmd/hzctl` / `cmd/hzd` — off 0% (2026-05-27): hzd 15.8%, hzctl
  9.4%. Covered the pure helpers (`parseSize`/`parseQuotaSpec`/`envInt`,
  `register*` error paths) and the `graphToPB`↔`graphFromPB` round-trip;
  the remainder is `main()` + cobra command builders (not unit-testable
  without an integration harness). **The push surfaced + fixed two real
  bugs:** (1) `parseSize` couldn't parse `10MB`/`1GB` — its own
  `--quota` flag examples — because it stripped the multiplier before the
  `B`; (2) `controlpb.GraphTrigger` had no `interval_seconds`, so a poll
  trigger's interval was silently dropped saving a graph via `hzctl`
  (gRPC), leaving the trigger inert. Added the proto field (regenerated)
  + threaded it through the conversion.
- [x] `workspace/` 59% → 76.3% → **83.3%** (2026-05-28) — added
  `store_extra_test.go`: idempotent Save returning HEAD, LoadAt with
  a 40-char hash that doesn't resolve to a commit, invalid JSON in a
  graph file (writes through the underlying billy.FS), ListGraphs on
  an init-only repo, LoadAt via branch name (ResolveRevision path),
  OpenFS through a regular-file barrier (MkdirAll error), Save after
  .git wipe, PromoteToEnvironment moving a tag across commits. The
  remaining ~7% is defensive error handling against go-git internals
  (worktree() errors, fs.Create failures on memfs) that would require
  mocking the storage layer.
- [x] `integrations/io/` 66% → 76.6% → **87.9%** (2026-05-28) — added
  `coverage_test.go` covering: coerceTypedCell for every
  excelize.CellType (unset / bool / number / formula / date / error /
  inline-string / shared-string), normalizeRows / coerceRowMap /
  normalizeHeaders type-variant matrix, download helpers (downloadURL
  input-vs-param precedence, downloadHeaders bad-shape variants,
  downloadStatusOK expect_status matrix, paramIntSliceLocal), every
  early-return bad_param / missing_input / no_sandbox branch on the
  four execute* drops, http_download mkdirs + bad-URL + header-send +
  custom expect_status, streamToFile write-failure via a mock root,
  guessMIMEByExt full extension map, isSandboxEscape variants,
  inlineToBytes variants, excel_read typed-without-headers
  (flattenRowsTyped col_N branch), excel_write append-to-existing,
  named sheet, autosize+freezeRow, bad-headers-input, bad-rows-input.
  The remaining ~12% is defensive error handling on excelize/file IO
  edge cases (renderXLSX/renderAppendedXLSX cell-coordinate
  serialization failures) that would require fault injection.

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
  is a guess; should validate against actual restart times. (The
  double-execution *hazard* on lease loss is now fenced — see Reliability;
  what's left is tuning the duration itself.)
- Cron scheduler under clock skew — we use `time.Now`; haven't
  reasoned about NTP jumps, daylight-saving transitions, or container
  clock drift.

## UI / gateway gaps (called out at ship time)

Surfaced as "honest gaps" when individual UI features landed — none
blocking, but listed so we don't lose them.

### Admin
- [x] **Audit log.** **Shipped 2026-05-27 (backend + frontend).** (a) Persistence:
  `core.AuditLog` + `daemon.MemAuditLog` / `PgAuditLog` (`audit_events`
  table). (b) Instrumentation: gateway-layer `h.audit(...)` on graph
  save/run, secret put/delete (name only, never the value), API-key
  issue/revoke, approval decisions, and run cancel. (c) `GET
  /api/v1/admin/audit` (tenant:admin gated, tenant-scoped, paginated;
  501 when unconfigured). Wired in `cmd/hzd` (pg-backed with a DSN, else
  mem). Tests: store scoping/ordering/pagination + endpoint admin-gate +
  end-to-end (save → trail). Frontend: read-only `AdminAudit`
  page (`web/src/pages/AdminAudit.tsx`) at `/admin/audit`, tenant:admin
  gated, table of time/actor/action/target/detail; the Admin dashboard
  card is now `ready`; `api.listAudit` + `AuditEvent` type + en/sv i18n.
  `tsc -b` + `vite build` green.
- [~] **Workspace settings UI** (read-only limits, 2026-05-27).
  `AdminWorkspace` page at `/admin/workspace` (tenant:admin gated) shows
  the tenant's effective disk quota (used/limit) + the daemon-wide graph
  caps (max nodes, default/max run timeout), via a new read-only
  `GET /api/v1/admin/limits` (`api.getWorkspaceLimits`); dashboard card
  now `ready`; en/sv i18n; backend tests + `tsc`/`vite` green.
  **Deliberately read-only:** these are operator flags, not
  runtime-mutable. **Still open (a product call):** editable per-tenant
  config would need a new mutable settings store + write endpoints.
- [x] **Module registry admin view** (2026-05-27). Read-only
  `AdminModules` page (`web/src/pages/AdminModules.tsx`) at
  `/admin/modules`, tenant:admin gated, lists every registered drop
  grouped by category with id/label/provider/integration/tags +
  idempotent/approval/subgraph flags. Frontend-only — reuses the existing
  `GET /api/v1/drops` (`api.listDrops`); en/sv i18n; dashboard card now
  `ready`. `tsc -b` + `vite build` green. (Approving remote/MCP modules
  is a separate future gate.)
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
