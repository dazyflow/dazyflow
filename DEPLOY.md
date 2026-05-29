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

- `HAZYFLOW_DEV_KEY` defaults **off**; only set for local dev (mints
  an insecure bearer token at startup).
- The auth rate limiter is hardcoded at 20/min per IP with a burst of
  10 on `/api/v1/auth/{signin,signup}` (defense against credential
  stuffing) — not configurable per-deploy.
- `HAZYFLOW_HTTP_EGRESS_ALLOW` pins the `http_request` /
  `webhook_send` drops to an allowlist of hosts (`api.stripe.com`,
  `*.slack.com`, CIDRs). The IP-level SSRF guard (blocks
  private/loopback/metadata) is always on.
- `HAZYFLOW_ALLOW_PRIVATE_EGRESS` defaults **off**. The
  `http_request` / `http_download` / `http_upload` drops expose an
  `allow_private_networks` param that disables the SSRF guard; that
  param is ignored unless this is set to `1`. Leave it off on
  multi-tenant deployments, or any tenant could reach cloud metadata
  (`169.254.169.254`), `localhost`, or internal services. The same
  opt-in also governs the SSRF guard on the Postgres/MySQL `dsn` host
  and the SMTP `host` (so a flow can't point a DB or email drop at an
  internal address either).
- `HAZYFLOW_ENABLE_SHELL` defaults **off** — and should stay off on any
  multi-tenant or internet-facing deployment. It registers the `shell`
  drop, which runs arbitrary host commands as the daemon's user with
  full host filesystem and network access, bypassing the scripted-drop
  sandbox. Enabling it gives **anyone who can run a flow remote code
  execution on the host**; only turn it on for a single-tenant/CI box
  you fully control. When enabled, the command env is scrubbed of all
  `HAZYFLOW_*` variables so the master key and daemon secrets are never
  exposed to the command.
- `HAZYFLOW_ISOLATE_SHARED_SECRETS=1` in shared multi-tenant
  deployments forces `env://` lookups to be `<tenant>.<key>` so tenant
  A can't read tenant B's operator-supplied env secrets.

## Observability

- **Health probes:** HTTP `GET /healthz` (liveness) and `GET /readyz`
  (readiness — pings Postgres when `HAZYFLOW_POSTGRES_DSN` is set). For
  gRPC-only / k8s deployments, the standard `grpc.health.v1.Health`
  service is registered on the gRPC port (use `grpc_health_probe`); its
  overall status tracks the same readiness check.
- **Metrics:** `HAZYFLOW_ENABLE_METRICS=1` exposes a Prometheus
  `GET /metrics` endpoint (`hazyflow_up`, per-tenant
  `hazyflow_quota_bytes_used/_limit`, `hazyflow_jobs{status}`). Off by
  default — it reveals tenant names, so enable it only behind a
  restricted scrape network.
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
