# syntax=docker/dockerfile:1

# Multi-stage build for the Hazyflow daemon + web bundle.
#
#   1. web   — build the React/Vite bundle to /web/dist
#   2. build — compile the static hzd binary (CGO off)
#   3. final — distroless nonroot runtime with the binary + bundle
#
# The daemon serves the bundle from the same port as the API when
# HAZYFLOW_WEB_DIST is set (we set it to /srv/web below).

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
# Pin the exact patch that go.mod's `go 1.26.3` line requires. With the
# floating `golang:1.26-alpine` tag, a builder whose Go is older than
# 1.26.3 would (under the default GOTOOLCHAIN=auto) try to DOWNLOAD the
# 1.26.3 toolchain during `go mod download` — which fails in a
# network-restricted prod build. GOTOOLCHAIN=local forbids that implicit
# fetch so any future drift fails loud at build time instead.
FROM golang:1.26.3-alpine AS build
ENV GOTOOLCHAIN=local
# Optional Go module proxy. The build fetches dependencies from here at
# `go mod download`; when the build host can't reach the public
# proxy.golang.org, point this at a reachable internal mirror:
#   docker compose build --build-arg GOPROXY=https://goproxy.internal
# Unset keeps the standard public default, so this is a no-op locally.
# (Corporate HTTP_PROXY/HTTPS_PROXY/NO_PROXY are well-known buildkit
# args and reach RUN steps automatically — no Dockerfile change needed.)
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
# Module graph first for layer caching. The cache mount keeps the
# downloaded module tree across builds so a go.mod/go.sum change
# re-resolves rather than re-fetches the whole graph.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
# Static, stripped binary. CGO is off (pure-Go pgx + go-git, no sqlite
# in the daemon path) so it runs on distroless static-nonroot.
#
# The two cache mounts are the difference between a 2-3 minute build and
# a few seconds: /root/.cache/go-build persists the COMPILED-package
# cache (the ~660-package graph: go-git, pgx, gRPC, otel, protobuf)
# across builds, so only changed packages recompile. Without it every
# `docker build` recompiles the whole graph cold. (/go/pkg/mod is shared
# with the download step above.) Cache mounts are local to the builder —
# add registry cache-to/cache-from for ephemeral CI runners.
ENV CGO_ENABLED=0 GOOS=linux
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/hzd ./cmd/hzd
RUN mkdir -p /data/workspace /data/sandbox /data/state

# ---- 3. runtime -------------------------------------------------------
# Scripted drops run in the Node drop host (engine/containerdrop/nodehost),
# so the runtime image needs `node`. The default ("process") tier spawns node
# directly; the "gvisor" tier instead runs node inside a runsc container and
# needs a Docker socket + the runsc runtime on the host (out of image scope).
FROM node:22-alpine AS final
WORKDIR /srv
COPY --from=build /out/hzd /usr/local/bin/hzd
# drophost.mjs sits next to hzd so resolveNodeDropHost() finds it (it looks
# beside the running executable). It's our trusted runtime, not a drop.
COPY engine/containerdrop/nodehost/drophost.mjs /usr/local/bin/drophost.mjs
COPY --from=web /web/dist /srv/web
# Workspace + sandbox dirs live under /data, owned by the unprivileged `node`
# user (uid 1000, shipped in the node image). Mount a volume here in production
# so git-backed graphs and per-tenant sandboxes persist across container
# restarts. (Set HAZYFLOW_POSTGRES_DSN for the durable control-plane stores.)
COPY --from=build --chown=1000:1000 /data /data
EXPOSE 50050 8080
USER node
# Container layout defaults — every other knob is configured via
# HAZYFLOW_* env vars on the container (see .env.example for the full
# catalogue). Override these here only when rebaking the image.
ENV HAZYFLOW_HTTP=:8080 \
    HAZYFLOW_WEB_DIST=/srv/web \
    HAZYFLOW_DATA_DIR=/data
ENTRYPOINT ["/usr/local/bin/hzd"]
