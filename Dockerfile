# syntax=docker/dockerfile:1

# Multi-stage build for the Dazyflow daemon + web bundle.
#
#   1. web   — build the React/Vite bundle to /web/dist
#   2. build — compile the static dzd binary (CGO off)
#   3. final — pinned Alpine runtime, nonroot, with the binary + bundle
#
# The daemon serves the bundle from the same port as the API when
# DAZYFLOW_WEB_DIST is set (we set it to /srv/web below).

# ---- 1. web bundle ----------------------------------------------------
FROM node:26-alpine AS web
WORKDIR /web
# Install deps against the lockfile first so this layer caches unless
# package.json / lock change.
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- 2. Go build ------------------------------------------------------
# Pin the exact patch that go.mod's `go 1.26.7` line requires. With the
# floating `golang:1.26-alpine` tag, a builder whose Go is older than
# 1.26.7 would (under the default GOTOOLCHAIN=auto) try to DOWNLOAD the
# 1.26.7 toolchain during `go mod download` — which fails in a
# network-restricted prod build. GOTOOLCHAIN=local forbids that implicit
# fetch so any future drift fails loud at build time instead.
FROM golang:1.27.0-alpine AS build
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
# Version stamping. The Makefile and CI pass real values from git via
# --build-arg; they flow into the linker -X flags and surface on GET /api/v1
# (see core/buildinfo). The defaults are EMPTY, not "dev", so the build step can
# fall back to the committed ./VERSION file when the caller said nothing. That
# keeps a bare `docker compose up --build` (the documented production deploy)
# from shipping an image that reports itself as "dev"; `.git` is
# .dockerignore'd, so the file is the only in-context record of the release.
ARG VERSION=
ARG COMMIT=unknown
ARG BUILD_DATE=
# Static, stripped binary. CGO is off (pure-Go pgx + go-git), so it has no libc
# dependency; the Alpine runtime below is a debugging convenience, not a need.
# The cache mounts persist the compiled-package cache across builds so only
# changed packages recompile (2-3 minutes cold vs seconds warm). They are local
# to the builder; add registry cache-to/cache-from for ephemeral CI runners.
ENV CGO_ENABLED=0 GOOS=linux
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    V="${VERSION:-$(cat VERSION 2>/dev/null || echo dev)}"; \
    D="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"; \
    echo "stamping version=$V commit=${COMMIT} date=$D"; \
    go build -trimpath -ldflags="-s -w \
      -X github.com/dazyflow/dazyflow/core/buildinfo.Version=${V} \
      -X github.com/dazyflow/dazyflow/core/buildinfo.Commit=${COMMIT} \
      -X github.com/dazyflow/dazyflow/core/buildinfo.Date=${D}" \
      -o /out/dzd ./cmd/dzd
RUN mkdir -p /data/workspace /data/sandbox /data/state

# ---- 3. runtime -------------------------------------------------------
# Just the binary, CA roots for outbound HTTPS, and the web assets.
#
# Pinned by tag AND digest: `alpine:latest` floats, and this is the stage that
# reaches production. The digest is what is enforced; the tag is for the
# reader. To bump, resolve the new digest with
#   docker pull alpine:<tag> && docker inspect alpine:<tag> --format '{{index .RepoDigests 0}}'
# and update both halves together (a mismatched pair still builds).
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS final
RUN apk add --no-cache ca-certificates && adduser -D -u 1000 dazyflow
WORKDIR /srv
COPY --from=build /out/dzd /usr/local/bin/dzd
COPY --from=web /web/dist /srv/web
# Workspace + sandbox dirs live under /data, owned by the unprivileged
# `dazyflow` user (uid 1000). Mount a volume here in production so git-backed
# graphs and per-tenant sandboxes persist across container restarts. (Set
# DAZYFLOW_POSTGRES_DSN for the durable control-plane stores.)
COPY --from=build --chown=1000:1000 /data /data
EXPOSE 50050 8080
USER dazyflow
# Container layout defaults — every other knob is configured via
# DAZYFLOW_* env vars on the container (see .env.example for the full
# catalogue). Override these here only when rebaking the image.
ENV DAZYFLOW_HTTP=:8080 \
    DAZYFLOW_WEB_DIST=/srv/web \
    DAZYFLOW_DATA_DIR=/data
ENTRYPOINT ["/usr/local/bin/dzd"]
