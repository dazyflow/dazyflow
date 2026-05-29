# Scripted drops — design + status

**Drops are authored and distributed as source (`.ts`), and executed by Node in
a sandbox.** A drop is bundled (esbuild, at authoring time — `hz-drops bundle`)
into one ESM file, signed with an Ed25519 `.sig`, and distributed via git. At
run time **every** scripted drop — official and runtime-installed alike —
executes in the **Node drop host** (`engine/containerdrop/nodehost/drophost.mjs`),
out-of-process, reaching the daemon only through the capability **broker**.

There is **one runtime** (Node), **one capability path** (the broker), and one
isolation boundary. "containerdrop" is the substrate that hosts Node + mediates
its capabilities; it is **not** about shipping drops as OCI images (that was
considered and rejected — see [No images](#no-images-rejected)).

goja (the former in-process JS engine) is retained **only** as a dev/test
fallback and as the registration-time **manifest reader** — it no longer runs
drop logic in production. Fully removing it from `go.mod` is a deferred
follow-up.

**Status:** the migration to the Node runtime is complete and tested. The full
untrusted-drop safety bar (T1a–T5) plus the gVisor kernel boundary (T1b) and
read-only rootfs (T7) are implemented — see the
[hardening checklist](#untrusted-drop-safety-hardening-checklist). The only
escalation left is an optional microVM tier (another `Runner`).

## What's built (`engine/containerdrop`)

- **`Broker`** — the unix-socket capability server (`GET /job`; `POST /fetch`,
  `/secret`, `/token`, `/files/{read,write,exists}`, `/log`, `/result`),
  mediating the host implementations (`BrokerDeps` reuses
  `jsdrop.HTTPDoer`/`FileStore`/`TokenResolver`). `/job` also carries the granted
  secrets so the Node host can expose a synchronous `ctx.secrets.get`.
- **`nodehost/drophost.mjs`** — the Node drop host: reads `HZ_BROKER_SOCKET`,
  imports the bundled drop (ESM), and runs it with the full `ctx.*` capability
  surface (`params/inputs/secrets/auth/env/fetch/crypto/files/log/progress`,
  `DropError`) backed by the broker. Also a `--emit-manifest` mode for
  install-time manifest inspection without running the drop.
- **`Transport`** (`core.Transport`) — stands up a per-execution broker on a
  private socket, launches the drop via a pluggable **`Runner`**, maps the drop's
  reported `/result` (or typed error / no-result / runner-error) to a
  `core.Result`. Same Manifest/Execute contract as native drops.
- **`Runner`** seam with two implementations: **`ProcessRunner`** (launches
  `node drophost.mjs` as a child — address-space isolation) and **`DockerRunner`**
  (launches it under gVisor/`runsc` — the kernel boundary). A microVM runner
  would be a third, same contract.

## What runs in Node vs. native

Not everything is a drop; the engine's own machinery stays native Go:

| Category | Examples | Runtime | Why |
|---|---|---|---|
| **Connectors / integrations / third-party** | gmail, slack, sheets, github, notion, AI, excel, custom drops | **Node, sandboxed** | The marketplace surface. Every scripted drop (official + installed) runs in the Node host via the broker. |
| **Engine primitives / flow-control** | branch, merge, route_rows, sleep, await_approval, subgraph | **native Go** | These *are* the engine — they steer scheduling, pause-for-resume, submit child graphs. A drop that just "returns outputs" can't express `Status:"awaiting"` or `SubmitsChildGraph`. |
| **Hot-path transforms** | map_rows, filter, compute_rows, group_aggregate, join_rows, sort_rows | **native Go** | Marshaling every row across the broker for a `filter` is prohibitive. Hot-path data work belongs in-process. |
| **Triggers** | cron, webhook | **native Go** | Different lifecycle — they *start* graphs (inbound HTTP the daemon hosts), not per-execution sandboxes. |

The extensible/marketplace surface is the connector tier (Node); everything else
is the native engine core, unchanged.

## Routing: the catalog `Run` hook

`jsdrop.Catalog` registers drops (per-tenant + version-pinned), stores each one's
ESM transpile, and resolves execution through an injected **`Run`** hook:

- The daemon (`cmd/hzd/configureScriptedRuntime`) sets `Run` to build a
  `containerdrop.Transport` (Node host + the chosen `Runner`) for the drop's ESM.
  So `GetForTenant` returns a Node-backed transport for **every** drop.
- If `Run` is nil (a bare catalog with no runtime wired), a drop can be cataloged
  and inspected but not executed — `GetForTenant` returns `false`. There is no
  in-process JS engine; Node is the sole runtime.

Manifests are read by the **same Node runtime** via the catalog `Extract` hook
(`drophost.mjs --emit-manifest`), so what the daemon gates on at install is
exactly what will run. Official drops embed their manifests at generate time
(`officialdrops/manifests.json`, via `go generate`), so boot registers them with
`AddPrebuilt` and spawns no Node process per drop.

## The capability broker

A sandboxed drop can't have `ctx.fetch` injected in-process, so the daemon
exposes the **same capabilities** over a per-execution unix socket bind-mounted
into the sandbox. The sandbox network is **default-deny except that socket**, so
the broker is the only path out — preserving every guard already built:

```
POST /fetch          → daemon performs the guarded request (SSRF guard + per-drop
                       egress allowlist + quota); the ONLY network path the drop has
POST /secret         → a granted secret's value; scoped to this node, never on disk
POST /token          → current OAuth token (host-side refresh)
POST /files/{...}    → confined to the job's sandbox roots
POST /log, /result   → telemetry + the drop's output
```

Identity is **possession of the socket**: each execution gets its own broker
endpoint bound to that job + tenant, so the drop needs no token and can't reach
another job's capabilities (the cloud-metadata + egress-proxy pattern).

## Isolation tiers

Selected by `HAZYFLOW_SANDBOX_RUNTIME`; same broker contract, swap the `Runner`:

- **`process`** (`ProcessRunner`) — `node drophost.mjs` as a child process:
  address-space isolation (a crash/OOM kills the child, not the daemon), node
  heap bounded by `--max-old-space-size`. Shares the host kernel — **not**
  sufficient against a kernel exploit; fine against bugs/DoS. The default; needs
  `node` on PATH.
- **`gvisor`** (`DockerRunner` → `runsc`) — the kernel boundary for untrusted
  code: `node drophost.mjs` under runsc with `--network=none`, `--read-only`
  rootfs, cgroup-hard `--memory`/`--pids-limit`, in a stock node image with
  `drophost.mjs` bind-mounted. The tier to enable for community drops.
- **microVM (Kata/Firecracker)** — *future*, optional higher-assurance tier: a
  new `Runner` + swap the broker's unix socket for **vsock** (the HTTP capability
  API is identical). Not built.

## Trust & distribution (source)

- **Authoring:** `.ts` source, bundled to one ESM by `hz-drops bundle` (esbuild),
  which inlines any pure-JS npm + multi-file imports. The runtime never resolves
  modules; the signature attests to the exact bundle that runs.
- **Distribution:** git-as-source — `repo@ref:path` fetched and pinned to the
  resolved commit (the immutable provenance), SSRF-guarded transport.
- **Trust tier:** signature-derived from a detached Ed25519 `.sig` over the exact
  bundle bytes, verified against `HAZYFLOW_TRUSTED_KEYS` — Hazy's key →
  `official`, a reviewed publisher → `verified`, unsigned/unknown → `community`.
  Re-verified on every boot (a removed key downgrades; see T5).
- **Manifest:** the `.ts` default-export `{ manifest, run }`; the manifest gains
  an optional `egress` allowlist (T3).

No registry, no image digests, no cosign — the git commit + Ed25519 `.sig` are
the immutable pin + signature.

## Libraries (npm) and what's excluded

Node runs the drop, so a **pure-JS** library bundled at authoring time runs
natively — no goja compatibility gaps, no per-library shims:

- **Excel — `excel_read`/`excel_write`** bundle the pure-JS SheetJS (`xlsx`)
  build (generated under `officialdrops/excelsrc/`) and replaced the native
  excelize drops (same ids/ports). They run in Node; the goja-era date polyfill
  is now unnecessary (harmless leftover). Slower than native and no cell styling,
  but real xlsx read/write. The template for any pure-JS library drop.
- **Genuinely excluded:** native-addon npm packages (`.node` binaries — `sharp`/
  libvips, native PDF/canvas) can't be bundled; they'd need the base image to
  carry them. Pure-JS npm (the vast majority) is fine.
- **DB / SMTP** would be a **broker egress capability** (a `/sql` or `/tcp`
  endpoint the broker mediates, like `/fetch` for HTTP), not a library. *Not
  built; noted as the path if wanted.*

Note: it's real Node, so a bundled dependency *could* `require('fs')`/`net`/etc.
— but the sandbox contains it: `--network=none` means the only egress is the
broker `fetch` (SSRF-guarded), and the filesystem is read-only except the
broker-mediated workspace. Ambient I/O dead-ends; only the broker reaches real
resources.

## No images (rejected)

Shipping drops as **OCI images** (registry-hosted `image@sha256:…`, cosign-
signed) was considered and rejected: it adds a registry, a cosign keypair +
verification, and an image build/publish pipeline per drop — for no gain over
*source + authoring-time bundle*, which already attests the exact runnable bytes
via the Ed25519 signature and reaches the full pure-JS npm ecosystem. (Building
images **on the daemon** at install was also rejected — it's arbitrary build-time
code execution + supply-chain exposure on our infra.)

## Untrusted-drop safety: hardening checklist

> **Status: complete (process + gVisor tiers), Node runtime.** Every scripted
> drop runs out-of-process in the Node host, reaches the daemon only through the
> broker, is confined to a per-drop egress allowlist (when it declares one),
> requires install-time consent, and is re-verified against the current keyring
> on boot (with a revoke kill switch). Two runners via `HAZYFLOW_SANDBOX_RUNTIME`:
> `process` (default) and `gvisor`.
>
> **gVisor verified (`TestNodeHost_GVisor`):** a real drop runs the Node host
> under `runsc` and completes the broker capability round-trip (secret/fetch/
> files). One finding baked into `DockerRunner`: connecting to the host broker
> socket needs gVisor's `--host-uds=open`, set **per-container** via the
> `dev.gvisor.flag.host-uds=open` OCI annotation — so no daemon-wide `runtimeArgs`
> change (and no root) is required. Capabilities are regression-tested in
> `TestNodeHost_Capabilities`; authoring bundle → Node run in
> `TestAuthoringBundle_RunsInNode`; official drops (incl. excel) via Node in
> `TestOfficialDrops_RunViaNode`.

"Safe to install community drops" is its own bar, and not all of it is
isolation.

### The three threats (they need *different* fixes)

1. **Host / cross-tenant compromise** — escape the runtime, reach the daemon's
   memory or another tenant's data. → solved by **isolation** (T1).
2. **Resource exhaustion (DoS)** — CPU spin / memory balloon takes the daemon
   down. → solved by **hard limits** (T2), independent of escape.
3. **Data exfiltration** — leak the data/secrets the drop was *legitimately*
   handed. → **isolation does nothing here**; needs the **capability model**
   (T3/T4). This is the one teams forget.

### Minimum bar — required before community installs can be enabled at all

| # | Control | Status | What shipped |
|---|---|---|---|
| T1a | **Out-of-process execution** | ✅ | Every drop resolves to `containerdrop.Transport` + a `Runner` launching the Node host (`drophost.mjs`) with broker-backed capabilities. A crash/OOM/hang kills the child, not `hzd` (a hard run budget — `HAZYFLOW_SCRIPTED_DROP_TIMEOUT`, default 5m — reaps a runaway). Node is the sole runtime; routed by `jsdrop.Catalog.Run`. |
| T2 | **Resource limits** | ✅ (gVisor hard) | gVisor tier: cgroup-hard `--memory` + `--pids-limit`. Process tier: node `--max-old-space-size` (soft heap cap) — hard caps are the gVisor tier's job. |
| T3 | **Network default-deny + per-drop egress** | ✅ | The drop's only network path is the broker `fetch`; when a drop declares `egress`, it's enforced against that allowlist (`*.host` wildcards; empty ⇒ deny all). Else the daemon-global SSRF guard applies. gVisor adds `--network=none`. `core.Manifest.Egress` + broker `RestrictEgress`. |
| T4 | **Least-privilege + install consent** | ✅ | Broker scopes secrets/tokens/files by socket possession; install requires `acknowledged` after a `/drops[/from-git]/preview` returns the declared OAuth/secrets/egress. Admin consent UI in `AdminMarketplace.tsx`. |
| T5 | **Provenance + revoke + audit** | ✅ | Signed provenance + trust tiers + audit, plus signatures persisted and **re-verified on boot** (`Restore` re-runs `keyring.Verify`; a removed key downgrades / refuses a reserved id; forged sig skipped), and a boot-config **revoke/kill switch** (`HAZYFLOW_REVOKED_INSTALLS`). |

### Full bar — required to open the marketplace to untrusted authors

| # | Control | Status | What shipped |
|---|---|---|---|
| T1b | **Kernel boundary** | ✅ gVisor | `DockerRunner` runs the Node host under `runsc` (`HAZYFLOW_SANDBOX_RUNTIME=gvisor`); verified end-to-end. Kata/Firecracker microVM is an optional further `Runner`, broker contract unchanged. |
| T7 | **Read-only rootfs + minimal mounts** | ✅ | `DockerRunner` runs `--read-only` with only the per-execution dir (broker socket + drop bundle) and `drophost.mjs` bind-mounted. |

Per-drop egress for *untrusted* drops is currently opt-in by declaration (a drop
that declares `egress` is locked to it; one that doesn't falls back to the
global SSRF guard, as official drops need). A **tier-aware** policy ("untrusted
must declare or get denied") is a follow-up.

### The residual no sandbox removes

A drop legitimately granted "read this Sheet **and** call this API" can send the
Sheet to that API. Isolation can't stop it — only **least-privilege (T4) +
per-drop egress allowlist (T3) + a human reviewing the declared capabilities**
bounds it. So the trust tier and the egress declaration are not decoration; for
the legitimate-capability leak they *are* the control.

## Still open

- **Cold start.** A Node-host spawn (especially under gVisor) has latency. For
  high-volume drops, a warm-pool / reuse strategy may be worth it; measure first.
- **Process-tier hard limits.** The process tier only soft-caps memory
  (`--max-old-space-size`); untrusted workloads should use the gVisor tier.
- **microVM tier** — vsock-backed broker + a Kata/Firecracker `Runner`.
- **DB / SMTP egress capability** — a `/sql` or `/tcp` broker endpoint.
