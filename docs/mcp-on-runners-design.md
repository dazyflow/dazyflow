# MCP servers on runners — design note

Status: **NOT BUILT — design note only.** Written 2026-08-27, the day per-org
HTTP MCP servers shipped (see the `[Unreleased]` entry in `CHANGELOG.md` and
[docs/guide/mcp-servers.md](./guide/mcp-servers.md)). This is the third leg of
that story, written while the context was fresh; it is **not** scheduled. Build
it when a customer says "our tool only ships as a command," not before.

Where the code would live: `engine/mcp/` (a third registration path beside
`RegisterStdio` / `RegisterHTTP`), `daemon/mcpservers.go` +
`daemon/mcpservers_pg.go` (the `transport` discriminator and the persisted tool
list), `daemon/runner_tasks.go` (nothing new if Phase 1 holds), and
`web/src/pages/admin/AdminMCPServers.tsx` (one more field, one more status).

## The gap this closes

Two populations of MCP server exist today and neither is reachable by a tenant:

- **Command-distributed servers.** Most of the ecosystem — `npx @foo/mcp-server`,
  `uvx …`, a downloaded binary. They have no URL, so an org's only route is to
  host one behind a web address themselves. `DAZYFLOW_MCP_SERVERS` handles this
  for the *operator*, instance-wide, and deliberately not for a tenant: see
  `StdioDescriptor`'s doc comment for why.
- **Servers inside the org's own network.** The HTTP path refuses loopback,
  RFC 1918 and link-local at dial time (`hfnet.SSRFDialControl`, wired in
  `applyNetworkPolicy`). That is correct and must not be relaxed — which means
  "an MCP server for our internal ERP" is *impossible over HTTP by design*.

A runner answers both, because a runner is a machine the org owns, inside the
org's network, already running an agent.

## Why a runner, and not the daemon

The reason tenant stdio is refused is not the transport, it is **whose machine
the process runs on**. `daemon/runners.go` already states the runner bargain:

> a runner executes a script the FLOW supplies, so whoever can edit a flow can
> run commands on that machine.

So running an MCP server on a runner grants **no new capability**. It is
strictly less than `run_on_runner` already allows. On the daemon the same
feature is remote code execution as the daemon user, available to any customer
on a shared instance. Same mechanism, entirely different blast radius — and that
asymmetry is the whole justification for this design existing at all.

## Phase 1: one session per tool call — no agent change

The runner protocol is pull-based and one-shot: `RunnerTask` carries
`Script` + `Shell` + `Env` + `Stdin` + `Timeout` and comes back as
`Result{ExitCode, Stdout, Stderr}` (`daemon/runner_tasks.go`,
`drops/runner/run.go`). An MCP session is stateful and long-lived, so the two
do not obviously fit.

They fit if a **call** is the unit rather than a session. To invoke one tool:

1. The daemon renders a small Python script: spawn the configured command, do
   `initialize` + `notifications/initialized`, send one `tools/call`, print the
   result as JSON on stdout, exit.
2. Enqueue it as an ordinary runner task with `Shell: "python"` (already in
   `drops/runner.Shells`) and the tool arguments as `Stdin`.
3. The agent claims it, runs it, posts the result. The MCP transport reads the
   result off stdout.

**The MCP client lives in the generated script, not in the agent.** That is the
point of Phase 1: no new endpoints, no agent release, no change to the poll
loop, and `dzrunner.py` stays the one dependency-free file it is today. The
whole feature becomes a daemon-side concern plus a Python template.

The cost is a process spawn per call, and an `npx` cold start is seconds. See
Phase 3.

### What holds this together on the engine side

`engine/mcp.Catalog` is already keyed by `(tenant, tool id)` (`toolKey`), so a
runner-hosted server needs no new scoping. It needs a `session` implementation
(the one-method interface `serverConn` holds) whose `CallTool` dispatches a
runner task instead of writing to a pipe — so `Transport.Execute`,
`buildArguments`, `synthesizeManifest` and the port synthesis are all reused
unchanged. Mark it `concurrent: true`: separate tasks, no shared stream.

Dispatch must go through the same indirection `run_on_runner` uses
(`runnerdrop.SetDispatcher` / `runnerBridge` in `cmd/dzd/main.go`) rather than
importing the daemon, because `engine/mcp` cannot reach `daemon` and must not
try.

## The hard part: the palette, not the transport

This is the decision worth writing down, because it is the one that will be
expensive to re-derive.

Today `RegisterHTTP` handshakes **synchronously** and caches the tool list in
memory. That is how an org's steps are in its palette before any run, and how
`MCPServers.Reconcile` can treat the catalog as authoritative-and-ephemeral —
rebuilt from live handshakes on every boot, holding nothing that Postgres does
not also hold.

A runner-hosted server breaks both properties:

- **Listing tools needs a machine that may be asleep.** Registration cannot be
  synchronous. A new server sits in a *"waiting for a machine to come online"*
  state until some eligible runner claims the list-tools task.
- **The tool list must be persisted.** Otherwise the org's palette empties
  whenever their laptop sleeps — and worse, their *saved flows* reference
  `mcp:<server>:<tool>` ids that stop resolving. A flow must not break because
  a machine is off; it must fail at the step, with a reason.

So the catalog becomes partly a **cache of persisted manifests** for this
transport. Concretely:

```sql
CREATE TABLE tenant_mcp_server_tools (
    tenant     TEXT NOT NULL,
    server     TEXT NOT NULL,
    tool       TEXT NOT NULL,
    manifest   JSONB NOT NULL,   -- the synthesized core.Manifest
    listed_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant, server, tool)
);
```

`Reconcile` then registers runner-hosted servers **from the stored manifests**,
without contacting anything, and re-lists only when a tool-list task succeeds.
Staleness is shown, never hidden: the admin page says *"tools last read
{when}"*, and a server whose machines have all been offline past
`RunnerOnlineWindow` reads as *"no machine online"* rather than as connected.

> **Alternative considered and rejected:** keep it ephemeral and re-list at
> boot. It makes daemon startup depend on a customer's laptop being awake, and
> makes the palette flap. Not acceptable.

## Data model

`MCPServer` (in `daemon/mcpservers.go`) gains a discriminator rather than a
parallel type:

- `Transport` — `"http"` (today's only value, and the default for every existing
  row) or `"runner"`.
- `Command` + `Args` — the program, for `runner`.
- `Tags` — which machines may host it, reusing the runner tag vocabulary
  exactly (`Runner.Tags()` / `HasTags`): a machine's own name is one of its
  tags, so pinning to one machine is a single-tag list. No second "which
  machine" concept.
- `URL` / `AuthKind` / `AuthHeader` / the sealed token stay `http`-only.
  Validation splits on `Transport` and each half refuses the other's fields, so
  a row cannot be half-configured for both.

Env for the subprocess is worth having (an MCP server usually wants an API key)
and should reuse the sealing the runner queue already does for `RunnerTask.Env`
— `PgRunnerTaskStore.Cipher`, wired in `setupRunners`. Do not invent a second
mechanism.

## Two things to decide deliberately

**The allowlist is sharper than it looks.** The agent already supports a
permitted-command list, and `plan_interpreter` in `runner/dzrunner.py` reasons
about exactly this trap: the list must name *the interpreter*, not the script,
because a runner allowed `python` can run any Python. The same applies here and
harder — allowing `npx` allows **any npm package on the internet**. This belongs
in the docs as a stated consequence, not left for an org to discover. Phase 1
runs MCP servers via `python`, so an org that has restricted its runner to a
command set will find MCP tasks refused until `python` is permitted; the error
must say so.

**A pool is not one machine.** Tags may match several runners, so two calls can
land on two machines running two copies of the server — fine for the stateless
request/response most tools are, wrong for anything holding state between
calls. Phase 1 should say so plainly rather than pretend otherwise; Phase 3 is
where it stops being true.

## Phasing

1. **Session per tool call.** Transport discriminator, persisted tool list,
   async registration, one Python template, admin-page fields. No agent change.
   This is the whole feature for most tools.
2. **Tool-list refresh on a timer.** Re-list when the stored list is older than
   some window and a machine is online, so a server that gained a tool is
   picked up without anyone pressing the button.
3. **Persistent sessions.** Only if per-call startup proves to be the problem —
   a `for_each` over 100 rows is 100 spawns. Needs a real protocol change: a
   session id, the agent holding the subprocess across polls, and the "each
   agent runs one job at a time" model revisited. Do not start here.

## Decisions taken (2026-08-27)

- **Runner-hosted, not daemon-hosted, and not "stdio for tenants".** The
  security argument is about whose machine runs the process, so the answer is
  the org's machine. `DAZYFLOW_MCP_SERVERS` remains operator-only and unchanged.
- **A call is the unit, not a session.** It fits the existing queue exactly and
  keeps the agent untouched, which is worth more than the latency it costs.
- **The tool list is persisted for this transport.** A palette that empties when
  a laptop sleeps is not shippable.
- **Reuse the tag vocabulary and the env sealing.** No new targeting concept, no
  second secret path.
- **One `MCPServer` type with a discriminator**, not two types — the admin page,
  the store, the reconcile loop and the audit trail are all shared.

## Open questions

- What does a run do when no eligible machine is online? Failing the step with
  *"no machine with these tags is online"* is right; whether it should instead
  **wait** (as `run_on_runner` does, up to its timeout) is a real choice and
  probably the better one for a scheduled flow.
- Should a runner-hosted server be allowed to shadow an HTTP one's name within
  the same org? `attach` currently refuses a tenant taking an instance-wide
  name; the same-tenant case is a rename question and the UI locks names, so
  probably moot.
- Does the tool list need per-machine identity? Two machines could serve
  different *versions* of the same server. Recording which runner produced the
  listing would make that visible; whether anyone cares is unknown.

## Deferred (deliberately not designed)

- Streaming / progress from an MCP tool call. `RunnerTask.Progress` exists and
  could carry it, but no MCP tool the ecosystem ships today needs it.
- MCP resources and prompts. `engine/mcp` models only `initialize`,
  `tools/list`, `tools/call`; nothing here changes that.
- Installing or updating the server program. That is the org's business on their
  own machine, exactly as it is for anything `run_on_runner` invokes.
