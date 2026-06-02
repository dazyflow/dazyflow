# Hazy Flow

A workflow automation engine. `hzd` is a Go daemon that runs graph based
flows (connectors, transforms, AI steps, branching, schedules and
webhooks) and serves a web UI for building and watching them.

This README gets you running. **[DEPLOY.md](DEPLOY.md)** is the full
reference (TLS, backups, the marketplace, secrets, observability);
**[SECURITY.md](SECURITY.md)** covers the master key.

## Quick start (Docker Compose)

You need Docker with the Compose plugin. Everything is configured through
`.env`; the bundled `docker-compose.yml` runs the daemon plus Postgres.

**1. Create your `.env`:**

```sh
cp .env.example .env
```

**2. Set the four mandatory values** in `.env` (the daemon refuses to
boot with insecure defaults once a database is configured — see the guard
below):

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

`hzd` **refuses to start** when it's pointed at a database while still
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
example, backup/restore, the master key, per-org subdomains, the
marketplace (drops & integrations), secrets (built-in store + OpenBao/Vault),
security knobs, and observability. For multi-node Kubernetes, see
**[deploy/README.md](deploy/README.md)**.

## Run locally for development

Postgres is optional in dev (state falls back to in-memory or a JSON file,
and the daemon logs a warning).

```sh
make env         # seed .env from .env.example (one-time)
make dev         # boots hzd; sources .env, or a minimal dev set if absent.
                 # http://localhost:8080
make web         # (other terminal) Vite dev server on http://localhost:5173
```

`make env` seeds the bundled Postgres DSN, which trips the boot guard. For
local dev either blank `HAZYFLOW_POSTGRES_DSN` (in-memory fallback, no
guard) or set `HAZYFLOW_DEV=1`. The dev defaults turn on signup and mint a
throwaway API key on every boot (`HAZYFLOW_DEV_KEY=1`) — never set that
outside local development.
