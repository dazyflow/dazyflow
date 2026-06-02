# Deploying hzd

Every knob below is a `HAZYFLOW_*` environment variable. `hzd` itself
only has two flags, both one-shot operator commands that exit after
running (`--rotate-master-key`, `--import-users-from-json`). For the
canonical list see `.env.example`.

## TLS / reverse-proxy contract

`hzd` does **not** terminate TLS. Run it behind a TLS-terminating reverse
proxy (nginx, Caddy, Traefik, a k8s ingress) and proxy plain HTTP to the
gateway port.

The proxy MUST:
- terminate TLS and forward to `hzd`'s HTTP port (`HAZYFLOW_HTTP`) over
  HTTP;
- set `X-Forwarded-Proto: https` on forwarded requests;
- forward the `Host` and `Origin` headers unchanged (the gateway's CSRF
  origin check + CORS allowlist depend on them);
- upgrade WebSocket/SSE connections (Vite HMR in dev, the chat + run SSE
  streams in prod).

`hzd` MUST be configured with:
- `HAZYFLOW_TRUST_PROXY_HEADERS=1` — so it honors `X-Forwarded-Proto` and
  marks session cookies `Secure` + sends HSTS on forwarded-HTTPS
  requests. **Do not set this if hzd is exposed directly** (a client
  could spoof the header to flip Secure on over plain HTTP).
- `HAZYFLOW_WEB_ORIGIN=https://your.domain` — the exact browser origin,
  for the CORS allowlist + the cookie-origin CSRF check.
- `HAZYFLOW_PUBLIC_BASE_URL=https://your.domain` — used for OAuth
  redirect URIs and failure-notification deep links.

What the gateway does once `HAZYFLOW_TRUST_PROXY_HEADERS=1` is on and the
request arrives as forwarded-HTTPS:
- session cookie gets `Secure` (plus the existing `HttpOnly` +
  `SameSite=Lax`);
- responses carry `Strict-Transport-Security: max-age=31536000;
  includeSubDomains` and `X-Content-Type-Options: nosniff`.

### nginx example

```nginx
server {
    listen 443 ssl;
    server_name app.example.com;

    ssl_certificate     /etc/letsencrypt/live/app.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/app.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;     # hzd listens on HAZYFLOW_HTTP=:8080
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header Origin            $http_origin;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;   # required
        # SSE + WebSocket (HMR / run streams)
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 600s;
    }
}
```

Matching `.env` for the daemon (everything operator-controllable is an
env var — see `.env.example` for the full catalogue):

```env
HAZYFLOW_HTTP=:8080
HAZYFLOW_WEB_DIST=/srv/web
HAZYFLOW_TRUST_PROXY_HEADERS=1
HAZYFLOW_WEB_ORIGIN=https://app.example.com
HAZYFLOW_PUBLIC_BASE_URL=https://app.example.com
HAZYFLOW_POSTGRES_DSN=postgres://hazyflow:…@db/hazyflow?sslmode=require
HAZYFLOW_MASTER_KEY=<32-byte base64; openssl rand -base64 32>
```

Container deployments don't have to set `HAZYFLOW_HTTP` /
`HAZYFLOW_WEB_DIST` — the supplied Dockerfile bakes those in via
`ENV` (see `Dockerfile`).

## Kubernetes (multi-node)

`deploy/k8s/hazyflow.yaml` is a 2-replica Deployment, a Service, and a
Secret template. Multi-replica works out of the box: the Postgres event
bus lets any pod stream a run's events (`PgBus`), and a Postgres
advisory-lock leader ensures only one pod fires each schedule
(`PgLeader`). Everything is configured by `HAZYFLOW_*` env vars on the
container — there are no daemon flags to set.

1. Edit the `hazyflow-secrets` Secret: a fresh `HAZYFLOW_MASTER_KEY`
   (`openssl rand -base64 32`) and your managed-Postgres
   `HAZYFLOW_POSTGRES_DSN`.
2. Build and push the image from the repo Dockerfile
   (`docker build -t hazyflow/hzd:latest .`) and set it in the Deployment.
3. Update `HAZYFLOW_WEB_ORIGIN` / `HAZYFLOW_PUBLIC_BASE_URL` in the
   Deployment env to your real hostnames (`HAZYFLOW_TRUST_PROXY_HEADERS=1`
   is already set), then `kubectl apply -f deploy/k8s/hazyflow.yaml`.
4. Front it with an ingress that terminates TLS and forwards `Host` /
   `Origin` unchanged (the same reverse-proxy contract as above).

Probes: liveness `/healthz`, readiness `/readyz` (pulls a pod from the
Service when Postgres is unreachable). The `grpc.health.v1.Health` service
on :50050 is available for a `grpc_health_probe` sidecar if preferred.

## Per-org subdomains (optional)

Set `HAZYFLOW_WILDCARD_DOMAIN=<apex>` (e.g. `hazyflow.app`) to give each
org its own subdomain. A visit to `acme.hazyflow.app` lands on the
sign-in page with `org=acme` preselected, so that org's "Sign in with
Google" button shows without a `?org=` query param. Leave it empty and
the daemon behaves exactly as a single-host deploy.

What it changes when set:

- **CORS + CSRF** accept any `*.<apex>` subdomain as a browser origin, in
  addition to the exact `HAZYFLOW_WEB_ORIGIN` entries. The apex itself is
  not implied — list it in `HAZYFLOW_WEB_ORIGIN` as usual. The match is a
  strict subdomain suffix, so `evil-hazyflow.app` does not match
  `hazyflow.app`.
- **Session cookies stay host-only** (no parent-domain cookie). Each
  org's session is scoped to its own subdomain, so one org's subdomain
  can never read another's cookie.
- **Google/OAuth sign-in** still uses a single redirect URI on the apex
  (`HAZYFLOW_PUBLIC_BASE_URL`), so you register **one** redirect URI with
  the provider regardless of how many org subdomains exist. The apex
  callback issues the session, then 302s the browser to
  `<subdomain>/api/v1/auth/handoff?ot=…` with a single-use, short-lived
  (2 min) token; the subdomain exchanges it for a host-only session
  cookie. Password sign-in needs no handoff — it already happens on the
  subdomain origin.

Infrastructure prerequisites:

- A wildcard DNS record `*.<apex>` pointing at the proxy.
- A wildcard TLS certificate (`*.<apex>`) at the proxy. Let's Encrypt
  issues these via the DNS-01 challenge.
- `HAZYFLOW_PUBLIC_BASE_URL` set to the apex (`https://<apex>`).
- The proxy must route every `*.<apex>` host to the same hzd upstream
  (the sign-in handoff state is held in-process). An nginx `server_name`
  of `<apex> *.<apex>` with the same `proxy_pass` covers both.

Org slugs become DNS labels: only single-label, DNS-valid slugs resolve
to an org, and reserved labels (`www`, `api`, `app`, `admin`, `auth`,
`docs`, `status`, …) never map to one, so those hosts can serve
infrastructure or marketing without colliding with a tenant. There is no
automatic DNS provisioning — adding an org's subdomain is an ops step
(the wildcard record + cert already cover it; nothing per-org to create).

## Durability

Set `HAZYFLOW_POSTGRES_DSN` so jobs, API keys, sessions, users,
encrypted secrets, memberships, invitations, per-org SSO config, and
per-org profiles all persist to Postgres. Without it those run
in-memory or as JSON files under `HAZYFLOW_DATA_DIR/state/` and are
lost on restart (dev only — the daemon logs a warning). Provide a
stable `HAZYFLOW_MASTER_KEY` (32-byte base64); losing it makes every
stored secret undecryptable.

### Fail-closed config guard

When `HAZYFLOW_POSTGRES_DSN` is set (the durable-deployment signal),
`hzd` **refuses to start** if it would otherwise run with a bundled
insecure default — specifically a `HAZYFLOW_POSTGRES_DSN` still using the
shipped default DB password, or an empty `HAZYFLOW_MASTER_KEY`. The boot
log prints a `FATAL` line naming each offending value. Fix them (set a
strong `POSTGRES_PASSWORD` and a real master key) and restart.

`HAZYFLOW_DEV=1` downgrades the guard from fatal to warnings so the
bundled defaults boot for a local trial. **Never set it in production** —
it exists only so `docker compose up -d` works for a throwaway smoke
test.

### Migrating an existing JSON user file to Postgres

If you ran in dev mode with users in a JSON file (legacy:
`./.hazyflow-users.json`; current layout: `<data>/state/users.json`)
and are adopting Postgres, import those accounts once so nobody is
stranded. This is one of the only two flags `hzd` still accepts — a
one-shot command that exits after running:

```sh
HAZYFLOW_POSTGRES_DSN="$DSN" \
    hzd --import-users-from-json ./.hazyflow/state/users.json
# logs "user import complete: N imported, M skipped", then exits
```

It's idempotent (accounts already in Postgres are skipped, never
overwritten), so re-running is safe.

### Backup & restore

Everything durable lives in the one Postgres database, so a standard
Postgres backup is a complete backup — **plus** the
`HAZYFLOW_MASTER_KEY`, which lives outside the DB and is required to
decrypt the `encrypted_secrets` rows.

- **Logical backup (simplest):**
  `pg_dump "$HAZYFLOW_POSTGRES_DSN" | gzip > hazyflow-$(date +%F).sql.gz`.
  Restore into a fresh DB with `gunzip -c … | psql "$DSN"`.
- **Point-in-time recovery:** for larger deployments use base backups +
  WAL archiving (`pg_basebackup` / your managed provider's PITR). The app
  needs no special handling — it re-applies its `CREATE TABLE IF NOT
  EXISTS` schema on boot, so restoring the data is sufficient.
- **Back up the master key separately** (the break-glass copy from the
  "If it's lost" section of `SECURITY.md`). A DB backup without the key
  leaves every tenant secret undecryptable.
- **What's safe to lose:** the `bus_events` spool is ephemeral
  (auto-swept, ~1h retention) and the per-tenant sandbox/scratch dirs are
  derived working data — neither needs backing up. `file_write` outputs
  in the workspace sandbox are the exception if your flows treat them as
  durable artifacts; back up the sandbox base dir too in that case.

## Security knobs worth setting

Every variable is documented in full — semantics, default, and warnings —
in `.env.example`. This is the security-critical subset to review before
you expose the daemon; see `.env.example` for the detail.

- The auth rate limiter is fixed at **20/min per IP (burst 10)** on
  `/api/v1/auth/{signin,signup}` — not a knob, but worth knowing it's there.
- `HAZYFLOW_DEV_KEY` / `HAZYFLOW_DEV` — dev-only; never set in production.
- `HAZYFLOW_HTTP_EGRESS_ALLOW` — allowlist the hosts outbound HTTP drops may
  reach (the IP-level SSRF guard blocks private/loopback/metadata regardless).
- `HAZYFLOW_ALLOW_PRIVATE_EGRESS` — keep **off** on multi-tenant deploys; on,
  it lets flows reach private/loopback/cloud-metadata addresses (and the
  DB/SMTP drop hosts).
- `HAZYFLOW_ENABLE_SHELL` — **off** by default; on, it's host RCE for anyone
  who can run a flow. Single-tenant / CI box only.
- `HAZYFLOW_ISOLATE_SHARED_SECRETS` — scope `env://` lookups per tenant on
  shared multi-tenant deployments.

## Observability

- **Health probes:** HTTP `GET /healthz` (liveness) and `GET /readyz`
  (readiness — pings Postgres when `HAZYFLOW_POSTGRES_DSN` is set). For
  gRPC-only / k8s deployments, the standard `grpc.health.v1.Health`
  service is registered on the gRPC port (use `grpc_health_probe`); its
  overall status tracks the same readiness check.
- **Metrics:** `HAZYFLOW_ENABLE_METRICS=1` exposes a Prometheus
  `GET /metrics` endpoint. Off by default — it reveals tenant names, so
  enable it only behind a restricted scrape network. Series exposed:
  - `hazyflow_up` — liveness.
  - `hazyflow_jobs{status}` — node-job counts by status. `queued` is
    queue depth, `running` is in-flight work.
  - `hazyflow_jobs_oldest_queued_seconds` — age of the oldest claimable
    node job. **The leading indicator for execution backlog**: when this
    climbs, workers can't keep up. Raise `HAZYFLOW_WORKER_COUNT` or add
    replicas before users feel the lag.
  - `hazyflow_pg_pool_connections{state}` (`acquired`/`idle`/`total`),
    `hazyflow_pg_pool_max_connections`, and
    `hazyflow_pg_pool_empty_acquires_total`. **The earliest warning of
    pool exhaustion**: a rising `empty_acquires_total` (or `acquired`
    pinned at `max`) means requests are waiting for a connection. Raise
    `HAZYFLOW_PG_MAX_CONNS` or scale out.
  - `hazyflow_session_cache_hits_total` / `_misses_total` — auth-lookup
    cache effectiveness. A healthy hit ratio means the per-request
    session lookup isn't hammering Postgres; the miss rate tracks raw
    authenticated-request load.
  - `hazyflow_quota_bytes_used/_limit{tenant}` — per-tenant disk usage.
  - `hazyflow_http_requests_total{method,code}` and
    `hazyflow_http_request_duration_seconds{method}` (histogram) — HTTP
    RED. Rate is the counter's increase; error ratio is the share of
    `code` >= 500 (or >= 400); latency percentiles come from the
    histogram (`histogram_quantile(0.99, ...)`). The front-door health
    signal.
  - `hazyflow_node_duration_seconds{status}` (histogram) — per-node
    execution latency, split by terminal status. Rising p99 here is the
    flow-engine analogue of slow requests; the `failed` series' rate is
    your node error rate. Counts retried attempts (each is a real
    execution).

  Suggested alerts as you approach scale: p99 of
  `http_request_duration_seconds` and `node_duration_seconds` above your
  SLO; HTTP 5xx ratio over a small threshold; `oldest_queued_seconds`
  above your acceptable trigger-to-start latency for a few minutes; pool
  `acquired / max` sustained above ~0.8 or any sustained rise in
  `empty_acquires_total`; and watch the `jobs` table and `audit_events`
  row counts against the `HAZYFLOW_JOB_RETENTION` /
  `HAZYFLOW_AUDIT_RETENTION` windows to confirm the retention sweeps keep
  up. The histograms use fixed buckets (5ms to 60s); per-route HTTP
  labels and per-module node labels are intentionally omitted to keep
  cardinality bounded — say so if you want either dimension added.
- **Tracing:** set the standard `OTEL_EXPORTER_OTLP_ENDPOINT` (or
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`) and `hzd` installs an OTLP trace
  exporter so graph/node spans flow to your collector (Jaeger, Tempo,
  the OTel Collector, Honeycomb, …). Unset = no export (zero overhead).
  All standard `OTEL_EXPORTER_OTLP_*` env vars (headers, TLS, timeout)
  are honored.

## Graceful shutdown

On `SIGTERM`/`SIGINT`, `hzd` drains: it stops the gRPC server gracefully,
stops claiming new jobs, and lets any in-flight node finish and finalize
its run before exiting. The wait is bounded by `HAZYFLOW_SHUTDOWN_GRACE`
(default `25s`); if it elapses with work still running, `hzd` exits
anyway and the unfinished node's lease expires so another instance
reclaims it. Set `HAZYFLOW_SHUTDOWN_GRACE` below your orchestrator's
termination grace (e.g. keep it under k8s
`terminationGracePeriodSeconds`, default 30s) so the process drains
rather than being `SIGKILL`ed mid-write.

A separate reaper sweep (every `HAZYFLOW_REAP_INTERVAL`, default `1m`,
plus once at startup) recovers any graph run left marked `running` by a
hard crash whose nodes have all reached a terminal state — so a `SIGKILL`
or power loss can't strand a run forever.

## Human approvals (await_approval)

Flows can pause on an `await_approval` node until a human resumes them.
Two paths:

- **Authenticated (inbox UI):** `POST /api/v1/approvals/{run}/{node}` on the
  gateway — always available, uses the caller's API-key/session.
- **Unauthenticated link (email/Slack):** opt in by setting
  `HAZYFLOW_APPROVAL_HMAC_SECRET=<base64≥16B>` (plus
  `HAZYFLOW_PUBLIC_BASE_URL` so links resolve to a real origin). The
  engine then mints signed `<base>/approve/<run>/<node>?token=…` URLs
  and the main HTTP gateway routes them through HMAC verification
  before resuming — no separate listener to expose. Use the **same
  secret on every node** in a multi-node deployment so a token minted
  by one verifies on another.

## Marketplace: drops & integrations

A **drop** is a node you can drop on the canvas (a connector, transform, or
action); an **integration** is a connection prerequisite a drop depends on
(e.g. `gmail`, `slack`). Besides the built-ins, a platform admin can install
more at runtime from the web UI — **Admin → Marketplace** (`/admin/marketplace`),
platform-admin only — by pointing at a git repo:

- **Install an integration** from a repo's `integration.json`, then connect
  accounts via the OAuth flow.
- **Install a drop** from a repo path (`repo`, `ref`, `path`). Installs are
  persisted and restored on boot. A drop is gated on its required integrations:
  install the integration first or the drop install is refused.

Git fetches are pinned to the resolved commit and routed through the SSRF guard
(only `https`/`ssh`/local schemes; private/loopback addresses are blocked).

**Versioning & uninstall.** Drops are versioned; a graph node refers to a drop
by bare id (`gmail_send_email`, tracks the latest installed version) or pins an
exact version (`gmail_send_email@2.0.0`), so re-installing a newer version can't
silently change a flow that pinned the old one. Uninstalling an integration is
refused while any installed drop version still requires it — uninstall the
dependent drop first.

### Trust tiers

Every install is shown as **official**, **verified**, or **community**. The tier
is *derived from a signature*, never self-declared — a drop cannot mark itself
official. A repo ships a detached `<file>.sig` (Ed25519) next to each signed
artifact; the daemon verifies it over the exact bytes against the keys in
`HAZYFLOW_TRUSTED_KEYS` (boot config, not runtime-editable — it's the root of
trust):

```
HAZYFLOW_TRUSTED_KEYS="id:tier:publisher:base64key;…"   # tier = official | verified
```

Unsigned or unknown-key artifacts install as **community**. Reserved built-in
provider ids (`google`, `slack`, `github`, `notion`) can only be claimed by a
signed official/verified manifest, so a community drop can't shadow a built-in
provider.

### Authoring & publishing

Drops are authored in TypeScript against
`engine/jsdrop/sdk/hazyflow-drop.d.ts` (integrations against
`hazyflow-integration.d.ts`); see `engine/jsdrop/sdk/examples/` for the Gmail
and Slack connectors and **[engine/jsdrop/DESIGN.md](engine/jsdrop/DESIGN.md)**
for the capability surface and runtime. To run local drops in dev, point
`HAZYFLOW_SCRIPTED_DROPS_DIR` at a directory of `.ts` files.

To publish a signed, official-tier repo, drive the `hz-drops` tool. The whole
flow is just keygen → sign → publish a git repo the daemon can fetch:

```sh
# 1. One-time: generate a signing keypair. Keep the .key secret (never commit
#    it); .keys/<id>.trustedkey holds the public HAZYFLOW_TRUSTED_KEYS entry.
go run ./cmd/hz-drops keygen --id hazy-official --publisher "Hazy Flow" \
    --tier official --out .keys

# 2. Lay out the drops and sign each → writes a detached drops/<file>.ts.sig.
mkdir -p repo/drops && cp officialdrops/{gmail_send_email,slack_send_message}.ts repo/drops/
go run ./cmd/hz-drops sign --key .keys/hazy-official.key --id hazy-official repo/drops/*.ts

# 3. Commit + tag. The daemon resolves the install ref via a go-git fetch, so
#    use a LIGHTWEIGHT tag and force gpg-signing off (a signed/annotated tag,
#    e.g. from a global commit.gpgSign, won't resolve the same way).
cd repo && git init -q && git add -A
git -c commit.gpgSign=false commit -q -m "Official drops v1.0.0"
git -c tag.gpgSign=false -c tag.forceSignAnnotated=false tag -f v1.0.0

# 4. Configure the printed key on the daemon, ';'-separated, in HAZYFLOW_TRUSTED_KEYS.
cat ../.keys/hazy-official.trustedkey
```

Point the admin marketplace (`/admin/marketplace` → Install drop) at that repo +
ref; installs then show as **official**. The private key is the authority to
mint official drops: generate it on a trusted machine and keep it in a secret
manager/HSM.

## Secrets

Out of the box, secrets are held in the **built-in encrypted store** — flows
reference them as `${secret:NAME}` (the `tenant://` provider), values are
AES-256-GCM encrypted under a per-tenant key wrapped by `HAZYFLOW_MASTER_KEY`,
and the UI is write-only (you never read a value back). That's the zero-infra
default; no external dependency. For master-key handling and rotation, see
**[SECURITY.md](SECURITY.md)**.

### Bring your own secret manager (OpenBao / Vault)

An org that already runs **OpenBao** or **HashiCorp Vault** can point the
platform at it instead, and reference its secrets in flows as
`${vault:PATH#FIELD}` (e.g. `${vault:stripe#api_key}` reads field `api_key` of
the KV-v2 secret at `stripe`). This is **additive** — it coexists with the
built-in store; orgs that don't run a manager are unaffected.

It's configured **per tenant** over the API (gated on the same secret
permissions), and the connection is stored encrypted in the tenant's own store
— never in plaintext config:

```sh
curl -X PUT https://your.domain/api/v1/secret-manager \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"address":"https://openbao.internal:8200","mount":"secret",
       "auth":{"method":"approle","role_id":"…","secret_id":"…"}}'
```

- **Auth**: `token` (a long-lived token) or `approle` (`role_id` + `secret_id`;
  the daemon logs in and caches the lease). `GET` returns a redacted view (no
  credential); the `PUT` connection-tests before saving, so a bad address or
  credential fails fast.
- Reads are cached briefly per `(tenant, path, field)`, so a flow referencing a
  secret every run doesn't round-trip the manager each time.
- The manager normally lives at a private address, so these calls deliberately
  bypass the flow-egress SSRF guard (the address is admin-configured, not
  attacker input).
- Built on OpenBao's official Go client (`github.com/openbao/openbao/api/v2`),
  which is API-compatible with HashiCorp Vault — so the same config works
  against either.
