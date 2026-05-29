# Migration: goja → one runtime (Node in gVisor)

> **DONE (Phases A–E + option 2 — goja fully removed).** Node is the sole
> runtime for every drop (`configureScriptedRuntime` wires the catalog's `Run`
> hook to the Node host — `process` or `gvisor` tier). The dedicated goja host
> binary (`cmd/hz-drophost`) and the goja container-execution tests are deleted;
> the Dockerfile is `node:22-alpine` + `drophost.mjs`. **`github.com/dop251/goja`
> is gone from `go.mod`.** The in-process executor is deleted: the catalog stores
> only each drop's manifest + ESM transpile and resolves through the `Run` hook
> (nil `Run` → a drop can be cataloged/inspected but not executed). Manifest
> reading moved off goja onto the Node `Extract` hook (`drophost.mjs
> --emit-manifest`); official drops embed their manifests at generate time
> (`officialdrops/manifests.json`) so boot needs no Node spawn. The jsdrop,
> scenario/journey, and e2e test harnesses now wire the Node runtime (skipping
> when `node` is absent). The prose below is the original plan, kept for history;
> **`DESIGN.md` reflects the shipped Node runtime.**


**Decision.** Collapse the three execution tiers (in-process goja / process goja /
gVisor goja) into **one model**: every scripted drop is a bundled JS file run by
**Node inside a gVisor container**, reaching capabilities only through the
broker. goja is removed.

This is a runtime change, not a model change: **authoring, the Ed25519
signature, git distribution, install, trust tiers, per-drop egress, and consent
are all unchanged.** A drop is still a signed source artifact; it just runs in
Node, not goja, and always in the sandbox.

## Why

- **Consistency.** One runtime, one capability path (the broker), one isolation
  boundary. Official and installed drops differ only by *trust tier*, not by how
  they execute. Today there are three execution paths + two capability-injection
  mechanisms (in-process Go funcs vs broker).
- **No goja limitations.** Real JS semantics — no ES-compat gaps, no per-library
  workarounds (the SheetJS `getYear`/`ArrayBuffer` hacks disappear; Node runs
  SheetJS natively). The full pure-JS npm ecosystem is available via
  authoring-time bundling.

## Target architecture

```
hzd daemon
  └─ NodeResolver
       ├── Native (Registry)         — engine primitives, transforms, triggers, DB, file/http  (Go, unchanged)
       └── Script  (one catalog)     — every scripted drop → containerdrop.Transport
                                          │
              docker run --runtime=runsc  ▼
              ┌──────────── gVisor sandbox (shared Node base image) ───────────┐
              │  node /drophost.js  (baked into the image)                     │
              │    └─ loads the mounted, bundled drop JS  →  run(ctx)          │
              │         ctx.* capabilities ──► broker unix socket (the only    │
              │                                 path out; --network=none)      │
              └────────────────────────────────────────────────────────────────┘
                         ▲ broker mediates fetch/secret/token/files/log/result
                         │ (host-side, guarded — unchanged)
```

**Packaging:** a drop is bundled (esbuild, at **authoring** time) into one JS
file — pure-JS npm libs inlined there. The daemon never runs `npm install` or a
build; it verifies the signature over the bundled JS and mounts it into the
shared Node image. So: **no registry, no per-drop image build, no supply-chain
code execution on the daemon.** The signature attests to exactly the bundle that
runs.

## What's removed / kept / added

**Removed**
- `engine/jsdrop`'s goja executor (`Compile`/`Run`/the `ctx.*` Go-function
  injection) and the in-process `jsdrop.Transport`.
- The Go `hz-drophost` (goja drop host) and the in-process + dual-capability
  paths. The three-tier split collapses to one.
- The `goja` dependency. (`esbuild` moves to the authoring/`hz-drops` tool; the
  daemon no longer transpiles.)
- The SheetJS goja shims (date polyfill, ArrayBuffer→number[] dance) — Node
  handles these natively.

**Kept (already built, reused — ~80% of the substrate)**
- The **broker** (`broker.go`) — now the *only* capability path, for everyone.
- The **`Runner`** seam + gVisor `DockerRunner`; `--network=none`, `--read-only`,
  cgroup limits, the `dev.gvisor.flag.host-uds=open` annotation.
- Trust tiers + signature verify + boot re-verify + revoke, per-drop **egress
  allowlist**, install **consent**, the per-tenant + version-pinned catalog
  logic, the capability **Host** (guarded HTTP / OAuth / sandboxed FileStore).
- The native tier (engine primitives, transforms, triggers, DB, file/http) —
  **unchanged**; only the scripted-drop runtime changes.

**Added**
- A **Node drop-host** (`drophost.js`) — the in-container runtime: a JS port of
  `containerdrop/client.go` (the broker client) + the `ctx.*` capability shim,
  matching `engine/jsdrop/executor.go` semantics exactly.
- A **shared Node base image** (one Dockerfile: Node-slim + `drophost.js`), built
  once, not per drop.
- An **authoring-time bundle step** in `hz-drops` (esbuild Build → one JS → sign).

## Phases (each lands behind a test gate)

> **Progress:** Phases A ✅, B ✅, C ✅, D ✅ — see notes inline. goja + Node
> coexist; reversible until E. **Phase D:** the catalog's `Sandbox` hook became
> `Run`, stores the ESM transpile, and routes EVERY drop (official + installed)
> through it to the Node host when the daemon wires it (`configureScriptedRuntime`,
> process/gvisor); a drop's egress is restricted only when it declares `egress`,
> else the global SSRF guard applies (so official drops keep working). Verified:
> official drops incl. excel/SheetJS run via Node end-to-end; full suite green.
> goja remains only as the in-process fallback + the manifest reader — Phase E
> removes it. Next: **Phase E**.

### Phase A — the Node drop-host ✅ (done)
**Shipped:** `engine/containerdrop/nodehost/drophost.mjs` (broker client + full
`ctx.*` shim) and `nodehost_parity_test.go` — every capability run through both
goja and Node over an identical broker/host/job, asserting equal `core.Result`
(9/9 pass: params, secrets, env, crypto, fetch, files, auth, inputs, DropError).
One broker tweak: `JobContext` now carries the granted `Secrets` so `ctx.secrets.get`
stays **synchronous** in Node (reads a prefetched map). Finding: goja drops a
*synchronous* throw in a non-async `run` to `script_error` (its promise shim
evaluates `run(ctx)` before wrapping); the Node host catches both sync and async
throws — a strict improvement.

(original spec ↓)
Port `hz-drophost` from Go/goja to a Node program:
- JS broker client over `HZ_BROKER_SOCKET` (Job/Fetch/Secret/Token/ReadFile/
  WriteFile/Exists/Log/Result/Fail — same wire protocol the Go `Client` speaks).
- The `ctx` shim reproducing the executor's surface **exactly**: `params`,
  `inputs` (get/ref/all/has), `secrets` (get/has), `auth.token`, `env`, `fetch`
  (method/headers/query/body/timeoutMs/expectStatus; response `.ok/.status/.text()/
  .json()/.bytes()`), `crypto` (hmac/hash/base64/randomBytes/utf8…), `files`
  (read/readText/write/exists), `log.*`, `progress`, and the `DropError` class.
- **Gate (parity):** run a set of existing `.ts` drops through *both* the goja
  executor and the Node drop-host against the same mocks; assert identical
  `core.Result`. This is the contract that lets goja retire safely.

### Phase B — Node base image + runner wiring ✅ (done)
**Shipped:** `DockerRunner` generalized to a `Command` + `Mounts` (Node mode)
alongside the legacy goja bind-mount path. `runner_node_gvisor_test.go` runs a
real drop under `runsc` via the Node host and the broker (secret/fetch/files
round-trip), and `TestDockerRunner_GVisor` (goja) still passes.

**Simplification found:** no custom base image is needed. We use a **stock
official node image** (`node:22-alpine`) and **bind-mount `drophost.mjs`**
read-only (it's our trusted ~few-KB file, like the drop bundle). This removes
the "build + maintain a base image" task entirely — you pin an upstream node tag
and track its CVEs instead of curating an image. No Dockerfile.

### Phase C — authoring-time bundling + manifest inspection ✅ (done)
**Shipped:** `hz-drops bundle [-o out] <entry.ts>` (esbuild Build, Bundle+ESM+
Platform=node → one self-contained file that inlines every npm + relative
import; the runtime never resolves modules). `drophost.mjs --emit-manifest`
reads a bundle's manifest with no broker — the install-time inspection that
replaces goja's in-process `Inspect`. E2e (`bundle_run_test.go`): a two-file
drop is bundled, the relative import is gone (inlined), the manifest reads back,
and it runs through the Node host with the imported code executing. The npm
case is the same esbuild resolution, so this stands in for "use any pure-JS npm
package."

(original spec ↓)
- `hz-drops` grows `bundle` (esbuild Build, resolving the author's node_modules)
  → one JS → then `sign`. Single-file drops bundle trivially; npm-using drops
  inline their pure-JS deps.
- **Manifest inspection without goja:** install/validate must read a drop's
  manifest without running it. Add a `--emit-manifest` mode to `drophost.js`
  (load the bundle, print `manifest` JSON, exit) invoked once at install. This is
  heavier than goja's in-process `Inspect` (a Node spawn) — acceptable at install
  time; noted as a cost.
- **Gate:** install a bundled drop end-to-end — manifest populated, consent
  summary + validation correct.

### Phase D — uniform catalog + resolver
- The scripted catalog emits a `containerdrop.Transport` for **every** drop
  (official + installed), preserving the existing per-tenant + version-pinned
  resolution. Remove the in-process transport and the `Sandbox`-hook special case
  (everything is sandboxed now).
- `cmd/hzd`: build one container `Host` + one `Runner`; register `officialdrops`
  as bundled JS; point `NodeResolver.Script` at the uniform catalog.
- **Gate:** journey + scenario + e2e suites pass with all scripted drops running
  as Node containers.

### Phase E — migrate the 18 official drops + retire goja
- Bundle the 18 `officialdrops` (esbuild). The excel drops **simplify** — drop
  the goja polyfill/byte hacks; use Node `Buffer`. Verify each runs under Node
  (the parity gate from Phase A covers the contract).
- Delete `engine/jsdrop`'s goja executor + in-process transport + Go
  `hz-drophost`; drop `goja` from `go.mod`. Keep the manifest types, the catalog
  tenant/version logic, and the `HTTPDoer`/`FileStore`/`TokenResolver` host
  interfaces (the broker uses them).
- **Gate:** full `go test ./...` green; no `goja` import remains.

## What stays native (boundary unchanged)

Engine primitives + flow-control (`branch`, `merge`, `for_each`, `sleep`,
`await_approval`, `subgraph`), hot-path transforms (`map_rows`, `group_aggregate`,
…), triggers (`webhook_input`, cron, `*_on_*`), DB (`sqlite_*`/`postgres_*`/
`builtin_store_*`), and `file_*`/`http_*` stdlib stay native Go. Only the
**connector/scripted-drop tier** changes runtime.

## Hard parts & open decisions

1. **Manifest inspection cost.** Reading a manifest now spawns Node (Phase C).
   Mitigate by caching the manifest in the install record (already persisted) so
   it's read once at install, not per resolve.
2. **Cold start.** Every drop = container + Node start (no in-process fast path).
   Acceptable as the *consistent* cost; mitigate later with **warm pools** per
   popular drop. Measure before optimizing. Engine primitives stay native, so the
   hot graph machinery is unaffected.
3. **One runner in prod, dev escape hatch.** Production = gVisor for all drops
   (consistency). Keep `ProcessRunner` (Node, no gVisor) behind
   `HAZYFLOW_SANDBOX_RUNTIME=process` for local dev where runsc isn't installed.
4. **Native-addon npm packages** (`.node` binaries — `sharp`, native PDF) still
   can't bundle; they'd need the base image to carry them. Pure-JS npm (the vast
   majority) bundles fine. This is the one residual library limit.
5. **Dev loop.** `HAZYFLOW_SCRIPTED_DROPS_DIR` drops now need bundling; the dev
   flow gains a bundle step (or the daemon keeps esbuild only for the dev dir).
6. **Base-image lifecycle.** Resolved in Phase B: no *custom* image — a stock
   official `node:NN` image is used with `drophost.mjs` bind-mounted. Ops surface
   = pin an upstream node tag + track its CVEs (no image to build/curate).

## Sequencing & risk

Phases A→B→C→D→E in order; A is the linchpin (the parity gate de-risks
everything downstream). goja and Node coexist through D, so the cutover is
reversible until E. Biggest risk is **ctx-shim fidelity** — the parity test
suite is the mitigation and must cover every capability + error path before E.
