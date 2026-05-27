# Overnight autonomous session — 2026-05-27

## NEXT — Phase 1 remaining (updated 2026-05-27 PM)
DONE this session: SSRF egress allowlist (http_request) + webhook_send
SSRF guard (was wide open to cloud metadata) — both with tests, suite green.

DONE 2026-05-27 PM (continued): TLS proxy-contract hardening
(--trust-proxy-headers → Secure cookies + HSTS + nosniff; fixed the
always-nil r.TLS Secure bug; DEPLOY.md documents the nginx contract).

DONE 2026-05-27 (cont.): secret-sanitization rule #2 `hardcoded_secret`
(+ per-node canvas warning badges) and the master-key runbook
(SECURITY.md). Go + web green.

Phase 1 still open:
- Quota write race (deferred — needs supervision).
- Per-tenant egress allowlist (today it's operator-global). ← can do
- Master-key KEK re-wrap *command* (rotation tooling; doc shipped, code v2).
Then Phase 1 is essentially done → Phase 2 (HA: multi-node bus, leader
election).

DONE 2026-05-27 — Phase 2 HA (both done, tests pass against real PG):
- Multi-node event bus: daemon/eventbus_pg.go (PgBus, bus_events +
  pg_notify) + eventbus_pg_test.go (cross-instance, terminal round-trip,
  no-cross-talk). cmd/hzd selects PgBus when --postgres-dsn set.
- Scheduler leader election: daemon/leader.go (PgLeader,
  pg_try_advisory_lock) + leader_test.go (single-holder + failover).
  Wired via Scheduler.SetLeader in cmd/hzd's cron block.
Full suite green both with and without HAZYFLOW_TEST_DB.
- Two-process load test: scripts/ha_loadtest.sh + scripts/ha_loadtest/seed.go.
  Spins up throwaway Docker PG, two real hzd processes sharing it, seeds a
  node-less 1s poll graph, asserts exactly-one-leader + no-double-fire +
  failover (kill leader → follower takes over, keeps firing). PASSED
  2026-05-27 (~6 fires/10s single-leader; would be ~2x if both fired).
Phase 2 is now functionally complete. Throwaway PG containers removed;
the load-test script tears its own container + tmp dirs down (KEEP=1 to keep).

New files this session: SECURITY.md, DEPLOY.md, integrations/net/egress.go (+test),
daemon/ratelimit.go (+test), daemon/eventbus_pg.go (+test), daemon/leader.go (+test).
All uncommitted (gpg signing needs you).

---


Running a safe batch while you sleep. Rules: local-only, reversible,
no push/deploy, build+tests green per change, local commits at
checkpoints, skip anything needing a product decision.

## Batch — final status (7 done, 1 deliberately deferred)
- [x] #15 --dev-key default off
- [x] #16 rate-limit auth/signup
- [x] #17 fail-loud on port-bind failure
- [~] #18 quota write race — DEFERRED (correctness-critical, see note)
- [x] #19 retry backoff jitter
- [x] #20 /readyz readiness endpoint
- [x] #21 Dockerfile (multi-stage) — image builds (60MB) + /healthz,/readyz 200 in-container
- [x] #22 CI build manifest + Postgres service

## What to review (everything UNCOMMITTED — sign-commit when you're ready)
New files:
  daemon/ratelimit.go, daemon/ratelimit_test.go
  auth/postgres.go, auth/postgres_test.go      (from the earlier Phase 0)
  Dockerfile, .dockerignore, .build.yml
Changed:
  cmd/hzd/main.go          (--dev-key default, --postgres-dsn, --auth-rate-*, pre-bind listeners, ReadyCheck)
  daemon/httpgateway.go    (ServeListener, /readyz, AuthRateLimit, rateLimitAuth)
  daemon/httpgateway_test.go (readyz tests)
  daemon/webhook.go        (ServeListener)
  daemon/worker.go         (retry jitter)
  engine/jobstore/postgres.go + schema.sql + postgres_test.go (Phase 0 fixes)
  daemon/encrypted_secrets.go (SecretsBackend alias, Phase 0)
  TODO.md                  (Phase 0/1/4 + detail items updated)

Verify: `go build ./...` + `go test ./...` green (did, 25 ok pkgs).
For the Pg-gated tests: `HAZYFLOW_TEST_DB=postgres://… go test ./auth/ ./engine/jobstore/`.
Docker: `docker build -t hazyflow . && docker run --rm -p 8080:8080 hazyflow` → /healthz 200.

Suggested commit split:
  1. Phase 0 (postgres durability) — auth/postgres*, jobstore pg + schema, encrypted_secrets alias, main.go store wiring
  2. Phase 1 security — dev-key default, ratelimit*, port-bind ServeListener, /readyz
  3. retry jitter
  4. ops — Dockerfile, .dockerignore, .build.yml
  5. TODO.md updates
(Delete this OVERNIGHT.md + any leftover /tmp logs whenever.)

Left running from your dev session (untouched): hzd on :8089, vite on :8080.
Throwaway test PG container stopped. Image `hazyflow:overnight` left for you to try.

## Note on commits
Repo has gpg commit signing — can't sign unattended (pinentry timeout).
So everything is left **uncommitted** for you to review + sign-commit.
All changes keep `go build ./...` + `go test ./...` green.

## Log
- #15 DONE: --dev-key defaults off (opt-in via flag / HAZYFLOW_DEV_KEY=1).
- #17 DONE: webhook + HTTP gateway now bind on the main goroutine (new
  ServeListener methods); hzd fatals on a port-in-use bind error instead
  of silently dropping the listener. daemon tests green.
- #20 DONE: GET /readyz added (pings Postgres when configured via new
  gw.ReadyCheck hook; 200 when no dep). 2 tests pass.
- #16 DONE: per-IP token-bucket rate limiter (no new dep, daemon/
  ratelimit.go) on /auth/signin + /auth/signup. Flags --auth-rate-per-min
  (default 20) / --auth-rate-burst (10); 0 disables. 3 tests pass.
- #19 DONE: ±25% jitter on the default retry backoff (worker.go) so
  sibling-node failures don't re-synchronize. Retry tests green.
- #18 SKIPPED (left for you): quota write race. Fixing it right needs a
  reserve-and-hold-across-write change threaded through the module write
  path — correctness-critical, shouldn't land unattended. The snapshot
  model in engine.go:397 + module write checks is where it lives.
