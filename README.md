# Dazyflow

A workflow automation engine. `dzd` is a single Go daemon that runs
graph-based flows — connectors, transforms, AI steps, branching, schedules
and webhooks — and serves the web UI for building and watching them.

This file gets you running. Beyond that:
**[docs/guide](docs/guide)** for using Dazyflow (concepts, first flow,
triggers, runners), **[docs/DEPLOY.md](docs/DEPLOY.md)** for running it for
real (TLS, backups, observability, Kubernetes), and
**[SECURITY.md](SECURITY.md)** for the master key.

## Quick start

You need Docker with the Compose plugin. Postgres is bundled; everything
else is configured through `.env`.

**1. Create your `.env`**

```sh
cp .env.example .env    # or: make env
```

**2. Fill in the two required values**

`dzd` refuses to boot with insecure defaults, so these are not optional:

| Variable | What to set it to |
|---|---|
| `POSTGRES_PASSWORD` | A strong password. **Put the same value inside `DAZYFLOW_POSTGRES_DSN`** — it appears inline there, and the two must match. |
| `DAZYFLOW_MASTER_KEY` | `openssl rand -base64 32`. Encrypts stored secrets. Keep a sealed backup — losing it makes every stored secret undecryptable. |

A local-only trial needs nothing else: `DAZYFLOW_WEB_ORIGIN` already
defaults to `http://localhost:8080`, and `DAZYFLOW_PUBLIC_BASE_URL` can
stay blank until you want OAuth sign-in or human-approval links.

There is **no default login**. To create the first account, also set
`DAZYFLOW_ENABLE_SIGNUP=1` and put your email in
`DAZYFLOW_PLATFORM_ADMINS` — that grants instance super-admin on first
sign-in. Turn signup back off afterwards.

**3. Boot**

```sh
docker compose up -d    # or: make up
```

This builds the image, starts Postgres, and serves the API and web UI on
http://localhost:8080 (gRPC on `:50050`).

**4. Verify and sign in**

```sh
curl -fsS http://localhost:8080/readyz   # -> "ready"
docker compose logs -f dzd               # watch the boot log
```

Open http://localhost:8080 and sign up. Stop with `docker compose down`
(named volumes persist).

### If it won't start

The boot log names the exact value it rejected — an empty master key, or
the bundled default DB password. Fix it and restart.

For a throwaway local trial you can set `DAZYFLOW_DEV=1`, which downgrades
the guard to warnings and seeds a `test@example.com` / `test` admin.
**Never set it in production.**

> **Changed `POSTGRES_PASSWORD` but it's still rejected?** That variable
> only applies when the `pgdata` volume is first created; if you already
> booted once, the old password is baked into the volume. `docker compose
> down -v` resets it and **deletes all data**.

## Where your data lives

Control-plane state — jobs, API keys, sessions, users, encrypted secrets —
persists to Postgres. Graphs and sandboxes persist to the `dzddata` volume.
Back up both.

## Going to production

`dzd` does not terminate TLS. Put it behind a TLS-terminating reverse proxy
(nginx, Caddy, Traefik, an ingress) and set in `.env`:

```sh
DAZYFLOW_TRUST_PROXY_HEADERS=1
DAZYFLOW_WEB_ORIGIN=https://your.domain
DAZYFLOW_PUBLIC_BASE_URL=https://your.domain
```

[docs/DEPLOY.md](docs/DEPLOY.md) has the rest: the reverse-proxy contract
and an nginx example, backup and restore, per-org subdomains, secrets
(built-in store plus OpenBao/Vault), security knobs, observability, and
multi-node Kubernetes (`deploy/k8s/dazyflow.yaml`).

**Self-hosting is unlimited.** Dazyflow is multi-tenant by design but
self-hosts cleanly as a single team — one org, signup closed, invite the
rest. Every quota knob defaults to off.

**Billing is optional and SaaS-only.** Leave Stripe unset and the UI hides
the whole plan/upgrade/billing surface; the Usage page still shows run and
step metering. You can comp an org to Pro or assign custom limit tiers from
**Admin → Platform** with no billing configured at all. Only set the
`DAZYFLOW_FREE_*` / `DAZYFLOW_STRIPE_*` knobs if you're running a paid SaaS.

## Development

`dzd` requires Postgres — there is no in-memory mode. `make dev` starts the
bundled Postgres (loopback-only) and runs the daemon on the host against it.

```sh
make dev      # bundled Postgres, then dzd on http://localhost:8080
make web      # (other terminal) Vite dev server on http://localhost:5173
make check    # gofmt, build, vet, Go tests, catalogues, changelog
make ci       # the full CI mirror — adds the web and runner suites
make help     # every target
```

`make check` is the fast gate for a Go-only change. It does **not** run the web
suite, the web build, or the runner agent tests, and CI gates all three — so a
change touching `web/` or `runner/` wants `make ci` before it goes up.

With no `.env`, `make dev` uses the bundled database and sets
`DAZYFLOW_DEV=1`, enables signup, and mints a throwaway API key on every
boot (`DAZYFLOW_DEV_KEY=1`) — local development only. To use your own
database, run `make env`, set `DAZYFLOW_POSTGRES_DSN`, and `make dev` will
source it.

## License

Dazyflow is free software under the **GNU Affero General Public License
v3.0 or later** (AGPL-3.0-or-later) — see [LICENSE](LICENSE).

The AGPL's network clause (section 13) applies: if you run a modified `dzd`
as a network service, you must offer its users the corresponding source of
your modified version.
