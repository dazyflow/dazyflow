# Steps for your own service — design note

Status: **ALL THREE PHASES BUILT** — a tenant can describe their own service in
Admin → Web APIs (by hand or by importing an OpenAPI spec) and get one step per
operation, in the palette and in the flow generator, and can point that catalog
at a runner to reach a service with no public address. The four "as built"
sections at the end record what the builds changed about this note.
Written 2026-08-27, the same day as
[docs/mcp-on-runners-design.md](./mcp-on-runners-design.md) and for the same
reason: per-org HTTP MCP servers had just shipped, the shape of a *tenant-owned
catalog* was fresh, and this is the other thing that shape is good for. Not
scheduled. Build it when a customer says "we have our own service and
`http_request` is wearing us out."

Where the code would live: a new `engine/webapi/` (catalog + manifest synthesis
+ HTTP executor, sibling of `engine/mcp/`), `engine/resolver.go` (a fourth
catalog in `lookup` and `ManifestsForTenant`), `daemon/webapis.go` +
`daemon/webapis_pg.go` (store, sealed credential, reconcile — modelled directly
on `daemon/mcpservers.go`), and `web/src/pages/admin/AdminWebAPIs.tsx` (a near
sibling of `AdminMCPServers.tsx`).

## Contents

- [The gap this closes](#the-gap-this-closes)
- [The core: one descriptor, two front ends](#the-core-one-descriptor-two-front-ends)
- [What OpenAPI gives that MCP cannot](#what-openapi-gives-that-mcp-cannot)
- [Connections: the ergonomic win, nearly free](#connections-the-ergonomic-win-nearly-free)
- [The hard parts](#the-hard-parts)
- [Reaching a service that isn't on the internet](#reaching-a-service-that-isnt-on-the-internet)
- [Phasing](#phasing)
- [Decisions taken (2026-08-27)](#decisions-taken-2026-08-27)
- [Open questions](#open-questions)
- [Deferred (deliberately not designed)](#deferred-deliberately-not-designed)
- [Commit 1 as built (2026-08-27)](#commit-1-as-built-2026-08-27)
- [Commit 2 as built (2026-08-27)](#commit-2-as-built-2026-08-27)
- [Commit 3 as built (2026-08-28)](#commit-3-as-built-2026-08-28)
- [Commit 4 as built (2026-08-28)](#commit-4-as-built-2026-08-28)

## The gap this closes

A tenant with their own service — an internal orders API, a pricing service, the
thing their own team ships — has three routes today, and a fourth that looks
available and isn't.

1. **`http_request` + a secret** (`drops/net/http_request.go`). Works now, zero
   setup, and it is the honest baseline: the URL, method, headers and body typed
   on each step, `${secret.MYSVC_TOKEN}` in the headers map.
2. **Wrap the service in an MCP server** (`daemon/mcpservers.go`,
   [docs/guide/mcp-servers.md](./guide/mcp-servers.md)). Gets everything — typed
   steps, palette presence, curation. Costs a server they write and host, on a
   public HTTPS address, because `hfnet.SSRFDialControl` blocks private ranges by
   design.
3. **Runner + script** ([docs/guide/runners.md](./guide/runners.md)). The only
   route that reaches a service inside their own network. Costs a machine, a
   script per operation, and `run_on_runner`'s bargain: whoever can edit a flow
   can run commands there.
4. **Not a route: tenant-registered gRPC drops.** `RemoteCatalog.Register`
   *requires* a tenant (`engine/remote.go:263`), so the isolation is already
   built — but the only wiring is `DAZYFLOW_REMOTE_MODULES` in
   `cmd/dzd/main.go:108`. Operator env, no admin API. Worth stating because it is
   the route that sounds like the intended answer and a reader will otherwise go
   looking for it.

So the gap is not capability. Route 1 or 3 always works. The gap is:

> **A tenant cannot add a first-class step for their own service without writing
> a server.**

What route 1 structurally cannot give them: a name and description in the
palette, required fields, typed ports, a credential stored once instead of
pasted into forty nodes, and — the one that actually sells the product — a step
the flow generator can find. `daemon/mcpservers_flowgen_test.go` pins exactly
that property for MCP tools, and its comment names the pitch: *bring the
catalogue nobody wrote a connector for, then describe what you want in a
sentence.* A tenant's own service is the most common such catalogue there is,
and it is the one case where we make them write a protocol server to get in.

## The core: one descriptor, two front ends

The decision this document exists to record: **an imported OpenAPI operation and
a hand-built custom step are the same thing internally.** Both are "one HTTP
call, described well enough to synthesize a manifest." Build one descriptor and
one executor; make the spec parser and the admin form two ways of producing the
descriptor.

```go
// Catalog is one tenant-owned collection of operations under a base URL.
type Catalog struct {
    Tenant     string
    Name       string   // steps are api:<name>:<operation>
    BaseURL    string
    Operations []Operation
}

type Operation struct {
    ID          string // unique within the catalog; from operationId or typed
    Method      string // GET | POST | PUT | PATCH | DELETE
    Path        string // "/orders/{id}", joined onto BaseURL
    Summary     string // → Manifest.Subtitle
    Description string // → Manifest.Description
    Args        []Arg
    BodyMode    string // "none" | "json" (Args with In:"body") | "raw"
    Deprecated  bool
}

type Arg struct {
    Name        string
    In          string // "path" | "query" | "header" | "body"
    Type        string // JSON Schema type
    Required    bool
    Label       string
    Description string
    Schema      json.RawMessage // enum/object detail, rendered by the params form
}
```

`Arg.In` is the field that does not exist in MCP and is the whole reason this
needs its own descriptor rather than a reused `mcp.Tool`. MCP hands you one flat
argument object; HTTP splits arguments across path, query, header and body, and
execution has to know which is which to rebuild the request. Everything *else*
is shared.

Building the form-driven front end alone and retrofitting the spec importer
later is the expensive order — the descriptor is the thing that has to be right
first, and a form is the fastest way to get it wrong in a way a spec then can't
express. Design the descriptor against OpenAPI; ship whichever front end first.

### What is reused, not rewritten

Most of this feature already exists in `engine/mcp` and `daemon/mcpservers.go`
and should be lifted, not copied:

- **Port synthesis.** `toolInputPorts` / `maxToolPorts` / `portableArgName` /
  `scalarMIME` (`engine/mcp/transport.go:258`) already encode the rules —
  scalars get pins, required first then alphabetical, twelve maximum,
  deterministic order because ports are addressed by position on the canvas.
  Applies unchanged to `Args`; the `In` field only affects execution.
- **The overlay port.** `toolOverlayPort` ("input") and `buildArguments`'
  three-layer precedence (params → overlay → per-arg port, most specific last)
  is the answer for a nested body object no port can express. Keep the name.
- **`InlineOnly` on every input.** Same reason, restated: the service is on
  another machine and a `Ref` path is on the daemon's disk.
- **Catalog scoping.** `toolKey`-style `(tenant, id)` keying, and `attach`'s
  refusal to let a tenant occupy an instance-wide name. Both arguments carry
  over verbatim — a job reaching a transport carries *resolved secrets*.
- **The store.** `pgMCPServerSchema` (`daemon/mcpservers.go:115`) is the
  template: credential sealed under the tenant DEK with `(domain, name)` as AAD
  so a blob cannot be relocated into another org's row, `enabled` as the
  reversible half of delete, `SetStatus` separate from `Put`, a per-tenant cap
  like `maxMCPServersPerTenant`.
- **The reconcile loop.** `MCPServers.Reconcile` + `StepSourceReconcileInterval`
  (`daemon/mcpservers.go:724`) — `UpdatedAt`-compared, picks up another
  replica's edits, unregisters what the store no longer wants. Identical needs.
- **The executor.** `drops/net.Do` (`drops/net/do.go:33`) is already the guarded
  outbound call: SSRF dial guard, `EgressAllowedFor` per-tenant allowlist,
  `AcquireEgress` rate limiting and 429 cooldown, `ObserveEgressResponse`, body
  cap. An operation's `Execute` should be method + rendered URL + headers + body
  handed to `Do`, and nothing more. Do not write a second HTTP path. **It cannot
  be imported** — `drops/net` imports `engine`, so it arrives through an
  injected hook instead; see "Commit 1 as built".
- **The platform killswitch.** `AllManifests` + `NodeResolver.DropGate`, same
  contract, so a platform admin can switch off a misbehaving org's catalog.

## What OpenAPI gives that MCP cannot

Worth recording, because it argues for the spec importer over the form as the
*primary* front end wherever a spec exists:

- **The spec is already written.** FastAPI, Spring, NestJS, go-swagger and
  friends generate it. For a tenant's own service this is the difference between
  "paste a URL" and "fill in twenty forms."
- **Honest idempotency.** MCP declares none, which is why
  `synthesizeManifest` hardcodes `Idempotent: false`
  (`engine/mcp/transport.go:207`). HTTP method semantics are not a guess: GET,
  HEAD, PUT and DELETE are idempotent, POST and PATCH are not. So retry edges
  validate correctly, and `core.RetryExponentialBackoff` can be attached where
  it is safe — the thing `http_request` already reasons about in its own
  manifest.
- **Real metadata.** `summary`, `description`, `tags`, `deprecated` map onto
  `Subtitle`, `Description`, `Tags` and a deprecation marker. Most MCP servers
  ship a one-line description and nothing else.
- **Declared responses**, so failure mapping can read the API's own error shape
  instead of stringifying whatever arrived.

## Connections: the ergonomic win, nearly free

`ConnectionFields` injection is **manifest-driven at run time**
(`engine/secrets.go:48`), and the slug lookup simply scans manifests for a
matching `Integration` (`daemon/connectionverify.go:41`, `daemon/service.go:1810`).
Nothing in that path cares whether the manifest was written in Go or
synthesized.

So a synthesized operation manifest that declares:

```go
Integration: "myservice",
ConnectionFields: []core.ConnectionField{
    {Key: "base_url", Label: "Service address", Required: true,
     Placeholder: "https://api.internal.example.com"},
    {Key: "token", Label: "API token", Secret: true, Required: true},
},
```

lands the tenant's own service on an Apps page beside Gmail and Stripe:
connected once, encrypted, injected into params the author left unset, never
visible inside a flow — and a token rotation is one edit instead of forty. This
is the single biggest ergonomic difference from `http_request` and it costs
almost nothing to claim.

Two things to check before relying on it — **both since checked, and the second
was a real bug**; see "Commit 2 as built": the connection scans do see
tenant-scoped manifests, and a tenant-chosen integration name absolutely could
collide with a built-in's slug.

## The hard parts

**Curation, and it is the big one.** An MCP server curates `tools/list`; a spec
does not. Stripe's is ~500 operations, GitHub's ~900. Registering one naively
dumps them all into the palette and into the manifest map handed wholesale to
the UI and the generator. So the feature is **"import operations from a spec"**,
never "register a spec": parse, list what's in there, let the admin pick by tag,
path prefix or operation. Cap operations per catalog. A tenant's own service
makes this look unnecessary — twenty operations, all wanted — which is exactly
why it has to be decided now rather than after the first vendor spec arrives.

**Refresh is a diff, not a replace.** Re-importing a changed spec can delete or
rename operations that live flows reference by id. `MCPServer`'s "renaming is
not an edit" rule (`daemon/mcpservers.go:68`) applies to operations here, and
one level worse because the tenant does not control when their spec changes.
Refresh must report added, changed and *removed* operations and require
confirmation for removals, rather than silently emptying steps out of saved
flows.

**Spec parsing grind.** `$ref` resolution, `allOf` merging, 3.0's `nullable`
versus 3.1's JSON-Schema-2020-12 unions, discriminators, `servers` variables and
per-operation server overrides. Two rules to fix up front: **external `$ref`s
are refused, not fetched** — that is an SSRF vector wearing a document's
clothes, and the spec URL itself must go through `Do` like any other tenant-
supplied URL — and Swagger 2.0 is either explicitly in scope or explicitly
refused with a message that says so. It is still everywhere; "we crashed on it"
is not a position.

**Argument collisions.** A query param and a body property both called `id`.
Precedence must be declared in the descriptor (prefix the body one, or refuse
the import and make the author rename), not resolved by map order.

**No handshake.** A spec is a document, so registration cannot prove the
endpoint works — there is no `initialize` to fail. `LastConnected` / `LastError`
therefore mean something different here: either an optional probe of one GET the
admin nominates, or the outcome of first use. Say which in the UI. This is the
place a reader will assume the MCP semantics carry over, and they do not.

**Output ports.** Mirror `http_request`'s three (`status`, `response_body`,
`headers`) rather than MCP's single `out`: flows branch on status, and a typed
step that hides it would be a downgrade from the generic box it replaces.
Decode JSON responses onto `response_body`; leave binary alone and note that
`InlineOnly` means a spec returning a PDF has no good answer here.

## Reaching a service that isn't on the internet

The remaining hole, and the reason this is one document rather than two: an
internal service is blocked over HTTP by design and always will be. Today that
leaves runner-plus-script.

The cheap version is **not a new feature** — it is one field on the catalog:
*reach this through runner X* (a tag list, reusing the runner tag vocabulary
exactly as `docs/mcp-on-runners-design.md` proposes for the same reason: a
machine's own name is one of its tags, so there is no second targeting concept).
Execution then renders the request as a small script, queues it as an ordinary
`RunnerTask` with the body on `Stdin`, and reads status/headers/body back off
stdout. Same descriptor, same manifests, same ports, same connection — only the
last mile changes.

This is materially smaller than the MCP-on-runners design because there is no
session to fake: an HTTP request *is* one-shot, which is precisely the shape
`RunnerTask` already has. Dispatch goes through the existing indirection
(`runnerdrop.SetDispatcher` / `runnerBridge`, `cmd/dzd/main.go:2397`) because
`engine/webapi` cannot import `daemon` and must not try.

It inherits two properties honestly: the manifests are persisted rather than
live (nothing to handshake with, so this is easier here than for MCP — the
descriptor is already the source of truth), and a tag matching several machines
means calls land on any of them, which is right for HTTP and needs no caveat.

## Phasing

1. **Custom HTTP step, tenant-defined, with `ConnectionFields`.** Descriptor,
   synthesis, executor via `drops/net.Do`, store, reconcile, admin page. The
   smallest thing that closes the actual gap, and it establishes everything the
   next two phases reuse. **Built** — see the two sections below.
2. **OpenAPI import** as a second front end onto the same descriptor: fetch or
   paste, parse, pick operations, refresh-as-diff. **Built** — see "Commit 4 as
   built" below.
3. **Reach through a runner** — one field, for the internal-service case.
   **Built** — see "Commit 3 as built" below.

Anything before (1) is a workaround for the absence of (1).

## Decisions taken (2026-08-27)

- **One descriptor, two front ends.** A form-built step and an imported
  operation are the same object; the spec parser is a producer, not a parallel
  system.
- **Reuse `engine/mcp`'s port synthesis and `drops/net.Do`.** No second set of
  port rules, no second HTTP path.
- **Import operations, never "register a spec".** Curation is the feature, not a
  later optimization.
- **Refresh diffs and confirms removals.** A saved flow must not lose steps
  because someone regenerated a spec.
- **External `$ref`s are refused, and the spec URL is fetched through the guard.**
- **Declare idempotency from the HTTP method**, which is the one place we can be
  more honest than MCP.
- **`ConnectionFields` on the synthesized manifest** — a tenant's own service
  gets the Apps-page treatment, not a `${secret.X}` in every header map.
- **Internal reach is a field on the catalog, not a fourth product.**

## Open questions

- Does the flow generator need anything beyond `ManifestsForTenant`? MCP's case
  works because the generator grounds on `SearchDrops → listDrops →
  ManifestsForTenant` and nothing in flowgen mentions MCP — the connection is
  invisible in the code and pinned only by a test. This feature needs the
  equivalent test on day one, for the same reason.
- Where does an operation's `Icon` and `Color` come from? MCP hardcodes `#7a5`.
  A tenant-chosen icon per catalog is probably right and probably trivial.
- Should a catalog be able to declare an operation `Idempotent` against the
  method — a POST the service documents as safe to replay via an idempotency
  key? `http_request` already attaches a stable `Idempotency-Key` for exactly
  this; whether to expose it per operation is a real choice.
- Pagination. A GET returning a page and a cursor is the single most common
  shape in an in-house API, and nothing here helps with it. Probably out of
  scope, but it will be asked for immediately.
- Is `api:` the right id prefix, given `mcp:` is taken and the two catalogs are
  siblings? Whatever it is, it is unchangeable once a flow references it.

## Deferred (deliberately not designed)

- **OAuth2.** `apiKey`-in-header and `http bearer` land straight onto the auth
  kinds `MCPServer` already stores. Client-credentials needs token fetch,
  refresh and caching; authorization-code needs a redirect and an account model.
  Both deserve their own decision, and neither is what an in-house service uses.
- **Webhooks in the other direction.** A tenant's service *calling* Dazyflow is
  already solved by webhook triggers and API keys; nothing here touches it.
- **Response-schema-driven output ports.** Splitting a declared response object
  into typed output pins is tempting and is a second port-synthesis problem
  (naming, nesting, arrays). One `response_body` port until someone asks.
- **Uploading or downloading files.** `InlineOnly` forbids it, for the same
  reason it forbids it for MCP.

## Commit 1 as built (2026-08-27)

The engine half of phase 1 exists: `internal/schemaports` (the shared port
policy), `engine/webapi` (descriptor, manifest synthesis, HTTP executor,
catalog), a fourth catalog in `NodeResolver`, and the `cmd/dzd` wiring. No
persistence and no UI, so nothing registers a catalog yet — the point of
stopping here was to find out whether the design above survives contact.

**What held.** The central bet did: an operation and a hand-built step are one
descriptor, and `Arg.In` does not contaminate the shared parts. It is read only
when assembling the request.

**The lift was bigger than described, in a good way.** Not just the port rules
moved to `internal/schemaports` but the three-layer argument precedence too
(`Assemble`) — params, then the overlay port, then a per-argument port. That is
the same policy as `Build`, read from the other end, so keeping them apart would
have been the drift the lift exists to prevent. `engine/mcp`'s `toolInputPorts`
and `buildArguments` are now four lines each and its tests pass unchanged, which
is the evidence that the behavior is identical.

**The executor claim above was wrong about mechanism.** `drops/net` imports
`engine`, so `engine/webapi` importing `drops/net.Do` is an import cycle —
exactly the cycle `engine/mcp` already works around for its dial guard. So the
caller is injected: `webapi.SetDoer`, wired to `hfnet.Do` in
`applyNetworkPolicy` beside `mcp.SetDialControl`. Unset means every web-API step
fails loudly; a fallback to `http.DefaultClient` would have made every guard
opt-in and the omission invisible.

**Phase 2 needs no credential storage.** Because auth rides on
`ConnectionFields`, the credential lives in the connection store that already
exists — the engine injects it into the `token` param at run time and
`engine/webapi` never sees a secret at rest. The phase-2 store holds only the
DESCRIPTOR, which is loggable. That deletes the sealed-blob half of the
`daemon/mcpservers.go` template from the next commit, and with it the AAD
question.

**A wart found next door.** Values travel as text on every port, so a number
wired into a JSON body arrives as `"42"`. `engine/webapi` coerces body fields
back to their declared type; `engine/mcp` does not, and hands `"42"` to a tool
whose schema says number. Pre-existing, not caused by the lift, and worth its
own small fix rather than a widening of this one.

**Decisions the build had to make that this note did not.**

- **Header arguments get no ports** — in a real spec they are versioning and
  content negotiation, set once per catalog, and a pin each would spend the
  twelve-port budget on the arguments least likely to be wired. They stay
  params, and their schema carries an `x_location` so the form says where the
  value goes. It is the only place the request's structure is visible to an
  author at all.
- **A base URL may not carry a query or fragment.** Paths and the encoded query
  are appended to it, so `https://x/v1?debug=1` would build
  `https://x/v1?debug=1/orders?id=2`. Refused at import AND at run time, because
  the connection can supply one later.
- **Path values are percent-escaped; header values may not contain a line
  break.** The first keeps an id from walking out of its collection, the second
  is refused with a message naming the argument rather than deep in the wire
  format.
- **`api:` is the prefix**, so a step id cannot collide with a native drop by
  construction and there is no `Reserved`-style check to forget. Now
  unchangeable.
- **Failure classification follows `http_request` exactly**: `unexpected_status`
  as a node error with no transport error, 2xx by default, `expect_status` to
  widen — so a 404 can be an answer rather than a failure.

**Still owed by phase 1**, for the commit that adds persistence: the store and
reconcile loop, the admin API and page, and the flowgen test that pins these
manifests reaching the generator the way `daemon/mcpservers_flowgen_test.go`
pins MCP's. Also worth doing then: `drops/internal/mimetype` is walled off from
`engine/` by Go's internal rule and now has a second caller that wants it, so it
belongs in `internal/`.

## Commit 2 as built (2026-08-27)

Persistence, the admin API, the page, and the grounding test. `daemon/webapis.go`
+ `daemon/webapis_pg.go` (store, service, reconcile), `daemon/httpwebapis.go`
(four routes under `/api/v1/admin/web-apis`), `web/src/pages/admin/AdminWebAPIs.tsx`,
and `daemon/webapis_flowgen_test.go`.

**The prediction held: no credential storage.** `tenant_web_apis` has no
`auth_secret` column, no sealing, no `SealedToken`, and `WebAPIs` needs no
`EncryptedSecrets` dependency — the whole of MCP's write-only-column care is
absent because there is nothing here to protect. The credential lives in the
org's connection for the integration and reaches the step as an injected param.
One consequence worth stating: `daemon.WebAPI` is safe to log, return, and audit
*by construction* rather than by convention, and the API test asserts the wire
row has nowhere to put a token.

**One validation, called twice over.** `Save` builds the descriptor and calls
`webapi.Descriptor.Validate()` rather than restating its rules, so a row that
saves is a row that registers, and the message an admin sees next to the field is
the engine's own. The daemon adds only what is POLICY and not assemblability:
https-unless-private-egress (shared with MCP servers), the label length, the
name derivation, and the caps.

**What the shared step-source rules turned out to be.** Extracted to
`daemon/stepsources.go` when the second source arrived: the slug (with its
diacritic-folding table), the id charset, the numbered uniqueness pass, and the
URL policy. `requireStepSourceAdmin` already used that vocabulary, which is how
the file got its name. MCP keeps only what differs — its taken-set includes the
operator's instance-wide servers, a web API's does not.

**Where the two features genuinely diverge, and why the UI says so.** There is no
`refresh` route and no "connected" chip. A described API has nothing to
re-handshake with, so re-reading its operations is a save (and belongs to the
importer); and since no call was made, the strong status is *"in your palette"* —
the daemon holds the catalog and the steps resolve. A green light implying the
service answers would be the one lie the page could tell. `last_error` therefore
means something narrower than MCP's: the reconcile loop could not register a
STORED row, which is only reachable when a later release tightens validation.
Without it an org would experience that as steps quietly gone.

**The integration-slug collision was real, and worse than the note guessed.**
`connectionFieldsForSlug` scans the tenant's whole manifest map and takes the
FIRST match — over a Go map, so the order is random. A tenant naming their
catalog's app "Gmail" would therefore make the Gmail connection page show their
web-API fields on some requests and Gmail's on others. Two rules now stop it: a
`ReservedIntegration` hook (the `RemoteCatalog.Reserved` pattern), wired at
startup from every integration the native registry names — not only the
connectable ones, since a name that has no connection today would collide the day
it gains one — and a per-org uniqueness check, because two catalogs sharing a
slug would share one connection and send one's address from the other.

A second, quieter version of the same bug: the slug lands inside a secret name
(`conn.<slug>.<field>`) and that validator allows only `[A-Za-z0-9_.-]`. An app
called "Ordrar!" would have saved fine and then failed at CONNECT time, with an
error about secret names on a page the admin could not connect to what they
typed. The name is validated where it is entered instead.

Where an app name is DERIVED rather than typed it falls back to the catalog's own
id when the label is taken, because a duplicate label is legal — that is what the
numbered ids are for, and refusing the second "Order service" would make the id
derivation pointless. And an existing app name is never re-derived: an edit that
changed only the base URL would otherwise resolve from an empty label, land on
the id, and quietly orphan the address and credential the org had already
entered. Moving the connection now requires asking for the move.

**Caps chosen.** 100 catalogs per org, 60 operations per catalog, 40 arguments
per operation. The operation cap is the curation guard this note argues for, and
it is set where a hand-built catalog never meets it and a vendor spec import
always will — which is the intended pressure: the importer must offer selection
rather than raise the number.

**The operation editor is where the UI effort went**, because it is where the
value is entered: a step beats a generic web request only if its arguments are
named, typed and placed. Two decisions inside it are worth keeping — a body
argument is offered only when the operation sends a JSON body (the daemon would
refuse it otherwise, and preventing beats explaining), and switching a body mode
away from JSON REHOMES body arguments to the query string rather than dropping
what the admin typed.

**Still owed.** `drops/internal/mimetype` is walled off from `engine/` by Go's
internal rule and now has a second caller that wants it; it belongs in
`internal/`. `engine/mcp` still hands port text to tools uncoerced (see commit
1). Neither is this feature's business to fix.

## Commit 3 as built (2026-08-28)

Phase 3, built out of order: phase 2 removes typing for a service that is public
and already has a spec, while this removes a hard blocker for one that is not
reachable at all — and the guide had been telling that reader to go write a
script per operation.

`engine/webapi/runner.go` (dispatcher hook, the request script, reply parsing),
one field on the descriptor (`RunnerReach`), one column (`runner_tags`), one
form field, and a second bridge in `cmd/dzd`. The note's estimate held: the
descriptor, the synthesized manifests, the ports and the connection are all
untouched, and `buildRequest` assembles the call identically for both paths.

**The whole envelope goes on stdin, not just the body.** The note said "the body
on `Stdin`", which would have left method, URL and headers to be templated into
the script — and the auth header is one of those headers. `runner_tasks` stores
`script` and `stdin` in separate columns and both are read while debugging a
queue, so a templated script is a second place a token can be. Putting the whole
request there instead makes the script a CONSTANT: it carries no request detail
and no credential, and there is one column to reason about rather than two. A
test asserts the credential is absent from the script and present in stdin.

Two properties fell out of that which are worth stating. The script is sealed at
rest along with stdin when `DAZYFLOW_MASTER_KEY` is set (`PayloadCipher` covers
script, stdin and env), so the credential gets the same protection a
`${secret.X}` in a hand-written runner script already gets. And because the
script never varies, it can be tested by running it: `TestRunnerScript_
ActuallyPerformsTheCall` executes the real python against a real `httptest`
server, which a per-request templated program could not be. That test exists
because the script is a string constant in a Go file — never compiled, never
linted, and a typo in it would otherwise fail once, on a customer's machine.

**Python, and it is not a toss-up.** `runner/dzrunner.py` IS python3 and its
`interpreter_argv` falls back to `sys.executable` for this shell, so it is the
one interpreter guaranteed present on a machine running an agent. A shell script
would have needed curl, which is guaranteed nowhere. The one failure mode is a
runner installed with `--allow` that does not permit python; its stderr is
quoted verbatim rather than summarised, because it is the only thing that
explains the failure.

**The guard bypass is the cost, and it is stated three times on purpose** — in
`RunnerReach`'s doc comment, in the admin form beside the field, and in the
guide. A runner-borne call does not pass through the guarded `Doer`, so it loses
the SSRF dial guard (that is the point), the per-tenant egress allowlist and the
per-host rate limit. The response cap is the one that had to be REBUILT rather
than inherited: the script re-imposes `max_bytes` itself, because a machine
streaming a gigabyte back through a task row is a different failure from the one
the allowlist prevents.

**Decisions the note did not make.**

- **A 4xx/5xx from inside the network is an answer, not a transport failure.**
  `urllib` raises `HTTPError` for both; the script catches it and reports the
  status, so `expect_status` behaves identically on both paths and a 404 can
  still be an answer.
- **Bodies are base64 across the boundary.** stdout is text and a response is
  not; a PDF would not survive being handed through as-is.
- **The task timeout is the HTTP timeout plus a margin**, not the same value.
  Equal values race, and the loser is the more useful message.
- **Tags are normalised (lower-cased, de-duplicated) at the daemon**, and an
  un-normalised tag is REFUSED by `Validate` rather than stored — the matching
  side lower-cases, so `"Linux"` would have matched nothing, silently.
- **`RunnerTags` is nil-means-unsent** on both the input struct and the wire,
  like `Enabled` and `Description`. An API caller changing only the base URL
  must not move a catalog off its runner and onto a call the network refuses.
  The admin form therefore always sends the field, empty included.

**Still owed.** Nothing blocking, but two things a reader should not assume:
there is no probe, so a catalog pointed at the wrong tags reports it on first
use rather than at save time (the note already says `LastConnected` means
something different here); and the runner path is not covered by the flowgen
test, which grounds on manifests and cannot see which transport they use.


## Commit 4 as built (2026-08-28)

Phase 2. `engine/webapi/openapi.go` (parser + guarded fetch),
`engine/webapi/diff.go` (refresh), one endpoint, one column (`spec_url`), and
`web/src/components/admin/WebAPISpecImport.tsx`.

**The central bet paid off again, and more visibly than in phase 3.** The
importer produces `[]webapi.Operation` and hands it to the form; the form saves
it through the same path a hand-built catalog uses. No second store, no second
validation, no second synthesis. The API test that pins this
(`TestHTTP_ImportedOperationsBecomeStepsThroughTheOrdinarySave`) is four lines
long, which is the evidence.

**A parser of our own, not a library.** The note did not take this decision and
it turned out to be the load-bearing one. `go.yaml.in/yaml/v3` was already a
direct dependency and a JSON document is valid YAML, so the format half was
free; what a library would have bought is the schema grind, and what it would
have cost is the curation stance. A library validates a DOCUMENT and refuses a
DOCUMENT — so one operation we cannot express would block the fifty we can,
which is the opposite of "import operations, never register a spec". Here an
operation that does not fit is skipped with a warning naming it. It also makes
the note's external-`$ref` rule structural rather than configured: there is no
fetcher in the parser, so an external reference cannot be followed even by
mistake, where a library's equivalent is a flag someone can turn off.

The honest cost: 3.1's JSON-Schema unions, discriminators and `servers`
variables get a conservative reading. Each of those surfaces as a warning, never
as a wrong request.

**Skipping is done by running the real validation.** `p.operation` calls
`op.validate()` — the descriptor's own — rather than reimplementing its rules.
So an operation that would fail Save is skipped at parse time with that same
message, and the two can never disagree about what is importable.

**Decisions the note left open.**

- **`summary` → Title, `description` → Description, and Summary left empty.**
  OpenAPI has two prose fields and the descriptor has three. Setting Title and
  Summary both from `summary` put the same sentence on the node card twice;
  leaving Summary empty lets the subtitle fall back to `GET /orders/{id}`, which
  complements the caption. `description` absorbs `summary` when absent, because
  Description is what the flow generator grounds on.
- **Argument collisions are refused, not renamed.** The note offered "prefix the
  body one, or refuse". A path-level parameter restated on the operation is
  deduplicated (it is the same parameter); a name genuinely used in two
  locations is left for `validate()` to refuse with its own message, so this
  file never invents a winner.
- **A non-object JSON body becomes `BodyRaw`,** not a skipped operation — an
  array or an upload has no field-per-argument reading but is still a working
  step with a `request_body` port.
- **Operation tags ride beside the operations, not on them.** `Operation` is a
  STORED type where a new field changes what every persisted descriptor means,
  and tags are useful only in the picker. They travel as a separate
  `operation_tags` map.
- **Ids are derived deterministically** from method and path when a spec has no
  `operationId`, because a refresh matches on id: an id that varied between
  parses would read as a removal plus an addition on every import.

**Refresh is additive until confirmed**, which is stronger than the note's
"require confirmation for removals". `ApplyRefresh` KEEPS an unconfirmed
removal, so the failure mode of an admin clicking through a report is a stale
operation, not a flow that lost a step. Argument reordering is normalised out of
the comparison for the same reason: a refresh that cried "changed" on every spec
regeneration would train people to stop reading it, and the one that matters is
the one with a removal in it.

**Still owed.** The refresh has no scheduler — it is a button, not a poll, which
is right for something that can require confirmation but means a spec change is
noticed when someone looks. And `sameOperation` compares marshalled JSON so a
new field on `Operation` is covered automatically; that is deliberate, but it
does mean a purely cosmetic field added later would start reporting "changed".
