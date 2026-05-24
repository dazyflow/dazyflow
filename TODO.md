# TODO

Open items collected across iterations. Each entry notes the impact and a
hint at what's needed.

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
- [ ] **SecretProvider implementations.** Interface exists in
  `core/secrets.go`, no backends. Spec lists builtin (encrypted file),
  vault, gcp, aws, azure. None plumbed; Engine never resolves
  `secret://` refs into params.

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
- [ ] **Scheduler role.** Spec mandates `pg_try_advisory_lock`-based
  leader election so one hzd handles cron triggers, expired-lease
  cleanup, etc. Workers are currently peers with no coordinator.
- [ ] **Postgres JobStore real-DB tests.** Implementation exists
  (`engine/jobstore/postgres.go`) and tests are gated on
  `HAZYFLOW_TEST_DB`. Wire a CI service or docker-compose so the
  Postgres path is actually exercised.

## Modules

- [ ] **`shell` module.** Spec lists it under `modules/io/`. Needs
  process-level isolation beyond filesystem sandbox: nsjail, bubblewrap,
  Linux user namespaces, or a per-job container. Resource limits via
  ulimit/cgroups. Output capturing of stdout/stderr/exitcode per spec.
- [ ] **`http_request` module.** Spec lists it. Needs egress allowlist
  (otherwise tenants can SSRF internal services), TLS validation, body
  size limits.
- [ ] **`webhook_trigger`.** Trigger execution model exists only as a
  `core.ExecutionTrigger` constant. Engine has no trigger-loop —
  trigger nodes never get woken up by external events.
- [ ] **`schedule` (cron).** Spec lists it. Belongs to whatever the
  scheduler role becomes (see above).
- [ ] **`split` / `branch`.** Spec lists them under `modules/flow/`. Not
  implemented. Branch needs a small expression evaluator.

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

## Observability

- [ ] **Disk-usage metrics per tenant.** Expose
  `hazyflow_quota_bytes_used{tenant=...}` and `_limit` via the OTel
  metrics SDK so dashboards can show approaching-limit signals.
- [ ] **Worker health / readiness.** No `/healthz` / `/readyz` surface
  on hzd. Add to the gRPC server (health-check service) or a sidecar
  HTTP handler.
- [ ] **Structured graph-progress audit log.** Right now progress only
  flows over the gRPC stream. Long-running graphs that disconnect
  lose history.

## Coverage gaps (the honest list)

- [ ] `engine/` at 56% — `LocalCatalog.LoadDir`, `cancelledResult`,
  some error branches in `runProtocol`.
- [ ] `engine/jobstore/` at 36% — Postgres path gated, exercised only
  when `HAZYFLOW_TEST_DB` is set.
- [ ] `cmd/hzctl` / `cmd/hzd` at 0% — no tests.
- [ ] `workspace/` at 59% — most missed branches are error paths in
  Save/Promote.
- [ ] `modules/flow/` at 69% — primarily the lesser-used progress
  branches in `sleep`.

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
