# Dazyflow

A workflow automation engine. `dzd` is a Go daemon that runs graph based
flows (connectors, transforms, AI steps, branching, schedules and
webhooks) and serves a web UI for building and watching them.

This README gets you running. **[docs/DEPLOY.md](docs/DEPLOY.md)** is the full
reference (TLS, backups, secrets, observability);
**[SECURITY.md](SECURITY.md)** covers the master key.

## Quick start (Docker Compose)

You need Docker with the Compose plugin. Everything is configured through
`.env`; the bundled `docker-compose.yml` runs the daemon plus Postgres.

**1. Create your `.env`:**

```sh
cp .env.example .env
```

**2. Set the four mandatory values** in `.env` (`dzd` requires Postgres and
refuses to boot with insecure defaults — see the guard below):

- `POSTGRES_PASSWORD` — a strong password. **You must put the same value
  into `DAZYFLOW_POSTGRES_DSN`** (it appears inline there). The two must
  match or the daemon can't reach the database.
- `DAZYFLOW_MASTER_KEY` — a stable 32-byte key that encrypts stored
  secrets. Generate with `openssl rand -base64 32` and keep a sealed
  backup; losing it makes every stored secret undecryptable.
- `DAZYFLOW_PUBLIC_BASE_URL` + `DAZYFLOW_WEB_ORIGIN` — your public origin
  (for a purely local trial, leave the defaults of `http://localhost:8080`).

To create the first account, also set `DAZYFLOW_ENABLE_SIGNUP=1` and put
your email in `DAZYFLOW_PLATFORM_ADMINS` (it grants the instance
super-admin role on next sign-in). A production deploy ships **no default
login** — signup is how you bootstrap.

**3. Boot:**

```sh
docker compose up -d
```

This builds the image, brings up Postgres, and serves the API + web UI on
http://localhost:8080 (gRPC on :50050). Control-plane state (jobs, API
keys, sessions, users, encrypted secrets) persists to Postgres; graphs and
sandboxes persist to the `dzddata` volume.

**4. Verify and sign in:**

```sh
curl -fsS http://localhost:8080/readyz && echo OK   # -> "ready"
docker compose logs -f dzd                           # watch the boot log
```

Open http://localhost:8080 and sign up. You can turn `DAZYFLOW_ENABLE_SIGNUP`
back off afterwards.

Stop with `docker compose down` (named volumes persist).

### Boot guard

`dzd` requires `DAZYFLOW_POSTGRES_DSN` and **refuses to start** while still
using the bundled default DB password or an empty master key — the boot
log names exactly which value is insecure. Fix it and restart. To boot the
shipped defaults for a throwaway local trial only, set `DAZYFLOW_DEV=1`,
which downgrades the guard to warnings (and seeds a `test@example.com` /
`test` admin so there's something to sign in with). **Never set
`DAZYFLOW_DEV=1` in production.**

> **If the password is rejected after you changed it:** `POSTGRES_PASSWORD`
> only takes effect when the `pgdata` volume is first created. If you
> already booted once (e.g. a default trial), the old password is baked
> into the volume. Reset it with `docker compose down -v` (this **deletes
> all data**) and boot again.

## Going to production

`dzd` does not terminate TLS. Run it behind a TLS-terminating reverse
proxy (nginx, Caddy, Traefik, an ingress) and set, in `.env`:
`DAZYFLOW_TRUST_PROXY_HEADERS=1`, `DAZYFLOW_WEB_ORIGIN=https://your.domain`,
and `DAZYFLOW_PUBLIC_BASE_URL=https://your.domain`.

See **[docs/DEPLOY.md](docs/DEPLOY.md)** for the reverse-proxy contract and nginx
example, backup/restore, the master key, per-org subdomains,
secrets (built-in store + OpenBao/Vault),
security knobs, observability, and multi-node Kubernetes
(`deploy/k8s/dazyflow.yaml`).

### Self-hosting notes

Dazyflow is **multi-tenant by design** but self-hosts cleanly as a single
team too — one org, signup closed, invite the rest. A self-hosted instance
needs only Postgres + a master key (the four mandatory values above); it is
effectively **unlimited** because every quota knob defaults to off.

**Billing is optional and SaaS-only.** Leave Stripe unset (the default) and
the web UI hides the entire plan/upgrade/billing surface — the Usage page
shows run/step metering only. You can still comp an org to Pro or assign
custom limit tiers from **Admin → Platform** with no billing configured at
all (see docs/DEPLOY.md, "Managing tenants, tiers & entitlements"). Set the
`DAZYFLOW_FREE_*` / `DAZYFLOW_STRIPE_*` knobs only if you intend to run a
paid SaaS that charges its tenants.

## Run locally for development

`dzd` requires Postgres — there is no in-memory mode. For local dev,
`make dev` starts the bundled Postgres container (loopback-only) and runs
the daemon on the host against it; no other setup needed.

```sh
make dev         # starts bundled Postgres (make pg), then boots dzd on
                 # http://localhost:8080
make web         # (other terminal) Vite dev server on http://localhost:5173
```

With no `.env`, `make dev` points dzd at the bundled database
(`postgres://dazyflow:dazyflow@localhost:5432/dazyflow`) and sets
`DAZYFLOW_DEV=1` so the bundled-default password is allowed. It also turns
on signup and mints a throwaway API key on every boot
(`DAZYFLOW_DEV_KEY=1`) — never set those outside local development. To run
against your own database instead, `make env`, set `DAZYFLOW_POSTGRES_DSN`
in `.env`, and `make dev` will source it. (`make pg` / `make pg-down` start
and stop the bundled dev Postgres on their own.)

## License

Dazyflow is free software, licensed under the **GNU Affero General Public
License v3.0 or later** (AGPL-3.0-or-later). See [LICENSE](LICENSE) for the
full text.

The AGPL's network clause (section 13) applies: if you run a modified
version of `dzd` as a network service, you must offer its users the
corresponding source of your modified version.
