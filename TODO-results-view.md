# Results view — an in-app place to see flow output

**Status:** planned, not started. This file is self-contained so it can be
implemented from a fresh session with no prior context.

---

## Why (the gap)

Today a flow's result can only be sent to an **external** sink: Email, Slack,
ntfy, or a database (`drops/notify/`, `drops/db/`). Every one of those pushes
data *out* and needs an external account or DB knowledge. There is **no
in-app, zero-config way for a non-technical user to see what their flow
produced.**

What already exists (verified):
- **Collection is solved.** The *Built-in store* drop (`drops/db/builtin_store.go`,
  drop IDs `builtin_store_append` + the built-in store query) is a zero-setup
  table: it appends rows to a workspace-local SQLite file at
  `<workspaceRoot>/.hazyflow-store/data.db` with no DSN or filename to pick.
- **Per-run output** is visible at `/runs/:runID` (`web/src/pages/RunDetail.tsx`) —
  but that's transient, per-run, and developer-flavored (raw node outputs).
- **There is no `/results` page** and no way to browse the built-in store's
  tables in the UI (confirmed: no data/board/dashboard page exists).

So: data can be *collected* but not *seen*. This plan adds the "see it" half.

## Design decisions (already made — don't re-litigate)

1. **Layer on the Built-in store; do NOT create a new storage system.** A
   "board" is just a Built-in store table. This is the single most important
   constraint, because:
   - The storage half is already built and zero-config.
   - The built-in store file lives under the **sandbox** tree, so it is
     **already covered by the GDPR erasure cascade** (`FSSandbox.RemoveTenant`
     in `daemon/sandbox.go` wipes the whole tenant sandbox subtree; the
     deletion/export work is in `daemon/gdpr.go`). A board needs no new GDPR
     wiring.
2. **Table-first, charts later.** The 80% win is "show my results as a table on
   a page I can revisit." Charts are a second, optional layer with a dependency
   cost. Build the table page first.
3. **Two board modes** (Phase 2): *accumulate* (append every run's rows — a
   log/leaderboard) and *latest* (replace on each run — a status dashboard).
   Phase-1 MVP uses the existing append behavior only.
4. **Read path is a thin, read-only SQLite open in the daemon** — NOT by
   running a flow. The `/results` page calls a new HTTP endpoint that opens the
   workspace's `.hazyflow-store/data.db` read-only and SELECTs.

## Known tradeoff to document, not solve

The built-in store is **not** swept by retention (the retention sweeps in
`cmd/hzd/main.go startRetentionSweeps` cover jobs/audit/run-logs only). A board
therefore accumulates until the user clears it. That's acceptable (boards are
user-curated, not machine exhaust), but: (a) ship a "clear board" action
(DELETE endpoint, Phase 1), and (b) note in PRIVACY.md § Retention that board
data persists until cleared or the workspace/account is deleted.

---

## Phase 1 — MVP: `/results` page over the Built-in store

Goal: a non-techie runs a flow that saves to the Built-in store, then opens
**Results** in the nav and sees their rows as a friendly table.

### Backend

**New read-only access to the built-in store from the daemon.** The path
constant `.hazyflow-store/data.db` currently lives in `drops/db/builtin_store.go`
(`builtinStorePath`). Do NOT import `drops/db` into `daemon` (its `init()`s
register drops; risks duplicate registration). Instead either:
  - lift the relative path to a shared spot (e.g. a `core` const), or
  - re-declare it in the daemon with a comment cross-referencing the drop.

Resolve the workspace sandbox dir via the existing sandbox provider:
`h.svc.Engine.Sandbox.Root(tenant, workspace)` (see `daemon/service.go` ~L458
and `daemon/httpfiles.go:61` for the pattern), then open
`<root>/.hazyflow-store/data.db` read-only with `modernc.org/sqlite` (already a
dep; imported in `drops/db/builtin_store.go`).

**Service methods** (add to `daemon/service.go`, mirror the auth scoping of
`RunLogPage`/`ListGraphRuns` — force the principal's tenant/workspace):
- `ListBoards(ctx, p) ([]BoardSummary, error)` — list user tables in the
  workspace store (`SELECT name FROM sqlite_master WHERE type='table'`,
  excluding `sqlite_%` internal tables), each with a row count.
- `BoardRows(ctx, p, name string, limit, offset int) (BoardPage, error)` —
  columns + rows for one table. **SECURITY: validate the table name** before
  interpolating it into SQL — reuse the `validateIdent` + quote-identifier
  pattern from `drops/db/idents.go` (this repo had a real SQL-injection bug in
  the db drops via spliced identifiers/types; see `drops/db/idents.go`
  `validateColumnType`/`quoteIdent`). Cap `limit` (e.g. 1000) and only ever
  SELECT from a table that appears in the `sqlite_master` list.
- `ClearBoard(ctx, p, name string) error` — `DROP TABLE` (validated/quoted).
  Returns cleanly if the file/table is absent (empty store is not an error —
  mirror the read path in `openBuiltinStore`, `create=false`).

**HTTP endpoints** (register in `daemon/httpgateway.go mountRoutes`, next to the
other `/me/...` routes ~L380–440; handler signature
`func (h *HTTPGateway) x(rw http.ResponseWriter, r *http.Request, p core.Principal)`,
wrapped in `h.requireAuth`; respond with `writeJSON` / `writeAPIError`; audit
with `h.audit`):
- `GET    /api/v1/me/boards`              → `{boards: [{name, rows}]}`
- `GET    /api/v1/me/boards/{name}`       → `{columns: [...], rows: [...], total, truncated}` (query: `limit`, `offset`)
- `DELETE /api/v1/me/boards/{name}`       → clear a board (audit `board.clear`)
- Return `501` when run logs/sandbox aren't configured, `404` for an unknown
  board, `400` for an invalid name — match the conventions in `daemon/me_routes.go`.

**Tests** (`daemon/results_test.go` or alongside): seed a `.hazyflow-store/data.db`
under a temp sandbox (open with modernc sqlite, create a table + rows), point a
`Service` at an `FSSandbox` over that temp base, and assert `ListBoards` /
`BoardRows` return them and that a crafted table name (e.g. `x"; DROP TABLE…`)
is rejected. Reuse the FSSandbox temp-dir pattern from
`daemon/gdpr_test.go TestDeleteOrgData_NoResidual`.

### Frontend

- **Route:** add `<Route path="/results" element={<Results />} />` in
  `web/src/App.tsx` (authed routes block, near `/runs` ~L75).
- **Nav:** add a `NavLink to="/results"` in `web/src/components/AppShell.tsx`
  next to the `/runs` link (~L415) with an icon from `web/src/components/icons.tsx`
  (e.g. a Table/BarChart icon) and i18n key `nav.results`.
- **Page:** `web/src/pages/Results.tsx` — left: board list (name + row count);
  right: the selected board rendered as a table with **client-side search**, a
  **CSV download** button, and a **"Clear" action** (confirm). Empty state when
  no boards: explain "Add a *Built-in store · Save* step to your flow and its
  rows show up here." Mirror the data-fetching + layout conventions in
  `web/src/pages/RunList.tsx` / `RunDetail.tsx`.
- **API client:** add to `web/src/api.ts` (object-of-methods, using
  `request(token, method, path, body)`): `listBoards`, `getBoard(name, {limit,
  offset})`, `clearBoard(name)`.
- **i18n:** add `nav.results` + page strings to BOTH `web/src/i18n/en.json` and
  `web/src/i18n/sv.json` (the build/tests check both locales).
- **Typecheck:** `cd web && npx tsc -b --noEmit` must pass (the repo's LSP shows
  spurious lib errors — trust `tsc -b`, not the inline diagnostics).

### Discovery polish (cheap, do in Phase 1)

- Update the *Built-in store* drop's `Summary`/`Tags`/`Description`
  (`drops/db/builtin_store.go`) to mention results show up on the Results page.
- Consider a small `SearchBoost` (see `core/manifest.go` `SearchBoost` +
  `daemon/search.go`) so it ranks for `results`/`dashboard`/`report`.

**Phase 1 checklist**
- [ ] Daemon read-only built-in-store access (path + sandbox root resolution)
- [ ] `ListBoards` / `BoardRows` / `ClearBoard` service methods (tenant-scoped, name-validated)
- [ ] `GET/DELETE /api/v1/me/boards[...]` endpoints + audit
- [ ] Backend tests incl. table-name injection rejection
- [ ] `/results` route + nav link + icon + i18n (en + sv)
- [ ] `Results.tsx` page: board list, table, search, CSV, clear, empty state
- [ ] api.ts client methods
- [ ] Built-in store drop copy/tags updated for discovery
- [ ] `go build ./... && go vet ./... && go test ./...` green; `tsc -b` green

---

## Phase 2 — A dedicated "Results / Dashboard" drop + board modes

Once the page exists, make the writer obvious and add modes.

- New drop **"Results board"** (`drops/db/` or a new small package) — in
  practice a friendlier-labelled wrapper over `builtin_store_append` that:
  - takes a **board title** param (display name, distinct from the SQL table),
  - takes a **mode** param: `accumulate` (default; append) or `latest` (truncate
    table then write — gives a status-dashboard feel).
  - Follow the non-techie drop UX conventions (label/subtitle, inline pin
    editors, no blob pins) used across the other drops.
- Store board metadata (title, mode, optional chart hint) in a `_hf_boards`
  meta table inside the same SQLite file, so the page can show friendly titles
  and remember the chosen view. Keep meta-table names prefixed so they're
  excluded from the board list.
- Surface board title (not raw table name) on `/results`.

**Phase 2 checklist**
- [ ] Results board drop (title + mode params), registered + manifest example
- [ ] `latest` mode = truncate-then-write; `accumulate` = append
- [ ] `_hf_boards` meta table; board list shows titles, hides meta tables
- [ ] Tests for both modes

---

## Phase 3 — Visualization (charts)

Lowest priority; flashiest. Only after Phases 1–2 land.

- Add **chart view modes** on a board (bar / line / pie) rather than a separate
  "graph drop" — infer sensible x/y from columns, let the user tweak. Non-techies
  won't configure axes, so auto-inference + good defaults matter.
- ⚠️ **Dependency decision required:** charts need a frontend charting library
  (e.g. `recharts`). This is a deliberate dep-add — get sign-off before pulling
  it in (same kind of hold as the `qrcode.react` decision noted in
  `TODO-walkthrough-form-ai-reply.md`). Until then, the table + CSV-download
  (Phase 1) already lets users chart in their own tool.
- Single-number **stat cards** (count / sum / latest value) are a cheaper visual
  win than full charts and need no new dep — consider before charts.

**Phase 3 checklist**
- [ ] Decide + add charting dependency (sign-off)
- [ ] Chart view modes with auto-axis inference
- [ ] Stat cards for single-number results

---

## Cross-cutting notes

- **Auth/visibility:** boards are per `(tenant, workspace)`. Scope every endpoint
  to the principal exactly like `/me/runs` (force `p.Tenant` / `p.Workspace`;
  platform admins may pass a tenant). A workspace-scoped principal sees only its
  workspace's boards.
- **GDPR/retention:** boards ride the existing erasure/export cascade for free
  (sandbox subtree). They are NOT auto-pruned by retention — document this in
  PRIVACY.md § Retention and rely on the per-board "Clear" action. Consider
  adding boards to the data export (`daemon/gdpr_export.go`) as a follow-up so
  Art. 15/20 includes collected results.
- **SQL safety:** the table/board name is the only user-controlled SQL
  identifier on the read path — validate + quote it (`drops/db/idents.go`
  patterns) and only ever query a name confirmed present in `sqlite_master`.
  Never interpolate column names unquoted.
- **Run the app to verify:** `make dev` needs `HAZYFLOW_DEV=1` (sslguard) and the
  manifests compile into hzd, so restart the daemon after drop edits; frontend
  is `make web` (Vite on :5173, proxies the API). Sign in, build a flow with a
  Built-in store · Save step, run it, open Results.

## Open questions to confirm before Phase 2/3

1. Board modes: confirm `accumulate` as the default (vs `latest`).
2. Should boards appear in the GDPR data export immediately, or as a follow-up?
3. Charts: which library, and is the dep-add approved?
