# Dazyflow — common tasks. `make up` boots the Docker Compose stack
# (Postgres + dzd); the rest cover its lifecycle and local dev.

COMPOSE ?= docker compose

# Version metadata stamped into the binary (see core/buildinfo). Computed
# from git so every build carries the same identity: the native `make
# bin`, the Compose image, and CI all read these. Exported so the
# `docker compose` invoked by up/build/restart/rebuild picks them up as
# build args (docker-compose.yml maps them onto the Dockerfile ARGs).
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
export VERSION COMMIT BUILD_DATE

# Linker flags for the native `make bin` build. -s -w strip the symbol
# and DWARF tables; the -X flags inject the version vars into buildinfo.
LDFLAGS := -s -w \
  -X git.sr.ht/~klahr/dazyflow/core/buildinfo.Version=$(VERSION) \
  -X git.sr.ht/~klahr/dazyflow/core/buildinfo.Commit=$(COMMIT) \
  -X git.sr.ht/~klahr/dazyflow/core/buildinfo.Date=$(BUILD_DATE)

.DEFAULT_GOAL := help

.PHONY: help up down restart logs ps build rebuild env pg pg-down dev web test vet fmt check ci \
        bin version major minor patch _bump upgrade

help: ## List targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sort | \
		awk -F':.*?## ' '{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

## --- Docker Compose stack ---

up: ## Start the stack (Postgres + dzd) detached on http://localhost:8080
	$(COMPOSE) up -d

down: ## Stop the stack (named volumes persist)
	$(COMPOSE) down

restart: ## Recreate the daemon with the latest config/code
	$(COMPOSE) up -d --build dzd

logs: ## Follow the daemon logs
	$(COMPOSE) logs -f dzd

ps: ## Show stack status
	$(COMPOSE) ps

build: ## Build the daemon image
	$(COMPOSE) build dzd

rebuild: ## Rebuild the image from scratch (no layer cache)
	$(COMPOSE) build --no-cache dzd

env: ## Sync .env with .env.example (creates one if missing; appends new keys; never overwrites existing values)
	@./scripts/sync-env.sh

## --- Local development (containers for Postgres only) ---

pg: ## Start (and wait for) just the bundled Postgres on 127.0.0.1:5432 — `make dev` needs it
	$(COMPOSE) up -d --wait postgres

pg-down: ## Stop the bundled dev Postgres (data persists in the pgdata volume)
	$(COMPOSE) stop postgres

# .env is shared with the Compose stack, where the DSN host is the
# `postgres` service name. That hostname only resolves inside the Compose
# network, so for the native run we rewrite it to localhost (the bundled
# Postgres publishes 127.0.0.1:5432 exactly for this). The containerized
# dzd is stopped first — both want :8080. `make restart` brings it back.
dev: pg ## Run dzd locally against the bundled Postgres (make pg). Sources .env when present (DSN host rewritten to localhost), else a minimal dev set.
	@$(COMPOSE) stop dzd >/dev/null 2>&1 || true
	@if [ -f .env ]; then \
		set -a; . ./.env; set +a; \
		DAZYFLOW_POSTGRES_DSN=$$(printf '%s' "$$DAZYFLOW_POSTGRES_DSN" | sed 's/@postgres:/@localhost:/') \
		DAZYFLOW_DEV=1 \
		DAZYFLOW_HTTP=:8080 go run ./cmd/dzd; \
	else \
		DAZYFLOW_HTTP=:8080 \
		DAZYFLOW_DEV=1 \
		DAZYFLOW_DEV_KEY=1 \
		DAZYFLOW_ENABLE_SIGNUP=1 \
		DAZYFLOW_WEB_ORIGIN=http://localhost:5173 \
		DAZYFLOW_PUBLIC_BASE_URL=http://localhost:5173 \
		DAZYFLOW_MASTER_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= \
		DAZYFLOW_POSTGRES_DSN=postgres://dazyflow:dazyflow@localhost:5432/dazyflow?sslmode=disable \
		go run ./cmd/dzd; \
	fi

web: ## Run the Vite dev server (http://localhost:5173)
	cd web && npm install && npm run dev

test: ## Run the Go test suite with the race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format Go sources
	gofmt -w .

## --- Build & release ---
# The Compose targets above (build/rebuild/up/restart) already stamp the
# image: VERSION/COMMIT/BUILD_DATE are exported, so `docker compose` reads
# them as build args. These targets cover the native binary and tagging.

bin: ## Build a stamped native dzd binary (CGO off, trimmed, stripped)
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o dzd ./cmd/dzd
	@ls -lh dzd

version: ## Print the version that a build would stamp right now
	@echo "version=$(VERSION) commit=$(COMMIT) date=$(BUILD_DATE)"

# major/minor/patch cut an annotated release tag, bumping from the latest
# existing tag (0.0.0 if none yet). They delegate to the shared _bump
# recipe so the semver arithmetic lives in one place.
#
# Update CHANGELOG.md FIRST — move the [Unreleased] entries under a new
# [X.Y.Z] - YYYY-MM-DD heading and commit — so the tag points at the
# commit where the changelog announces the version. The tag is local;
# the recipe prints the push command.
#
#   make patch   0.1.0 -> 0.1.1
#   make minor   0.1.0 -> 0.2.0
#   make major   0.1.0 -> 1.0.0
major: ## Cut a major release tag (X+1.0.0)
	@$(MAKE) --no-print-directory _bump BUMP=major
minor: ## Cut a minor release tag (x.Y+1.0)
	@$(MAKE) --no-print-directory _bump BUMP=minor
patch: ## Cut a patch release tag (x.y.Z+1)
	@$(MAKE) --no-print-directory _bump BUMP=patch

_bump:
	@CUR=$$(git describe --tags --abbrev=0 2>/dev/null || echo 0.0.0); \
	MAJOR=$$(echo "$$CUR" | cut -d. -f1); \
	MINOR=$$(echo "$$CUR" | cut -d. -f2); \
	PATCH=$$(echo "$$CUR" | cut -d. -f3); \
	case "$(BUMP)" in \
		major) MAJOR=$$((MAJOR + 1)); MINOR=0; PATCH=0 ;; \
		minor) MINOR=$$((MINOR + 1)); PATCH=0 ;; \
		patch) PATCH=$$((PATCH + 1)) ;; \
		*) echo "Use: make major|minor|patch"; exit 1 ;; \
	esac; \
	NEW="$$MAJOR.$$MINOR.$$PATCH"; \
	git tag -a "$$NEW" -m "Release $$NEW"; \
	echo "$$CUR -> $$NEW"; \
	echo "Push with: git push origin master $$NEW"

upgrade: ## Check out the latest release tag, rebuild the stack, return to master
	git fetch --tags
	@LATEST=$$(git tag --sort=-v:refname | head -1); \
	if [ -z "$$LATEST" ]; then \
		echo "No release tags yet — nothing to upgrade to."; exit 1; \
	fi; \
	echo "Upgrading to $$LATEST"; \
	git checkout "$$LATEST"; \
	VERSION="$$LATEST" \
	COMMIT="$$(git rev-parse --short HEAD)" \
	BUILD_DATE="$$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
	$(COMPOSE) up -d --build; \
	git checkout master

## --- Gates (run locally; CI on builds.sr.ht is advisory, not blocking) ---
# These mirror .build.yml so a push never lands red. gofmt is intentionally
# NOT a gate: CI doesn't enforce it and the tree carries pre-existing
# gofmt-version drift, so a gofmt gate would fail on files unrelated to the
# change. Run `make fmt` before committing instead.

check: ## Fast local gate before pushing: build, vet, tests
	@echo "==> go build"; go build ./...
	@echo "==> go vet"; go vet ./...
	@echo "==> go test"; go test ./...

ci: ## Full local mirror of CI (.build.yml): build, vet, race tests, web build
	@echo "==> go build"; go build ./...
	@echo "==> go vet"; go vet ./...
	@echo "==> go test -race"; go test -race ./...
	@echo "==> web build"; cd web && npm ci && npm run build
