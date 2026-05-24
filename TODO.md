# TODO

Open items collected across iterations. Each entry notes the impact and a
hint at what's needed. Items at the top section block real usage; items
further down are quality, observability, and known-unknowns.

## Top of the pile — biggest realism gaps

These showed up while building the AP-invoice demo. Without them the
platform can demonstrate but not actually power a real workflow.

- [ ] **Webhook body → graph input.** Today's webhook fires the graph but
  discards the body. Real webhooks carry the data the graph needs to act
  on (invoice ID, GitHub PR number, Stripe event payload). Two ways to
  fix: a `webhook_input` module the engine pre-completes with the body,
  or a graph-level `initial_input` that the trigger can populate. The
  second is more flexible (works for cron too — passing the fire time).
- [ ] **Template-style secret substitution.** Resolver is whole-string:
  `Authorization: env://KEY` resolves the *entire* header value. So an
  env var must already contain `Bearer <token>`, not just the token.
  Add `${env:KEY}` or `{{ secret "..." }}` substitution so users can
  write `Authorization: Bearer ${env:STRIPE_KEY}` and store only the
  token in the secret store.
- [ ] **`await_approval` pause/resume.** A graph that runs until a human
  clicks "approve" then continues. Engine has no pause state today.
  Requires: node-record status `awaiting`, external resume mechanism
  (HTTP endpoint? signed token in approval link?), persistence across
  worker restart.

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
- [x] **Realistic shape demo (`examples/ap-invoice/`)** — single
  graph, two webhook fires, branch routes on amount, secrets injected,
  archived to sandbox. 10/10 assertions pass.
