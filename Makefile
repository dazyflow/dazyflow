# Hazyflow — common tasks. `make up` boots the Docker Compose stack
# (Postgres + hzd); the rest cover its lifecycle and local dev.

COMPOSE ?= docker compose

.DEFAULT_GOAL := help

.PHONY: help up down restart logs ps build rebuild env pg pg-down dev web test vet fmt check ci

help: ## List targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sort | \
		awk -F':.*?## ' '{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

## --- Docker Compose stack ---

up: ## Start the stack (Postgres + hzd) detached on http://localhost:8080
	$(COMPOSE) up -d

down: ## Stop the stack (named volumes persist)
	$(COMPOSE) down

restart: ## Recreate the daemon with the latest config/code
	$(COMPOSE) up -d --build hzd

logs: ## Follow the daemon logs
	$(COMPOSE) logs -f hzd

ps: ## Show stack status
	$(COMPOSE) ps

build: ## Build the daemon image
	$(COMPOSE) build hzd

rebuild: ## Rebuild the image from scratch (no layer cache)
	$(COMPOSE) build --no-cache hzd

env: ## Sync .env with .env.example (creates one if missing; appends new keys; never overwrites existing values)
	@./scripts/sync-env.sh

## --- Local development (containers for Postgres only) ---

pg: ## Start (and wait for) just the bundled Postgres on 127.0.0.1:5432 — `make dev` needs it
	$(COMPOSE) up -d --wait postgres

pg-down: ## Stop the bundled dev Postgres (data persists in the pgdata volume)
	$(COMPOSE) stop postgres

dev: pg ## Run hzd locally against the bundled Postgres (make pg). Sources .env when present, else a minimal dev set pointed at localhost:5432.
	@if [ -f .env ]; then \
		set -a; . ./.env; set +a; \
		HAZYFLOW_HTTP=:8080 go run ./cmd/hzd; \
	else \
		HAZYFLOW_HTTP=:8080 \
		HAZYFLOW_DEV=1 \
		HAZYFLOW_DEV_KEY=1 \
		HAZYFLOW_ENABLE_SIGNUP=1 \
		HAZYFLOW_WEB_ORIGIN=http://localhost:5173 \
		HAZYFLOW_PUBLIC_BASE_URL=http://localhost:5173 \
		HAZYFLOW_MASTER_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= \
		HAZYFLOW_POSTGRES_DSN=postgres://hazyflow:hazyflow@localhost:5432/hazyflow?sslmode=disable \
		go run ./cmd/hzd; \
	fi

web: ## Run the Vite dev server (http://localhost:5173)
	cd web && npm install && npm run dev

test: ## Run the Go test suite with the race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format Go sources
	gofmt -w .

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
