# Scripted drops (JS/TS)

> **Superseded as the execution mechanism** by containerized drops (see
> `engine/containerdrop/DESIGN.md`) once the project went hosted-first. This
> goja runtime was the spike that proved the component model, capability
> surface, install pipeline, and trust tiers — all of which carry over. The
> capability surface here becomes the broker API there; only the in-process
> goja *executor* is retired. Kept for reference.

Status: **working spike, wired into the daemon and tested.** A drop authored in
TypeScript loads from a configured directory at startup, appears in the palette,
and executes through the engine exactly like a native drop. Not yet hardened for
fully-untrusted authors (see [Safety](#safety-posture-mvp--goja-in-process)) and
no marketplace plumbing yet (see [Next steps](#next-steps)).

## Current status

Implemented across these files:

| File | Role |
|------|------|
| `sdk/hazyflow-drop.d.ts` | The author contract (TypeScript types for `manifest` + `run` + the `DropContext` capabilities). Stable across engines. |
| `executor.go` | `Compile` (esbuild → goja) and `CompiledDrop.Run` (per-job VM on a goja_nodejs event loop), capability injection, the `promiseFrom` async helper, and the `ctx.Done()→vm.Interrupt` watchdog. Core-free. |
| `transport.go` | `core.Transport` adapter, camelCase-manifest → `core.Manifest` mapping, `jobFiles` (os.Root-confined + quota), and the `TokenResolver`/`QuotaReserve` hook types. |
| `catalog.go` | The 5th `NodeResolver` catalog: `Register` / `LoadDir` / `Get` / `Manifests`, with `HTTP` / `Tokens` / `Quota` injection points. |
| `engine/resolver.go` | `NodeResolver.Script` field, consulted last in `Resolve` + `Manifests`. |
| `cmd/hzd/main.go` | `HAZYFLOW_SCRIPTED_DROPS_DIR`, the guarded `fetch` doer (egress + SSRF), the OAuth token resolver, and the `FSQuota.Reserve` hook. |

Capabilities backed today: `fetch` (SSRF + egress guarded, with `timeoutMs` +
opt-in `expectStatus`), `secrets`, `auth.token` (host-refreshed OAuth), `crypto`
(hmac/hash/hex/base64 incl. url/no-pad, randomBytes, utf8), `files`
(read/readText/write/exists, sandboxed + atomic quota), `inputs`
(get/ref/all/has — single + variadic ports, with file-backed paths), `env`
(non-secret only), `log` + `progress`. Typed failures via `DropError(code, msg)`
map onto the engine's JobError; a returned `{ mime, value }` sets an explicit
output MIME. `inputs`, `env`, the crypto/fetch additions, `DropError`, and Ref
outputs landed with the first connector port (see next step 5).

Test coverage (`engine/jsdrop/*_test.go`, `engine/script_resolver_test.go`):
end-to-end pagination via `fetch`; secret-denial; `crypto.hmac` vector;
`LoadDir` registration; resolve-and-execute through `NodeResolver` with manifest
mapping; `files` write/read round-trip + traversal refusal + atomic-quota
refusal; `auth.token` resolution; and the runaway-loop interrupt (race-clean).

**Implementation note:** the execution mechanics sketched below are conceptual.
The shipped code (`executor.go`) uses `loop.Start()` + `RunOnLoop` with a small
JS promise-bridge shim (`__invoke`) to hand the settled `run()` result back to
Go, and `promiseFrom` to back the async capabilities. The transpile target is
**ES2017** (keeps `async`/`await` native; goja supports it) rather than ES2015.

## Shape

```
TypeScript  ──esbuild (Go lib)──▶  JavaScript (ES2017)  ──▶  goja VM
 author DX      strip types +          what the engine          + injected
 + .d.ts        downlevel syntax        actually runs            capabilities
```

A scripted drop is a JS module whose default export is a `DropDefinition`
(`{ manifest, run }`). It maps onto the existing `core.Transport` interface, so
the canvas, `ListDrops`, and `SchemaForm` need **zero** changes — a scripted
drop's manifest flows through the same path as a native one.

The trust model is **capability injection**: goja starts with an empty global
object, and the host adds exactly the surface in the `.d.ts` — `fetch`,
`secrets`, `auth`, `crypto`, `files`, `log`. No `require`, no `process`, no
ambient network/fs. A drop can only do what it was handed.

## Engine seam

Add a fifth catalog to `engine.NodeResolver`, after the existing four:

```go
type NodeResolver struct {
    Native *Registry
    Local  *LocalCatalog
    Remote *RemoteCatalog
    MCP    *mcp.Catalog
    Script *jsdrop.Catalog // NEW — resolves "script:<id>"
}
```

`jsdrop.Catalog` loads drop sources (from disk now; from the marketplace
registry later), transpiles + parses each once to read its manifest, and hands
back a `jsdrop.Transport` per id. Same `Manifests()` / `Get(id)` contract as the
other catalogs.

```go
type Transport struct {
    manifest core.Manifest
    program  *goja.Program // compiled once, reused per job
    host     *Host         // builds the per-job capability sandbox
}

func (t *Transport) Manifest() core.Manifest { return t.manifest }

func (t *Transport) Execute(
    ctx context.Context, job core.Job, progress chan<- core.Progress,
) (core.Result, error) {
    return t.host.run(ctx, t.program, job, progress)
}
```

## Loading: transpile → compile → read manifest

```go
import "github.com/evanw/esbuild/pkg/api"

// transpile strips TS types and downlevels modern syntax to a dialect goja
// runs. esbuild is pure Go — no Node toolchain on the host.
func transpile(src string) (string, error) {
    r := api.Transform(src, api.TransformOptions{
        Loader: api.LoaderTS,
        Format: api.FormatCommonJS, // goja resolves module.exports
        Target: api.ES2017,         // keeps async/await native
    })
    if len(r.Errors) > 0 {
        return "", fmt.Errorf("transpile: %s", r.Errors[0].Text)
    }
    return string(r.Code), nil
}
```

On load, run the module once in a throwaway VM to read `module.exports.default
.manifest`, convert it to `core.Manifest`, and enforce the same registration
checks native drops get (non-empty `summary`, ≥1 `examples`). Cache the
compiled `*goja.Program`.

## Per-job execution + the event loop

`fetch`/`sleep`/`poll` are async, so the VM needs a microtask pump. Use
`goja_nodejs/eventloop`; the `run()` promise is resolved/rejected on the loop.

```go
import (
    "github.com/dop251/goja"
    "github.com/dop251/goja_nodejs/eventloop"
)

func (h *Host) run(
    ctx context.Context, prog *goja.Program, job core.Job, progress chan<- core.Progress,
) (core.Result, error) {
    // Hard timeout / cancellation. Interrupt unwinds even a tight `while(true)`.
    runCtx, cancel := context.WithTimeout(ctx, h.maxRun)
    defer cancel()

    loop := eventloop.NewEventLoop(eventloop.WithRegistry(h.registry))
    loop.Start()
    defer loop.Stop()

    var (
        out core.Result
        err error
        done = make(chan struct{})
    )

    loop.RunOnLoop(func(vm *goja.Runtime) {
        go func() { // watchdog: external cancel/timeout kills the VM
            <-runCtx.Done()
            vm.Interrupt(runCtx.Err())
        }()

        // Empty global object, then add ONLY the contract surface.
        h.injectCapabilities(vm, runCtx, job, progress)

        def, rerr := loadDefault(vm, prog) // module.exports.default
        if rerr != nil {
            err = rerr
            close(done)
            return
        }

        ctxObj := h.buildDropContext(vm, runCtx, job, progress)
        promise := callRun(vm, def, ctxObj) // def.run(ctx)

        // Settle the returned promise on the loop, then marshal outputs.
        h.settle(vm, promise, func(val goja.Value, rejected bool) {
            if rejected {
                err = h.toJobError(val)
            } else {
                out = h.toResult(job, val) // DropOutput → map[string]core.Ref
            }
            close(done)
        })
    })

    <-done
    return out, err
}
```

### Capability injection (the interesting part)

Each capability is a Go closure exposed as a JS function/object. `fetch` is the
load-bearing one — it delegates to the **same guarded client the `http_request`
drop uses**, so SSRF blocking, the operator egress allowlist, and the tenant
byte-quota apply uniformly. A scripted drop has no other network path.

```go
func (h *Host) injectCapabilities(
    vm *goja.Runtime, ctx context.Context, job core.Job, progress chan<- core.Progress,
) {
    // fetch(url, opts) -> Promise<FetchResponse>
    vm.Set("__fetch", func(call goja.FunctionCall) goja.Value {
        url := call.Argument(0).String()
        opts := parseFetchOptions(vm, call.Argument(1))

        // Reuse the existing guard + allowlist + quota accounting. Same code
        // path as integrations/net.http_request — no second implementation.
        resp, ferr := h.http.Do(ctx, job, url, opts) // SSRF + egress + quota inside
        if ferr != nil {
            return vm.NewGoError(ferr) // surfaces as DropError on the JS side
        }
        return wrapResponse(vm, resp) // status/ok/headers + json()/text()/bytes()
    })

    // secrets.get(name) — scoped to job.Env entries this drop declared.
    vm.Set("__secretGet", func(name string) (string, error) {
        if !h.granted(job, name) {
            return "", fmt.Errorf("secret_denied: %q not granted to this drop", name)
        }
        return job.Env["secret:"+name], nil
    })

    // crypto.hmac / hash / encode — thin wrappers over crypto/hmac, crypto/sha*.
    vm.Set("__hmac", h.hmac)
    vm.Set("__hash", h.hash)

    // log.* → forward to the run log / progress channel.
    vm.Set("__log", func(level, msg string, data goja.Value) {
        progress <- core.Progress{JobID: job.ID, NodeID: job.NodeID, Message: msg}
    })

    // files.* → confined via os.Root. engine/jsdrop is outside integrations/,
    // so it can't import integrations/internal/sandbox — resolveRoot mirrors
    // sandbox.Resolve (scratch:// vs bare-workspace). files.write reserves
    // bytes through the same FSQuota.Reserve the file_write drop uses.
    vm.Set("__fileRead", h.fileRead(ctx, job))
    vm.Set("__fileWrite", h.fileWrite(ctx, job))
}
```

The user-facing ergonomic objects (`ctx.fetch`, `ctx.secrets.get`,
`ctx.crypto.hmac`, the `FetchResponse` with real `.json()`/`.text()`) are a
small JS prelude, injected ahead of the drop, that wraps those `__`-prefixed Go
primitives into the `.d.ts` shape. Keeps the Go/JS boundary tiny and the author
API clean.

## Safety posture (MVP = goja in-process)

| Concern        | MVP mitigation                                                        |
|----------------|------------------------------------------------------------------------|
| Runaway CPU / hangs | **executor-enforced wall-clock budget** (`RunInput.MaxRunDuration`, default 60s, `HAZYFLOW_SCRIPTED_DROP_TIMEOUT`) + `vm.Interrupt` watchdog. Bounds a tight `while(true)` AND an idle never-settling `await`, *without trusting the caller to pass a deadline*. A caller deadline, if sooner, wins. |
| Network        | `fetch` is the only egress; SSRF guard + allowlist + quota inherited   |
| Filesystem     | `files.*` only, confined to job roots via `os.Root`                    |
| Ambient access | empty global object; no `require`/`process`/`eval`-of-host             |
| Secrets        | per-drop grant check; OAuth refresh stays host-side (`auth.token`)     |
| **Memory**     | **weak** — goja shares the daemon heap; no hard cap. See below.        |
| CPU granularity | wall-clock only; no per-instruction fuel (goja has none). See below.  |

Wall-clock is now bounded properly: the executor wraps every run in its own
deadline and interrupts the VM, so a buggy/hostile drop can't hang or spin a
worker forever even if the caller forgot a timeout (the watchdog goroutine also
exits via that deadline rather than leaking). What goja still **can't** do is a
hard **heap cap** or **CPU fuel** — a drop allocating gigabytes hurts the daemon,
and tight CPU is bounded only by the clock. Under the [uniform trust model](#distribution--trust-model)
this is **load-bearing, not a later nicety** — community drops get the same
runtime surface as official ones, so the sandbox is the whole safety boundary.
The fix is to swap the engine to **QuickJS compiled to WASM on wazero** —
pure-Go host, hard memory cap + CPU fuel, identical capability injection via host
functions. **The `.d.ts`, the author experience, and every published drop are
unchanged** — only `Host.run` swaps its engine. That's the whole reason to pin
the contract now, and why the swap is high on [Next steps](#next-steps).

## Distribution & trust model

Decided: **total uniformity.** There is no privileged drop. Hazy's own
connectors (Gmail, Slack, …) are ordinary scripted drops that run through the
exact same executor as a stranger's drop — no compiled fast-path, no special
runtime access. This is a deliberate dogfooding bet: the platform's capability
surface is proven by its own connectors, and the trusted core stays as small as
the sandbox itself.

**The badge is signature-derived and purely informational.**

- `official` — signed by **Hazy's key**.
- `verified` — signed by a **publisher key Hazy has reviewed** (the "open
  submissions + review" tier; a human read the source).
- `community` — unsigned / self-signed; shown with a "not reviewed" warning.

A drop **cannot mark itself** — the tier is computed at install/load time from
signature verification, never read from the manifest's self-claim. (This is
what upgrades today's `Manifest.Provider` from an unverified string into a
verified fact.) The badge changes **nothing** about the runtime: every drop,
official or community, gets the same sandbox and the same limits.

**Why an "informational" badge still has teeth.** With no per-tier runtime
gating, safety rests on exactly two things:

1. **The capability sandbox** — uniform, therefore it must be strong enough to
   run *a stranger's drop* with the same surface Gmail has. (This is why the
   wazero hardening below is now load-bearing, not optional.)
2. **Per-node credential consent** — a drop only ever touches the secrets/tokens
   the user wired into *that node*. A community Stripe drop can't read your
   Stripe key unless you hand it to that node.

The badge gates the *human's consent decision*, not the runtime: it's what tells
a user "this drop is community, unreviewed" at the moment they're about to wire
a token into it. That's where it earns its keep.

**Performance** is a non-issue for this: connector drops are I/O-bound (the
HTTPS round-trip dominates), so goja's interpreter overhead on ~30 lines of glue
is negligible. goja-vs-compiled only matters for CPU-heavy work, which
connectors aren't.

**The one seam.** A drop can *use* an OAuth token (`ctx.auth.token("google")`)
and any drop — official or third-party — may declare
`requiresConnections: [{kind:"oauth", name:"google"}]` to reuse an
already-registered provider. So a stranger's "Gmail label sorter" is genuinely
identical to official Gmail. But **registering a brand-new OAuth provider**
(client id/secret/scopes/redirect for a service Hazy hasn't onboarded) is
operator infra, not something a drop can ship. OAuth-against-existing-provider
and secret/API-key auth are fully uniform; new-provider registration is the only
privileged step.

## Next steps

Reordered by the decisions above: signing and hard-isolation are no longer
deferred — with every other layer of defense removed, the whole bet rests on
the sandbox + signature-informed consent, so both become prerequisites.

1. ~~**Daemon-level integration test.**~~ *Done* — `cmd/hzd/scripted_integration_test.go`
   drives `auth`/`files`/quota through `NodeResolver` with a tenant-bearing
   `ctx`, the real `scriptedDropDoer`, and a real `FSQuota`, including an
   SSRF-refusal assertion. Locks the `cmd/hzd` wiring against regressions.
2. ~~**`ResolveRoot` drift guard.**~~ *Done* — parity test at
   `integrations/internal/sandbox/jsdrop_parity_test.go` asserts jsdrop's
   `ResolveRoot` stays identical to `sandbox.Resolve` across all path schemes.
3. ~~**Signing + signature-derived badge.**~~ *Done* — `cmd/hz-drops`
   (`keygen` + `sign`) produces detached Ed25519 `<file>.sig`; the daemon verifies
   at install over the exact bytes and derives `official`/`verified`/`community`
   (badge shown in the marketplace UI, surfaced at the consent moment). See
   DEPLOY.md "Marketplace" for the end-to-end publish flow.
4. **Engine hardening — goja → QuickJS-on-wazero** *(partially done).* The
   wall-clock half landed: the executor enforces its own per-run budget
   (`RunInput.MaxRunDuration`, default 60s, `HAZYFLOW_SCRIPTED_DROP_TIMEOUT`),
   bounding runaway loops and idle `await`s *without* trusting the caller to set
   a deadline (Safety posture table; `safety_test.go`). What goja still can't
   give — a hard **memory cap** and per-instruction **CPU fuel** — is the actual
   engine swap: QuickJS compiled to WASM on wazero, pure-Go host, same capability
   injection; the `.d.ts` and published drops don't change. Load-bearing for
   untrusted/community drops.
5. ~~**Dogfood: reimplement a real connector as a scripted drop.**~~ *Done* —
   `sdk/examples/gmail_send_email.ts` is a behaviour-for-behaviour port of the
   native Gmail connector (RFC822 build, RFC 2047 subject, multipart attachments,
   base64url raw), proving "official == scripted" against a production connector.
   `gmail_send_test.go` runs the shipped `.ts` and asserts the wire output — and
   did catch a MIME-encoding bug (lowercase QP hex) as an ordinary drop test, as
   predicted. The port drove the capability additions noted above (`inputs`,
   `env`, crypto/fetch options, `DropError`, Ref outputs). **Slack
   (`sdk/examples/slack_send_message.ts`, `slack_send_test.go`) followed and
   added nothing** — it ported entirely on the existing surface despite a
   different failure model (HTTP 200 + `{ok:false}` envelope) and a structured
   `blocks` input (array, not text), confirming the capabilities aren't
   Gmail-specific. The substrate is steady; further ports are drop authoring,
   not engine work.

   **The HTTP-connector cutover is done** (a clean change, not a back-compat
   migration). All 16 official connectors live in the top-level `officialdrops/`
   package, embedded in the binary and registered into the scripted catalog at
   boot (`officialdrops.Register`); their native Go implementations are deleted:
   **Gmail** ×3, **Slack** send + list, **GitHub** add-comment/create-issue/
   list-issues, **Notion** ×2, **Sheets** read/append/export-pdf, **ai/claude**,
   **notify** ntfy + webhook_send. Packages fully removed: `gmail`, `notion`,
   `sheets`, `ai`. The port drove two more capabilities: `crypto.base64Decode`/
   `utf8Decode` (Gmail bodies) and `res.bytes()` (Sheets PDF export).

   **Staying native** (need capabilities the sandbox doesn't grant, or are engine
   primitives): DB (postgres/mysql/sqlite), excel, git, shell, http_request,
   SMTP `email_send`, and all webhook *triggers* (slack on_mention, github
   on_push/on_new_pr). Deliberate feature drops under the no-back-compat rule:
   claude's `--claude-cli` mode (no shell in the sandbox) and webhook_send's
   `allow_private_networks` (scripted fetch is always SSRF-guarded).

   Resolved: every official drop now exposes a `base_url` param, so the two
   connected-execution journey tests (gmail/slack) and claude's e2e all point
   their scripted HTTP at a mock and are un-skipped.
6. **Per-tenant catalogs + version pinning.** ✅ Done. The `Catalog` is a
   shared base + per-tenant overlay (`installed[tenant] ∪ installed[""]`), and
   `NodeResolver.Resolve(ctx, moduleID)` reads the tenant off the context and
   parses `id@version`, so a tenant's install is isolated and a graph that pins
   `drop@version` keeps resolving that exact version across an install-update.
   See the [appendix](#appendix-per-tenant-catalog--version-pinning-sketch).
7. **Registry / sources + install flow.** Where drops come from (central Hazy
   registry, addable repos, or git-as-source — undecided; see git-as-source as a
   zero-infra v1 to validate install→version→resolve). The `Catalog` is the seam.
8. **Capability growth as needed.** `crypto.awsSigV4`, richer `files`
   (list/delete), a `poll`/`sleep` helper on `ctx`.

## Non-goals (v1 scope boundaries)

Distinct from [Next steps](#next-steps) — these are intentional limits of the
design, not a backlog.

- **Triggers/webhooks** (`executionModel: "trigger"`) — different lifecycle
  (inbound HTTP, signature verification); v1 is batch drops only.
- **Streaming** outputs.
- **npm / arbitrary module imports** — never inside the sandbox; only the
  curated contract. (Multi-file drops could bundle via esbuild at publish
  time, but the runtime never resolves third-party modules.)

## Dependencies this would add

- `github.com/dop251/goja` + `github.com/dop251/goja_nodejs` (eventloop) — both pure Go.
- `github.com/evanw/esbuild/pkg/api` — pure Go.

No cgo; cross-compiles cleanly for self-hosted binaries.

## Appendix: per-tenant catalog & version pinning (implemented)

> Status: implemented. This appendix is the original design sketch; the
> shipped shape matches it — `Catalog` is a shared base + per-tenant overlay,
> `Resolve(ctx, moduleID)` reads the tenant off the context and parses
> `id@version`, and validation runs against `ManifestsForTenant`. The two
> problems below are what it solves.

Two coupled problems the marketplace forces:

- **Per-tenant visibility.** Tenant A installs a drop; tenant B must not see or
  run it. Solved by the shared-base + per-tenant-overlay `Catalog`
  (`installed[tenant] ∪ installed[""]`), driven off the tenant on the context.
- **Version pinning.** A graph pins `drop@version` and the resolver returns that
  exact version (lockfile semantics), so an install-update can't silently change
  a running flow; a bare id tracks the latest installed version.

### Versioning & graph encoding

A scripted drop is identified by `(id, version)`. The version rides in the
module reference as `"id@version"` (e.g. `stripe_list_charges@1.2.0`) — the
docker-tag / Go-module mental model, and the least invasive encoding since
`Resolve` already takes a string. **Bare ids (no `@`) stay built-in/native**, so
every existing graph keeps working untouched.

Run-time resolution is **exact-match only** — no range resolution, like a
lockfile. Semver ranges (`^1.2`) are an *authoring-time* convenience that
resolves to a concrete pin the moment a drop is dropped on the canvas. Updating
a node to a newer version is an **explicit edit** (the UI surfaces "1.3.0
available"); a pinned graph never changes under a running flow.

### Catalog shape

```go
// dropKey identifies an exact drop version. Built-ins use a bare id
// (Version ""); scripted drops are always pinned.
type dropKey struct{ ID, Version string }

type Catalog struct {
    HTTP   HTTPDoer
    Tokens TokenResolver
    Quota  QuotaReserve

    mu sync.RWMutex
    // base: every compiled drop version, shared across tenants. A version is
    // compiled once on Add and reused by everyone who installs it.
    base map[dropKey]*Transport
    // installed: which versions each tenant has. The "" tenant is the global
    // default set (official drops everyone sees unless they remove them), so a
    // tenant's effective set is installed[tenant] ∪ installed[""].
    installed map[string]map[dropKey]struct{}
}

// Add compiles a source into base, keyed by its (id, version). global=true also
// puts it in the default set every tenant sees (how official drops ship).
func (c *Catalog) Add(name, source string, global bool) (id, version string, err error)

// Install / Uninstall record a tenant's choice for an already-Added version.
func (c *Catalog) Install(tenant, id, version string) error
func (c *Catalog) Uninstall(tenant, id, version string) error

// GetForTenant resolves an exact pinned version visible to the tenant. An empty
// version means "latest installed for this tenant" (authoring only — graphs
// always carry an explicit version for run-time determinism).
func (c *Catalog) GetForTenant(tenant, id, version string) (core.Transport, bool) {
    c.mu.RLock(); defer c.mu.RUnlock()
    if version == "" {
        if version = c.latestInstalledLocked(tenant, id); version == "" {
            return nil, false
        }
    }
    key := dropKey{id, version}
    t := c.base[key]
    if t == nil {
        return nil, false // version was never Added
    }
    if _, ok := c.installed[tenant][key]; ok {
        return t, true
    }
    if _, ok := c.installed[""][key]; ok { // global default (official)
        return t, true
    }
    return nil, false // exists, but not installed for this tenant
}

// ManifestsForTenant returns one entry per installed id (latest version) for
// the tenant's palette / ListDrops.
func (c *Catalog) ManifestsForTenant(tenant string) map[string]core.Manifest
```

### The one cross-cutting change: tenant-aware resolution

`Resolve` learns the tenant and splits the version. The tenant rides in `ctx`
(the engine sets `core.WithTenant(ctx, graph.Tenant)` before resolving):

```go
type Resolver interface {
    Resolve(ctx context.Context, moduleID string) (core.Transport, error) // +ctx
}

func (r *NodeResolver) Resolve(ctx context.Context, moduleID string) (core.Transport, error) {
    id, version := splitModuleVersion(moduleID) // "stripe@1.2.0"→("stripe","1.2.0"); "branch"→("branch","")
    tenant, _ := core.TenantFromContext(ctx)
    // Native/Local/Remote/MCP are version- and tenant-blind: resolve by bare
    // id and ignore any pin (a built-in's behavior doesn't fork per version).
    if t, ok := r.Native.Get(id); ok { return t, nil }
    // …Local, Remote, MCP by bare id…
    if r.Script != nil {
        if t, ok := r.Script.GetForTenant(tenant, id, version); ok { return t, nil }
    }
    return nil, fmt.Errorf("no transport registered for module %q", moduleID)
}
```

The blast radius landed as: the `Resolver` interface signature + its callers
(engine, worker, for_each pass the tenant via ctx) + tenant-aware graph
**validation** (`e.validate` runs `ValidateWithManifests` against
`ManifestsForTenant(graph.Tenant)`, which folds in an `id@version` entry per
installed version so a pinned node validates). The four other catalogs keep
their bare `Get(id)` — only `NodeResolver.Resolve` does the split and tenant
routing.

### Back-compat / migration

- Bare `moduleID` → built-in path, unchanged; **all existing graphs keep working**.
- Today's `LoadDir` becomes `Add(name, src, global=true)` per file → base +
  global-default, so boot-loaded drops stay visible to everyone exactly as now,
  until explicit per-tenant install is layered on.
- Keep thin `Get(id)` / `Manifests()` wrappers (delegating to the `""` tenant /
  latest) so current tests and callers survive the transition.

### Deferred to the registry/store layer (#7)

- **Persistence** of install state — a `(tenant, id, version)` table. The sketch
  above is in-memory; the store is part of the registry work.
- **GC** of `base` versions no tenant references.

### Open decisions

- **Official drops = global default (auto-visible), or explicit install?** Lean:
  global default, so "everyone has Gmail" needs no install step while community
  drops are explicit per-tenant — keeps the uniform model without a setup wall.
- **Range→pin authoring UX** — does the editor offer "latest" / "^1.2", always
  resolving to a concrete pin on drop?
- **Per-workspace vs per-tenant** install granularity (the sketch keys on tenant;
  workspace is a finer grain if needed).
