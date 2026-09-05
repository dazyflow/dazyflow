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
5. **Reach it** — `kubectl port-forward deploy/dazyflow 8642:8080`, then open
   <http://localhost:8642>. `DAZYFLOW_WEB_ORIGIN` / `DAZYFLOW_PUBLIC_BASE_URL`
   are preset to `http://localhost:8642` to match. The pod still listens on
   8080; 8642 is only the local side of the forward.

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
Postgres event bus lets any pod stream a run's events (`PgBus`), a Postgres
advisory-lock leader ensures only one pod fires each schedule (`PgLeader`), the
scheduler reads what to fire from a `flow_schedules` table rather than from
local disk (so a schedule authored on one pod is enrolled on the leader), and
a shared `write_dedupe` table (`PgWriteDedupeStore`) means a job reclaimed from a
dead pod's expired lease won't re-fire a non-idempotent external write (Twilio
SMS, Gmail/Discord/Sheets/Home Assistant) the original pod already sent. That
last guarantee is **at-least-once, not exactly-once**: the dedupe record is
written only *after* the send succeeds, so a pod that crashes in the narrow
window between the API returning and the record committing can still send twice.
This is deliberate — recording before the send would instead risk silently
dropping a message that never went out, the worse failure for these connectors.
No configuration is needed; the table is created on boot and `dzd` refuses to
start if it can't be (same as every other Postgres-backed store).

> **Session affinity is not required.** Four pieces of short-lived auth state
> are minted on one request and consumed on a *second* one moments later — the
> Google sign-in state, the connector OAuth pending authorization, the
> per-org-subdomain sign-in handoff, and the TOTP challenge. They used to live
> in the minting pod's memory, so a second request landing elsewhere found
> nothing and the user saw "invalid or expired state" (or a failed 2FA prompt)
> at random; sticky sessions were the workaround.
>
> They are in Postgres now (`auth_ephemeral`), so any pod can serve either leg.
> If you already enabled cookie affinity on the ingress you can drop it — it
> does no harm, it just no longer does anything.

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
2. **Switch `/data` to `ReadWriteMany`.** Block storage can't be shared across
   pods — on DO that means storing workspace/`file_write` artifacts in object
   storage / DO Spaces, or using an RWX StorageClass — then patch the PVC's
   `accessModes` to `[ReadWriteMany]`. Set the same
   `DAZYFLOW_APPROVAL_HMAC_SECRET` on every pod if you use unauthenticated
   approval links.

   > **Set `DAZYFLOW_GRAPH_STORE=postgres` before running more than one pod.**
   > A git workspace is serialized only *within* a process: two pods committing
   > to one org's workspace share a single `.git/index`, and concurrent writes
   > corrupt the repository. Runs, schedules, secrets and events are unaffected
   > — this is specifically flow authoring. See
   > [Flow storage](#flow-storage-git-or-postgres) for the switch and the
   > one-command migration.
3. **Add the scale-up resources.** `deploy/k8s/scale-up.yaml` ships a
   `PodDisruptionBudget` (`minAvailable: 1`), a CPU `HorizontalPodAutoscaler`
   (`minReplicas: 2`; DOKS ships metrics-server — for backlog-aware scaling
   swap in the Prometheus metric `dazyflow_jobs_oldest_queued_seconds` via
   prometheus-adapter), and a default-deny `NetworkPolicy` (DOKS enforces it
   via Cilium). Uncomment `- scale-up.yaml` **and** the multi-replica `patches:`
   block in `kustomization.yaml` — the patch drops the static `replicas` (the
   HPA owns it) and flips `Recreate` to `RollingUpdate`. The
   `PodDisruptionBudget` is *not* useful at one replica — `minAvailable: 1`
   there blocks node drains entirely — which is why it lives in this overlay,
   not the single-pod base. The `NetworkPolicy` is the one piece worth adopting
   even on a single pod.

### Flow storage: git or Postgres

Flows and their revision history live in one of two places, chosen by
`DAZYFLOW_GRAPH_STORE`:

| | `git` (default) | `postgres` |
|---|---|---|
| Where | one repo per workspace under `DAZYFLOW_DATA_DIR` | rows every replica shares |
| History, diff, rollback, labels | yes | yes |
| Safe to write from >1 pod | **no** | **yes** |
| Needs `ReadWriteMany` `/data` | yes | no |
| Git mirroring to your own remote | yes | yes (synthesized) |

Both keep the same revision history and the same semantics — the same
conformance suite runs against both backends. What differs is the trade at the
bottom two rows: git gives the org a repository it owns and can clone, and
that repository is exactly what cannot be written from two processes at once.

#### Moving an existing install

**Deploying a new version migrates nothing.** `DAZYFLOW_GRAPH_STORE` defaults to
`git`, so an upgrade leaves flow storage exactly where it was. The move is
deliberate, in this order:

```sh
# 1. Back up Postgres and $DAZYFLOW_DATA_DIR/workspace.
# 2. Copy the flows across. Read-only on git; re-runnable; safe to run while
#    the old version is still serving.
dzd --migrate-workspaces-to-postgres

# 3. Check that everything came across, before trusting it.
dzd --verify-workspace-migration          # exits non-zero on any difference

# 4. Switch and restart.
DAZYFLOW_GRAPH_STORE=postgres
```

The migration copies every flow's full history, labels and published pointers,
and **keeps the revision ids** — so a published pointer, a bookmarked revision
or a rollback someone is about to do still resolves afterwards.

`--verify-workspace-migration` compares the two stores flow by flow: current
content, published pointer, every revision's id, content and label. It reports
every difference rather than the first, and exits non-zero if any exist. A
failure means the git workspaces are still the authoritative copy. Re-running
the migration repairs a partial one.

#### Can the git directory be deleted afterwards?

**Archive it, don't delete it** — even on a clean verification. Two things live
only there:

- **Flows deleted before the migration.** The migration copies flows that still
  exist; a flow someone removed last month is not in that set, and its history
  is only in git. Restoring it later means going back to that directory.
- **History past 10,000 revisions per flow**, if the migration reported any
  flow as truncated. It names them in the log.

Erasing an org (GDPR Art. 17) removes its synthesized mirror as well as its
rows. The replica handling the erasure clears its own copy immediately, and
every other replica clears its copy on an hourly sweep — so a pod that was down
at the time still converges.

And delete only `$DAZYFLOW_DATA_DIR/workspace`. The rest of that directory is
live state the flow store has nothing to do with: `sandbox/` holds every org's
files, `file_write` output and `git_checkout` caches, and `mirrorcache/` is the
synthesized mirror. Removing the data directory wholesale would take those with
it.

**Mirroring in postgres mode** works by synthesizing a repository from the
revision log — every revision becomes a commit carrying the author, message and
timestamp the row records — and pushing that. Synthesis is deterministic, so
every replica derives the same commit hashes from the same rows: a pod that has
never mirrored this workspace rebuilds and its push still fast-forwards onto
what the previous one left, rather than force-pushing over your history.

The repository lives under `$DAZYFLOW_DATA_DIR/mirrorcache` on whichever pod
pushes. It is a **cache**, not state you have to protect: deleting it, or losing
the pod entirely, costs one rebuild at roughly 390 revisions/second and nothing
else. Steady-state pushes append at ~2.5 ms per revision and do not slow down as
history grows. Budget about 1 MB per 1,000 revisions, and only for workspaces
that have a mirror configured.

> **One-time divergence when you migrate.** Revision *ids* carry across the
> migration, but a synthesized repository computes different commit hashes than
> the original git workspace did — the tree encoding and committer metadata are
> not the ones git used. So the first push after migrating diverges from an
> existing mirror. Either point the mirror at a fresh remote, or allow one
> force-push ("Overwrite unrelated history" in Admin → Git sync) to restart it.
> This affects only installs that both migrate and mirror, once.

### Sizing execution

`DAZYFLOW_WORKER_COUNT` (default 8) is the per-process ceiling on concurrent
steps: each worker claims and runs one step at a time, and a step doing I/O
holds its worker for the whole call. Measured against a real Postgres queue with
50 ms steps, on one machine:

| Workers | Steps/sec | Efficiency vs. `W / 50ms` |
|---:|---:|---:|
| 2 | 28 | 71% |
| 8 | 120 | 75% |
| 16 | 220 | 69% |
| 32 | 337 | 53% |
| 64 | 398 | 31% |

Near-linear to ~16, knee around 32, and flat past that on queue round-trips
rather than on the worker count. Add replicas beyond the knee rather than more
workers per process.

`DAZYFLOW_PG_MAX_CONNS` tracks the worker count when left unset —
`max(20, workers + 12)` — because the two cannot be chosen independently. What a
starved pool costs is not throughput (workers are I/O-bound; 8 vs 64 connections
at 32 workers differed by ~12%) but **latency on everything else sharing the
pool**, the HTTP request path most visibly. At 32 workers, a 20-connection pool
logged 4,319 waits for a free connection where 32 connections logged 51. Watch
`dazyflow_pg_pool_empty_acquires_total`.

> **More workers is not always the fix.** Outbound connector calls are paced per
> (org, host) by the egress limiter — **5 calls/sec and 8 in flight by default**,
> active unless you change `DAZYFLOW_EGRESS_*`. A worker waiting on that budget
> is a worker held, so one org fanning out to a single API can occupy every
> worker in a process. Raising the worker count does nothing for a single org
> hammering one host. The diagnostic that separates the two cases is
> `dazyflow_jobs_oldest_queued_seconds`: climbing means steps are waiting for a
> worker; flat while flows still feel slow means the egress budget, not the
> workers.

**Fairness between orgs.** The queue is not first-come-first-served across the
fleet: a step joins it one `DAZYFLOW_QUEUE_BURST_SPACING` (100ms) behind the
last step its org already has waiting, so one org's burst of thousands of steps
spreads itself along the queue and every other org's next step — having nothing
of its own waiting — lands at the front. Nothing to configure on a shared
deployment; an org alone on the queue still gets every worker. What the order
cannot fix is steps that hold a worker *without running* (the egress wait
above): for that, `DAZYFLOW_MAX_CONCURRENT_JOBS` caps an org's running steps
outright. Off by default.

**Scheduler enrollment.** `flow_schedules` is a projection: every write that
changes what fires (publish, unpublish, pause, resume, a cadence edit, a delete)
re-derives that flow's rows, and the flow's own graph plus its published tag stay
authoritative. `DAZYFLOW_SCHEDULE_RECONCILE_INTERVAL` (default `1h`) rebuilds the
table from the workspaces on the scheduler leader, which is what fills it on the
first boot after upgrading and what repairs any drift since. It reads every flow
of every tenant, so it is deliberately infrequent and leader-only; a pass where
nothing changed writes nothing. Set it to `0` to disable it.

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

- **Apex deep links forward to the org's subdomain.** Mail carries apex links
  on purpose — the apex is the one host that stays valid when an org renames or
  drops its label, and an emailed link outlives that. But session cookies are
  host-only, so a member of an org that has a subdomain used to arrive at the
  apex signed out and authenticate a second time, on a second host. The links
  already name the org (`?org=<tenant>`), so the apex now 302s the whole request
  — path and query intact — to `<label>.<apex>`, where that member's session
  already lives. Only for `GET`, only when the request carries no valid session
  on the apex (someone signed in there is left alone), and the host is built
  from the claimed label plus your configured apex, never from anything in the
  request.

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
- The proxy must route every `*.<apex>` host to the dzd upstream(s). Any pod
  will do — the sign-in handoff is stored in Postgres, so the apex callback and
  the subdomain that redeems its one-time code need not be the same process.
  (Before that it was an in-process map, and this line required a single
  upstream.)

**TLS — on-demand, no wildcard cert needed.** The bundled `deploy/Caddyfile`
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

## Documentation site (docs.dazyflow.app)

The user docs are a **React SPA** that reuses the app's shell and design system,
so docs.dazyflow.app is visually the same product as the app. The source lives
under `web/src/docs/` (a second Vite entry, `web/docs.html`); the content is
hand-written guide pages (`docs/guide/`) plus a **generated** step catalog
(produced from the step manifests by `cmd/docsgen`). `make docs-content` copies
the guide + generates the catalog into `web/src/docs/content/` (git-ignored),
which the SPA bundles. The site is built fresh **inside a container image** at
deploy time.

`Dockerfile.docs` is a three-stage build: (1) `go run ./cmd/docsgen` generates
the step-catalog Markdown from the live manifests, (2) Vite builds the docs SPA
(`npm run build:docs`) with the guide + generated catalog dropped into the
content dir, (3) nginx serves it with a single-page-app fallback
(`deploy/docs-nginx.conf`: `try_files $uri /index.html`). The Compose prod
overlay builds this as the `docs` service; the Caddy container terminates TLS and
**reverse-proxies** `docs.dazyflow.app` to it. So the deploy is just:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --build
```

Nothing is built on the host — no `make docs-site`, no bind mount. To publish a
docs change (edited guide page, or a step manifest whose catalog text changed),
rebuild that one image:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --build docs
```

The `docs.dazyflow.app` block in `deploy/Caddyfile` uses **on-demand TLS**, the
same path as the per-org subdomains. This is subtle but necessary: `docs` is a
subdomain of the apex, so it matches the `*.dazyflow.app` on-demand policy — and
Caddy excludes any on-demand-covered name from up-front (proactive) certificate
issuance. A plain explicit block expecting a normal managed cert therefore never
gets one (no proactive obtain; the managed policy won't obtain at handshake
either), so every TLS handshake fails with an internal error and the site is
unreachable. On-demand fixes it, and the `ask` endpoint (`/api/v1/auth/tls-allow`)
authorizes `docs` via `auth.servedInfraSubdomains` — it's a reserved label (never
org-claimable) that we nonetheless serve, so it's a closed, non-abusable
allowlist entry. `docs` staying reserved also means it never collides with a
tenant.

DNS: no extra record needed if the wildcard `*.dazyflow.app` A record from the
subdomains section already points at the host — `docs` resolves through it, and
Caddy provisions its cert on-demand on the first request.

Kubernetes: build the same `Dockerfile.docs` image, push it, and run it as a
Deployment + Service with a `docs.dazyflow.app` Ingress host (or serve the
built `dist` from an object-storage/CDN bucket) instead of the Caddy block.

For local iteration without a container, `make docs-dev` runs the docs SPA on
the Vite dev server with hot reload (and `make docs-site` builds `dist/` on the
host); CI builds both the site (`make docs-site`) and the image
(`Dockerfile.docs`) so the published docs can't drift from the code.

## Version stamping & upgrades

> **Schema on boot.** `dzd` applies its own `CREATE TABLE / CREATE INDEX IF NOT
> EXISTS` DDL at startup, so an upgrade that adds an index builds it then — and a
> plain `CREATE INDEX` holds a write lock for as long as the build takes. On a
> small install that is imperceptible. If your `jobs` table has hundreds of
> millions of rows, build any new indexes with `CREATE INDEX CONCURRENTLY`
> before rolling the new image, and boot will find them already there; each
> release's changelog entry lists the statements when it adds one.

The running release is stamped into the binary at build time and surfaced on the
public `GET /api/v1` descriptor (`build.version`), in the startup log, in the web
UI's account menu, and in the admin **System** panel — which compares it against
the canonical deployment's reported version to tell you whether an upgrade is
available.

Two things feed that stamp, in priority order:

1. The `VERSION` / `COMMIT` / `BUILD_DATE` **build args**. The Makefile stack
   targets (`up`, `build`, `restart`, `rebuild`, `upgrade`) export these from
   `git describe`, so a build driven through `make` carries the full identity
   (`0.3.0-2-gabc1234`, the short SHA, the build timestamp).
2. The committed **`./VERSION`** file, which the Docker build reads when no
   `VERSION` build arg is supplied. `.git` is excluded from the build context, so
   this file is the only in-context record of the release — it is what a bare
   `docker build` or a bare `docker compose up --build` stamps.

Path 2 exists because the production command below deliberately invokes compose
directly (to control which overlay files merge) and therefore exports nothing. If
you ever add a `VERSION` default back into `docker-compose.yml`'s `args:` block,
make it empty rather than `dev`: a literal `dev` build arg **shadows** the file
fallback, and the image then reports itself as unstamped — which also breaks the
update check for every operator, since that check reads the canonical instance's
reported version.

### Cutting a release

`make patch` (or `minor` / `major`) promotes `[Unreleased]`, writes `./VERSION`,
commits and tags. It prints the push command:

```sh
git push origin master 0.29.0
```

**Pushing the tag is the release.** That run — and only that run — publishes the
images to ghcr and calls the production deploy webhook. A push to `master`, a
pull request, and a manual `workflow_dispatch` never deploy; dispatching with
`publish=true` re-publishes images and stops there, so re-cutting an image
cannot ship itself by accident. Pre-release tags (`1.0.0-rc1`) do not match the
trigger, so they neither move `:latest` nor deploy.

Pushing master and the tag together starts two CI runs: an ordinary one for the
branch and the releasing one for the tag. Only the second one can reach
production.

If the deploy secrets (`DAZYFLOW_WEBHOOK_KEY`, `DAZYFLOW_FLOW`) are missing, the
tag run **fails after publishing** rather than skipping quietly — the images are
on ghcr, production is untouched, and the log says so.

### Where the images come from

A host running the production overlay **pulls** its release images; it never
compiles them. CI builds both (`dzd` and `docs`) and publishes them to GitHub
Container Registry as `ghcr.io/<owner>/dazyflow-dzd` and `-docs`, tagged both
`X.Y.Z` and `latest`.

This is not a preference. An s-1vcpu-2gb droplet cannot build Go and Vite-build
two frontends inside a deploy step's timeout, and the attempt starves the site
while it fails. Set `GHCR_OWNER` in `.env` (lower-case — ghcr rejects an
uppercase path segment); the overlay refuses to resolve without it rather than
silently constructing a broken image name.

A ghcr package is **private by default**, and an anonymous pull of one fails
with a 401 that reads like "not found". Either make both packages public, or log
the host in once with a token holding `read:packages`:

```sh
docker login ghcr.io -u YOUR_LOGIN --password-stdin
```

A self-host **without** the production overlay has no images published for it
and still builds from source — that path is unchanged.

To upgrade a deployment by hand, fetch, check out the newest release tag, and
pull the images for it:

```sh
git fetch --tags --force --prune-tags
LATEST=$(make -s latest)          # newest bare X.Y.Z tag; see the note below
git checkout --force "$LATEST"
export VERSION="$LATEST"
docker compose -f docker-compose.yml -f docker-compose.prod.yml pull dzd docs
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --no-build
```

`VERSION` selects which published tag to pull, so it is doing different work
here than in a build: `COMMIT` and `BUILD_DATE` were stamped into the image by
CI at build time and are not needed on this host at all. Without the overlay —
a self-host that builds — use `up -d --build` and export all three, as the
stamping rules above describe.

Select the tag with `make latest`, not `git tag --sort=-v:refname | head -1`:
that sorts any non-version tag (a `nightly`) above every release, and version
sort also ranks `1.0.0-rc1` above `1.0.0`, so the naive form can deploy a
nightly or a release candidate. Exporting `VERSION` matters for the same reason
it does everywhere else — it is also the only way `COMMIT` and `BUILD_DATE` get
real values, since `.git` is not in the build context.

Confirm what actually shipped — this is the same value the UI shows:

```sh
curl -s https://your.host/api/v1 | jq .build
```

`PROD=1 make upgrade` automates all of it — fetch, newest-release-tag selection,
pulling that tag's images with the overlay merged — and leaves the checkout **on
the tag** so the tree matches the running image:

```sh
PROD=1 make upgrade
```

### Marking a host as production

Something has to tell compose to merge the overlay, or it runs
`docker-compose.yml` alone and auto-merges `docker-compose.override.yml` if
present (a local dev override, if you keep one) — recreating the stack
that way drops Caddy and the docs site.

**On a permanent production host, set compose's own variable once in `.env`:**

```ini
COMPOSE_FILE=docker-compose.yml:docker-compose.prod.yml
```

Every compose call then honours it — `make upgrade`, `make restart`, and a bare
`docker compose up -d --build` alike — and any local override is no longer
merged.
Nothing to remember per command, so this is the recommended setup.

`PROD=1` is the ad-hoc alternative: it merges the same two files for one make
invocation, from any checkout, touching no config. Useful for a one-off, but it
must be repeated every time and it only affects `make` — a bare `docker compose`
on that host still gets the default resolution. With `COMPOSE_FILE` set, leave
`PROD` unset; passing both is harmless.

Either way `make upgrade` checks before acting: if `caddy` is running but the file
set it is about to apply doesn't include it, it stops and prints both options
rather than taking TLS down. It also stays on the deployed tag whenever the
overlay is in play, so the tree keeps matching the running image.

## Durability

`DAZYFLOW_POSTGRES_DSN` is **required** — `dzd` runs on Postgres and
refuses to start without it. Jobs, API keys, sessions, users, encrypted
secrets, memberships, invitations, per-org SSO config, and per-org
profiles all persist to Postgres. (Graph workspaces and execution
sandboxes are git/filesystem-backed under `DAZYFLOW_DATA_DIR`.) Provide a
stable `DAZYFLOW_MASTER_KEY` (32-byte base64); losing it makes every
stored secret undecryptable.

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

2. Serve TLS from the `postgres` service. This is already wired in
   **`docker-compose.prod.yml`** — a `command:` enabling `ssl=on` with the
   cert/key paths, plus a read-only `./certs:/certs:ro` mount:

   ```yaml
   postgres:
     command:
       - -c
       - ssl=on
       - -c
       - ssl_cert_file=/certs/server.crt
       - -c
       - ssl_key_file=/certs/server.key
     volumes:
       - ./certs:/certs:ro
   ```

   It lives in the overlay, not the base file: `ssl=on` refuses to start
   without the cert files, and a fresh clone has no `certs/`. To run TLS
   without the rest of the overlay, put the same two keys in your own
   `docker-compose.override.yml`.

3. Point the DSN at TLS — change `sslmode=disable` to `sslmode=require` in
   `DAZYFLOW_POSTGRES_DSN` (in `.env`), then recreate with the overlay
   merged so `ssl=on` is actually in effect:

   ```sh
   docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
   docker compose logs -f dzd   # the FATAL TLS line should be gone
   docker compose ps            # dzd "Up (healthy)", not Restarting
   ```

   (`PROD=1 make restart` wraps the same `-f` pair — see "Version stamping &
   upgrades" above. Set `COMPOSE_FILE` in `.env` to make every bare
   `docker compose` call on this host merge the pair for you.)

`ssl=on` only *offers* TLS; it still accepts plaintext clients, so a host
with the overlay merged still serves a `DAZYFLOW_DEV=1` stack that connects
with `sslmode=disable`.

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

## First admin (bootstrap)

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

## Managing tenants, tiers & entitlements

Everything a multi-tenant operator needs is in the web UI under **Admin →
Platform** (visible to `platform:admin` accounts) — no SQL required:

- **Orgs** (`/admin/platform/orgs`) — every tenant on the instance; open one
  to suspend/resume it or adjust its plan.
- **Users** (`/admin/platform/users`) — every account across all orgs;
  suspend/unsuspend, inspect roles, and **grant or revoke `platform:admin`**
  (User detail → Platform role). This is the runtime counterpart to the
  `DAZYFLOW_PLATFORM_ADMINS` env allowlist: it adds/removes the cross-tenant
  super-admin role without a restart. A grant takes effect on the target's next
  sign-in; a revoke drops their live sessions so it applies immediately. Admins
  granted via the env allowlist show a non-revocable badge — to remove one,
  edit `DAZYFLOW_PLATFORM_ADMINS` and restart (a UI revoke would be undone at
  their next login).
- **Tiers** (`/admin/platform/tiers`) — reusable bundles of limits (runs/month,
  disk, concurrency, members, retention, max flows/nodes/timeout, polling). The
  built-in **Free** and **Pro** tiers ship with every limit unset, so they
  inherit the deployment-global defaults (the `DAZYFLOW_FREE_*` knobs) and a
  self-host with those unset is effectively unlimited. Create custom tiers
  (e.g. an Enterprise tier) here.
- **Org detail → Plan & limits** — assign a tier to an org, pin its plan
  (`free`/`pro`), grant a comp or trial, or override any single limit. The
  effective value resolves *override → tier → global default*, where `0 =
  unlimited/inherit`.
- **Steps** (`/admin/platform/drops`) — enable/disable connectors globally or
  per-tenant.

Plans/tiers are **independent of Stripe**: a `platform:admin` can comp an org to
Pro or assign a custom tier with no billing configured at all. Stripe only adds
the self-serve Checkout/portal buttons (see *Billing / plan gates* in
`.env.example`); leave it unset and the whole plan/billing surface stays hidden
in the UI while you still manage entitlements from Admin → Platform.

## Support tickets & consented flow access (optional)

Off by default. Set `DAZYFLOW_SUPPORT_ENABLED=1` to turn on the in-app support
surface: customers file tickets about a flow and chat with support in-app, support
agents work a **cross-org queue** (assign/claim, filter by owner and status), and
an agent can request read-only access to **one** flow that an org admin approves
for a time-boxed window. Two properties hold no matter what:

- Support only ever sees a **redacted** view — parameter values are replaced by
  their shape, run payloads are dropped, `${secret.…}` references are kept as
  references, and a secret-detector sweeps every remaining string. Filing a ticket
  auto-attaches such a bundle, so most tickets are diagnosable with no live access
  at all.
- Access is **consented, scoped, time-boxed and audited**: an approved grant
  covers one (agent, org, flow) triple for `4h` by default, the org can revoke it
  at any time from **Admin → Support access**, and every support action is written
  to the **org's own** audit log.

Enabling the flag is not enough on its own — the surface stays inert until you
provision a support agent under **Admin → Platform → Support agents** (a
`platform:admin` is *not* automatically support staff; the two roles are
deliberately separate). A grant takes effect on that person's next sign-in.

**Unread reminders.** Support mail already goes out on every reply. On top of
that, `DAZYFLOW_SUPPORT_NUDGE_AFTER` (default `24h`, `0` to disable) reminds
whichever side has left a message unread that long — the customer who never
opened the answer, or the queue when nobody has looked at a question. Three
properties keep it from becoming noise:

- It is **read-aware**. Opening the thread records a receipt, so someone who has
  read the message and simply not replied yet is never chased. Someone who has
  never opened the ticket at all is, because the age is the floor.
- It fires **once per waiting period**, not once per sweep. A new message from
  the other side starts a new period; nothing else does.
- It never fires on a **resolved or closed** ticket, and a reply from either side
  ends the wait.

Multi-node installs need no extra configuration: the sweep runs on the scheduler
leader only, so recipients get one reminder rather than one per daemon.

## Fail-closed config guard

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

## Security knobs worth setting

Every variable is documented in full — semantics, default, and warnings —
in `.env.example`. This is the security-critical subset to review before
you expose the daemon; see `.env.example` for the detail.

- The auth rate limiter is fixed at **20/min per IP (burst 10)** on
  `/api/v1/auth/{signin,signup}` — not a knob, but worth knowing it's there.
- `DAZYFLOW_DEV_KEY` / `DAZYFLOW_DEV` — dev-only; never set in production.
- `DAZYFLOW_HTTP_EGRESS_ALLOW` — allowlist the hosts outbound HTTP steps may
  reach (the IP-level SSRF guard blocks private/loopback/metadata regardless).
- `DAZYFLOW_ALLOW_PRIVATE_EGRESS` — keep **off** on multi-tenant deploys; on,
  it lets flows reach private/loopback/cloud-metadata addresses (and the
  DB/SMTP step hosts).
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

## Steps

Every node you drop on the canvas — triggers, transforms, and the connectors
(Gmail, Slack, Sheets, Notion, GitHub, Claude, Excel, ntfy, webhooks) — is a
native Go step compiled into `dzd`. There is no plugin/marketplace install
path and no separate runtime: the catalog is fixed at build time. Connectors
that need credentials use the OAuth providers configured under **Admin →
Connector apps** (`/admin/oauth`) or a `${secret.…}` token.

## Secrets

Out of the box, secrets are held in the **built-in encrypted store** — flows
reference them as `${secret.NAME}` (the `tenant://` provider), values are
AES-256-GCM encrypted under a per-tenant key wrapped by `DAZYFLOW_MASTER_KEY`,
and the UI is write-only (you never read a value back). That's the zero-infra
default; no external dependency. For master-key handling and rotation, see
**[SECURITY.md](../SECURITY.md)**.

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
