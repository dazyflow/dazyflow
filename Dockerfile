# syntax=docker/dockerfile:1

# Multi-stage build for the Hazy Flow daemon + web bundle.
#
#   1. web   — build the React/Vite bundle to /web/dist
#   2. build — compile the static hzd binary (CGO off)
#   3. final — distroless nonroot runtime with the binary + bundle
#
# The daemon serves the bundle from the same port as the API when run
# with --web-dist /srv/web (see cmd/hzd --web-dist).

# ---- 1. web bundle ----------------------------------------------------
FROM node:22-alpine AS web
WORKDIR /web
# Install deps against the lockfile first so this layer caches unless
# package.json / lock change.
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- 2. Go build ------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src
# Module graph first for layer caching.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static, stripped binary. CGO is off (pure-Go pgx + go-git, no sqlite
# in the daemon path) so it runs on distroless static-nonroot.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/hzd ./cmd/hzd
# Pre-create the data dirs here (the distroless final stage has no shell
# to mkdir) so the default CMD's --workspace-dir/--sandbox-base paths
# exist and are writable by the nonroot user.
RUN mkdir -p /data/workspace /data/sandbox

# ---- 3. runtime -------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS final
WORKDIR /srv
COPY --from=build /out/hzd /usr/local/bin/hzd
COPY --from=web /web/dist /srv/web
# Workspace + sandbox dirs live under /data, owned by the nonroot uid
# (65532). Mount a volume here in production so git-backed graphs and
# per-tenant sandboxes persist across container restarts. (Use
# --postgres-dsn for the durable control-plane stores.)
COPY --from=build --chown=65532:65532 /data /data
EXPOSE 50050 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/hzd"]
# Sensible container defaults; override at `docker run`. Serves the API +
# bundle on :8080, gRPC on :50050. Provide --postgres-dsn + --master-key
# for a durable, real deployment.
CMD ["--http=:8080", "--web-dist=/srv/web", "--workspace-dir=/data/workspace", "--sandbox-base=/data/sandbox"]
