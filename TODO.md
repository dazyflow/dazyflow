# TODO

Open items collected across iterations. Each entry notes the impact and a
hint at what's needed. Items at the top section block real usage; items
further down are quality, observability, and known-unknowns.

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
- [ ] **`split` module.** Spec lists it under `modules/flow/`. Branch
  is done; split (one input → N outputs) is still missing. Semantics
  TBD: does it require the input to be a list? What happens if N
  doesn't match list length?
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
- [ ] **Blob storage** — S3 / GCS / R2 with proper streaming via the
  Ref pointer (not Inline).
- [ ] **`split` module** (still open from spec days).

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
