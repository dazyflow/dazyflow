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
cp .env.example .env   # optional — defaults boot as-is
docker compose up -d
```

Everything is configured through `.env` (see `.env.example` for the full
list of knobs). The defaults boot out of the box; before pointing real
users at the box, set at least `HAZYFLOW_MASTER_KEY` (a stable 32-byte
key that encrypts stored secrets — `openssl rand -base64 32`, keep a
sealed backup), `POSTGRES_PASSWORD`, and your public origin.

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
proxy (nginx, Caddy, Traefik, or an ingress) and start it with
`--trust-proxy-headers`, `--web-origin`, and `--public-base-url`.

See **[DEPLOY.md](DEPLOY.md)** for the full reference:

- the reverse-proxy contract and a worked nginx example,
- durability, the master key, and backup and restore,
- security flags (auth rate limiting, egress allowlist),
- observability (health probes, metrics, OpenTelemetry tracing),
- human-approval links.

## Kubernetes (multi node)

`deploy/k8s/hazyflow.yaml` is a 2-replica Deployment, Service, and Secret
template. Multi-replica works out of the box: a Postgres event bus lets
any pod stream a run's events, and a Postgres advisory-lock leader makes
sure only one pod fires each schedule. Steps are in
[deploy/README.md](deploy/README.md).

## Run locally for development

Postgres is optional in dev (state falls back to in-memory or a JSON
file, and the daemon logs a warning). In one terminal:

```sh
go run ./cmd/hzd --http :8080 --dev-key --signup --web-origin http://localhost:5173
```

In another, start the web dev server:

```sh
cd web
npm install
npm run dev      # http://localhost:5173
```

Do not use `--dev-key` outside local development.
