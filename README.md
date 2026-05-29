# Hazy Flow

A workflow automation engine. `hzd` is a Go daemon that runs graph based
flows (connectors, transforms, AI steps, branching, schedules and
webhooks) and serves a web UI for building and watching them.

This README covers how to deploy it. The marketing site lives in the
separate `hazy-flow-landing` repo.

## Deploy with Docker Compose

The quickest durable setup is the daemon plus Postgres, via the
root-level `docker-compose.yml`. You need Docker with the Compose plugin.

```sh
cp .env.example .env
docker compose up -d
```

Everything is configured through `.env` (see `.env.example` for the full
list of knobs). Before first boot you **must** set, at least:

- `POSTGRES_PASSWORD` — a strong password (and update
  `HAZYFLOW_POSTGRES_DSN` to match it);
- `HAZYFLOW_MASTER_KEY` — a stable 32-byte key that encrypts stored
  secrets (`openssl rand -base64 32`; keep a sealed backup — losing it
  makes every stored secret undecryptable);
- `HAZYFLOW_PUBLIC_BASE_URL` + `HAZYFLOW_WEB_ORIGIN` — your public origin.

`hzd` **refuses to start** when it's pointed at a database (the durable
deployment signal) while still using the bundled default password or an
empty master key — the guard is deliberate, and the boot log names
exactly which value is still insecure. To boot the shipped defaults for a
throwaway local trial — never in production — set `HAZYFLOW_DEV=1`, which
downgrades the guard to warnings.

This brings up Postgres and the daemon. The API and web UI are on
http://localhost:8080, gRPC on :50050. Control-plane state (jobs, API
keys, sessions, users, encrypted secrets) persists to Postgres; graphs
and sandboxes persist to the `hzddata` volume.

Common follow-ups:

```sh
docker compose logs -f hzd
docker compose down      # stop
```

### Optional marketing landing

By default `/` serves the app to everyone (a logged-out visitor lands on
the sign-in screen). To front the install with the marketing site from
the separate `hazy-flow-landing` repo, mount it and point
`HAZYFLOW_LANDING_DIR` at the mount: uncomment the `:/srv/landing:ro`
volume in `docker-compose.yml` and set `HAZYFLOW_LANDING_DIR=/srv/landing`
in `.env`. `GET /` then becomes auth-gated — anonymous visitors get the
landing page, signed-in users keep their dashboard — and `/pricing`,
`/privacy`, `/terms`, and the landing assets serve publicly. Leave it
unset for a private self-host install.

## Going to production

`hzd` does not terminate TLS. Run it behind a TLS terminating reverse
proxy (nginx, Caddy, Traefik, or an ingress) and set, in `.env`:
`HAZYFLOW_TRUST_PROXY_HEADERS=1`, `HAZYFLOW_WEB_ORIGIN=https://your.domain`,
and `HAZYFLOW_PUBLIC_BASE_URL=https://your.domain`.

See **[DEPLOY.md](DEPLOY.md)** for the full reference:

- the reverse-proxy contract and a worked nginx example,
- durability, the master key, and backup and restore,
- security knobs (auth rate limiting, egress allowlist),
- observability (health probes, metrics, OpenTelemetry tracing),
- human-approval links.

## Marketplace: drops & integrations

A **drop** is a node you can drop on the canvas (a connector, transform, or
action); an **integration** is a connection prerequisite a drop depends on
(e.g. `gmail`, `slack`). Besides the built-ins, a platform admin can install
more at runtime from the web UI — **Admin → Marketplace** (`/admin/marketplace`),
platform-admin only — by pointing at a git repo:

- **Install an integration** from a repo's `integration.json`, then connect
  accounts via the OAuth flow.
- **Install a drop** from a repo path (`repo`, `ref`, `path`). Installs are
  persisted and restored on boot. A drop is gated on its required integrations:
  install the integration first or the drop install is refused.

Git fetches are pinned to the resolved commit and routed through the SSRF guard
(only `https`/`ssh`/local schemes; private/loopback addresses are blocked).

**Versioning & uninstall.** Drops are versioned; a graph node refers to a drop
by bare id (`gmail_send_email`, tracks the latest installed version) or pins an
exact version (`gmail_send_email@2.0.0`), so re-installing a newer version can't
silently change a flow that pinned the old one. Uninstalling an integration is
refused while any installed drop version still requires it — uninstall the
dependent drop first.

### Trust tiers

Every install is shown as **official**, **verified**, or **community**. The tier
is *derived from a signature*, never self-declared — a drop cannot mark itself
official. A repo ships a detached `<file>.sig` (Ed25519) next to each signed
artifact; the daemon verifies it over the exact bytes against the keys in
`HAZYFLOW_TRUSTED_KEYS` (boot config, not runtime-editable — it's the root of
trust):

```
HAZYFLOW_TRUSTED_KEYS="id:tier:publisher:base64key;…"   # tier = official | verified
```

Unsigned or unknown-key artifacts install as **community**. Reserved built-in
provider ids (`google`, `slack`, `github`, `notion`) can only be claimed by a
signed official/verified manifest, so a community drop can't shadow a built-in
provider.

### Authoring & publishing

Drops are authored in TypeScript against
`engine/jsdrop/sdk/hazyflow-drop.d.ts` (integrations against
`hazyflow-integration.d.ts`); see `engine/jsdrop/sdk/examples/` for the Gmail
and Slack connectors and **[engine/jsdrop/DESIGN.md](engine/jsdrop/DESIGN.md)**
for the capability surface and runtime. To run local drops in dev, point
`HAZYFLOW_SCRIPTED_DROPS_DIR` at a directory of `.ts` files.

To publish a signed, official-tier repo, use the `hz-drops` tool:

```sh
# one-time: generate a signing keypair (keep the .key secret; never commit it)
go run ./cmd/hz-drops keygen --id hazy-official --publisher "Hazy Flow" --out .keys

# sign each artifact → writes <file>.sig
go run ./cmd/hz-drops sign --key .keys/hazy-official.key --id hazy-official drops/*.ts
```

`scripts/publish-official-drops.sh` wraps this end to end — it signs the example
drops into a standalone git repo and prints the `HAZYFLOW_TRUSTED_KEYS` entry to
configure on the daemon. The private key is the authority to mint official
drops: generate it on a trusted machine and keep it in a secret manager/HSM.

## Secrets

Out of the box, secrets are held in the **built-in encrypted store** — flows
reference them as `${secret:NAME}` (the `tenant://` provider), values are
AES-256-GCM encrypted under a per-tenant key wrapped by `HAZYFLOW_MASTER_KEY`,
and the UI is write-only (you never read a value back). That's the zero-infra
default; no external dependency.

### Bring your own secret manager (OpenBao / Vault)

An org that already runs **OpenBao** or **HashiCorp Vault** can point the
platform at it instead, and reference its secrets in flows as
`${vault:PATH#FIELD}` (e.g. `${vault:stripe#api_key}` reads field `api_key` of
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

## Kubernetes (multi node)

`deploy/k8s/hazyflow.yaml` is a 2-replica Deployment, Service, and Secret
template. Multi-replica works out of the box: a Postgres event bus lets
any pod stream a run's events, and a Postgres advisory-lock leader makes
sure only one pod fires each schedule. Steps are in
[deploy/README.md](deploy/README.md).

## Run locally for development

Postgres is optional in dev (state falls back to in-memory or a JSON
file, and the daemon logs a warning).

```sh
make env         # seed .env from .env.example (one-time)
make dev         # boots hzd; sources .env, falls back to a minimal dev
                 # set when .env is absent. http://localhost:8080
```

`make env` seeds `.env` with the bundled Postgres DSN, which trips the
fail-closed config guard. For local dev either blank
`HAZYFLOW_POSTGRES_DSN` (in-memory fallback, no guard) or set
`HAZYFLOW_DEV=1`.

In another terminal:

```sh
make web         # vite dev server on http://localhost:5173
```

`make env` writes a fresh `.env` for you. Edit it to taste — the dev
defaults turn on signup and mint a throwaway API key on every boot
(`HAZYFLOW_DEV_KEY=1`). Do not set `HAZYFLOW_DEV_KEY=1` outside local
development.
