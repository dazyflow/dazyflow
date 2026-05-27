# Deploying hzd

## TLS / reverse-proxy contract

`hzd` does **not** terminate TLS. Run it behind a TLS-terminating reverse
proxy (nginx, Caddy, Traefik, a k8s ingress) and proxy plain HTTP to the
gateway port.

The proxy MUST:
- terminate TLS and forward to `hzd`'s `--http` port over HTTP;
- set `X-Forwarded-Proto: https` on forwarded requests;
- forward the `Host` and `Origin` headers unchanged (the gateway's CSRF
  origin check + CORS allowlist depend on them);
- upgrade WebSocket/SSE connections (Vite HMR in dev, the chat + run SSE
  streams in prod).

`hzd` MUST be started with:
- `--trust-proxy-headers` — so it honors `X-Forwarded-Proto` and marks
  session cookies `Secure` + sends HSTS on forwarded-HTTPS requests.
  **Do not set this if hzd is exposed directly** (a client could spoof
  the header to flip Secure on over plain HTTP).
- `--web-origin https://your.domain` — the exact browser origin, for the
  CORS allowlist + the cookie-origin CSRF check.
- `--public-base-url https://your.domain` — used for OAuth redirect URIs
  and failure-notification deep links.

What the gateway does once `--trust-proxy-headers` is on and the request
arrives as forwarded-HTTPS:
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
        proxy_pass http://127.0.0.1:8080;     # hzd --http :8080
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

Matching daemon flags:

```sh
hzd --http :8080 --web-dist /srv/web \
    --trust-proxy-headers \
    --web-origin https://app.example.com \
    --public-base-url https://app.example.com \
    --postgres-dsn "$HAZYFLOW_POSTGRES_DSN" \
    --master-key "$HAZYFLOW_MASTER_KEY"
```

## Durability

Pass `--postgres-dsn` (or `$HAZYFLOW_POSTGRES_DSN`) so jobs, API keys,
sessions, users, and encrypted secrets persist to Postgres. Without it
those run in-memory/JSON and are lost on restart (dev only — the daemon
logs a warning). Provide a stable `--master-key` (32-byte base64); losing
it makes every stored secret undecryptable.

### Migrating an existing JSON user file to Postgres

If you ran in dev mode with a `.hazyflow-users.json` and are adopting
Postgres, import those accounts once so nobody is stranded:

```sh
hzd --postgres-dsn "$HAZYFLOW_POSTGRES_DSN" \
    --import-users-from-json ./.hazyflow-users.json
# logs "user import complete: N imported, M skipped", then exits
```

It's idempotent (accounts already in Postgres are skipped, never
overwritten), so re-running is safe.

### Backup & restore

Everything durable lives in the one Postgres database, so a standard
Postgres backup is a complete backup — **plus** the `--master-key`, which
lives outside the DB and is required to decrypt the `encrypted_secrets`
rows.

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

## Security flags worth setting

- `--dev-key` defaults **off**; only enable for local dev.
- `--auth-rate-per-min` / `--auth-rate-burst` throttle sign-in/sign-up
  per IP (defaults 20 / 10).
- `--http-egress-allow` pins the `http_request` / `webhook_send` drops to
  an allowlist of hosts (`api.stripe.com`, `*.slack.com`, CIDRs). The
  IP-level SSRF guard (blocks private/loopback/metadata) is always on.

## Observability

- **Health probes:** HTTP `GET /healthz` (liveness) and `GET /readyz`
  (readiness — pings Postgres when `--postgres-dsn` is set). For
  gRPC-only / k8s deployments, the standard `grpc.health.v1.Health`
  service is registered on the gRPC port (use `grpc_health_probe`); its
  overall status tracks the same readiness check.
- **Metrics:** `--metrics` exposes a Prometheus `GET /metrics` endpoint
  (`hazyflow_up`, per-tenant `hazyflow_quota_bytes_used/_limit`,
  `hazyflow_jobs{status}`). Off by default — it reveals tenant names, so
  enable it only behind a restricted scrape network.
- **Tracing:** set the standard `OTEL_EXPORTER_OTLP_ENDPOINT` (or
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`) and `hzd` installs an OTLP trace
  exporter so graph/node spans flow to your collector (Jaeger, Tempo,
  the OTel Collector, Honeycomb, …). Unset = no export (zero overhead).
  All standard `OTEL_EXPORTER_OTLP_*` env vars (headers, TLS, timeout)
  are honored.

## Human approvals (await_approval)

Flows can pause on an `await_approval` node until a human resumes them.
Two paths:

- **Authenticated (inbox UI):** `POST /api/v1/approvals/{run}/{node}` on the
  gateway — always available, uses the caller's API-key/session.
- **Unauthenticated link (email/Slack):** opt in with `--approval-listen
  :8090` + `--approval-hmac-secret <base64≥16B>` + `--public-base-url`.
  The engine then mints signed `<base>/approve/<run>/<node>?token=…` URLs
  and the listener verifies the HMAC before resuming. Use the **same
  secret on every node** (a token minted by one node must verify on
  another). Put the listener behind your TLS ingress like the gateway.
