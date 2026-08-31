# Dazyflow

A workflow automation engine. `dzd` is a single Go daemon that runs graph-based
flows — connectors, transforms, AI steps, branching, schedules and webhooks —
and serves the web UI for building and watching them.

Beyond getting it running: **[docs/guide](docs/guide)** for using Dazyflow,
**[docs/DEPLOY.md](docs/DEPLOY.md)** for running it for real (TLS, backups,
observability, Kubernetes), **[SECURITY.md](SECURITY.md)** for the master key,
and **[CONTRIBUTING.md](CONTRIBUTING.md)** for the development gates.

## Quick start

You need Docker with the Compose plugin. Postgres is bundled; everything else is
configured through `.env`.

```sh
cp .env.example .env    # or: make env
```

`dzd` refuses to boot with insecure defaults, so two values are not optional:

| Variable | What to set it to |
|---|---|
| `POSTGRES_PASSWORD` | A strong password. **Put the same value inside `DAZYFLOW_POSTGRES_DSN`** — it appears inline there, and the two must match. |
| `DAZYFLOW_MASTER_KEY` | `openssl rand -base64 32`. Encrypts stored secrets. Keep a sealed backup — losing it makes every stored secret undecryptable. |

A local-only trial needs nothing else: `DAZYFLOW_WEB_ORIGIN` defaults to
`http://localhost:8080`, and `DAZYFLOW_PUBLIC_BASE_URL` can stay blank until you
want OAuth sign-in or human-approval links.

There is **no default login.** To create the first account, also set
`DAZYFLOW_ENABLE_SIGNUP=1` and put your email in `DAZYFLOW_PLATFORM_ADMINS` —
that grants instance super-admin on first sign-in. Turn signup back off
afterwards.

```sh
docker compose up -d                     # or: make up
curl -fsS http://localhost:8080/readyz   # -> "ready"
```

That builds the image, starts Postgres, and serves the API and web UI on
http://localhost:8080 (gRPC on `:50050`). Sign up, then stop with
`docker compose down` (named volumes persist).

If it won't start, the boot log names the value it rejected. For a throwaway
trial, `DAZYFLOW_DEV=1` downgrades the guard to warnings and seeds a
`test@example.com` / `test` admin — **never set it in production.**

> **Changed `POSTGRES_PASSWORD` but it's still rejected?** That variable only
> applies when the `pgdata` volume is first created. `docker compose down -v`
> resets it and **deletes all data**.

Control-plane state — jobs, API keys, sessions, users, encrypted secrets —
persists to Postgres; graphs and sandboxes to the `dzddata` volume. Back up both.

## Going to production

`dzd` does not terminate TLS. Put it behind a TLS-terminating reverse proxy
(nginx, Caddy, Traefik, an ingress) and set in `.env`:

```sh
DAZYFLOW_TRUST_PROXY_HEADERS=1
DAZYFLOW_WEB_ORIGIN=https://your.domain
DAZYFLOW_PUBLIC_BASE_URL=https://your.domain
```

[docs/DEPLOY.md](docs/DEPLOY.md) has the rest: the reverse-proxy contract and an
nginx example, backup and restore, per-org subdomains, secrets (built-in store
plus OpenBao/Vault), security knobs, observability, and multi-node Kubernetes
(`deploy/k8s/dazyflow.yaml`).

**Self-hosting is unlimited.** Dazyflow is multi-tenant by design but self-hosts
cleanly as a single team — one org, signup closed, invite the rest. Every quota
knob defaults to off.

**Billing is optional and SaaS-only.** Leave Stripe unset and the UI hides the
whole plan/upgrade/billing surface; the Usage page still shows run and step
metering. You can comp an org to Pro or assign custom limit tiers from
**Admin → Platform** with no billing configured. Only set the `DAZYFLOW_FREE_*` /
`DAZYFLOW_STRIPE_*` knobs if you're running a paid SaaS.

## Development

There is no in-memory mode — `dzd` requires Postgres. `make dev` starts the
bundled Postgres (loopback-only) and runs the daemon on the host against it.

```sh
make dev      # bundled Postgres, then dzd on http://localhost:8080
make web      # (other terminal) Vite dev server on http://localhost:5173
make check    # the fast pre-push gate
make ci       # the full CI mirror — adds the web and runner suites
make help     # every target
```

With no `.env`, `make dev` uses the bundled database, sets `DAZYFLOW_DEV=1`,
enables signup, and mints a throwaway API key on every boot
(`DAZYFLOW_DEV_KEY=1`). To use your own database, run `make env`, set
`DAZYFLOW_POSTGRES_DSN`, and `make dev` will source it.

[CONTRIBUTING.md](CONTRIBUTING.md) covers what the gates enforce.

## License

Free software under the **GNU Affero General Public License v3.0 or later**
(AGPL-3.0-or-later) — see [LICENSE](LICENSE). The network clause (section 13)
applies: if you run a modified `dzd` as a network service, you must offer its
users the corresponding source of your modified version.
