# Deploying dzd

Every knob below is a `DAZYFLOW_*` environment variable. `dzd` itself
only has two flags, both one-shot operator commands that exit after
running (`--rotate-master-key`, `--import-users-from-json`). For the
canonical list see `.env.example`.

## TLS / reverse-proxy contract

`dzd` does **not** terminate TLS. Run it behind a TLS-terminating reverse
proxy (nginx, Caddy, Traefik, a k8s ingress) and proxy plain HTTP to the
gateway port.

The proxy MUST:
- terminate TLS and forward to `dzd`'s HTTP port (`DAZYFLOW_HTTP`) over
  HTTP;
- set `X-Forwarded-Proto: https` on forwarded requests;
- forward the `Host` and `Origin` headers unchanged (the gateway's CSRF
  origin check + CORS allowlist depend on them);
- upgrade WebSocket/SSE connections (Vite HMR in dev, the chat + run SSE
  streams in prod).

`dzd` MUST be configured with:
- `DAZYFLOW_TRUST_PROXY_HEADERS=1` — so it honors `X-Forwarded-Proto` and
  marks session cookies `Secure` + sends HSTS on forwarded-HTTPS
  requests. **Do not set this if dzd is exposed directly** (a client
  could spoof the header to flip Secure on over plain HTTP).
- `DAZYFLOW_WEB_ORIGIN=https://your.domain` — the exact browser origin,
  for the CORS allowlist + the cookie-origin CSRF check.
- `DAZYFLOW_PUBLIC_BASE_URL=https://your.domain` — used for OAuth
  redirect URIs and failure-notification deep links.

What the gateway does once `DAZYFLOW_TRUST_PROXY_HEADERS=1` is on and the
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
        proxy_pass http://127.0.0.1:8080;     # dzd listens on DAZYFLOW_HTTP=:8080
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
DAZYFLOW_HTTP=:8080
DAZYFLOW_WEB_DIST=/srv/web
DAZYFLOW_TRUST_PROXY_HEADERS=1
DAZYFLOW_WEB_ORIGIN=https://app.example.com
DAZYFLOW_PUBLIC_BASE_URL=https://app.example.com
DAZYFLOW_POSTGRES_DSN=postgres://dazyflow:…@db/dazyflow?sslmode=require
DAZYFLOW_MASTER_KEY=<32-byte base64; openssl rand -base64 32>
```

Container deployments don't have to set `DAZYFLOW_HTTP` /
`DAZYFLOW_WEB_DIST` — the supplied Dockerfile bakes those in via
`ENV` (see `Dockerfile`).

## Kubernetes — single pod (simplest start)

`deploy/k8s/dazyflow.yaml` is a one-replica Deployment + a PersistentVolumeClaim
+ a Service + a Secret template — no load balancer, no ingress. You reach the
UI through `kubectl port-forward`. `deploy/k8s/kustomization.yaml` ties it
together so the whole thing applies with `kubectl apply -k deploy/k8s/`.
Everything is configured by `DAZYFLOW_*` env vars on the container — there
are no daemon flags to set.

1. **Postgres** — create a managed Postgres (on DO: a DigitalOcean Managed
   PostgreSQL database; it's external to the cluster). DO requires SSL, so the
   DSN ends in `?sslmode=require`. Add the cluster (or its VPC) to the
   database's trusted sources. No in-cluster DB and no migration Job — `dzd`
   applies its schema on boot.
2. **Secret** — edit `dazyflow-secrets`: a fresh `DAZYFLOW_MASTER_KEY`
   (`openssl rand -base64 32`, keep a sealed off-cluster backup) and the
   `DAZYFLOW_POSTGRES_DSN` from step 1. (For where secrets come from on DO —
   which has no managed secret store — see "Getting secrets into the cluster"
   below.)
3. **Image** — build and push from the repo Dockerfile. On DO: push to
   DigitalOcean Container Registry (`doctl registry login`, then
   `docker push registry.digitalocean.com/<you>/dzd:<tag>`) and integrate it
   with the cluster (`doctl kubernetes cluster registry add <cluster>`) so
   pulls are authenticated without a manual `imagePullSecret`. Set the image
   in the Deployment (or via the `images:` stub in `kustomization.yaml`).
4. **Apply** — `kubectl apply -k deploy/k8s/`.
5. **Reach it** — `kubectl port-forward deploy/dazyflow 8080:8080`, then open
   <http://localhost:8080>. `DAZYFLOW_WEB_ORIGIN` / `DAZYFLOW_PUBLIC_BASE_URL`
   are preset to `http://localhost:8080` to match.

Why a PVC and not `emptyDir`: **flow graphs are stored as git repos on disk
under `/data`, not in Postgres.** An `emptyDir` would lose every flow on a
pod restart. A `ReadWriteOnce` PVC is correct for a single pod (DOKS'
`do-block-storage` is RWO) and persists across restarts. Because the volume
is RWO, the Deployment uses the `Recreate` strategy — a replacement pod can't
attach the volume while the old one holds it, so the old pod is torn down
first (a few seconds' downtime per rollout). Postgres still holds the rest of
the durable state (jobs, users, sessions, encrypted secrets); back up both
the database **and** the `/data` PVC (or `git push` your workspaces
elsewhere). See "Backup & restore".

Probes: liveness `/healthz`, readiness `/readyz` (marks the pod NotReady when
Postgres is unreachable). The `grpc.health.v1.Health` service on :50050 is
also registered if you prefer a `grpc_health_probe` sidecar.

The pod runs as the image's unprivileged `dazyflow` user (uid 1000, which
owns `/data`) with a hardened security context: `runAsNonRoot`,
`readOnlyRootFilesystem` (only the `/data` PVC and a `/tmp` emptyDir are
writable), all capabilities dropped, no privilege escalation, and the
`RuntimeDefault` seccomp profile. `terminationGracePeriodSeconds: 35` sits
above `DAZYFLOW_SHUTDOWN_GRACE` (25s) so a rollout drains in-flight nodes
rather than killing them mid-write.

`DAZYFLOW_TRUST_PROXY_HEADERS` is intentionally **unset**: the pod is reached
directly via port-forward with no TLS-terminating proxy, and setting it there
would let a client spoof `X-Forwarded-Proto`. Turn it on only once a real
TLS proxy sits in front (next section).

### Getting secrets into the cluster

DigitalOcean has **no managed secret manager** (and no KMS) for DOKS — App
Platform's encrypted env vars don't extend to Kubernetes. The inline
`Secret` in `dazyflow.yaml` is a convenience for a first apply; don't commit
real values in it. Options, simplest first:

- **`kubectl` out of band** — apply the Secret by hand (or `kubectl create
  secret`) and keep it out of git. DO encrypts etcd at rest on the managed
  control plane. Fine for a single-pod start.
- **Sealed Secrets** — encrypt locally with `kubeseal`, commit the
  `SealedSecret`, an in-cluster controller decrypts it. No external service;
  the GitOps-friendly default. The `secretGenerator` stub in
  `kustomization.yaml` is the on-ramp.
- **SOPS + age**, or **External Secrets Operator** pointed at Vault/OpenBao,
  Doppler, Infisical, 1Password, etc. — heavier; reach for these when you
  already run one of those stores.

Either way the `DAZYFLOW_MASTER_KEY` needs a sealed backup *outside* whichever
mechanism you choose — losing it makes every stored flow secret undecryptable.

## Scaling up — load balancer, TLS, multi-replica

When one pod isn't enough, the app scales out without code changes: the
Postgres event bus lets any pod stream a run's events (`PgBus`) and a Postgres
advisory-lock leader ensures only one pod fires each schedule (`PgLeader`).
The steps:

1. **Front it with an ingress + TLS.** `deploy/k8s/ingress.yaml` is an
   ingress-nginx + cert-manager setup (WebSocket/SSE-friendly timeouts,
   `force-ssl-redirect`, and a snippet that 403s `/metrics` at the edge). On
   DOKS: install ingress-nginx (DO 1-Click "NGINX Ingress Controller", or Helm
   into the `ingress-nginx` namespace) — it provisions a DO Load Balancer
   automatically; point a DNS A record at the LB IP. Install cert-manager and
   create a `letsencrypt-prod` ClusterIssuer (stub at the bottom of
   `ingress.yaml`). Edit `app.example.com` in `ingress.yaml`, set
   `DAZYFLOW_WEB_ORIGIN` / `DAZYFLOW_PUBLIC_BASE_URL` to the same https origin,
   add `DAZYFLOW_TRUST_PROXY_HEADERS=1`, uncomment `ingress.yaml` in
   `kustomization.yaml`, and re-apply.
2. **Raise `replicas`** on the Deployment. Switch the `/data` volume from the
   RWO PVC to a `ReadWriteMany` volume (block storage can't be shared across
   pods — on DO that means storing workspace/`file_write` artifacts in object
   storage / DO Spaces instead) and drop the `Recreate` strategy back to a
   rolling update. Set the same `DAZYFLOW_APPROVAL_HMAC_SECRET` on every pod
   if you use unauthenticated approval links.
3. **Add the operational resources** as needed: a `PodDisruptionBudget`
   (`minAvailable: 1`), a CPU `HorizontalPodAutoscaler` (DOKS ships
   metrics-server; for backlog-aware scaling use the Prometheus metric
   `dazyflow_jobs_oldest_queued_seconds` via prometheus-adapter), and a
   default-deny `NetworkPolicy` (DOKS enforces it via Cilium). Note a
   `PodDisruptionBudget` is *not* useful at one replica — `minAvailable: 1`
   there blocks node drains entirely.

`/metrics` is off by default. If you enable it (`DAZYFLOW_ENABLE_METRICS=1`)
note it shares port 8080 with the API, so it can't be isolated by
NetworkPolicy — block it at the ingress (as `ingress.yaml` does) and let
Prometheus scrape the in-cluster Service directly.

## Per-org subdomains (optional)

Set `DAZYFLOW_WILDCARD_DOMAIN=<apex>` (e.g. `dazyflow.app`) to give each
org its own subdomain. An org owner claims a label under **Admin →
Workspace → Custom web address** (stored on the org profile, unique,
validated as a DNS label, reserved names blocked). A visit to
`acme.dazyflow.app` then resolves that label back to the org and lands on
the sign-in page with it preselected, so that org's "Sign in with Google"
button shows without a `?org=` query param. Leave the env var empty and
the daemon behaves exactly as a single-host deploy (and the UI hides the
field).

What it changes when set:

- **CORS + CSRF** accept any `*.<apex>` subdomain as a browser origin, in
  addition to the exact `DAZYFLOW_WEB_ORIGIN` entries. The apex itself is
  not implied — list it in `DAZYFLOW_WEB_ORIGIN` as usual. The match is a
  strict subdomain suffix, so `evil-dazyflow.app` does not match
  `dazyflow.app`.
- **Session cookies stay host-only** (no parent-domain cookie). Each
  org's session is scoped to its own subdomain, so one org's subdomain
  can never read another's cookie.
- **Google/OAuth sign-in** still uses a single redirect URI on the apex
  (`DAZYFLOW_PUBLIC_BASE_URL`), so you register **one** redirect URI with
  the provider regardless of how many org subdomains exist. The apex
  callback issues the session, then 302s the browser to
  `<subdomain>/api/v1/auth/handoff?ot=…` with a single-use, short-lived
  (2 min) token; the subdomain exchanges it for a host-only session
  cookie. Password sign-in needs no handoff — it already happens on the
  subdomain origin.

Infrastructure prerequisites:

- A wildcard DNS record `*.<apex>` pointing at the proxy. (One-time, at the
  registrar — every org subdomain rides this; nothing per-org to create.)
- `DAZYFLOW_PUBLIC_BASE_URL` set to the apex (`https://<apex>`).
- The proxy must route every `*.<apex>` host to the same dzd upstream
  (the sign-in handoff state is held in-process).

**TLS — on-demand, no wildcard cert needed.** The bundled `Caddyfile`
serves the apex with a normal managed (HTTP-01) certificate and serves
`*.<apex>` with **on-demand TLS**: Caddy mints an ordinary HTTP-01 cert the
first time each org host is requested, gated by an authorization endpoint
(`on_demand_tls { ask … }` → `GET /api/v1/auth/tls-allow?domain=<host>`).
That endpoint returns 2xx only for subdomains an org has actually claimed,
so a stranger pointing arbitrary hosts at the IP can't make Caddy burn
Let's Encrypt rate limits. This avoids a wildcard certificate (which would
need a DNS-01 challenge and a DNS-provider plugin baked into a custom Caddy
image). Ports 80/443 must be reachable (the DO firewall already allows them).

Org slugs become DNS labels: only single-label, DNS-valid slugs resolve
to an org, and reserved labels (`www`, `api`, `app`, `admin`, `auth`,
`docs`, `status`, …) never map to one, so those hosts can serve
infrastructure or marketing without colliding with a tenant. Claiming a
subdomain is self-serve in the UI; the only ops step is the one-time
wildcard DNS record above.

## Durability

`DAZYFLOW_POSTGRES_DSN` is **required** — `dzd` runs on Postgres and
refuses to start without it. Jobs, API keys, sessions, users, encrypted
secrets, memberships, invitations, per-org SSO config, and per-org
profiles all persist to Postgres. (Graph workspaces and execution
sandboxes are git/filesystem-backed under `DAZYFLOW_DATA_DIR`.) Provide a
stable `DAZYFLOW_MASTER_KEY` (32-byte base64); losing it makes every
stored secret undecryptable.

### First admin (bootstrap)

A fresh instance has no users, and signup is invite-only by default
(`DAZYFLOW_ENABLE_SIGNUP` off) — a chicken-and-egg, since there's no admin
to send the first invite. Resolve it with the platform-admin allowlist:

```sh
DAZYFLOW_PLATFORM_ADMINS=you@example.com   # comma-separated for several
```

Emails in this allowlist may sign up via `POST /api/v1/auth/signup` (the
web UI's sign-up form) **even while `DAZYFLOW_ENABLE_SIGNUP` is off** — so
you don't have to open self-serve signup to the world just to create your
own account. On sign-up (and every later sign-in) the listed email is
granted `platform:admin`. The bypass is self-limiting: once the account
exists, a second signup for that email is rejected as a duplicate, and
signup stays closed for everyone else. From there you invite the rest of
your team as the platform admin.

(With Google SSO configured, the first sign-in by a listed email
auto-provisions the account and elevates it the same way — no signup
toggle involved at all.)

### Fail-closed config guard

`dzd` **refuses to start** if it would run with a bundled insecure
default — a missing `DAZYFLOW_POSTGRES_DSN`, a DSN still using the shipped
default DB password, a DSN that does not enforce TLS (`sslmode` is
anything other than `require`/`verify-ca`/`verify-full`), or an empty
`DAZYFLOW_MASTER_KEY`. The boot log prints a `FATAL` line naming each
offending value. Fix them (set a strong `POSTGRES_PASSWORD`, a TLS
`sslmode`, and a real master key) and restart.

The TLS check has no loopback exemption: even when Postgres is a sibling
container on the same host, the DSN must enforce TLS — the DB link carries
personal data and the rows holding wrapped DEKs, and the guard can't tell
on-host from remote. If you use the bundled `postgres` service, see
[Postgres TLS for the bundled service](#postgres-tls-for-the-bundled-service)
below; for a managed/remote DB the provider already terminates TLS and you
just append `?sslmode=require` to the DSN.

`DAZYFLOW_DEV=1` downgrades the guard from fatal to warnings so the
bundled defaults boot for a local trial. **Never set it in production** —
it exists only so `docker compose up -d` works for a throwaway smoke
test.

### Postgres TLS for the bundled service

The bundled `postgres:16-alpine` ships with **SSL off**, so the fail-closed
guard above will reject the default `sslmode=disable` DSN and `dzd` will
restart-loop (nginx then returns `502`). This is a different TLS hop from
your reverse proxy: the proxy terminates browser↔`dzd` TLS, while this guard
is about the `dzd`↔Postgres link. The proxy can't satisfy it — you have to
give Postgres its own cert.

A **self-signed cert is enough**: `sslmode=require` demands an encrypted
channel but does not verify the certificate, so no CA is needed for a
same-host sibling. (Use `verify-full` with a CA only if you want to pin a
remote DB's identity.)

1. Generate the cert. `postgres:16-alpine` runs as uid 70 and refuses a
   group/world-readable key, so set ownership and mode explicitly:

   ```sh
   cd <your compose dir>
   mkdir -p certs
   openssl req -new -x509 -days 3650 -nodes -subj /CN=postgres \
       -out certs/server.crt -keyout certs/server.key
   chown 70:70 certs/server.crt certs/server.key
   chmod 600 certs/server.key
   chmod 644 certs/server.crt
   ```

2. Serve TLS from the `postgres` service (already wired in the shipped
   `docker-compose.yml`): a `command:` enabling `ssl=on` with the cert/key
   paths, and a read-only `./certs:/certs:ro` mount.

   ```yaml
   postgres:
     image: postgres:16-alpine
     command:
       - -c
       - ssl=on
       - -c
       - ssl_cert_file=/certs/server.crt
       - -c
       - ssl_key_file=/certs/server.key
     volumes:
       - pgdata:/var/lib/postgresql/data
       - ./certs:/certs:ro
   ```

3. Point the DSN at TLS — change `sslmode=disable` to `sslmode=require` in
   `DAZYFLOW_POSTGRES_DSN` (in `.env`), then recreate:

   ```sh
   docker compose up -d
   docker compose logs -f dzd   # the FATAL TLS line should be gone
   docker compose ps            # dzd "Up (healthy)", not Restarting
   ```

`ssl=on` only *offers* TLS; it still accepts plaintext clients, so this
change is backward-compatible with a `DAZYFLOW_DEV=1` local stack that
connects with `sslmode=disable`.

### Migrating an existing JSON user file to Postgres

If you ran in dev mode with users in a JSON file (legacy:
`./.dazyflow-users.json`; current layout: `<data>/state/users.json`)
and are adopting Postgres, import those accounts once so nobody is
stranded. This is one of the only two flags `dzd` still accepts — a
one-shot command that exits after running:

```sh
DAZYFLOW_POSTGRES_DSN="$DSN" \
    dzd --import-users-from-json ./.dazyflow/state/users.json
# logs "user import complete: N imported, M skipped", then exits
```

It's idempotent (accounts already in Postgres are skipped, never
overwritten), so re-running is safe.

### Backup & restore

Most durable state lives in the one Postgres database, so a standard
Postgres backup covers jobs, users, sessions, encrypted secrets,
memberships, and the rest of the control plane. Two things live **outside**
the DB and must be backed up alongside it:

- the **`DAZYFLOW_MASTER_KEY`**, required to decrypt the `encrypted_secrets`
  rows (a DB backup without it is undecryptable);
- the **`DAZYFLOW_DATA_DIR` (`/data`) volume** — your flow graphs are stored
  as git repos there, not in Postgres, so a DB-only backup does not capture
  them. Back up the PVC (snapshot it, or `git push` each workspace to a
  remote).

- **Logical backup (simplest):**
  `pg_dump "$DAZYFLOW_POSTGRES_DSN" | gzip > dazyflow-$(date +%F).sql.gz`.
  Restore into a fresh DB with `gunzip -c … | psql "$DSN"`.
- **Point-in-time recovery:** for larger deployments use base backups +
  WAL archiving (`pg_basebackup` / your managed provider's PITR). The app
  needs no special handling — it re-applies its `CREATE TABLE IF NOT
  EXISTS` schema on boot, so restoring the data is sufficient.
- **Back up the master key separately** (the break-glass copy from the
  "If it's lost" section of `SECURITY.md`). A DB backup without the key
  leaves every tenant secret undecryptable.
- **What's safe to lose under `/data`:** the `bus_events` spool is ephemeral
  (auto-swept, ~1h retention) and the per-tenant sandbox/scratch dirs are
  derived working data. But the workspace git repos (your flow graphs) and —
  if your flows treat them as durable — `file_write` outputs are **not**
  derived; they're only on disk. That's why the whole `/data` volume is on
  the back-up list above.

## Security knobs worth setting

Every variable is documented in full — semantics, default, and warnings —
in `.env.example`. This is the security-critical subset to review before
you expose the daemon; see `.env.example` for the detail.

- The auth rate limiter is fixed at **20/min per IP (burst 10)** on
  `/api/v1/auth/{signin,signup}` — not a knob, but worth knowing it's there.
- `DAZYFLOW_DEV_KEY` / `DAZYFLOW_DEV` — dev-only; never set in production.
- `DAZYFLOW_HTTP_EGRESS_ALLOW` — allowlist the hosts outbound HTTP drops may
  reach (the IP-level SSRF guard blocks private/loopback/metadata regardless).
- `DAZYFLOW_ALLOW_PRIVATE_EGRESS` — keep **off** on multi-tenant deploys; on,
  it lets flows reach private/loopback/cloud-metadata addresses (and the
  DB/SMTP drop hosts).
- `DAZYFLOW_ENABLE_SHELL` — **off** by default; on, it's host RCE for anyone
  who can run a flow. Single-tenant / CI box only. The toggle is **fail-closed**:
  only `1`/`true`/`yes`/`on` enable it — any other value (including `disabled`
  or a typo) leaves it off, so you can't arm RCE by accident.
  - **Env exposure:** an enabled shell command inherits the daemon's
    environment with only `DAZYFLOW_*` removed. The app's own secrets (master
    key, DSN, signing keys) are withheld, but **third-party** secrets in the
    daemon env (`AWS_*`, `GOOGLE_APPLICATION_CREDENTIALS`, generic API keys)
    are NOT — they pass through to the command. If the daemon holds such
    secrets, set `DAZYFLOW_SHELL_ENV_ALLOW` to a comma-separated allowlist of
    variable names (e.g. `GOPATH,CARGO_HOME`); the command then sees only
    those plus `PATH`/`HOME`, and everything else is withheld.
  - **Cleanup:** on timeout the daemon kills the command's whole process
    group (SIGKILL), so backgrounded/daemonized children are reaped rather
    than orphaned onto the host. There is still no CPU/memory/PID cgroup cap —
    a runaway command can consume host resources until its timeout; size the
    box and `timeout_ms` accordingly.
- `DAZYFLOW_AUDIT_SECRET_READS` — **off** by default; on, every successful
  secret resolution emits a `secret.read` audit event (secret name + actor,
  never the value). High-volume (resolution runs on every node execution) —
  enable only when a compliance regime requires a read trail.
- Secrets are referenced as `${secret.NAME}` and live in the per-tenant
  encrypted store, set via the API / UI.

## Observability

- **Health probes:** HTTP `GET /healthz` (liveness) and `GET /readyz`
  (readiness — pings Postgres when `DAZYFLOW_POSTGRES_DSN` is set). For
  gRPC-only / k8s deployments, the standard `grpc.health.v1.Health`
  service is registered on the gRPC port (use `grpc_health_probe`); its
  overall status tracks the same readiness check.
- **Metrics:** `DAZYFLOW_ENABLE_METRICS=1` exposes a Prometheus
  `GET /metrics` endpoint. Off by default — it reveals tenant names, so
  enable it only behind a restricted scrape network. Series exposed:
  - `dazyflow_up` — liveness.
  - `dazyflow_jobs{status}` — node-job counts by status. `queued` is
    queue depth, `running` is in-flight work.
  - `dazyflow_jobs_oldest_queued_seconds` — age of the oldest claimable
    node job. **The leading indicator for execution backlog**: when this
    climbs, workers can't keep up. Raise `DAZYFLOW_WORKER_COUNT` or add
    replicas before users feel the lag.
  - `dazyflow_pg_pool_connections{state}` (`acquired`/`idle`/`total`),
    `dazyflow_pg_pool_max_connections`, and
    `dazyflow_pg_pool_empty_acquires_total`. **The earliest warning of
    pool exhaustion**: a rising `empty_acquires_total` (or `acquired`
    pinned at `max`) means requests are waiting for a connection. Raise
    `DAZYFLOW_PG_MAX_CONNS` or scale out.
  - `dazyflow_session_cache_hits_total` / `_misses_total` — auth-lookup
    cache effectiveness. A healthy hit ratio means the per-request
    session lookup isn't hammering Postgres; the miss rate tracks raw
    authenticated-request load.
  - `dazyflow_quota_bytes_used/_limit{tenant}` — per-tenant disk usage.
  - `dazyflow_http_requests_total{method,code}` and
    `dazyflow_http_request_duration_seconds{method}` (histogram) — HTTP
    RED. Rate is the counter's increase; error ratio is the share of
    `code` >= 500 (or >= 400); latency percentiles come from the
    histogram (`histogram_quantile(0.99, ...)`). The front-door health
    signal.
  - `dazyflow_node_duration_seconds{status}` (histogram) — per-node
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
  row counts against the `DAZYFLOW_JOB_RETENTION` /
  `DAZYFLOW_AUDIT_RETENTION` windows to confirm the retention sweeps keep
  up. The histograms use fixed buckets (5ms to 60s); per-route HTTP
  labels and per-module node labels are intentionally omitted to keep
  cardinality bounded — say so if you want either dimension added.
- **Tracing:** set the standard `OTEL_EXPORTER_OTLP_ENDPOINT` (or
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`) and `dzd` installs an OTLP trace
  exporter so graph/node spans flow to your collector (Jaeger, Tempo,
  the OTel Collector, Honeycomb, …). Unset = no export (zero overhead).
  All standard `OTEL_EXPORTER_OTLP_*` env vars (headers, TLS, timeout)
  are honored.

## Graceful shutdown

On `SIGTERM`/`SIGINT`, `dzd` drains: it stops the gRPC server gracefully,
stops claiming new jobs, and lets any in-flight node finish and finalize
its run before exiting. The wait is bounded by `DAZYFLOW_SHUTDOWN_GRACE`
(default `25s`); if it elapses with work still running, `dzd` exits
anyway and the unfinished node's lease expires so another instance
reclaims it. Set `DAZYFLOW_SHUTDOWN_GRACE` below your orchestrator's
termination grace (e.g. keep it under k8s
`terminationGracePeriodSeconds`, default 30s) so the process drains
rather than being `SIGKILL`ed mid-write.

A separate reaper sweep (every `DAZYFLOW_REAP_INTERVAL`, default `1m`,
plus once at startup) recovers any graph run left marked `running` by a
hard crash whose nodes have all reached a terminal state — so a `SIGKILL`
or power loss can't strand a run forever.

## Human approvals (await_approval)

Flows can pause on an `await_approval` node until a human resumes them.
Two paths:

- **Authenticated (inbox UI):** `POST /api/v1/approvals/{run}/{node}` on the
  gateway — always available, uses the caller's API-key/session.
- **Unauthenticated link (email/Slack):** opt in by setting
  `DAZYFLOW_APPROVAL_HMAC_SECRET=<base64≥16B>` (plus
  `DAZYFLOW_PUBLIC_BASE_URL` so links resolve to a real origin). The
  engine then mints signed `<base>/approve/<run>/<node>?token=…` URLs
  and the main HTTP gateway routes them through HMAC verification
  before resuming — no separate listener to expose. Use the **same
  secret on every node** in a multi-node deployment so a token minted
  by one verifies on another.

## Drops

Every node you drop on the canvas — triggers, transforms, and the connectors
(Gmail, Slack, Sheets, Notion, GitHub, Claude, Excel, ntfy, webhooks) — is a
native Go drop compiled into `dzd`. There is no plugin/marketplace install
step and no separate runtime: the catalog is fixed at build time. Connectors
that need credentials use the OAuth providers configured under **Admin →
Connector apps** (`/admin/oauth`) or a `${secret.…}` token.

## Secrets

Out of the box, secrets are held in the **built-in encrypted store** — flows
reference them as `${secret.NAME}` (the `tenant://` provider), values are
AES-256-GCM encrypted under a per-tenant key wrapped by `DAZYFLOW_MASTER_KEY`,
and the UI is write-only (you never read a value back). That's the zero-infra
default; no external dependency. For master-key handling and rotation, see
**[SECURITY.md](SECURITY.md)**.

### Bring your own secret manager (OpenBao / Vault)

An org that already runs **OpenBao** or **HashiCorp Vault** can point the
platform at it instead, and reference its secrets in flows as
`${vault.PATH#FIELD}` (e.g. `${vault.stripe#api_key}` reads field `api_key` of
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
