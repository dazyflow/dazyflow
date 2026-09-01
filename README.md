# Dazyflow

**Workflow automation you host yourself.** Build a flow by wiring steps on a
canvas — a trigger, some connectors, a branch, an AI step — and `dzd` runs it on
your own machine, on your own database, with your credentials never leaving it.

163 steps across 36 apps, hosted forms and webhooks, schedules, human approvals,
and first-class Nordic/EU connectors (Fortnox, Klarna, Roaring, 46elks, nShift,
SMHI) alongside the usual Google, Slack, Stripe and GitHub. English and Swedish
UI. AGPL, no feature gates, no seat limits.

<!-- TODO(screenshot): a shot of the editor canvas belongs here, above the
     fold — it is the single biggest thing missing from this page for anyone
     deciding whether to try a visual flow builder. Save it to `docs/img/`
     and link it as an image at the top of this section. -->

## Try it in 60 seconds

You need Docker with the Compose plugin. Nothing else — Postgres is bundled.

```sh
git clone https://github.com/dazyflow/dazyflow && cd dazyflow
cp .env.example .env && echo 'DAZYFLOW_DEV=1' >> .env
docker compose up -d
```

Open <http://localhost:8642> and sign in as **`test@example.com` / `test`**.
Start from **New flow → "From a template" → "See a flow run (no setup)"** — it
needs no connected account at all. `docker compose down` stops it; `down -v`
also deletes the data.

> **Port 8642 already taken?** Set both `DAZYFLOW_PORT` and
> `DAZYFLOW_WEB_ORIGIN` in `.env` — they have to agree, or the browser's
> sign-in is rejected as a cross-origin request and the form reports it as a
> wrong password. (8642 rather than the usual 8080 precisely because 8080 is
> so often already in use.)

> `DAZYFLOW_DEV=1` is what makes this three commands instead of a config
> session: it seeds that sign-in and downgrades the insecure-defaults guard to
> warnings. It is for trying things out on your laptop. **Never set it on a
> real deployment** — see [Running it for real](#running-it-for-real) for the
> two minutes that takes.

## What you can build with it

Thirteen templates ship in the gallery, each a working flow you fork and fill in:

| | |
|---|---|
| **Web form → Google Sheet** | A hosted form with a public link appends every submission to a sheet. |
| **New email → Slack** | Checks Gmail every few minutes, posts a one-line summary. |
| **AI reads the inbox and sorts it out** | Classifies each new email and routes it. |
| **Stripe payment → thank-you, team ping, sales log** | One event fans out to three places. |
| **Nothing goes out until someone approves** | The flow parks itself until a human clicks approve. |
| **Watch a page → ping my phone** | Compares the visible words, not the markup, so it fires on real change. |
| **Invoices emailed to you → filed in Drive** | Finds mail carrying a PDF, files the attachment. |

[`tests/usecases/README.md`](tests/usecases/README.md) works through 35 more
plain-language asks — "chase overdue invoices", "remind people about
appointments" — each with a verdict and a real graph behind it.

**Beyond the canvas:** `dzd` speaks [MCP](docs/guide/mcp-servers.md) in both
directions — it registers MCP servers as steps, and `dz-mcp` exposes your flows
as tools to Claude or any MCP client. Long-running or on-prem work goes to
[self-hosted runners](docs/guide/runners.md). Everything the UI does is on a
[documented HTTP API](docs/guide/web-apis.md), and `dzctl` drives it from a
terminal.

## Running it for real

The dev shortcut above is not a deployment. For anything durable, drop
`DAZYFLOW_DEV=1` and satisfy three things in `.env` — `dzd` refuses to boot
without them, by design:

| Variable | What to set it to |
|---|---|
| `POSTGRES_PASSWORD` | A strong password. **Put the same value inside `DAZYFLOW_POSTGRES_DSN`** — it appears inline there, and the two must match. |
| `DAZYFLOW_MASTER_KEY` | `openssl rand -base64 32`. Encrypts stored secrets. Keep a sealed backup — losing it makes every stored secret undecryptable. |
| `sslmode=require` in `DAZYFLOW_POSTGRES_DSN` | The link to Postgres carries personal data and wrapped keys, so it may not run in cleartext. The shipped DSN says `sslmode=disable`, which is fine for the dev trial and rejected here. |

That last one needs the bundled Postgres to actually serve TLS, which takes a
one-time self-signed cert on the host plus the production overlay that turns
`ssl=on`:

```sh
mkdir -p certs
openssl req -new -x509 -days 3650 -nodes -subj /CN=postgres \
    -out certs/server.crt -keyout certs/server.key
sudo chown 70:70 certs/server.crt certs/server.key
chmod 600 certs/server.key && chmod 644 certs/server.crt

docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

A self-signed cert is enough — `require` encrypts the channel without verifying
the certificate. Full walkthrough, including a managed or external database
instead: [docs/DEPLOY.md](docs/DEPLOY.md).

There is **no default login** without dev mode. To create the first real
account, set `DAZYFLOW_ENABLE_SIGNUP=1` and put your email in
`DAZYFLOW_PLATFORM_ADMINS` — that grants instance super-admin on first sign-in.
Turn signup back off afterwards.

`dzd` does not terminate TLS. Put it behind a reverse proxy (nginx, Caddy,
Traefik, an ingress) and set:

```sh
DAZYFLOW_TRUST_PROXY_HEADERS=1
DAZYFLOW_WEB_ORIGIN=https://your.domain
DAZYFLOW_PUBLIC_BASE_URL=https://your.domain
```

If it won't start, the boot log names the value it rejected.

> **Changed `POSTGRES_PASSWORD` but it's still rejected?** That variable only
> applies when the `pgdata` volume is first created. `docker compose down -v`
> resets it and **deletes all data**.
>
> **`database "dazyflow" does not exist`?** Postgres died partway through its
> very first start, leaving a `pgdata` volume it will not finish initialising on
> a retry. Same fix — `docker compose down -v`, then bring it up again. Safe
> whenever it happens on a first boot; there is nothing in there yet.

Control-plane state — jobs, API keys, sessions, users, encrypted secrets —
persists to Postgres; graphs and sandboxes to the `dzddata` volume. Back up both.

**Self-hosting is unlimited.** Dazyflow is multi-tenant by design but self-hosts
cleanly as a single team — one org, signup closed, invite the rest. Every quota
knob defaults to off. Billing is optional and SaaS-only: leave Stripe unset and
the whole plan/upgrade surface disappears.

## Documentation

| | |
|---|---|
| [docs/guide](docs/guide) | Using Dazyflow — concepts, first flow, triggers, forms, approvals, runners, MCP. |
| [docs/DEPLOY.md](docs/DEPLOY.md) | Running it for real: TLS, backups, per-org subdomains, Vault, observability, Kubernetes. |
| [SECURITY.md](SECURITY.md) | Reporting a vulnerability, the master key, the security knobs worth setting. |
| [docs/PRIVACY.md](docs/PRIVACY.md) · [docs/COMPLIANCE.md](docs/COMPLIANCE.md) | GDPR data handling, and the ISO/IEC 27001 Annex A control mapping. |
| [CONTRIBUTING.md](CONTRIBUTING.md) | What the gates enforce before a change lands. |

## Development

There is no in-memory mode — `dzd` requires Postgres. `make dev` starts the
bundled Postgres (loopback-only) and runs the daemon on the host against it.

```sh
make dev      # bundled Postgres, then dzd on http://localhost:8642
make web      # (other terminal) Vite dev server on http://localhost:5173
make check    # the fast pre-push gate
make ci       # the full CI mirror — adds the web and runner suites
make help     # every target
```

With no `.env`, `make dev` uses the bundled database, sets `DAZYFLOW_DEV=1`,
enables signup, and mints a throwaway API key on every boot
(`DAZYFLOW_DEV_KEY=1`). To use your own database, run `make env`, set
`DAZYFLOW_POSTGRES_DSN`, and `make dev` will source it.

## License

Free software under the **GNU Affero General Public License v3.0 or later**
(AGPL-3.0-or-later) — see [LICENSE](LICENSE). The network clause (section 13)
applies: if you run a modified `dzd` as a network service, you must offer its
users the corresponding source of your modified version.
