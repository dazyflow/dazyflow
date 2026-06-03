# Hazyflow

A workflow automation engine. `hzd` is a Go daemon that runs graph based
flows (connectors, transforms, AI steps, branching, schedules and
webhooks) and serves a web UI for building and watching them.

This README gets you running. **[DEPLOY.md](DEPLOY.md)** is the full
reference (TLS, backups, secrets, observability);
**[SECURITY.md](SECURITY.md)** covers the master key.

## Quick start (Docker Compose)

You need Docker with the Compose plugin. Everything is configured through
`.env`; the bundled `docker-compose.yml` runs the daemon plus Postgres.

**1. Create your `.env`:**

```sh
cp .env.example .env
```

**2. Set the four mandatory values** in `.env` (`hzd` requires Postgres and
refuses to boot with insecure defaults — see the guard below):

- `POSTGRES_PASSWORD` — a strong password. **You must put the same value
  into `HAZYFLOW_POSTGRES_DSN`** (it appears inline there). The two must
  match or the daemon can't reach the database.
- `HAZYFLOW_MASTER_KEY` — a stable 32-byte key that encrypts stored
  secrets. Generate with `openssl rand -base64 32` and keep a sealed
  backup; losing it makes every stored secret undecryptable.
- `HAZYFLOW_PUBLIC_BASE_URL` + `HAZYFLOW_WEB_ORIGIN` — your public origin
  (for a purely local trial, leave the defaults of `http://localhost:8080`).

To create the first account, also set `HAZYFLOW_ENABLE_SIGNUP=1` and put
your email in `HAZYFLOW_PLATFORM_ADMINS` (it grants the instance
super-admin role on next sign-in). A production deploy ships **no default
login** — signup is how you bootstrap.

**3. Boot:**

```sh
docker compose up -d
```

This builds the image, brings up Postgres, and serves the API + web UI on
http://localhost:8080 (gRPC on :50050). Control-plane state (jobs, API
keys, sessions, users, encrypted secrets) persists to Postgres; graphs and
sandboxes persist to the `hzddata` volume.

**4. Verify and sign in:**

```sh
curl -fsS http://localhost:8080/readyz && echo OK   # -> "ready"
docker compose logs -f hzd                           # watch the boot log
```

Open http://localhost:8080 and sign up. You can turn `HAZYFLOW_ENABLE_SIGNUP`
back off afterwards.

Stop with `docker compose down` (named volumes persist).

### Boot guard

`hzd` requires `HAZYFLOW_POSTGRES_DSN` and **refuses to start** while still
using the bundled default DB password or an empty master key — the boot
log names exactly which value is insecure. Fix it and restart. To boot the
shipped defaults for a throwaway local trial only, set `HAZYFLOW_DEV=1`,
which downgrades the guard to warnings (and seeds a `test@example.com` /
`test` admin so there's something to sign in with). **Never set
`HAZYFLOW_DEV=1` in production.**

> **If the password is rejected after you changed it:** `POSTGRES_PASSWORD`
> only takes effect when the `pgdata` volume is first created. If you
> already booted once (e.g. a default trial), the old password is baked
> into the volume. Reset it with `docker compose down -v` (this **deletes
> all data**) and boot again.

## Going to production

`hzd` does not terminate TLS. Run it behind a TLS-terminating reverse
proxy (nginx, Caddy, Traefik, an ingress) and set, in `.env`:
`HAZYFLOW_TRUST_PROXY_HEADERS=1`, `HAZYFLOW_WEB_ORIGIN=https://your.domain`,
and `HAZYFLOW_PUBLIC_BASE_URL=https://your.domain`.

See **[DEPLOY.md](DEPLOY.md)** for the reverse-proxy contract and nginx
example, backup/restore, the master key, per-org subdomains,
secrets (built-in store + OpenBao/Vault),
security knobs, observability, and multi-node Kubernetes
(`deploy/k8s/hazyflow.yaml`).

## Run locally for development

`hzd` requires Postgres — there is no in-memory mode. For local dev,
`make dev` starts the bundled Postgres container (loopback-only) and runs
the daemon on the host against it; no other setup needed.

```sh
make dev         # starts bundled Postgres (make pg), then boots hzd on
                 # http://localhost:8080
make web         # (other terminal) Vite dev server on http://localhost:5173
```

With no `.env`, `make dev` points hzd at the bundled database
(`postgres://hazyflow:hazyflow@localhost:5432/hazyflow`) and sets
`HAZYFLOW_DEV=1` so the bundled-default password is allowed. It also turns
on signup and mints a throwaway API key on every boot
(`HAZYFLOW_DEV_KEY=1`) — never set those outside local development. To run
against your own database instead, `make env`, set `HAZYFLOW_POSTGRES_DSN`
in `.env`, and `make dev` will source it. (`make pg` / `make pg-down` start
and stop the bundled dev Postgres on their own.)
