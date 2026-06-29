# Support tickets + consented read-only access — design TODO

Status: **design only, nothing built yet.** Hand-off doc so this can be
continued in a fresh session. Written 2026-06-29.

## Goal

A Support surface where an org files a ticket about a flow, support staff
help via a chat thread, and support can request a **scoped, time-boxed,
audited, read-only** view of a single flow — **without ever seeing secrets
or raw run data.**

The premise of the product is "the user's stuff is secret," so every part
of this is built around a redaction boundary and explicit org consent.

## Prerequisite (build first): the redacted support bundle

This was specced in discussion but not yet written down as code. It is the
foundation for both the ticket attachment and the live read-only view.

- One pure function in `core`, **redaction by construction** — a separate
  `SupportBundle` type that physically cannot hold a raw value; you build it
  *from* `core.Graph` + run records, never serialize the raw structs.
  - `core/support_bundle.go`: `func BuildSupportBundle(g Graph, run RunSnapshot, issues []LintIssue, mode RedactMode) SupportBundle`
- Redaction boundary (the only 4 danger zones; everything else is safe structure):
  - `Node.Params` values → **redact to shape** (`{"__redacted":"string","len":19}`);
    keep KEYS; keep reference templates verbatim (`${secret.NAME}`, `${node.…}`,
    `${item.…}`) — use `secretPlaceholderPattern` / `walkParams` in `core/lint.go`
    to tell a reference from a literal.
  - `Node.Env` values → redact to shape, keep keys.
  - `Graph.Triggers` / webhook params → **scrub** (carry generated bearer secrets;
    see `hardcodedSecretExempt` `webhook_input.secrets` in `core/lint.go`).
  - `Result.Output` → `core.Ref.Inline` → **drop**; keep `Ref.MIME` + shape.
    `Ref.Headers` (column names) → count by default, opt-in keep.
- Keep (safe, the diagnostic gold): all `Edge`s, node IDs/modules/`Disabled`/
  timings, `JobStatus`, `JobError.Code` + `Message` (Message is contractually
  user-facing per `core/job.go:92`). **Drop `JobError.Details`** (may embed
  URLs/tokens/data). Never include `JobRecord.GraphPayload` raw — rebuild from
  the redacted graph.
- Attach `ValidateGraphFull(g, manifests)` + `LintGraph(g)` output — safe by
  design (references node IDs / field names, never values).
- **Secret-scrub safety net:** final pass over EVERY string in the bundle
  through existing detectors (`knownSecretValue`, `secretKeyName` + `isLiteralSecret`
  in `core/lint.go`); replace any match. Catches a token pasted into a flow Name
  or echoed in an error Message.
- Two modes: `RedactStructureOnly` (default, recommended) and
  `RedactStructurePlusValues` (opt-in; keep non-secret literals, still scrub +
  drop payloads).
- Tests: golden (graph w/ `sk_live_…`, a `${secret.X}` ref, an empty required
  field, a run w/ `Ref.Inline`) → assert refs/codes/empty-sentinel present and
  the raw secret + payload appear NOWHERE in serialized output; property test:
  no `knownSecretValue` match survives; trigger bearer secret is scrubbed.

---

## Data model

New package or files under `core/` for the pure types + store interfaces;
implementations in `daemon/` (in-memory for tests + Postgres for prod, mirror
the `core.JobStore` / `core.AuditLog` pattern).

### Ticket

```go
type TicketStatus string // open | awaiting_user | awaiting_support | resolved | closed

type Ticket struct {
    ID         string
    Tenant     string   // the org that filed it (scopes everything)
    Workspace  string
    CreatedBy  string   // principal subject
    Subject    string
    Status     TicketStatus
    FlowID     string   // optional — the flow this ticket is about
    BundleID   string   // optional — attached SupportBundleRecord
    AssignedTo string   // optional — support agent subject
    CreatedAt  time.Time
    UpdatedAt  time.Time
}
```

### TicketMessage (chat)

```go
type AuthorKind string // user | support | system

type TicketMessage struct {
    ID         string
    TicketID   string
    Author     string     // subject ("" for system)
    AuthorKind AuthorKind
    Body       string     // SECRET-SCRUBBED before store (see note)
    BundleID   string     // optional attachment
    CreatedAt  time.Time
}
```
- **Scrub on ingest:** users WILL paste API keys into chat. Run `Body` through
  the same `knownSecretValue` detectors before persisting AND before display.

### SupportBundleRecord (persisted redacted bundle)

```go
type SupportBundleRecord struct {
    ID        string
    Tenant    string
    FlowID    string
    RunID     string // optional
    Mode      RedactMode
    Payload   []byte // redacted SupportBundle JSON — never raw
    CreatedBy string
    CreatedAt time.Time
}
```

### AccessGrant (the consented read-only view — the trust-critical piece)

```go
type GrantStatus string // requested | approved | denied | revoked | expired

type AccessGrant struct {
    ID          string
    TicketID    string      // the reason/anchor for the request
    Tenant      string      // scope: the org
    FlowID      string      // scope: ONE flow, not the account
    AgentSubject string     // the SPECIFIC support agent the grant is for
    Status      GrantStatus
    RequestedAt time.Time
    RequestedBy string      // agent subject
    DecidedBy   string      // org admin subject (approve/deny)
    DecidedAt   *time.Time
    ExpiresAt   time.Time   // time-boxed; access auto-expires
    RevokedAt   *time.Time
    RevokedBy   string
    // Invariants, always enforced regardless of grant:
    //   read-only, SecretsMasked=true, DataRedacted=true
}
```

Grant state machine: `requested → (approved | denied)`; `approved → (revoked | expired)`.
**Access is valid iff** `Status==approved && now.Before(ExpiresAt)`.
Default TTL: TBD (suggest a few hours, end-of-day cap).

### Stores

```go
type TicketStore interface {
    Create(ctx, Ticket) error
    Get(ctx, id string) (Ticket, error)
    ListForTenant(ctx, tenant string, ...) ([]Ticket, error) // user view
    ListQueue(ctx, ...) ([]Ticket, error)                    // cross-tenant support queue
    Update(ctx, Ticket) error
    AppendMessage(ctx, TicketMessage) error
    ListMessages(ctx, ticketID string) ([]TicketMessage, error)
}

type GrantStore interface {
    Create(ctx, AccessGrant) error            // request
    Decide(ctx, id, status GrantStatus, by string) error
    Revoke(ctx, id, by string) error
    Get(ctx, id string) (AccessGrant, error)
    ActiveGrant(ctx, agent, tenant, flowID string, now time.Time) (AccessGrant, bool)
    ListForTenant(ctx, tenant string) ([]AccessGrant, error)
}
```

---

## Auth changes (grounded in existing `core/authz.go` + `core/rbac.go`)

Existing model: `core.Principal{Subject,Tenant,Workspace,Roles,Extras}`;
`PermPlatformAdmin = "platform:admin"` is the cross-tenant super-admin and
`RequireTenant` (authz.go:72) short-circuits for it. `AuthorizeGraphView/Run/Edit`
gate on tenant + visibility + permission.

**Key principle: support is NOT platform-admin.** Support gets a new, weaker
permission and reaches a flow ONLY through an active grant — a *capability*
check, never tenant-crossing.

1. New permission in `core/rbac.go`:
   ```go
   PermSupportAgent Permission = "support:agent"
   ```
   By itself grants only: read the support queue, post chat, request a grant.
   It does NOT cross tenant for flows/secrets/runs and does NOT imply platform:admin.

2. New role constructor in `core/rbac.go`:
   ```go
   func SupportAgentRole() Role { return Role{Name:"support_agent", Permissions:[]Permission{PermSupportAgent}} }
   ```

3. New authorize function in `core/authz.go` — capability (grant) based, do NOT
   route through `AuthorizeGraphView` (it would fail `RequireTenant`):
   ```go
   func AuthorizeGraphSupportView(p Principal, graph Graph, grant AccessGrant, now time.Time) error {
       // require: p.Has(PermSupportAgent)
       //          grant.AgentSubject == p.Subject
       //          grant.Tenant == graph.Tenant && grant.FlowID == graph.ID
       //          grant.Status == approved && now.Before(grant.ExpiresAt)
       // read-only ONLY — never gate Run/Edit through this.
   }
   ```
   The support agent's own `Tenant` is irrelevant; the (agent, tenant, flow)
   tuple in the grant is the authority.

4. Do NOT add `PermSupportAgent` to `RequireTenant`'s short-circuit. Support
   must not get ambient cross-tenant access.

5. **Even with an active grant, serve the REDACTED view** — the support-view
   endpoint returns `BuildSupportBundle(...)` (a live, navigable, redacted
   render), never the raw graph. The grant unlocks freshness/navigation, not
   plaintext. Secrets stay masked, run data redacted.

6. **Audit into the ORG's log** (`core.AuditLog`, per-tenant): every support
   action writes `AuditEvent{Tenant: graph.Tenant, Actor: agentSubject,
   Action: "support.view"|"support.grant.request"|"support.grant.revoke",
   Target: flowID}` so the org sees everything support did. (Detail MUST stay
   non-sensitive — names/ids only, per audit.go contract.)

---

## Flow / surfaces

- Org user (`PermGraphRun`+ in their tenant) files a ticket; can attach a
  freshly built `SupportBundleRecord` for a flow.
- Support agent (`PermSupportAgent`) sees the cross-tenant queue, reads tickets,
  chats, clicks "Request read-only access" → creates an `AccessGrant{status:requested}`.
- Org admin (`CanAdminOrg`) gets a consent prompt — plain language, e.g.
  *"Support wants to view your 'Daily invoice' flow (read-only) until 5PM today.
  Secrets stay hidden. Revoke anytime."* → approve/deny.
- With an active grant, support opens the read-only redacted view of THAT one flow.
- Grant auto-expires; org or support can revoke early; all in the audit log.

### Getting fixes back to a non-techy user (already-discussed levels)
1. Guide + deep link to the exact node (`canvas_url` from
   `flowMutationResponse` in `daemon/me_routes.go` + node ID).
2. Reproduce: import the structure-only bundle into a support test workspace,
   run via `test_trigger_flow`.
3. Offer a `patch_flow` the user approves (no standing write access).

---

## Hosted vs self-host

- Hosted (lead deployment): "support" = vendor staff. Gate agents via an env
  allowlist (mirror `DAZYFLOW_PLATFORM_ADMINS`; e.g. `DAZYFLOW_SUPPORT_AGENTS`)
  or a runtime grant, constructing `SupportAgentRole()`.
- Self-host (free AGPL): no vendor support staff. Reframe the queue as
  **org-internal** (an org admin helping their own members) or leave it empty/
  disabled. Don't bake a hosted-only assumption into core — "support" is just a
  role; who fills it differs per deployment.

---

## Phasing

1. **Bundle + grant only (no ticket UI):** `BuildSupportBundle`, persist
   `SupportBundleRecord`, `AccessGrant` request/approve/expire/revoke,
   `AuthorizeGraphSupportView`, support-view endpoint serving the redacted view,
   audit wiring. Pipe to whatever support channel exists today.
2. **Minimal ticket + chat:** `Ticket`/`TicketMessage` stores + endpoints, chat
   with secret-scrub, grant prompt lives on the ticket, bundle auto-attached.
3. **Support dashboard:** cross-org queue, assignment, role-separation polish.

---

## Tests to write

- `AuthorizeGraphSupportView`: approved+unexpired = ok; expired/denied/revoked/
  wrong-agent/wrong-flow/wrong-tenant = ErrUnauthorized; read-only never gates run/edit.
- Support agent WITHOUT a grant gets nothing (no ambient cross-tenant access);
  confirm `RequireTenant` still rejects a support principal for a foreign tenant.
- Grant TTL expiry boundary (now == ExpiresAt → denied).
- Chat secret-scrub on ingest + display.
- Audit event written to the ORG tenant on every support view.
- Bundle redaction tests (see Prerequisite section).

---

## Open decisions (need answers before/while building)

- **Build native ticket/chat, or integrate an external helpdesk** (Zendesk/Plain/
  Intercom) and build ONLY the bundle + consented view natively? (My
  recommendation in discussion: build the trust primitives natively, defer/avoid
  rebuilding the inbox. This is the main fork.)
- Where to store bundles/blobs — reuse an existing store or new one?
- Realtime chat transport — reuse the run SSE `Bus`?
- How the org is notified of a grant request — reuse `FailureNotify` webhook,
  email, in-app?
- Default grant TTL.
- Does support ever exceed read-only (propose patches)? Default: no; defer.

## Note

Unrelated in-flight change on this branch (`parallel-work`): `ValidateWithManifests`
now ignores disabled nodes for wiring checks (`core/validate.go`). Not part of
this feature.
