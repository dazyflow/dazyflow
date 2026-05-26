# TODO

Open items collected across iterations. Each entry notes the impact and a
hint at what's needed. Items at the top section block real usage; items
further down are quality, observability, and known-unknowns.

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
  unauthenticated paths. Email verification deliberately deferred
  (needs an SES/SendGrid/SMTP story); rate-limiting + captcha
  open under Browser auth + transport.
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
- [ ] **`secret_set` drop + cursor-based polling.** Deferred from
  the poll_trigger landing. Two pieces: (a) a `secret_set` drop
  that takes a `name`+`value` and writes to the `tenant://` store
  during graph execution (the missing inverse of secret READ),
  (b) optional polling-state semantics where the daemon scrapes
  a designated node's output as the next "cursor" and feeds it
  back to `poll_trigger` on the next fire. Together they unlock
  the Zapier "fire only on new items" shape without per-connector
  bookkeeping. ~2 days.
- [ ] **`slack_on_mention` trigger.** Deferred from the Slack
  connector landing. Needs (a) a `POST
  /api/v1/events/slack/{tenant}` webhook endpoint that verifies
  Slack's HMAC-SHA256 signature against the app's signing secret,
  (b) URL-verification challenge handling on first subscription,
  (c) team_id → tenant routing (which connected account this
  team belongs to), (d) marshaling the event into a job that
  graphs with `slack_on_mention` triggers consume. ~2 days.
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
  **Webhook triggers** (`github_on_push`, `github_on_new_pr`)
  still deferred alongside slack_on_mention — they share the
  same HMAC + tenant-routing follow-up.
- [ ] **Notion launch connector.** OAuth, drops:
  `notion_create_page`, `notion_query_database`,
  `notion_on_db_change` (polling trigger). ~3 days.
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
  Five seed templates cover the breadth (Excel→DB,
  webhook→Slack, daily Postgres digest, Gmail→Sheets,
  Sheet→Postgres upsert sync). Open follow-up: 15+ more
  templates to fill the gallery, and an admin UI to add custom
  templates per tenant.

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
  (editor link still available). **Side-by-side input/output
  diffs across reruns** and **replay-with-modifications**
  deferred — both useful but lower-leverage than the basic
  "what happened" surface that landed here.
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
  custom receivers — typed Slack-channel / email pickers
  deferred to when there's a clean per-tenant-token plumbing
  story (those would invoke the existing connector drops; v1
  picks the smallest blast radius).
- [ ] **Trigger test/preview UX.** "Run trigger now" button that
  fetches sample data from the connected account without firing
  downstream. Critical for the field-mapping workflow — users need
  to see real shapes when wiring fields. ~3 days.

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
- [ ] **Per-tenant ACL on secrets.** Today any graph in any tenant can
  resolve any secret the daemon knows about. Production needs
  namespacing: tenant `acme` can only resolve secrets under `acme/...`,
  enforced inside provider `Get`.
- [ ] **Egress allowlist for `http_request`.** SSRF blocks private IPs
  but a tenant can still reach any public IP. Production needs a
  per-tenant allowlist of permitted domains/IPs above the SSRF layer.
- [ ] **Idempotency keys on outbound HTTP.** Modules that POST to
  payment / ERP systems should send an idempotency key derived from
  (graph_run_id, node_id, attempt) so retries don't double-charge.
- [ ] **Port-bind failures are silent.** When the webhook listener's
  port is taken, `hzd` logs and continues. Should fail-loud on startup
  so orchestration (k8s, systemd) restarts the process.
- [ ] **Output sanitization for secrets.** Modules can write resolved
  secret values into their `Result.Output`; downstream `file_write`
  could persist them. No automatic redaction. Documenting at minimum,
  ideally lint the graph for `file_write` connected downstream of
  http_request response_body when a request used a secret.

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
- [ ] **Postgres JobStore real-DB tests.** Implementation exists
  (`engine/jobstore/postgres.go`) and tests are gated on
  `HAZYFLOW_TEST_DB`. Wire a CI service or docker-compose so the
  Postgres path is actually exercised.

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
- [ ] **Jitter on retry backoff.** Pure exponential synchronizes
  failures when many sibling nodes fail together. Add small random
  jitter (e.g. ±25%).

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
- [ ] **Worker health / readiness.** No `/healthz` / `/readyz` surface
  on hzd. Add to the gRPC server (health-check service) or a sidecar
  HTTP handler. Particularly important given the silent bind-failure
  in the webhook listener — health check would catch that.
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
- [ ] **Cron expression validation** in the trigger settings modal.
  Currently freeform; bad expressions silently never fire. Either
  client-side parser or a daemon-side validate endpoint.
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
- [ ] **Cookie / CSRF auth.** Today the gateway is bearer-only.
  Tighten before exposing publicly — at minimum a same-site cookie
  session for the UI, with CSRF tokens on writes.
- [ ] **Rate limiting** on the gateway. None today.
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
- [ ] **`join_rows`** — SQL JOIN equivalent between two row streams.
  Two row-input ports, `on` param specifying the join keys
  (`{left_col: right_col}`), `kind` (inner/left/right/outer).
  The natural next ETL primitive after the dedupe/sort/split set —
  unlocks "enrich the Excel rows with this DB lookup table" without
  dropping into SQL.
- [ ] **`group_aggregate`** — group rows by one or more columns and
  emit one row per group with aggregated values. Aggregations:
  count, sum, avg, min, max, first, last, collect-as-list. Static
  config; CEL expressions for derived aggregates can come via a
  follow-up `compute_rows` step. Covers the pivot-table use case
  without inventing a Pivot drop yet.
- [ ] **`route_rows` (N-way split)** — variadic output ports keyed
  by a column value (or per-port CEL predicate). Picks up where
  `split_rows` stops: instead of two ports, route to N named
  destinations like `{SE: ..., NO: ..., default: ...}`. Needs the
  variadic-output-port story sorted in the editor first
  (per "Variadic input ports" under Editor).
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

## Recently shipped (delete-when-reviewed)

For context — these were on this TODO list and are now done. Keeping
them visible so we don't re-litigate the design choices.

- [x] **`http_request` module** — done with SSRF blocks (loopback,
  RFC1918, link-local incl. AWS metadata), body size cap, status
  filter, timeout, JSON/text MIME detection. 17 tests.
- [x] **`branch` module** — done with field-path + 10 ops. Engine
  edge-classifier extended so unused output ports correctly mark
  downstream as skipped.
- [x] **Cron triggers** — `daemon.Scheduler` polls workspace stores
  for graphs with `Triggers: [{Type: "cron", Cron: "..."}]` and fires
  via `Service.SubmitGraph`.
- [x] **Webhook triggers** — `daemon.WebhookListener` exposes
  `POST /trigger/<tenant>/<workspace>/<graph>` with per-graph bearer
  secret. (Body still discarded — see top of list.)
- [x] **Secret injection (env + builtin)** — `env://` and
  `builtin://` resolved just before `transport.Execute`. JobStore
  and workspace Git retain symbolic references; resolved values
  never persist.
- [x] **Engine fix: missing FromPort output = dormant edge** — was a
  latent footgun where downstream of a non-emitting port ran with
  empty input. Now correctly skipped.
- [x] **Browser UI scaffold (`web/`).** Shipped: Vite + React + TS +
  React Flow + lucide-react. Synthwave dark palette ported from
  `../hazy`. App shell (TopBar with mobile hamburger, sidebar nav),
  bearer-token sign-in, persistent session in localStorage. Pages:
  pipeline list, pipeline editor (drag-from-catalog onto canvas,
  edge connect, per-node Inspector with JSON params, Save, Run with
  live status from SSE), Admin (role-gated, stubbed cards for API
  keys / users / audit). `core.Manifest.Icon` field populated for
  every built-in module; the UI maps logical icon names to
  lucide-react glyphs. `core.Node.Position` round-trips via
  PUT /graphs. `/api/v1/whoami` returns the principal + flat
  permission set so the UI can gate features. Production build
  passes (`npm run build`); typecheck clean. Stubs:
  schema-driven param form, granular per-node SSE updates, admin
  endpoints (API keys / users / audit) — none wired yet.
- [x] **HTTP/JSON gateway for browser/UI access.** Shipped:
  `daemon.HTTPGateway` exposes `/api/v1/{modules,graphs,jobs}` over
  REST with bearer-token auth (reuses the API-key chain), permissive
  CORS (tightenable via `AllowedOrigins`), and `GET /jobs/{id}/events`
  as Server-Sent Events for live node-status streaming. The SSE frame
  set: `snapshot` (initial JobRecord), `progress` (engine events),
  `terminal` (closes the stream). 25-second keep-alive pings prevent
  proxy idle timeouts. Tested via 8 handler-level tests and one full
  e2e (PUT graph → POST /run → SSE-stream-to-terminal → final
  GET /jobs/{id}). `core.Node` gained an optional `Position{X,Y}`
  field for UI layout that the engine ignores. Limitations: no
  cookie/CSRF (bearer-only), no rate-limiting, no per-tenant origin
  pinning (CORS is global).
- [x] **Subgraph module — call-graph-as-step.** Shipped:
  `modules/flow/subgraph.go` is a declarative awaiting-style module
  (manifest flag `SubmitsChildGraph`). The worker hands the result to
  `daemon.SubGraphRunner` (Service satisfies it via `SubmitChild`),
  which loads the child from the parent's workspace and submits it
  with `ParentNodeRecID` linkage on the graph-record. When the child
  terminates, `Dispatcher.maybeResumeParent` projects the child's
  named (node, port) outputs onto the parent node's output ports via
  `pending_output_map`, then advances the parent's graph as usual.
  Child failures surface to the parent as `child_failed` so OnError
  rules apply normally. V1 limits: same-workspace only, no static
  cycle detection. Three e2e tests (happy path, child failure
  propagates, unknown child fails cleanly).
- [x] **Realistic shape demo (`examples/ap-invoice/`)** — single
  graph, two webhook fires, branch routes on amount, secrets injected,
  archived to sandbox. 10/10 assertions pass.
