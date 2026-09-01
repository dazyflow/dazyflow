<!--
SPDX-FileCopyrightText: 2026 Angels' Ware
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Contributing to Dazyflow

[README.md](README.md) covers what Dazyflow is and how to run it. This file
covers what CI expects from a change.

## Setup

Go (version in [go.mod](go.mod)), Node (version in [web/.nvmrc](web/.nvmrc)), Python 3
for the runner tests, Docker for Postgres. There is no in-memory mode — `dzd`
requires Postgres.

```sh
make dev      # bundled Postgres, then dzd on http://localhost:8642
make web      # (other terminal) Vite dev server on http://localhost:5173
make help     # every target
```

## Before you push

```sh
make check    # fmt, build, vet, Go tests, catalogues, doc links, changelog
make ci       # the full CI mirror — adds npm audit, the web suite and build, the runner tests
```

`make check` is the fast gate for a Go-only change. **Run `make ci` for anything
touching `web/` or `runner/`** — CI gates all of it and `check` does not.

Much of the Go suite is Postgres-gated and skips silently without a database, so
a green run with these unset is a smaller claim than it looks:

`make test-db` starts the bundled Postgres, creates the database these tests
want, and prints the exports to paste:

```sh
make test-db
# 5442 is where the bundled Postgres lands on the host (DAZYFLOW_PG_PORT).
# Point these at your own database instead if you run one.
export DAZYFLOW_TEST_DB='postgres://dazyflow:dazyflow@localhost:5442/dazyflow_test'
export DZ_TEST_PG_DSN="$DAZYFLOW_TEST_DB?sslmode=disable"
```

`check` and `ci` both say out loud, at the end, when they skipped a gated suite
— a green run with these unset covers about 146 fewer cases than CI does.

The MySQL-backed `drops/db` tests read `DAZYFLOW_TEST_MYSQL` the same way.

## What the gates enforce

- **An SPDX header on every source file** — two lines, matching the file next to
  it. Go, TypeScript, shell, Python alike.
- **New entries under `## [Unreleased]` in CHANGELOG.md, never under a released
  heading.** `make patch` promotes `[Unreleased]` to a version and opens a fresh
  one; `scripts/check-changelog.sh` catches edits to released sections.
- **`make catalogs` after adding or rewording a drop.** Two committed snapshots
  back the web guards; a stale snapshot makes its guard pass vacuously.
- **Every new `DAZYFLOW_*` knob in `.env.example`.** It is the configuration
  reference, not a sample — `make env` copies from it. `make env-check` gates it.
- **Every relative Markdown link resolves.** `make links-check` walks every
  tracked `.md` file.
- **`make fmt`**, gated by `make fmt-check`.

## Adding a step (drop)

A drop lives under `drops/<integration>/` and registers itself with a manifest.
Copy a neighbour — `drops/elks/` is the shape of a static-key connector. A
change needs:

- the drop, its manifest, and a test;
- `make catalogs` re-run and the result committed;
- Swedish text for anything user-facing (`web/src/i18n/drops/`) — the coverage
  guard fails without it;
- a `## [Unreleased]` changelog entry.

The step reference on docs.dazyflow.app regenerates from the manifests, so the
description you write is the one users read.

## Writing user-facing text

**`step` in anything a person reads, `drop` in code** — Go packages, API routes,
JSON fields, MCP tool names, error codes, CSS classes. The split is deliberate.

Guide pages under `docs/guide/` are read both in the docs SPA and on GitHub, so
link a sibling as `./slug.md` and the generated step catalog as a full
`https://docs.dazyflow.app/reference/steps/…` URL. A test enforces both.

## Issues

There is a template for a bug report and one for a feature or connector request.
A bug wants the version from `GET /api/v1` and how you're running it; a
connector request wants the job in your own words and how the service
authenticates, which is the main cost driver.

For a suspected vulnerability, do **not** open a public issue — see
[SECURITY.md](SECURITY.md).

## Licence

Contributions ship under **AGPL-3.0-or-later**, the licence in
[LICENSE](LICENSE). Opening a pull request means you agree to that.
