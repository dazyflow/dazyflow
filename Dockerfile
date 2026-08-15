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
# Version stamping. These default to "dev"/"unknown" so a bare
# `docker build` still produces a runnable image; the Makefile
# (build/up/restart/rebuild) and CI pass real values computed from git
# via --build-arg. They flow into the linker -X flags below and surface
# at runtime on GET /api/v1 and in the startup log (see core/buildinfo).
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
# Static, stripped binary. CGO is off (pure-Go pgx + go-git, no sqlite
# in the daemon path), so the binary has no libc dependency and would run
# on a scratch/distroless base as-is. The runtime stage below is Alpine
# anyway — a shell and apk are worth the few MB for exec-into-the-container
# debugging — but nothing in the build depends on that choice.
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
    go build -trimpath -ldflags="-s -w \
      -X git.sr.ht/~klahr/dazyflow/core/buildinfo.Version=${VERSION} \
      -X git.sr.ht/~klahr/dazyflow/core/buildinfo.Commit=${COMMIT} \
      -X git.sr.ht/~klahr/dazyflow/core/buildinfo.Date=${BUILD_DATE}" \
      -o /out/dzd ./cmd/dzd
RUN mkdir -p /data/workspace /data/sandbox /data/state

# ---- 3. runtime -------------------------------------------------------
# dzd is a self-contained Go binary — every drop (connectors included) is
# native Go now, so the runtime image needs no Node. Just the binary, CA roots
# for outbound HTTPS to vendor APIs, and the web assets.
#
# Pinned by tag AND digest, for the same reason the build stages are pinned:
# `alpine:latest` floats, so two builds a month apart silently ship different
# base layers — and this is the stage that actually reaches production, where
# an unreviewed base bump is hardest to notice and most expensive to debug.
# The digest is what's enforced; the tag beside it is there so a human reading
# this line knows which release they're looking at.
#
# To bump: pick the new release, then resolve its digest with
#   docker pull alpine:<tag> && docker inspect alpine:<tag> --format '{{index .RepoDigests 0}}'
# and update both halves together. A tag/digest pair that disagrees still
# builds — Docker resolves the digest and ignores the tag — so they only stay
# in sync if you change them as a unit.
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
