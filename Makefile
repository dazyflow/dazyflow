# Dazyflow — common tasks. `make up` boots the Docker Compose stack
# (Postgres + dzd); the rest cover its lifecycle and local dev.

COMPOSE ?= docker compose

# PROD=1 merges the production overlay (Caddy + docs) into every stack target
# below. Without it compose runs docker-compose.yml alone and auto-merges
# docker-compose.override.yml (the DEV override: Postgres TLS off) if present —
# on a prod host that isn't cosmetic, it drops the TLS terminator and the docs
# site.
#
# PROD is the ad-hoc lever: it targets the prod file set for ONE command, from
# any checkout, touching no config. It is NOT the best way to mark a permanent
# production host, because it has to be remembered on every invocation and it
# only reaches make — a bare `docker compose up -d --build` still gets the
# default resolution. A production host is better off setting compose's own
# variable once, in .env:
#
#   COMPOSE_FILE=docker-compose.yml:docker-compose.prod.yml
#
# which every compose call honours (make included, bare compose included) and
# which also stops the dev override being merged. With that set, leave PROD
# unset; passing both is harmless (same two files, one via -f).
#
# `override` so this still applies when COMPOSE itself is passed on the command
# line, which would otherwise win over a plain assignment.
ifdef PROD
override COMPOSE += -f docker-compose.yml -f docker-compose.prod.yml
endif

# Content dir the docs SPA bundles (web/src/docs/content): the guide pages are
# copied from docs/guide/, the step catalog is generated there by cmd/docsgen.
DOCS_CONTENT_OUT ?= web/src/docs/content

# Version metadata stamped into the binary (see core/buildinfo). Computed
# from git so every build carries the same identity: the native `make
# bin`, the Compose image, and CI all read these. Exported so the
# `docker compose` invoked by up/build/restart/rebuild picks them up as
# build args (docker-compose.yml maps them onto the Dockerfile ARGs).
#
# git describe is preferred (it distinguishes a tagged release from N
# commits past it, and flags a dirty tree); ./VERSION is the fallback for
# a build from a tarball or a shallow/tagless clone, and is also what the
# Docker build reads when compose is invoked WITHOUT these exports — see
# the Dockerfile ARGs. The release targets below keep the file in step
# with the tag.
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || cat VERSION 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
export VERSION COMMIT BUILD_DATE

# Linker flags for the native `make bin` build. -s -w strip the symbol
# and DWARF tables; the -X flags inject the version vars into buildinfo.
LDFLAGS := -s -w \
  -X github.com/dazyflow/dazyflow/core/buildinfo.Version=$(VERSION) \
  -X github.com/dazyflow/dazyflow/core/buildinfo.Commit=$(COMMIT) \
  -X github.com/dazyflow/dazyflow/core/buildinfo.Date=$(BUILD_DATE)

.DEFAULT_GOAL := help

.PHONY: help up down restart logs ps build rebuild env pg pg-down dev web test vet fmt fmt-check env-check check ci \
	runner-embed runner-test \
        integration-catalog drop-catalog catalogs catalogs-check check-changelog flowgen-eval \
        links-check \
        docs-content docs-site docs-dev bin version latest major minor patch _bump upgrade

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

# The race detector multiplies runtime, and the daemon package (the HTTP API,
# the dispatcher, the worker, and their end-to-end tests) runs well past Go's
# default 10-minute per-package ceiling under it — the whole suite failed on a
# timeout, not on a test. The limit is per package, so this is headroom for the
# slowest one, not a licence for a slow suite.
GO_TEST_TIMEOUT ?= 30m

test: ## Run the Go test suite with the race detector
	go test -race -timeout $(GO_TEST_TIMEOUT) ./...

runner-embed: ## Refresh the copies of the runner agent the daemon serves
	cp runner/dzrunner.py daemon/embed/dzrunner.py
	cp runner/runner.sh daemon/embed/runner.sh
	@echo "refreshed daemon/embed from runner/"

runner-test: ## Test the runner agent and its installer (python3, no dependencies)
	cd runner && python3 -m unittest discover -v

integration-catalog: ## Refresh the list of apps the description guard checks (run after adding a connector)
	go run ./scripts/integrations.go > web/src/integrationMeta.catalog.json
	@echo "wrote web/src/integrationMeta.catalog.json"

drop-catalog: ## Refresh the drop text the Swedish coverage guard checks (run after adding or rewording a drop)
	go run ./scripts/droptext.go > web/src/i18n/drops/catalog.json
	@echo "wrote web/src/i18n/drops/catalog.json"

catalogs: integration-catalog drop-catalog ## Refresh both generated catalogues the web guards read

# The web guards check the Swedish against these committed snapshots, so a
# snapshot that is itself out of date makes them pass vacuously: add a drop,
# forget `make drop-catalog`, and "every drop has a description" is true only
# of the drops the snapshot remembers. Regenerating and diffing is what makes
# the guards mean anything. CI runs this before the web task.
#
# Deliberately does NOT regenerate in place and ask git what changed: that
# couples the answer to whether you happen to have committed yet — a correct
# but uncommitted catalogue reads as stale, and a brand-new untracked one
# reads as clean, which is backwards. Regenerating into a temp dir and diffing
# answers the actual question ("would regenerating change anything?") in any
# checkout state, and prints the drift instead of merely asserting it.
catalogs-check: ## Fail if a generated catalogue is out of date (CI)
	@tmp=$$(mktemp -d); rc=0; \
	go run ./scripts/integrations.go > $$tmp/integrations.json; \
	go run ./scripts/droptext.go    > $$tmp/droptext.json; \
	diff -u web/src/integrationMeta.catalog.json $$tmp/integrations.json || rc=1; \
	diff -u web/src/i18n/drops/catalog.json    $$tmp/droptext.json    || rc=1; \
	rm -rf $$tmp; \
	if [ $$rc -ne 0 ]; then \
	  echo; \
	  echo "Generated catalogue is out of date — run 'make catalogs' and commit the result."; \
	  exit 1; \
	fi; \
	echo "catalogues up to date"

vet: ## Run go vet
	go vet ./...

flowgen-eval: ## Score the AI flow generator against every scenario in tests/usecases/README.md (needs FLOWGEN_EVAL_KEY; writes a report)
	@test -n "$$FLOWGEN_EVAL_KEY" || { echo "set FLOWGEN_EVAL_KEY=<provider api key> (it calls a real model, which costs money)"; exit 1; }
	FLOWGEN_EVAL_OUT=$${FLOWGEN_EVAL_OUT:-.flowgen-eval} \
		go test ./daemon -run TestFlowGenScenarios -v -timeout 60m
	@echo "report: $${FLOWGEN_EVAL_OUT:-.flowgen-eval}/flowgen-eval.md"

fmt: ## Format Go sources
	# Same file set as fmt-check, so `make fmt` always satisfies the gate —
	# and so this never rewrites a vendored .go file under web/node_modules.
	gofmt -w $(GOFMT_PATHS)

docs-content: ## Populate the docs SPA content (guide pages + generated step catalog)
	rm -rf $(DOCS_CONTENT_OUT)
	mkdir -p $(DOCS_CONTENT_OUT)/guide
	# README.md is the repository-host index for docs/guide/ — the reading order
	# GitHub cannot infer from an alphabetical file list. The SPA has that order in
	# NAV, so shipping it as a page would put an untitled duplicate in the sidebar.
	cp $(filter-out docs/guide/README.md,$(wildcard docs/guide/*.md)) $(DOCS_CONTENT_OUT)/guide/
	go run ./cmd/docsgen -out $(DOCS_CONTENT_OUT)/reference/steps

docs-site: docs-content ## Build the docs SPA (docs.dazyflow.app) into web/dist-docs
	npm --prefix web ci
	npm --prefix web run build:docs

docs-dev: docs-content ## Run the docs SPA locally with hot reload (http://localhost:5173/docs.html)
	npm --prefix web install
	npm --prefix web run dev

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
# The recipe promotes CHANGELOG.md itself: the [Unreleased] entries move
# under a new [X.Y.Z] - YYYY-MM-DD heading, a fresh empty [Unreleased] is
# left for the next cycle, and CHANGELOG + VERSION are committed together
# so the tag points at the commit where the changelog announces the
# version.
#
# This used to be a manual step you were told to do FIRST, and it drifted:
# 0.3.0, 0.3.1, 0.3.2 and 0.4.0 were all tagged with no changelog entry,
# because nothing checked. Promotion is mechanical — the curation happens
# while you write entries under [Unreleased] during development — so the
# mechanical half is automated and the judgement half is enforced: an
# EMPTY [Unreleased] aborts the release, since it means nobody wrote down
# what shipped. Pre-promoted by hand? The recipe detects the heading and
# leaves the file alone.
#
# VERSION is committed because the Docker build reads it whenever compose
# is invoked without the VERSION export (the production deploy path); a
# stale file there is how a release ends up reporting itself as "dev".
# The tag is local; the recipe prints the push command.
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

check-changelog: ## Fail if a released CHANGELOG section has been edited
	@./scripts/check-changelog.sh

# Every bump checks first: the release that is about to promote [Unreleased] is
# exactly the moment an entry filed under the PREVIOUS version's heading gets
# silently baked in — and the refusal below ("[Unreleased] is empty") is the
# symptom people see instead of the cause.
_bump: check-changelog
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
	if grep -q "^## \[$$NEW\]" CHANGELOG.md; then \
		echo "CHANGELOG.md already has a [$$NEW] heading — leaving it alone"; \
	else \
		if [ "$$(awk '/^## \[Unreleased\]/{f=1;next} /^## \[/{f=0} f' CHANGELOG.md | grep -c '[^[:space:]]')" -eq 0 ]; then \
			echo "REFUSING: CHANGELOG.md [Unreleased] is empty — nothing to release."; \
			echo "Write what shipped under [Unreleased] first, or add a [$$NEW] heading by hand."; \
			exit 1; \
		fi; \
		awk -v h="## [$$NEW] - $$(date +%F)" '{print} /^## \[Unreleased\]$$/ && !d {print ""; print h; d=1}' \
			CHANGELOG.md > CHANGELOG.md.tmp && mv CHANGELOG.md.tmp CHANGELOG.md; \
		echo "CHANGELOG.md: [Unreleased] promoted to [$$NEW]"; \
	fi; \
	printf '%s\n' "$$NEW" > VERSION; \
	git add VERSION CHANGELOG.md; \
	git commit -q -m "Release $$NEW" -- VERSION CHANGELOG.md; \
	git tag -a "$$NEW" -m "Release $$NEW"; \
	echo "$$CUR -> $$NEW (committed ./VERSION + CHANGELOG.md, tagged)"; \
	echo "Push with: git push origin master $$NEW"

# LATEST_TAG selects the newest RELEASE tag. The three filters each fix a
# real mis-selection:
#   -l '[0-9]*...'  drops non-version tags — a plain `git tag --sort=-v:refname`
#                   sorts anything non-numeric (a `nightly` or `latest-stable`
#                   tag) ABOVE every version, and would deploy that instead.
#   grep -Ex        drops pre-releases: version sort puts `1.0.0-rc1` ahead of
#                   `1.0.0`, so a glob alone would ship an rc as "latest".
#   --sort=-v:refname  orders numerically, so 0.10.0 beats 0.9.0 (lexical sort
#                   gets this backwards).
# Not a $(shell ...) variable: that would run git on every make invocation,
# including in a fresh clone with no tags. The recipes below expand it.
LATEST_TAG = git tag -l '[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | grep -Ex '[0-9]+\.[0-9]+\.[0-9]+' | head -1

latest: ## Print the newest release tag (deploy scripts: use `make -s latest`)
	@$(LATEST_TAG)

# Deploy the newest release tag.
#
# The commentary lives out here rather than inside the recipe because make
# ECHOES recipe lines, comments included. This target is normally run by a
# flow step, so every one of those lines lands in the run log and buries the
# three that say what actually happened.
#
# `git fetch --tags --force --prune-tags`
#     --force updates a tag that was moved upstream; --prune-tags drops one
#     deleted upstream. Without them a stale local tag can win the selection.
#
# The caddy guard
#     Refuses to recreate a running production stack with a file set that omits
#     the overlay: that would tear down Caddy (TLS) and the docs site and bring
#     dzd back bare. The test compares what is RUNNING against what THIS
#     invocation would apply, rather than testing PROD directly — a host that
#     configures the overlay through compose's own COMPOSE_FILE is correctly
#     set up and must not be nagged about a flag it doesn't need. caddy exists
#     only in the overlay, so it stands in for "the overlay is in play"; we
#     look for the running one with the overlay merged, since a compose
#     invocation without it doesn't know the service exists.
#
# `set -e`
#     Load-bearing, and its absence was a real outage. The body is ONE
#     continued line, which make hands to a single shell — so make only ever
#     saw the exit status of the LAST command, the trailing `if`, which returns
#     0 unconditionally. A failed build therefore reported a SUCCESSFUL upgrade
#     while the box kept serving the previous release with its tree checked out
#     at the new tag. Not hypothetical: that is exactly how 0.25.0 "deployed"
#     onto images that were still 15 hours old.
#
# The pull branch
#     A host running the production overlay pulls prebuilt images from ghcr.
#     Anything else — a self-host without the overlay — still builds from
#     source, which is why the else branch stays.
#
#     Keyed on `caddy` because that service exists only in the production
#     overlay. It used to key on `registry`, back when the images came from a
#     registry:2 container running on this box; that service is gone, and a
#     condition still naming it would silently take the ELSE branch and set a
#     1-vCPU droplet compiling Go and two Vite builds — the exact failure the
#     prebuilt-image path exists to prevent.
upgrade: ## Deploy the latest release tag, pulling its prebuilt images (PROD=1 on a production host)
	git fetch --tags --force --prune-tags
	@if docker compose -f docker-compose.yml -f docker-compose.prod.yml ps \
	     --services --status running 2>/dev/null | grep -qx caddy && \
	   ! $(COMPOSE) config --services 2>/dev/null | grep -qx caddy; then \
		echo "caddy is running, but this invocation would recreate the stack without it,"; \
		echo "dropping TLS and the docs site. Either:"; \
		echo "  PROD=1 make upgrade                                          (per invocation)"; \
		echo "  echo COMPOSE_FILE=docker-compose.yml:docker-compose.prod.yml >> .env   (once)"; \
		exit 1; \
	fi
	@set -e; \
	LATEST=$$($(LATEST_TAG)); \
	if [ -z "$$LATEST" ]; then \
		echo "No release tags yet — nothing to upgrade to."; exit 1; \
	fi; \
	echo "Upgrading to $$LATEST"; \
	git checkout "$$LATEST"; \
	export VERSION="$$LATEST"; \
	export COMMIT="$$(git rev-parse --short HEAD)"; \
	export BUILD_DATE="$$(date -u +%Y-%m-%dT%H:%M:%SZ)"; \
	if $(COMPOSE) config --services 2>/dev/null | grep -qx caddy; then \
		echo "Pulling $$LATEST from ghcr — nothing is compiled on this host."; \
		n=0; until $(COMPOSE) pull dzd docs; do \
			n=$$((n+1)); \
			if [ $$n -ge 5 ]; then \
				echo "ghcr did not answer after $$n attempts. Check, in order:"; \
				echo "  * did CI publish $$LATEST? (Actions run with publish=true)"; \
				echo "  * is GHCR_OWNER set in .env?"; \
				echo "  * are the packages public, or is this host logged in to ghcr.io?"; \
				exit 1; \
			fi; \
			echo "ghcr not answering yet — retrying ($$n/5)"; sleep 2; \
		done; \
		$(COMPOSE) up -d --no-build; \
	else \
		echo "Not the production overlay — building from source instead."; \
		$(COMPOSE) up -d --build; \
	fi; \
	if $(COMPOSE) config --services 2>/dev/null | grep -qx caddy; then \
		echo "Deployed $$LATEST; staying on the tag so the tree matches the running image."; \
	else \
		git checkout master; \
	fi

## --- Gates (run locally; CI enforces the same ones) ---
# These mirror .github/workflows/ci.yml so a push never lands red.
#
# fmt-check is first because it is the cheapest and the one most likely to be
# the only thing wrong. It was previously left out on the grounds that the tree
# carried gofmt-version drift; it did not — the nine offending files were
# ordinary misalignment and one out-of-order import, fixed in one `make fmt`.

# Directories gofmt must not walk. go.mod's `ignore` directive keeps build/vet/
# test out of web/node_modules, but gofmt reads the filesystem and knows nothing
# about go.mod — and node_modules does contain Go sources (flatted ships a Go
# port beside its JS). Without this the gate can fail on a file the project
# does not own and cannot fix.
GOFMT_PATHS := $(shell git ls-files '*.go' 2>/dev/null)

fmt-check: ## Fail if any Go source is not gofmt-clean (CI)
	@out="$$(gofmt -l $(GOFMT_PATHS) 2>/dev/null)"; \
	if [ -n "$$out" ]; then \
		echo "not gofmt-clean (run 'make fmt'):"; echo "$$out"; exit 1; \
	fi

env-check: ## Fail if a DAZYFLOW_* knob the daemon reads is missing from .env.example (CI)
	@./scripts/check-env-example.sh

links-check: ## Fail if a relative Markdown link does not resolve on disk (CI)
	@./scripts/check-links.sh

check: ## Fast local gate before pushing: fmt, build, vet, tests, catalogues, doc links, changelog
	@echo "==> gofmt"; $(MAKE) --no-print-directory fmt-check
	@echo "==> go build"; go build ./...
	@echo "==> go vet"; go vet ./...
	@echo "==> go test"; go test ./...
	@echo "==> catalogues"; $(MAKE) --no-print-directory catalogs-check
	@echo "==> env catalogue"; $(MAKE) --no-print-directory env-check
	@echo "==> doc links"; $(MAKE) --no-print-directory links-check
	@echo "==> changelog"; ./scripts/check-changelog.sh
	@echo
	@echo "check passed. This does NOT cover the web suite, the web build, or"
	@echo "the runner agent tests — CI gates all three. Run 'make ci' for the"
	@echo "full mirror before a change that touches web/ or runner/."

ci: ## Full local mirror of CI (.github/workflows/ci.yml): fmt, build, vet, race tests, runner, web, docs
	@echo "==> gofmt"; $(MAKE) --no-print-directory fmt-check
	@echo "==> go build"; go build ./...
	@echo "==> go vet"; go vet ./...
	@echo "==> env catalogue"; $(MAKE) --no-print-directory env-check
	@echo "==> doc links"; $(MAKE) --no-print-directory links-check
	@echo "==> runner agent"; $(MAKE) --no-print-directory runner-test
	@echo "==> go test -race"; go test -race -timeout $(GO_TEST_TIMEOUT) ./...
	@echo "==> catalogues"; $(MAKE) --no-print-directory catalogs-check
	@echo "==> web deps"; cd web && npm ci
	@echo "==> npm audit"; cd web && npm audit --audit-level=high --omit=dev
	@echo "==> docs content"; $(MAKE) --no-print-directory docs-content
	@echo "==> web test"; cd web && npm test
	@echo "==> web build"; cd web && npm run build
	@echo "==> changelog"; ./scripts/check-changelog.sh
