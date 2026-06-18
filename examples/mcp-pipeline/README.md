# mcp-pipeline — Dazyflow ↔ MCP integration demo

Dazyflow speaks the [Model Context Protocol](https://spec.modelcontextprotocol.io/)
as a client. Any MCP stdio server — the npm-published
`@modelcontextprotocol/server-*` packages, custom Python servers built
with `mcp.server.fastmcp`, your in-house Go binaries — can be registered
at dzd startup. Its tools then appear as Dazyflow modules named
`mcp:<server>:<tool>` and slot into graphs alongside the native modules.

## What this demo proves

| Capability | Where |
|---|---|
| **Subprocess MCP server lifecycle** | dzd spawns `./server`, runs the JSON-RPC handshake, lists tools, keeps the connection alive across graph runs |
| **Tool → manifest synthesis** | The server's `lookup_user` and `categorize` tools appear in `dzctl module list` |
| **Per-tool node** | The graph references `mcp:ap-demo:lookup_user` like any other module |
| **Tool result → downstream nodes** | `lookup_user` returns JSON; `branch` parses it and routes on `tier == "premium"` |
| **Mixed native + MCP graph** | The same graph uses `mcp:*` for the tool call and `file_write` (native) for archival |
| **Tool args from graph params** | `{"id": "alice"}` in the node's params becomes the MCP `tools/call` arguments |

## Topology

```
   ┌────────────────────────────────── dzd ────────────────────────────────┐
   │                                                                       │
   │   ┌──────────────────────────┐                                       │
   │   │  mcp:ap-demo:lookup_user │ ──→ ┌────────┐ → ┌──────────────────┐│
   │   │  (in-process Transport)  │     │ branch │   │ save_premium     ││
   │   └────────────┬─────────────┘     └────┬───┘   │ (file_write)     ││
   │                │ tools/call             │       └──────────────────┘│
   │                ▼                        ↓                           │
   └────────────────│────────────────────[skipped: save_regular]─────────┘
                    │
                    │  JSON-RPC over stdin/stdout
                    ▼
              ┌─────────────────────┐
              │  ./server (Go)      │     ← would normally be:
              │  Tools:              │      npx @modelcontextprotocol/server-slack
              │  - lookup_user       │      uvx mcp-server-postgres
              │  - categorize        │      etc.
              └─────────────────────┘
```

## Run it

```
./run.sh
```

Output ends with five assertions; all should pass. The mock MCP server
in `./server/` is a Go binary that uses the shared `engine/mcp/mcptest`
package — exactly the same harness our unit tests run against, just
compiled to stand alone and exposed via stdin/stdout.

## Using a real MCP server

Drop in any stdio MCP server you have on PATH. Filesystem example:

```
./run.sh   # uses ./server
```

For a real one:

```
dzd \
  --mcp="fs=npx -y @modelcontextprotocol/server-filesystem /tmp"
```

Then the graph could call `mcp:fs:read_file`, `mcp:fs:list_directory`,
etc. Manifest synthesis happens at registration time, so the available
tools depend on which MCP server you wire up.

Multiple servers (semicolon-separated, since args contain spaces):

```
dzd --mcp="fs=npx -y @modelcontextprotocol/server-filesystem /tmp;\
          gh=npx -y @modelcontextprotocol/server-github;\
          slack=python -m mcp_server_slack"
```

## How this fits the bigger picture

The reason MCP belongs in a workflow engine is the same reason a Zapier
clone needs lots of integrations: workflows derive their value from
*what they can touch*. With MCP support, Dazyflow doesn't need a
hand-written module per SaaS. Every MCP server published — there are
hundreds now and the number grows weekly — becomes a usable module.

Compared to the alternative ("AI agent node that wraps an LLM with tool
use baked in"), MCP keeps the *graph* as the locus of agent-shape
logic:

- Every tool call is a separate node, visible in `dzctl job list`
- Retry / skip / fallback / branch policies apply per tool call
- The audit trail records exactly which tools fired with which args
- Cost / budget control can be applied per node, not per opaque agent

## What's not in this demo

- **Per-tenant ACL on MCP servers.** Right now every workspace can
  reach every registered server. Production needs tenant-scoped
  registration (`acme can use mcp:slack but not mcp:fs`).
- **HTTP+SSE transport.** Only stdio. The new "streamable HTTP" MCP
  transport will be needed once SaaS-hosted MCP servers (Anthropic
  Connectors, Cloudflare Workers MCP) become common.
- **Notifications + progress.** MCP supports server-initiated progress
  notifications; we drop them on the floor today. Worth wiring into
  Dazyflow's progress channel.
- **Resource and prompt access.** Only `tools/*` is implemented. MCP
  also exposes `resources/list`, `resources/read`, `prompts/get` — all
  useful but lower-priority than tools.
- **Cancellation propagation.** When a graph run is cancelled mid-flight,
  we currently let the MCP tool call finish. Should send a
  `notifications/cancelled` per the spec.

## Friction caught while building this

1. **Pipe deadlock in unit tests.** Initial test used `io.Pipe()` for both
   directions but only read one side, so writes blocked indefinitely.
   Fix: use `io.Discard` for the "server never replies" test cases.

2. **Test ordering flakes** when multiple webhook+MCP tests bound to
   ephemeral ports under `httptest.NewServer`. Likely a transient port
   collision; would be worth tightening if it returns.

3. **Tool idempotency is unspecified by MCP.** We default to
   `Idempotent: false` (the safer choice) which means `on_error=retry`
   edges to MCP tools fail graph validation. Users who know a specific
   tool is safe to retry would need a hint mechanism we haven't built.
