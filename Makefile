# Hazy Flow — common tasks. `make up` boots the Docker Compose stack
# (Postgres + hzd); the rest cover its lifecycle and local dev.

COMPOSE ?= docker compose

.DEFAULT_GOAL := help

.PHONY: help up down restart logs ps build rebuild env dev web test vet fmt

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

env: ## Create .env from the template if it doesn't exist
	@test -f .env || (cp .env.example .env && echo "wrote .env — edit it, then 'make up'")

## --- Local development (no containers) ---

dev: ## Run hzd locally against in-memory stores (dev key + signup)
	# Everything goes through HAZYFLOW_* env vars now. The fixed dev
	# master key turns on the per-tenant encrypted secret store so the
	# BYO Anthropic chat flow works locally. Never use this key in
	# production.
	HAZYFLOW_HTTP=:8080 \
	HAZYFLOW_DEV_KEY=1 \
	HAZYFLOW_ENABLE_SIGNUP=1 \
	HAZYFLOW_WEB_ORIGIN=http://localhost:5173 \
	HAZYFLOW_MASTER_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= \
	go run ./cmd/hzd

web: ## Run the Vite dev server (http://localhost:5173)
	cd web && npm install && npm run dev

test: ## Run the Go test suite with the race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format Go sources
	gofmt -w .
