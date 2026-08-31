# Dazyflow web UI

React + [React Flow](https://reactflow.dev) (`@xyflow/react`) + Vite, talking
to the daemon's HTTP gateway at `/api/v1`.

The same Vite project builds two bundles: the app, and the docs site served at
docs.dazyflow.app (`npm run build:docs`).

## Dev

From the repository root, `make web` does the whole thing. By hand:

```sh
cd web
npm install
DAZYFLOW_API=http://localhost:8080 npm run dev
```

Vite serves on http://localhost:5173 and proxies `/api/v1`, `/trigger` and
`/form` to the daemon at `DAZYFLOW_API` (default `http://localhost:8080`), so
the browser makes same-origin calls and there's no CORS to configure.

You need a daemon to talk to — `make dev` in the root starts one. With no
`.env` it sets `DAZYFLOW_DEV=1`, which seeds a `test@example.com` / `test`
admin you can sign in as.

| Command | Does |
|---|---|
| `npm run dev` | Dev server with HMR |
| `npm test` | The CSS/a11y guards in `scripts/`, then `vitest run` |
| `npm run test:watch` | `vitest` in watch mode |
| `npm run typecheck` | `tsc -b --noEmit` |
| `npm run build` | Production app bundle |
| `npm run build:docs` | Docs-site bundle (needs `make docs-content` first) |

`npm test` runs nine `check-*.mjs` guards before the unit tests — design-system
rules that a type checker can't catch (CSS tokens, class names, breakpoints,
icon sizes, modal a11y, UI primitives, style scales, wide tables, overlay
metrics). They fail with the offending file and line.

## Auth

Two paths, both handled in `src/api.ts`:

- **The browser uses a session cookie.** `dazyflow_session` is `HttpOnly`, so
  the token is never readable from JS. `api.ts` carries the `COOKIE_SESSION`
  sentinel in place of a token to mean "authenticate via the cookie", and sends
  `credentials: "include"`. State-changing cookie-authenticated requests are
  origin-checked server-side for CSRF (`daemon/httpsession.go`) — which is why
  a dev server on a hostname the daemon doesn't expect gets a `csrf_origin`
  403. Add it to `DAZYFLOW_WEB_ORIGIN`.
- **API clients use a bearer token.** `Authorization: Bearer <key>`, no cookie.

## Layout

```
src/
  api.ts         — typed fetch wrapper over /api/v1, and the auth contract above
  auth.tsx       — session + principal context, org reconciliation
  types.ts       — TypeScript mirrors of core.Graph, Manifest, etc.
  App.tsx        — router + auth gate
  theme.css      — design tokens (dark by default, light supported)
  app.css        — shell layout, editor grid, node cards
  icons.tsx      — Manifest.Icon → lucide-react map
  components/    — AppShell, NodeCard, NodeCatalog, Inspector, UI primitives
  pages/         — Dashboard, Apps, Files, Results, Settings, Usage, Welcome,
                   and the auth/ flows/ runs/ admin/ support/ subtrees
  lib/           — pure helpers, each with its own test (CEL highlighting, graph
                   diffing, auto-layout, error explanation, formatting, …)
  i18n/          — en.json + sv.json, plus the drop/template Swedish coverage
                   guards
  docs/          — the docs-site SPA: NAV, markdown renderer, content guards
  test/          — shared test setup and helpers
```

Anything under `src/docs/content/` is **generated** by `make docs-content`
(guide pages copied from `/docs/guide`, step reference emitted by
`cmd/docsgen`) and is git-ignored. Edit the sources, not the copies.

Adding a guide page means two edits: the markdown in `/docs/guide`, and a row
in `NAV` in `src/docs/content.ts`. `src/docs/content.test.ts` fails if you do
only one.
