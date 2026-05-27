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
