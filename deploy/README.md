# Deploying Hazy Flow

Two ready-to-use paths. Both run the daemon durably (control-plane state
in Postgres) — see `../DEPLOY.md` for the full flag/TLS/backup reference.

## docker-compose (single node)

```sh
export HAZYFLOW_MASTER_KEY=$(openssl rand -base64 32)   # keep a sealed backup
docker compose -f docker-compose.yml up --build
```

Brings up Postgres + the daemon; API + web UI on http://localhost:8080,
gRPC on :50050, Prometheus metrics on `/metrics`.

## Kubernetes (multi-node)

`k8s/hazyflow.yaml` is a Deployment (2 replicas), Service, and a Secret
template. Multi-replica is correct out of the box: the Postgres event bus
lets any pod stream a run's events, and a Postgres advisory-lock leader
ensures only one pod fires each schedule (`PgBus` + `PgLeader`).

1. Edit the `hazyflow-secrets` Secret: a fresh `HAZYFLOW_MASTER_KEY`
   (`openssl rand -base64 32`) and your managed-Postgres `HAZYFLOW_POSTGRES_DSN`.
2. Build + push the image (`docker build -t hazyflow/hzd:latest ..`) and
   set it in the Deployment.
3. `kubectl apply -f k8s/hazyflow.yaml`, then front it with an ingress that
   terminates TLS and forwards `Host`/`Origin` (the args already set
   `--trust-proxy-headers` + `--web-origin` / `--public-base-url` — update
   the hostnames).

Probes: liveness `/healthz`, readiness `/readyz` (pulls a pod from the
Service when Postgres is unreachable). The gRPC health service on :50050
is available for a `grpc_health_probe` sidecar if preferred.
