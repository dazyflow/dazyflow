---
name: run-web-e2e
description: Launch the hazyflow web app and drive it in a real (headless) browser to verify UI changes end-to-end. Use when asked to run/verify the web UI, click through a feature, or screenshot the app — not for unit tests. Covers the Go daemon + Vite + Playwright/Chromium stack and the headless-machine workarounds (staged libs/fonts, no sudo).
---

# Run & drive the hazyflow web app (E2E)

This launches the **real** app — current-source daemon, Vite serving the
frontend, headless Chromium driving the UI — and interacts with it as a
user would. It was cold-started and verified on a **headless Arch box
with no sudo, no system browser, and no fonts**, so the unobvious setup
(staging shared libs + fonts into `/tmp`) is committed here. Follow it
verbatim.

Repo root below is written as `$ROOT` =
`/home/klarre/dev/sourcehut/~klahr/hazyflow` (note the literal `~klahr`
path segment — it is NOT home expansion; `~` mid-path is literal in
bash, so the path works unquoted).

## 0. Why not just `make web` + `make dev`?

- `make dev` needs Postgres and binds the gRPC port `:50050`. If a daemon
  is **already running** (common — the dev instance on `:8089`), a second
  one collides on `:50050`. Run yours on **alt ports** instead (below) and
  leave the existing one alone.
- A stale prebuilt daemon (e.g. `/tmp/hzd`) may predate routes your
  current frontend calls (`/api/v1/me/flows`). Always run the daemon from
  **current source** so frontend and backend match.

## 1. Headless deps (no sudo) — stage libs + fonts into /tmp

Chromium won't launch without ~8 X/ATK libs, and crashes (Skia FATAL)
without a font + fontconfig. On Arch without sudo, fetch the packages and
extract them locally. Run the helper:

```bash
bash "$ROOT/.claude/skills/run-web-e2e/scripts/stage-headless-deps.sh"
```

It downloads `at-spi2-core libxcomposite libxdamage libxrandr
libxkbcommon libxext libxrender libxi ttf-dejavu` from the Arch mirror,
extracts the `.so`/`.ttf` files to `/tmp/libs` + `/tmp/fonts`, and writes
`/tmp/fonts/fonts.conf`. After it, every browser run needs:

```
LD_LIBRARY_PATH=/tmp/libs   FONTCONFIG_FILE=/tmp/fonts/fonts.conf
```

**Bake these into the Playwright script's `process.env`** (set them at the
top, before importing playwright) rather than on the command line — the
harness sometimes fails to launch long env-prefixed commands.

## 2. Install Playwright + Chromium (once)

```bash
cd /tmp && npm install playwright@latest && npx playwright install chromium
```

## 3. Daemon (current source, alt ports, fresh Postgres DB)

The daemon is **Postgres-backed — it will not boot without a DSN** (there
is no in-memory mode). The bundled dev Postgres container handles this, but
two traps will eat your first three restarts if you don't pre-empt them:

- **The container's real creds are `hazy` / `hazy` / `hazy`, NOT the
  `hazyflow` defaults in `.env`.** The pgdata volume was initialised with
  `hazy` and `.env` was edited afterwards, so the live container ignores
  `.env`. Confirm the truth before building the DSN:
  ```bash
  docker exec hazy-postgres-1 env | grep -E 'POSTGRES_(USER|DB|PASSWORD)'
  ```
- **The long-lived `hazy` database has a stale schema** (e.g. missing
  `users.subject`) → `signup` 500s with `column "subject" ... does not
  exist`. Don't migrate or touch it. Instead **create a throwaway DB and
  let the daemon run migrations fresh against it** (reset it each run so
  the schema always matches current source):
  ```bash
  docker exec hazy-postgres-1 psql -U hazy -d hazy \
    -c "DROP DATABASE IF EXISTS hazye2e;" -c "CREATE DATABASE hazye2e;"
  ```

Then launch the daemon. **`HAZYFLOW_DEV=1` is required** — without it the
daemon refuses to start, citing the default DB password and empty master
key as insecure production config:

```bash
cd "$ROOT" && \
HAZYFLOW_HTTP=:8090 HAZYFLOW_LISTEN=:50051 HAZYFLOW_DEV=1 HAZYFLOW_DEV_KEY=1 \
HAZYFLOW_ENABLE_SIGNUP=1 HAZYFLOW_WEB_ORIGIN=http://localhost:5173 \
HAZYFLOW_PUBLIC_BASE_URL=http://localhost:8090 \
HAZYFLOW_MASTER_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= \
HAZYFLOW_POSTGRES_DSN="postgres://hazy:hazy@localhost:5432/hazye2e?sslmode=disable" \
exec go run ./cmd/hzd
```

Run it with the Bash tool's `run_in_background: true` and **no trailing
`&`** (the `&` + background wrapper fight and the process gets SIGTERMed).
First `go run` compiles — wait ~15s, then poll `curl -s -o /dev/null -w
'%{http_code}' http://localhost:8090/` until it's non-`000` (a `404` at `/`
is fine — it means the API is up); the log says `http-api: listening on
[::]:8090`. State (flows, jobs, keys) now lives in the `hazye2e` Postgres
DB, not on disk.

## 4. Vite (project-local binary, pointed at the daemon)

```bash
cd "$ROOT/web" && HAZYFLOW_API=http://localhost:8090 exec node_modules/.bin/vite
```

- Use **`node_modules/.bin/vite`**, NOT `npx vite` — npx fetches a *newer*
  vite (8.x) that 404s the whole app. The project pins vite 5.4.
- `HAZYFLOW_API` overrides the proxy target (default is `:8080`).
- `run_in_background: true`, no `&`. Confirm `curl -s -o /dev/null -w
  '%{http_code}' http://localhost:5173/` → `200` and the served HTML
  references `/@vite/client`.

## 5. Auth + seed (via API — keeps the token off the CLI)

The session token contains an `hzs_…` secret; passing it on the shell
command line intermittently trips the harness. Do auth + seeding **inside
the node script with `fetch`**, reading nothing from the CLI. Sign up:

```
POST http://localhost:8090/api/v1/auth/signup
{ "email": "...", "password": "TestPassw0rd!23" }  → { token, tenant, workspace:"main" }
```

Seed a flow (note single-encoded `%2F` in the flow_id path segment):

```
PUT http://localhost:8090/api/v1/me/flows/{tenant}%2Fmain%2F{id}
{ "id":"...", "tenant":"...", "workspace":"main", "name":"...",
  "nodes":[{"id":"hook","module":"webhook_input","params":{},"position":{"x":200,"y":160}}],
  "edges":[] }
```

## 6. Drive it

A ready-to-edit harness is committed — it seeds a fresh flow, injects the
session, navigates, and runs checks with screenshots:

```bash
node "$ROOT/.claude/skills/run-web-e2e/scripts/drive.mjs"
```

Auth is injected via `localStorage` before app code runs
(`context.addInitScript`):

```js
localStorage.setItem("hazyflow.token", token);
localStorage.setItem("hazyflow.activeTenant", tenant);
localStorage.setItem("hazyflow.activeWorkspace", "main");
```

**Always screenshot and LOOK at it** — a blank canvas means a failed load,
not a pass. Read the screenshots with the Read tool.

## 7. Two gotchas that will waste an hour if you skip them

1. **Workspace-resolution race → load with empty workspace.**
   `activeWorkspace` starts `""` and only becomes `"main"` after an async
   `listWorkspaces`. A full `page.goto('/flows/<id>')` mounts the editor
   before that resolves → it builds `tenant//id` (empty workspace) → flow
   load 400s → empty canvas. **Fix: client-side navigate.** `goto('/flows')`
   (the list waits for the workspace), then **click the flow card**
   (`getByRole('link', {name: /<flow-id>/})` — the card shows the **id**,
   not the name). The editor then mounts with `main` already set.

2. **Saving from a half-loaded editor wipes nodes.** The Save button in
   modals calls `buildGraph()` from current editor state and PUTs it. If
   the node never loaded (gotcha #1), Save persists `nodes: []`,
   **destroying the seeded node on disk**. Symptom on later runs: node
   won't render, toolbar won't swap to "Send test event". **Fix: use a
   fresh flow id per run, and only Save after confirming the node
   rendered** (`.react-flow__node` count > 0).

## 8. Cleanup

Kill only what you started; leave any pre-existing daemon (`:8089`) alone.
Note `go run` forks a **child `hzd` binary** that holds `:8090` — killing
the listener pid alone leaves the parent (or vice-versa), so also
`pkill -f cmd/hzd`. Then drop the throwaway DB (the daemon must be dead
first, or Postgres reports "being accessed by other users"):

```bash
pkill -f "node_modules/.bin/vite"
PID=$(ss -ltnp | grep ':8090' | grep -oE 'pid=[0-9]+' | cut -d= -f2); [ -n "$PID" ] && kill "$PID"
pkill -f 'cmd/hzd'
sleep 3
docker exec hazy-postgres-1 psql -U hazy -d hazy -c "DROP DATABASE IF EXISTS hazye2e;"
```
