<!--
SPDX-FileCopyrightText: 2026 Angels' Ware
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Contributing to Dazyflow

Everything here was already true — it was spread across the Makefile's help
text, `web/README.md`, and comments in `.github/workflows/ci.yml`, so a first
contributor had to find it by breaking a build. This is that knowledge in one
place.

Start with [README.md](README.md) for what Dazyflow is and how to run it.
[docs/decisions/](docs/decisions/) is worth a skim before a change to
user-facing copy or the runner.

## Setup

You need Go (the version in [go.mod](go.mod)), Node (the version in
[.nvmrc](.nvmrc)), Python 3 for the runner tests, and Docker for Postgres.

```sh
make dev      # bundled Postgres, then dzd on http://localhost:8080
make web      # (other terminal) Vite dev server on http://localhost:5173
make help     # every target
```

`dzd` requires Postgres — there is no in-memory mode.

## Before you push

```sh
make check    # gofmt, build, vet, Go tests, catalogues, config catalogue, doc links, changelog
make ci       # the full CI mirror — adds npm audit, the web suite and build, the runner tests
```

`make check` is the fast gate for a Go-only change. **Run `make ci` for anything
touching `web/` or `runner/`** — CI gates all of it and `check` does not.

A large part of the Go suite is Postgres-gated and silently skips without a
database. To run what CI runs:

```sh
export DAZYFLOW_TEST_DB='postgres://dazyflow:dazyflow@localhost:5432/dazyflow_test'
export DZ_TEST_PG_DSN="$DAZYFLOW_TEST_DB?sslmode=disable"
```

The MySQL-backed `drops/db` tests read `DAZYFLOW_TEST_MYSQL` the same way. A
green suite with neither set is a much smaller claim than it looks.

## Rules the gates enforce

Each of these fails CI, and each exists because it was broken once.

**Every source file carries an SPDX header.** Two lines, matching the ones on
the file next to it. Go, TypeScript, shell and Python alike. The project is
AGPL-3.0-or-later and the headers are how that survives a file being copied.

**Write under `## [Unreleased]` in CHANGELOG.md, never under a released
heading.** `make patch` promotes `[Unreleased]` to a version and leaves a fresh
empty one; between releases the heading that *was* the place to write becomes
the one place you must not, and nothing about the file looks different.
`scripts/check-changelog.sh` catches it, because a reviewer will not — the entry
is correct prose under a plausible heading.

**Run `make catalogs` after adding or rewording a drop.** Two committed
snapshots — the app list and the drop text — back the web guards. A stale
snapshot makes its guard pass vacuously: "every drop has a Swedish description"
becomes true only of the drops the snapshot remembers.

**Add every new `DAZYFLOW_*` knob to `.env.example`.** It is the configuration
reference, not a sample: `make env` copies from it and operators are sent to it.
`make env-check` fails on a knob the daemon reads and the file doesn't mention.

**Every relative Markdown link has to resolve.** `make links-check` walks every
tracked `.md` file. It exists because eight links to a `docs/decisions/`
directory that had never been created shipped in the commit that wrote them —
one of them named as required reading in this file. A dead link in
`CONTRIBUTING.md` costs a first contributor more than a broken build does.

**`make fmt` before committing.** `make fmt-check` gates it.

## Adding a step (drop)

A drop lives under `drops/<integration>/`, registers itself with a manifest, and
needs:

- the drop and its manifest, plus a test — see a neighbour, e.g.
  `drops/elks/` for the shape of a static-key connector;
- `make catalogs` re-run and the result committed;
- Swedish text for anything user-facing (`web/src/i18n/drops/`) — the coverage
  guard fails without it;
- a `## [Unreleased]` changelog entry.

`docs/guide/` and the step reference on docs.dazyflow.app regenerate from the
manifests, so the description you write there is the one users read.

## Writing user-facing text

Read
[docs/decisions/2026-08-20-step-vocabulary.md](docs/decisions/2026-08-20-step-vocabulary.md)
first. The short version: **`step` in anything a person reads, `drop` in code** —
Go packages, API routes, JSON fields, MCP tool names, error codes, CSS classes.
That split is deliberate and holding it is cheaper than re-litigating it.

Guide pages under `docs/guide/` are read both in the docs SPA and on GitHub, so
link a sibling page as `./slug.md` and the generated step catalog as a full
`https://docs.dazyflow.app/reference/steps/…` URL. A test enforces both.

## Reporting a bug, or asking for a connector

Open an issue — there is a template for each. A bug report wants the version
from `GET /api/v1` and how you're running it; a feature or connector request
wants the job in your own words rather than in Dazyflow vocabulary, and how the
service authenticates, which is the main cost driver.

Check [TODO.md](TODO.md) first. The connector backlog is ranked there with the
reasoning, and two sections exist so the same suggestions don't arrive twice:
**Decided against** and **Corrections to the record**.

## Reporting a security issue

Do **not** open a public issue. See [SECURITY.md](SECURITY.md) — report through
GitHub's Private Vulnerability Reporting.

## Licence

Contributions are under **AGPL-3.0-or-later**, the licence in
[LICENSE](LICENSE). By opening a pull request you agree your contribution ships
under it.
