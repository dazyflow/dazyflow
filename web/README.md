# Hazy Flow web UI

React + React Flow + Vite, talking to the daemon's HTTP gateway
(`/api/v1/...`) over bearer-token auth.

## Dev

```sh
cd web
npm install
HAZYFLOW_API=http://localhost:8080 npm run dev
```

The Vite dev server runs on `http://localhost:5173` and proxies
`/api/v1/*` to the daemon set in `HAZYFLOW_API`.

You'll need an API key. The daemon's existing `auth.IssueAPIKey` is the
canonical path; a CLI helper or admin UI for it is on the TODO.

## Layout

```
src/
  api.ts          — typed fetch wrapper over /api/v1
  auth.tsx        — token + principal context, persisted to localStorage
  icons.tsx       — Manifest.Icon → lucide-react component map
  theme.css       — synthwave dark palette ported from ../hazy
  app.css         — shell layout, editor grid, node card styles
  types.ts        — TypeScript mirrors of core.Graph / Manifest / etc.
  App.tsx         — router + auth gate
  components/
    AppShell.tsx  — TopBar with mobile hamburger + side nav
    NodeCard.tsx  — React Flow custom node (icon + label + status dot)
    NodeCatalog.tsx — drag-source panel, grouped by category, searchable
    Inspector.tsx — right panel: node id, label, params (JSON editor)
  pages/
    SignIn.tsx        — bearer-token entry
    FlowList.tsx      — flows in current workspace
    FlowEditor.tsx    — React Flow canvas, save, run, live status
    Admin.tsx         — gated by tenant:admin / graph:admin; stubs
```

## Known stubs

- Admin pages are scaffolds; they need matching `/api/v1/admin/*`
  endpoints (API keys, users, audit).
- Per-node live status during a run is graph-level today (all nodes
  flip on submit and again on terminal). Granular per-node updates
  require the engine bus to publish node-status transitions, not just
  per-node Progress + graph-level Terminal.
- ~~The Inspector's "params" editor is a raw JSON textarea.~~ Now
  renders `manifest.params_schema` as a typed form (string / number /
  boolean / enum / object / dict / array). Unrecognized shapes fall
  back to a JSON textarea. Toggle between "Form" and "Raw JSON" in
  the Inspector head.
- No CSRF / cookie auth — bearer token only. Tighten before exposing
  publicly.
