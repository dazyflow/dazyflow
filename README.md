# Hazy Flow

A workflow automation engine. `hzd` is a Go daemon that runs graph based
flows (connectors, transforms, AI steps, branching, schedules and
webhooks) and serves a web UI for building and watching them.

This README covers how to deploy it. The marketing site lives in the
separate `hazy-flow-landing` repo.

## Deploy with Docker Compose

The quickest durable setup is the daemon plus Postgres, via
`deploy/docker-compose.yml`. You need Docker with the Compose plugin.

```sh
# A stable 32-byte key that encrypts stored secrets. Keep a sealed
# backup: losing it makes every stored secret undecryptable.
export HAZYFLOW_MASTER_KEY=$(openssl rand -base64 32)

docker compose -f deploy/docker-compose.yml up --build -d
```

This brings up Postgres and the daemon. The API and web UI are on
http://localhost:8080, gRPC on :50050, and Prometheus metrics on
`/metrics`. Control-plane state (jobs, API keys, sessions, users,
encrypted secrets) persists to Postgres; graphs and sandboxes persist to
the `hzddata` volume.

Common follow-ups:

```sh
docker compose -f deploy/docker-compose.yml logs -f hzd
docker compose -f deploy/docker-compose.yml down      # stop
```

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
