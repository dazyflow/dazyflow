# Hazy Flow REST API — v1 Design Proposal

Status: **draft for review**. This document proposes the consolidated,
self-describing `/api/v1` that becomes the canonical surface for
humans, scripts, and LLM agents. We are pre-1.0 — there is no
external contract to preserve, so the design replaces today's
`/api/v1` outright rather than versioning alongside it.

## Goals

1. **Discoverable end-to-end.** A new client (human or LLM) lands on
   `GET /api/v1` and can walk to anything it needs without prior
   knowledge of route paths.
2. **Self-describing integrations and drops.** Every node type
   advertises its inputs, outputs, params schema, examples, and a
   plain-language summary the LLM can quote.
3. **Consistent shape.** One pagination contract, one error shape,
   one filter syntax. No bespoke conventions per resource.
4. **Idempotent and safe by default.** Every mutating call accepts an
   `Idempotency-Key` header; reads never change state.
5. **Cheap for non-techies (via the UI) AND cheap for an LLM (via the
   API).** The non-technical UX still lives in the web app, but
   `Connect MCP Client` and any future agent-built flow path will
   call this surface.

## Non-goals

- WebSocket / long-lived streaming. Server-sent events (`text/event-stream`)
  on `/runs/{id}/events` covers the live-tail case; the rest is polling.
- Bulk import/export. Out of scope; a later non-breaking add-on.
- Replacing the trigger webhook / hosted form paths. Those are public
  ingress endpoints and stay on their own routes (`/trigger/`, `/form/`).

## URL shape

All authenticated routes live under `/api/v1`. The scope hierarchy is
**tenant → workspace → resource**, expressed in the path:

```
/api/v1/tenants/{tenant}/workspaces/{workspace}/flows/{flow_id}
```

Tenant and workspace are explicit so a key/session that can access
multiple of either still produces unambiguous URLs. For the common
"single tenant, single workspace" case, an alias resolves
automatically:

```
GET /api/v1/me/flows/{flow_id}    →  same as the explicit form
```

This is **literal path substitution** done in middleware. The LLM
prompt examples we publish will use `/me/...` to keep the surface
short; admin tools use the explicit form.

### Resource conventions

| Pattern                              | Verb    | Meaning                                |
|--------------------------------------|---------|----------------------------------------|
| `GET    /things`                     | list    | paginated, filterable                  |
| `POST   /things`                     | create  | returns the created thing              |
| `GET    /things/{id}`                | read    | one resource                           |
| `PUT    /things/{id}`                | replace | full replacement (idempotent)          |
| `PATCH  /things/{id}`                | update  | partial; JSON Merge Patch (RFC 7396)   |
| `DELETE /things/{id}`                | delete  | idempotent: 204 whether or not present |
| `POST   /things/{id}/action`         | action  | non-CRUD verb on a resource            |

The trailing `/action` path segment (e.g. `POST /flows/{id}/run`,
`POST /runs/{id}/cancel`) signals "this is not a CRUD write" by
position — distinct from `PUT /flows/{id}` (replace) and `PATCH`
(merge). We originally drafted this as `:action` (Google API guide
style), but Go's stdlib `http.ServeMux` doesn't allow a literal
suffix after a `{wildcard}` segment, so we use the path-segment form
instead. Same intent, no third-party router dependency.

## Discovery (the LLM entry point)

Three documents make the entire API navigable:

### `GET /api/v1`

A small "service descriptor" — links to the rest, version, identity.

```json
{
  "service": "hazy-flow",
  "version": "2.0.0",
  "auth": { "scheme": "Bearer", "issue_at": "/api/v1/me/api-keys" },
  "links": {
    "self":         "/api/v1",
    "openapi":      "/api/v1/openapi.json",
    "catalog":      "/api/v1/catalog",
    "me":           "/api/v1/me",
    "flows":        "/api/v1/me/flows",
    "runs":         "/api/v1/me/runs",
    "integrations": "/api/v1/catalog/integrations",
    "drops":        "/api/v1/catalog/drops"
  }
}
```

### `GET /api/v1/openapi.json`

The full OpenAPI 3.1 spec. Generated from the same source the server
mounts routes from (single source of truth — no hand-written drift).
A Custom GPT / Cursor MCP server / curl-wielding human can all consume
this.

### `GET /api/v1/catalog`

A "what can this Hazy Flow do" overview. Returns a few hundred lines
of JSON the LLM can fit in one context window:

```json
{
  "integrations": [
    { "id": "slack", "label": "Slack", "drops": 6 },
    { "id": "gmail", "label": "Gmail", "drops": 3 }
  ],
  "drop_count": 73,
  "categories": ["trigger", "transformation", "io", "ai", ...],
  "links": {
    "integration": "/api/v1/catalog/integrations/{id}",
    "drop":        "/api/v1/catalog/drops/{id}"
  }
}
```

## Catalog (the "what tools exist" layer)

This is the integration-discovery surface that powers both the LLM and
the UI palette. It supersedes today's `GET /api/v1/drops` +
`/api/v1/modules`.

### `GET /api/v1/catalog/integrations`

```json
{
  "items": [
    {
      "id": "slack",
      "label": "Slack",
      "provider": "internal",
      "summary": "Send messages, react to events, fetch channel history.",
      "drop_count": 6,
      "brand_logo": "/assets/slack.svg"
    }
  ],
  "page": { "next": null, "total": 14 }
}
```

### `GET /api/v1/catalog/integrations/{id}`

A "human and LLM both readable" page describing one integration:

```json
{
  "id": "slack",
  "label": "Slack",
  "summary": "...",
  "auth": {
    "kind": "oauth",
    "provider_id": "slack",
    "setup": "/api/v1/oauth/slack:authorize",
    "scopes_required": ["chat:write", "channels:history"]
  },
  "drops": [
    { "id": "slack_send_message",  "label": "Send Slack message", "role": "action" },
    { "id": "slack_event_trigger", "label": "When Slack event",   "role": "trigger" }
  ],
  "examples": [
    {
      "title": "Post to #general when a webhook fires",
      "flow":  "/api/v1/catalog/examples/slack-webhook-fanout"
    }
  ]
}
```

### `GET /api/v1/catalog/drops/{drop_id}`

The detailed shape an LLM needs to actually call a drop. Combines
today's Manifest with a richer params schema description and
worked examples:

```json
{
  "id": "slack_send_message",
  "integration": "slack",
  "label": "Send Slack message",
  "summary": "Post a message to a channel as the connected bot.",
  "category": "io",
  "execution_model": "batch",
  "idempotent": false,

  "inputs":  [{"port":"trigger","mime":["application/json"],"required":true}],
  "outputs": [{"port":"sent",   "mime":["application/json"]}],

  "params_schema": { /* JSON Schema 2020-12 */ },
  "params_examples": [
    {
      "title": "Plain message to a channel",
      "params": { "channel": "#general", "text": "Hello" }
    }
  ],

  "links": {
    "self":        "/api/v1/catalog/drops/slack_send_message",
    "integration": "/api/v1/catalog/integrations/slack"
  }
}
```

### `GET /api/v1/catalog/drops?q=…&category=…&integration=…&tag=…`

Search/filter the drop catalog. `q` is full-text against label,
description, tags; the other params are exact-match filters. Cheap
because the manifest registry is in-memory.

### How integrations self-describe

The catalog endpoints read from `core.Manifest` (which already has
`Category`, `Integration`, `Tags`, `Description`, `Icon`, `BrandLogo`,
`ParamsSchema`). Two extensions land with the rewrite:

1. **`Summary`** field, ~140 chars max, LLM-friendly: "what does this
   drop do, in one sentence." Distinct from `Description` (paragraph)
   and `Label` (chip text).
2. **`Examples []ParamsExample`** — at least one example per drop.
   Authors write them; the API serves them verbatim. Mandatory in
   manifest validation so new integrations can't ship without one.

Both extensions live in `core.Manifest` so the same source of truth
serves the UI palette tooltip and the LLM.

## Flows (the primary resource)

"Flow" is the new public name. Internally `Graph` stays; the API
translates. (Today's `/api/v1/graphs` already exposes the internal
name — that leaks an implementation detail.)

### `GET /api/v1/me/flows`

List, filterable: `?status=`, `?integration=` (any drop in the flow
uses this integration), `?owner=`, `?q=` full-text on name +
description, `?page_token=`. Returns `items[]` + a `page.next` token.

### `POST /api/v1/me/flows`

Create. Body is a `Flow` document (same shape as the read response,
minus server-generated fields). Returns 201 + the created flow. The
`Idempotency-Key` header is honored.

### `GET /api/v1/me/flows/{id}`

Single flow with `nodes[]`, `edges[]`, `triggers[]`, metadata.

### `PUT /api/v1/me/flows/{id}` and `PATCH /api/v1/me/flows/{id}`

Replace or merge-patch. PATCH supports targeted edits like "add this
node", "rewire this edge" — exactly what an LLM constructing a flow
incrementally wants.

### Flow actions

| Path                                       | Purpose                              |
|--------------------------------------------|--------------------------------------|
| `POST /api/v1/me/flows/{id}/run`           | Trigger a run; returns the new run.  |
| `POST /api/v1/me/flows/{id}/validate`      | Lint without saving.                 |
| `POST /api/v1/me/flows/{id}/test-trigger`  | Simulate a trigger payload.          |
| `POST /api/v1/me/flows/{id}/nodes/{nodeID}/sample` | Per-node sample run.         |

## Runs

Today's `/api/v1/jobs/{jobID}` (the implementation calls them "jobs")
is renamed `/api/v1/me/runs/{id}` to match the user-facing term. The
historical jobs ID is preserved as the run id verbatim.

| Path                                    | Purpose                          |
|-----------------------------------------|----------------------------------|
| `GET  /api/v1/me/runs`                  | List, with `?flow_id=`, `?status=`, time range filters |
| `GET  /api/v1/me/runs/{id}`             | Run snapshot                     |
| `GET  /api/v1/me/runs/{id}/nodes`       | Per-node summary                 |
| `GET  /api/v1/me/runs/{id}/nodes/{nodeID}` | One node's snapshot           |
| `GET  /api/v1/me/runs/{id}/events`      | SSE stream of run events         |
| `POST /api/v1/me/runs/{id}/cancel`      | Cancel a non-terminal run        |

## Secrets and OAuth connections

| Path                                              | Purpose                |
|---------------------------------------------------|------------------------|
| `GET    /api/v1/me/secrets`                       | List (names only)      |
| `PUT    /api/v1/me/secrets/{name}`                | Upsert (value in body) |
| `DELETE /api/v1/me/secrets/{name}`                | Remove                 |
| `GET    /api/v1/me/connections`                   | List OAuth connections |
| `POST   /api/v1/me/connections/{provider}:authorize` | Begin OAuth flow    |
| `DELETE /api/v1/me/connections/{provider}`        | Disconnect             |

OAuth was buried under `/api/v1/oauth/{provider}/authorize` in v1;
"connections" is the user-facing word and the URL now reflects it.

## Identity, admin, and org

Identity (today's `whoami`) becomes `GET /api/v1/me`, returning the
principal + a `links` block to the resources they can reach. Admin
moves under `/api/v1/admin/...` mirroring the resource hierarchy
(e.g. `/api/v1/admin/tenants/{tenant}/members`), so admin tools
authenticate the same way as user tools but pivot off the explicit
tenant in the URL.

| Path                                                       | Purpose         |
|------------------------------------------------------------|-----------------|
| `GET  /api/v1/me`                                          | Principal + permissions + links |
| `POST /api/v1/me/api-keys`                                 | Self-issue a key (used by Connect MCP). Roles capped by issuer's roles. |
| `GET  /api/v1/admin/tenants/{tenant}/api-keys`             | Tenant-wide key listing |
| `GET  /api/v1/admin/tenants/{tenant}/audit`                | Audit log       |
| `GET  /api/v1/admin/tenants/{tenant}/members`              | Member roster   |
| `PUT  /api/v1/admin/tenants/{tenant}/auth-config`          | SSO config      |

The `me/api-keys` route is new — today only admins can mint keys via
`/admin/api-keys`. Moving self-issue under `/me` lets the Connect MCP
modal drop its `tenant:admin` gate for the basic case (key scoped to
the current user, no role escalation). Admin issue stays on the admin
path for cross-user keys.

## Common contracts

### Pagination

Every list endpoint:

- Accepts `?page_token=…&page_size=…` (`page_size` default 50, max
  500).
- Returns `{"items":[...], "page":{"next": "…or null", "size": 50}}`.
- Page tokens are opaque, server-encoded; do not parse them client-side.

Counts (`total`) are returned when cheap (in-memory registry endpoints)
and omitted when expensive (run lists over Postgres). Clients must
not depend on `total` being present.

### Errors

One shape across the API:

```json
{
  "error": {
    "code": "validation_failed",
    "message": "params.channel: required",
    "details": [
      { "field": "params.channel", "issue": "required" }
    ],
    "doc": "/api/v1/openapi.json#/components/schemas/SlackSendMessageParams"
  }
}
```

`code` is a snake_case enum (stable, machine-readable). `message` is
LLM/human readable. `details` is optional and structured. `doc` is a
deep link into the OpenAPI spec where applicable — extremely useful
for an LLM that hit an error and needs to read what shape was expected.

Status codes: 400 (bad input), 401 (unauthenticated), 403 (forbidden),
404 (not found), 409 (conflict, e.g. running flow being deleted), 422
(semantic validation failure), 429 (rate-limited), 500 (server bug),
503 (dependency down).

### Idempotency

Mutating routes (`POST`, `PATCH`, `PUT` non-replace semantics) accept
`Idempotency-Key: <opaque-string>`. Duplicate calls within 24h return
the original response. Critical for `/run` and `/approve` so an LLM
retry can't double-fire a flow.

### Filtering, search, and sort

- Filters: simple key=value query params (`?status=running`,
  `?integration=slack`).
- Search: `?q=` for full-text where supported (flows, drops, audit).
- Sort: `?sort=field` or `?sort=-field` (descending). One sort field
  at a time keeps query parsing trivial.

No DSL, no operator syntax — these are LLM-friendly because the LLM
doesn't have to learn a query language.

### Auth

Bearer token (API key) in `Authorization: Bearer …`, exactly as v1.
Session cookies are accepted for browser callers via the same
middleware. The MCP server keeps using the bearer form.

## What we are NOT doing

- **No GraphQL.** Discoverability is solved by OpenAPI + the catalog
  endpoints; introducing GraphQL alongside is two surfaces to keep in
  sync. (Internal worker comms over gRPC are unrelated and unchanged.)
- **No HATEOAS link rels everywhere.** The `links` blocks on the
  service descriptor, `/me`, and integration/drop pages are enough
  for navigation. Adding `_links` to every list item is noise.
- **No event subscription / webhooks-out.** Out of scope; can be added
  as `/api/v1/me/subscriptions` later without breaking anything.

## Implementation plan (sketch)

We are replacing the existing `/api/v1` surface in place. Since the
web UI is the only consumer today and lives in this repo, we can
move it in lockstep — no parallel API period needed.

1. **Lock the OpenAPI spec.** `daemon/openapi.yaml` (this PR) is the
   contract. Review it, iterate, then freeze before code lands.
2. **Extend `core.Manifest`** with `Summary` (140 chars) and
   `Examples` (non-empty `ParamsExample` slice). Backfill all
   built-in integrations; make registration fail closed for new ones
   that omit them.
3. **Refactor the gateway to spec-driven routing.** Generate handler
   stubs + request/response types from `daemon/openapi.yaml`. Hand-write the
   handler bodies but let the spec own the URL patterns, schemas, and
   validation. One source of truth — drift is impossible by
   construction.
4. **Rename internally where the spec uses new public terms.** `Graph`
   stays internal; the handler layer translates to/from `Flow` on the
   wire. Same for `Job` → `Run` and `Module` → `Drop`. Keeps internal
   diffs small while the public surface gets the cleaner names.
5. **Migrate the web UI** to the new endpoints. `web/src/api.ts` is
   the only place that needs to change — replace it wholesale rather
   than maintaining two clients.
6. **Cut over `hz-mcp`** to the new catalog endpoints in the same PR
   as the first MCP tools that need them. Old `/drops` and `/modules`
   handlers go away.
7. **Delete the old handlers.** No `Sunset` header, no deprecation
   period — the rewrite ships as one coherent change. Any external
   API key holders we know about get a heads-up before the cutover.

## Open questions for the next pass

1. **Should `Flow` be exposed as JSON-only or also as YAML?** YAML is
   friendlier to copy-paste into LLM chat; JSON is friendlier to
   programmatic clients. Probably both via `Accept: application/yaml`.
2. **Post-launch versioning policy.** Lock to `/api/v1` and add
   non-breaking changes only? Or treat OpenAPI changelogs as the
   contract and let the path stay frozen? Recommend the latter — fewer
   moving parts for an LLM caller.
3. **How aggressive should the `Examples` requirement be?** Hard-fail
   integration registration without one, or warn? Hard-fail is the
   forcing function; warn keeps the door open for partial integrations.
4. **Connections vs OAuth providers** — should each drop in the catalog
   advertise which connections it needs (`requires_connections: ["slack"]`)
   so the LLM knows what to set up first? Strongly recommend yes.
