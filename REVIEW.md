# Fresh-eyes review — 2026-08-31

A first read of the repository by someone who had not seen it before. Verified
by running things, not by reading alone: `go build ./...`, `go vet ./...`,
`go test ./...`, `gofmt -l`, the web suite, the Python runner suite, `npm audit`,
a link check over every `.md`, and an env-var drift check between the code and
`.env.example`.

Grouped as asked: what's good, what's broken, what's unclear, what's unnecessary.

---

## What's good (leave it alone)

Stated so the fixes below are read in proportion. This is a well-kept tree.

- **Everything green.** `go build ./...` and `go vet ./...` clean. Full Go
  suite passes (exit 0, 91 packages, only 5 with no test files). Web: 123 files
  / 1134 tests pass. Runner: 94 Python tests pass. 565 test files against 502
  source files.
- **Licensing is complete.** SPDX headers on 1063/1063 Go files (excluding
  generated `api/gen/`) and 340/340 TS/TSX files.
- **Supply chain is unusually thorough.** govulncheck in both source *and*
  binary mode with the blind-spot reasoning written down, SBOM in two formats,
  Dependabot across four ecosystems, Alpine pinned by tag **and** digest,
  `GOTOOLCHAIN=local` so a prod build can't silently fetch a toolchain.
- **No SQL injection.** Every dynamic identifier goes through `quoteIdent` /
  `quoteBoardIdent`; no user data is concatenated into a statement.
- **Frontend discipline.** `strict` + `noUnusedLocals` + `noUnusedParameters`,
  nine custom design-system guards (`check-css-tokens`, `check-modal-a11y`, …),
  11 `any` across 340 files, zero stray `console.log`.
- **Design docs carry a status line.** `docs/*-design.md` each say NOT BUILT /
  BUILT up front and are cross-referenced from the code. That is rare and worth
  keeping.
- **Only 5 TODO/FIXME markers in ~260k lines of Go**, each pointing at a real
  note rather than orphaned.

---

## Broken

### 1. CI never runs on its own
`.github/workflows/ci.yml` is `on: workflow_dispatch:` only — no `push:`, no
`pull_request:`. Every gate above (race suite, both govulncheck passes, catalogue
freshness, changelog guard, web tests, three Vite builds) fires only when a human
remembers to press a button. The file says so itself, as a temporary state.
Nothing else builds this repo; the sourcehut manifest it was ported from is gone.

- [x] Added `pull_request:` and `push:` (branches: master) triggers.
- [x] `cancel-in-progress` is now `github.event_name != 'workflow_dispatch'` —
      the comment's reason for `false` (a hand-dispatched run mid-push to ghcr)
      only holds for a dispatch. Publish/deploy stay dispatch-gated: on a push
      or PR the `publish`/`deploy` inputs are unset, hence falsy.

### 2. Nine Go files fail `gofmt`
```
auth/auth_test.go          mcp/server/tools.go
auth/orgauthconfig.go      tests/e2e/*.go  (6 files)
```
The `Makefile` declines to gate it, on the stated grounds of "pre-existing
gofmt-version drift". The diffs are not version drift — they're ordinary struct
tag / map literal misalignment and one out-of-order import in
`tests/e2e/e2e_test.go`. One command fixes all nine.

- [x] `make fmt` — all nine fixed.
- [x] `make fmt-check` added, run first in CI, and the "NOT a gate" comment
      dropped. It operates on `git ls-files '*.go'` — see finding 17 for why
      `gofmt -l .` is the wrong file set here.

### 3. Nothing checks npm advisories at all
`npm audit`: 7 vulnerabilities (5 moderate, 1 high, 1 critical). There is no
`npm audit` step in CI, which is a striking gap next to two govulncheck passes
for Go — the Go half of the supply chain is gated twice and the JS half zero
times.

**Correction to my first pass on the severity.** I initially reported the
`react-router-dom` advisories (GHSA-wrjc-x8rr-h8h6 open redirect,
GHSA-337j-9hxr-rhxg SSR constructor injection) as a shipped, one-command fix.
Both halves of that were wrong, and the truth matters for what to do:

- **The fix is a major, not `npm audit fix`.** The advisory range is
  `6.0.0 - 7.17.0` — the whole v6 line, with no patched 6.x. npm's only remedy
  is `react-router-dom@7.18.3`. `npm audit fix` moved 6.30.4 → 6.30.6, which is
  worth keeping but does not clear the advisory.
- **Neither advisory is reachable in this app.** The open redirect needs
  user-controlled input reaching `Link`/`navigate`. The one place that happens
  — `return_to` in `src/pages/auth/SignIn.tsx:49` — already rejects `//evil.com`
  and `/\evil.com` by hand, mirroring `daemon/google_signin.go safeReturnPath`.
  Every other non-literal `navigate()`/`<Link to>` takes an internal constant.
  The SSR advisory needs `deserializeErrors`, which needs SSR hydration; this is
  a client-only SPA. `react-markdown` is confined to the docs SPA over generated
  content.

So: gate the shipped dependency tree, don't force a router major through on a
finding that doesn't apply. The dev-only high/critical (vite dev server, vitest
UI) are real but never reach a user — `--omit=dev` is the honest line, and it is
the same call the govulncheck comment already makes for
`golang.org/x/crypto` GO-2026-5932.

- [x] `npm audit fix` (react-router 6.30.4 → 6.30.6; no behaviour change).
- [x] `npm audit --audit-level=high --omit=dev` added to the web CI job. It
      exits 0 today and would catch the next high/critical in a **shipped**
      package.
- [ ] Schedule the Vite 5 → 8 / Vitest 2 → 4 and React Router 6 → 7 majors as
      their own change, not as review fallout.

### 4. Test files are never type-checked
`web/tsconfig.json` `exclude`s `src/**/*.test.ts(x)`. So `tsc -b`,
`npm run typecheck`, and the CI `npm run build` all skip 123 test files. A type
error in a test is invisible until someone opens the file.

- [x] `tsconfig.test.json` added and run first in `npm test`. It found
      **eleven real errors**: a mock whose signature had drifted from
      `api.whoami`, eight `never[]` spreads that cannot type-check against their
      target, an unused import, and a stale `@ts-expect-error`. All fixed.
- [x] The app config now pins `types: ["vite/client"]`, so the `@types/node`
      the guard tests need cannot leak `process` and `node:fs` into the browser
      bundle's type space.

### 5. `make check` is weaker than CI, and the README points at it
The README tells contributors "`make check` — build, vet, tests — run before
pushing". `make check` omits `catalogs-check`, the web tests, the web build, the
runner tests, and gofmt — four of which are CI gates. A contributor can run the
documented command, pass, and still break the build.

- [x] `make check` now runs gofmt, catalogues and the config catalogue, and
      prints what it does **not** cover.
- [x] `make ci` extended (runner tests, npm audit, docs content) and named in
      the README.

---

## Unclear

### 6. Every guide cross-link 404s on GitHub
40 links across `docs/guide/*.md` are extensionless SPA routes — `./concepts`,
`./glossary#run`, `../reference/steps/`. They resolve inside the docs SPA and
break for anyone reading on GitHub, which is exactly where the README sends
them ("**[docs/guide](docs/guide)** for using Dazyflow").

- [x] Guide-internal links are now `./concepts.md` — the renderer already
      stripped `.md`, so both readers are satisfied by one href.
- [x] The step catalog, which has no markdown source in the repo, is a full
      `https://docs.dazyflow.app/...` URL; a new `stripDocsOrigin` keeps those
      navigating in-SPA instead of opening a new tab.
- [x] Three guards added to `web/src/docs/content.test.ts`: every relative
      link resolves on disk, the step catalog is never linked relatively, and
      every catalog link names a page `cmd/docsgen` actually emits. Verified by
      reintroducing the bug and watching it fail.

### 7. Fifteen config knobs exist in the code and are documented nowhere
`.env.example` presents itself as the full catalogue (`make env` syncs from it,
the Dockerfile calls it "the full catalogue"). These are read by the daemon and
appear in neither it, nor `docs/`, nor `README`/`SECURITY.md`:

```
DAZYFLOW_TRUSTED_PROXIES          ← security-relevant, see below
DAZYFLOW_EGRESS_BURST / _CONCURRENCY / _RATE_PER_MIN
DAZYFLOW_FREE_MAX_CONCURRENCY / _MAX_MEMBERS / _RETENTION_DAYS
DAZYFLOW_SUPPORT_INBOX / _RETENTION
DAZYFLOW_OIDC_ALLOWED_TENANTS
DAZYFLOW_FAILURE_EMAIL_WINDOW
DAZYFLOW_MAX_ROWS
DAZYFLOW_PROMOTE_INTERVAL
DAZYFLOW_RUNNER_TASK_RETENTION    (one mention in docs/, absent from .env.example)
ANTHROPIC_API_KEY
```
Three of these are the worst kind of gap, because a sibling *is* documented and
the reader concludes the list is complete:

- **`DAZYFLOW_TRUSTED_PROXIES`** — `daemon/ratelimit.go` needs it to find the
  real client IP. `DAZYFLOW_TRUST_PROXY_HEADERS` is documented in README,
  SECURITY.md and `.env.example`; this one is documented nowhere. Every
  deployment following the README's reverse-proxy section rate-limits the whole
  internet into a single bucket and will not know why.
- **`DAZYFLOW_FREE_*`** — README says "only set the `DAZYFLOW_FREE_*` knobs if
  you're running a paid SaaS", but only 3 of the 6 are listed.
- **`DAZYFLOW_OIDC_ALLOWED_TENANTS`** — the other five `DAZYFLOW_OIDC_*` are all
  in `.env.example`.

- [x] All fifteen added to `.env.example`, each in its existing section.
- [x] `scripts/check-env-example.sh` (`make env-check`, run in CI and in
      `make check`): every `envStr`/`envInt`/`envBool`/`envDuration`/
      `os.Getenv("DAZYFLOW_*")` key must appear in `.env.example`, with a short
      EXEMPT list for the dzctl-client and test-harness variables. This is the
      only way the list stays true — it drifted to fifteen unnoticed.

### 8. TODO.md hides 18 open items in 440 lines of completed work
The file opens by saying "Completed work is not archived here. `git log` is the
record" — then spends sections 20–460 on retrospectives titled "worked through".
The 18 actual `- [ ]` items are scattered inside them; the first one is on line
140. A new contributor cannot tell what is left to do.

- [x] The five retrospectives moved to `docs/decisions/` as dated records
      with an index; `TODO.md` went from 557 lines to 305 and now leads with the
      open work (first item at line 37, was 140). Content preserved verbatim —
      checked line by line.
- [x] The count is **14**, not 18. Four `- [ ]` were not tasks: two under
      "Deferred" are decisions *not* to do something, and two are standing
      guidance that says "no open work" in its own text. They are now prose
      under `## Decided against` and in the copy section, so the checkbox count
      means what it says.

### 9. Nothing tells a contributor how to contribute
No `CONTRIBUTING.md`, no PR template, no issue templates, on a public AGPL repo
with 394 files under `drops/`. The knowledge exists — it's spread across the
Makefile's help text, `web/README.md`, and CI comments.

- [x] `CONTRIBUTING.md` written: setup, the pre-push gate, the
      Postgres/MySQL env vars a large part of the suite silently skips without,
      the four gate-enforced rules, how to add a drop, and the step/drop
      vocabulary split.
- [x] `.github/PULL_REQUEST_TEMPLATE.md` added with that checklist.

---

## Unnecessary

### 10. Stale `.dockerignore` entries
```
docs/.vitepress/dist      ← VitePress is not used anywhere in the tree
docs/.vitepress/cache     ← Dockerfile.docs is Vite + nginx
OVERNIGHT.md              ← does not exist
.dazyflow-users.json      ← does not exist
```
- [x] Deleted, and replaced with the two paths that are actually generated
      (`web/src/docs/content`, `web/dist-docs`) — neither was ignored.

### 11. `deploy/Caddyfile` describes the docs site as "the static VitePress build"
Same stale fact, in the file an operator reads while debugging TLS.
- [x] Reworded to "the static Vite/React docs SPA, served by nginx".

### 12. `.gitignore` carves out a directory that doesn't exist
```
/.claude/*
!/.claude/skills/    # "committed so the team gets them"
```
There is no `.claude/` and no committed skills. The comment states a policy that
isn't in force.
- [x] Dropped.

### 13. No ESLint
`FlowEditor.tsx` is 5,291 lines and `SchemaForm.tsx` 4,224, both hook-heavy, with
no `react-hooks/exhaustive-deps` anywhere. `tsc --strict` plus nine custom guards
covers a lot, but not stale closures over deps.
- [x] Added, with just the two `react-hooks` rules and the reasoning for the
      minimalism in `eslint.config.js`. It immediately found three errors:
      a `rules-of-hooks` violation (`useTemplate`, a plain async action whose
      `use` prefix made every linter and every reader take it for a hook —
      renamed `applyTemplate`), a disable comment naming a `jsx-a11y` plugin
      that was never configured, and two disable directives suppressing nothing.
- [x] `npm test` gates on **errors**; the 45 dependency warnings are visible
      via `npm run lint` and are triaged in finding 18 rather than blind-fixed.

### 14. The Python runner suite leaks a warning
`ResourceWarning: Implicitly cleaning up <HTTPError 401: 'Unauthorized'>` on
every run. Harmless, but it's the only noise in an otherwise silent suite.
- [x] `HTTPError` is now closed in a `finally`. Verified with
      `python3 -W error::ResourceWarning` — 94 tests, no warnings.

### 15. Two of three examples are never exercised — and both were broken
CI runs `examples/csv-pipeline/run.sh` only. Running the other two showed that
"likely to rot" understated it: **both had been failing at boot**, each on a
requirement that landed after it was written. Seven distinct breakages.

`examples/mcp-pipeline/run.sh` — four:
1. No `DAZYFLOW_POSTGRES_DSN` at all. `dzd` has required Postgres since, so it
   exited immediately with "DAZYFLOW_POSTGRES_DSN is required".
2. `sleep 0.5` before grepping the dev token out of the log. A Postgres-backed
   boot takes longer, the token came out empty, and `set -e` killed the script
   at an assignment having printed nothing.
3. No `DAZYFLOW_DEV=1`, so the insecure-defaults guard refused to start.
4. Read the sandbox at `$DATA_DIR/dev/main/...`, from before `DAZYFLOW_DATA_DIR`
   grew its `sandbox/` segment.

`examples/ap-invoice/run.sh` — three:
5. The guard's TLS check firing on its own throwaway loopback container.
6. **It never published its flows.** A webhook fires the *published* revision
   (`daemon/webhook.go`: `store.LoadPublished`), so every POST got the
   deliberately-generic pre-auth 401 — by design that error cannot tell you
   "unpublished flow" apart from "wrong secret", so the script's own output
   gave nothing to go on.
7. `flow_id` must be the percent-encoded `tenant/workspace/id` triple in a
   single path segment.

Worth noting from (6): **there is no way to publish a flow from `dzctl`** — no
publish RPC in `api/proto/control.proto`, no `graph publish` subcommand. It is
HTTP-only (`POST /api/v1/me/flows/{flow_id}/publish`). Not raised as a defect
here, but a CLI that can save a webhook-triggered flow and not arm it is a
sharp edge.

- [x] All seven fixed; both examples now pass every assertion.
- [x] CI runs all three examples.

---

### 16. Three code comments pointed at notes that do not exist
`daemon/timeout.go:67` ("flagged in the TODO") and `daemon/runlog_pg.go:16`
("see the TODO note") referenced TODO.md entries that were not there — checked
against the pre-change file, so this predates the restructure.
`core/flowstatus.go:164` pointed at a note that the restructure moved.

- [x] All three repointed at something real, or reworded to stand alone.

### 17. The Go toolchain walked into `web/node_modules`
`flatted` ships a Go port beside its JavaScript, so `./...` matched
`web/node_modules/flatted/golang/pkg/flatted` and `go build` / `vet` / `test`
descended into it — visible as a stray `[no test files]` line in every test run.
`gofmt -l .` walked it too.

Latent rather than live: that one file happens to be gofmt-clean. But it is
exactly the shape of thing that turns a *newly added* format gate red on a file
the project does not own and cannot fix.

- [x] `go.mod` now carries `ignore (web/node_modules)`.
- [x] `make fmt` / `fmt-check` operate on `git ls-files '*.go'`, since gofmt
      reads the filesystem and knows nothing about `go.mod`.

### 18. Follow-up: 45 `exhaustive-deps` warnings, ~5 worth a look
Now visible via `npm run lint`, deliberately not gated. Triaged so nobody
repeats the work:

- **22 are one false positive.** `setDirty` is a `useState` setter returned from
  the `useAutosave` custom hook. React guarantees its identity; ESLint cannot
  see through the destructuring.
- **~13 more are `t` / `i18n.language` from `useTranslation()`**, stable across
  renders in the same way.
- **2 are refs** (`dirtyRef`, `loadFailedRef`) — stable by definition.
- **~5 are real and want a human**: `FlowEditor.tsx:2020` (`disabledNodes`),
  `2284` (`continueOnError`, `wiredPlaceByNode`), the `graph` and
  `paramsByID`/`selected` effects, and the `waypoints`/`pts` render-identity
  churn in `RerouteEdge.tsx` (a perf issue, not a correctness one).

Deliberately not fixed here: changing hook dependencies in a 5,291-line editor
on a review's say-so is how you introduce the bug the rule warns about. Each
wants its own change with the behaviour in front of you.

## Considered and not raised

Recorded so the next reviewer doesn't spend time on them.

- **Top-level packages instead of `internal/`.** `auth`, `core`, `daemon`,
  `drops`, `engine`, `workspace`, `pollstate` are all importable by third
  parties. Conventionally an application keeps these under `internal/`. Not
  raised as a defect: the move is a thousand-file rename for a benefit
  (preventing external imports of an AGPL application's internals) this project
  does not appear to need.
- **`CHANGELOG.md` at 4,830 lines / 280 KB.** Large, but it's append-only,
  guarded by `check-changelog`, and splitting it by year would break the guard
  for no reader benefit.
- **`.nvmrc` says 22, Dockerfile pins `node:22-alpine`** — consistent. The suite
  also passes on Node 25 locally.
- **`go.mod` requires 1.26.7, local toolchain is 1.27.0** — the Docker build
  pins 1.26.7 with `GOTOOLCHAIN=local`, and CI's scan job uses `stable`
  deliberately, with the reasoning written down. Working as designed.
- **No `HEALTHCHECK` in the Dockerfile.** `docker-compose.yml` defines one for
  `dzd`; the prod file is an overlay and inherits it.

---

## What was left open, and why

Two things, both deliberate.

**The three dependency majors** (Vite 5 → 8, Vitest 2 → 4, React Router 6 → 7).
All are dev-tooling or a non-reachable advisory, none is a fix for anything this
app has, and each is a behaviour change to the build. They belong in their own
change with the app running in front of you — not as fallout from a review.

**The ~5 real `exhaustive-deps` warnings** (finding 18). Changing hook
dependencies in a 5,291-line editor on a linter's say-so is how you introduce
the bug the rule exists to warn about. They are now visible, triaged, and
separated from the 40 false positives, which is the part that was missing.

## Note on method

Every finding here was reproduced before it was written down, and every fix was
re-run after. Two of them changed shape in the process, which is the argument
for doing it that way:

- **Finding 3** started as "a shipped dependency has a live advisory, one
  command fixes it". Both halves were wrong. The fix is a major version, and the
  advisory is not reachable in this app because `SignIn.tsx` already blocks the
  exact attack by hand. Reported as written rather than quietly corrected,
  because the difference decides whether you take a breaking upgrade.

- **Finding 15** started as "two examples aren't exercised in CI" — a hygiene
  note. Running them showed both were broken, seven ways between them, one of
  which (a webhook needing a *published* revision) is a genuinely sharp product
  edge that the deliberately-generic 401 makes almost undiagnosable from
  outside.

The reverse also happened: five candidate findings were checked and dropped
before they reached this file. They are in **Considered and not raised** above,
so the next reviewer doesn't spend the same time on them.
