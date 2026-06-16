# Hazyflow — Things to Improve

A deep, codebase-wide review covering the Go backend (`daemon/`, `engine/`,
`core/`), the built-in drop library (`drops/`), the web frontend (`web/`), and
cross-cutting concerns (security, secrets, GDPR, build/CI, docs).

**How to read this.** Findings are grouped by area and tagged **[H]/[M]/[L]**
(high/medium/low). They are *candidates* surfaced by review, not confirmed
defects — line numbers can drift, and a few may already be intentional. Verify
each against current source before acting. Nothing here is a known data-loss or
RCE bug; the codebase is well-hardened overall. The biggest themes are
**frontend component bloat + thin web test coverage**, **scattered backend
error-handling/concurrency rough edges**, and **multi-tenant/audit hardening**.

---

## 1. Backend — `daemon/`, `engine/`, `core/`

### Concurrency & lifecycle
- **[H]** Verify the per-node progress channel is always closed on early-exit
  paths in `daemon/worker.go` (graph-fetch error, panic before close). A
  forwarding goroutine blocked on a never-closed channel is a leak. Prefer
  `defer close(nodeProgress)` right after creation.
- **[H]** `context.Background()` used for terminal-status writes / finalization
  in `daemon/worker.go` ignores graceful-shutdown cancellation. Where a write
  must outlive cancellation, use `context.WithoutCancel(ctx)` explicitly so the
  intent is visible and urgency still propagates.
- **[M]** Lazy-initialised maps read under a nil check that can race:
  `oauth.go` `refreshLocks`, `service.go` `suggestCache`. Initialise these in
  their constructors (`NewOAuthRegistry`, `NewService`) or guard with `sync.Once`.
- **[M]** `eventbus.go` `MemoryBus.Publish` can panic sending on a channel a
  subscriber closed on cancel. Recover, or track subscriber liveness.
- **[M]** gRPC progress forwarding (`grpcserver.go`) can deadlock if `RunGraph`
  returns/panics before the progress channel drains — ensure the close happens
  in a defer that always runs.

### Error handling & HTTP gateway
- **[M]** Silently-ignored errors: `json.Encode` in `errors.go`, optional-body
  decode in `me_routes.go`, health-check `rw.Write` in `httpgateway.go`. At
  minimum log them; a broken ResponseWriter currently yields a partial body
  with no trace.
- **[M]** Two error-envelope shapes coexist (structured `writeAPIError` vs
  legacy `{"error": "<string>"}`). Audit handlers and migrate everything to the
  structured envelope with explicit `code`s — the web client already wants a
  stable `code` (see §3).
- **[M]** CORS `WildcardDomain` trusts every subdomain; a misconfig like
  `"com"` would be catastrophic. Reject overly-broad wildcards (require ≥2 labels)
  and document the constraint.
- **[L]** Add an early Content-Length / body-size guard on POST routes so a
  request can't force large allocation before `MaxBytesReader` trips.

### Architecture & tests
- **[M]** `Service` is a god object (~40 fields: auth, storage, engine, bus,
  mailer, billing, oauth, secrets, usage, plans…). Extract `OAuthService`,
  `BillingService`, `SecretsService`; let `Service` be a thin facade.
- **[M]** Thin coverage on complex control flow: worker lease-loss handling,
  `dispatch.go` skip/wait/enqueue decisions, `DropSuggestions` cache
  invalidation. Add table-driven tests for each branch.

---

## 2. Drops — `drops/`

### Cross-drop consistency
- **[M]** Parameter-name drift for the same concept:
  - result caps: `max_bytes` / `max_output_bytes` / `max_body_bytes` → pick one.
  - list size: `limit` (stripe, git) vs `max_results` (gmail) → standardise on `limit`.
  - some `timeout_ms` params lack a description (e.g. `stripe_list_events`).
  A single naming convention improves LLM discoverability (the whole point of
  the manifest metadata). Consider a lint that flags non-canonical param names.
- **[L]** Egress checks invoked inconsistently (public `hfnet.EgressAllowed`
  vs an unexported `egressAllowed`). Funnel all callers through one public
  function so the SSRF/egress path is single-sourced.

### Test coverage (functional, beyond the existing safety sweep)
- **[M]** Stripe (~13 drops, ~1.5k LOC) leans on one generic harness — add
  per-action tests incl. API-error paths for create_customer, cancel_subscription,
  create_refund, send_invoice.
- **[M]** GitHub drops: cover pagination edge cases, webhook signature
  verification, rate-limit header handling.
- **[L]** Excel: range parsing (reversed coords), `skip`, typed-coercion edges.

> Note: the safety contract (no panic/hang/Result-contract break) and the new
> examples-conform-to-schema contract already cover *all* built-ins; the gaps
> above are about *behavioural* correctness per drop.

### Resource handling & duplication
- **[M]** `io/http_upload.go` multipart goroutine may block in `mw.Close()` if
  the context is cancelled mid-write — derive a child context and ensure it can exit.
- **[L]** Extract duplicated helpers (`paramIntSlice`, `paramStringSlice`,
  `clampInt`, MIME-by-ext guessing) into `drops/internal/`.
- **[L]** `db/conns.go` reuses the `pgPoolKey` type inside the SQL-agnostic
  registry — rename to `dbConnKey` for clarity.

---

## 3. Web frontend — `web/`

### Component bloat (highest-leverage refactors)
- **[H]** `pages/FlowEditor.tsx` is ~4.2k lines with 60+ `useState`. Extract
  `RunManager` (run/breakpoint/step), `PublishPanel`, `ResourceResolver`,
  `ConnectionValidator`.
- **[H]** `components/SchemaForm.tsx` (+ nested fields) ~2.9k lines, props-drilling
  10+ context fields and re-rendering the whole form per keystroke. Introduce a
  `FormContext` and per-field-type components; map schema keys to them.
- **[M]** `Inspector.tsx` (14 props) and `TriggersModal.tsx` (~1.4k lines) mix
  concerns — split out `ApprovalPanel`/`LiveConsolePanel` and
  `ScheduleEditor`/`WebhookEditor`/`PollEditor` respectively; use context for
  workspace/auth.

### Tests, perf, a11y
- **[H]** ~10 test files for 100+ components, zero component/integration tests.
  Stand up Vitest + React Testing Library with a mocked `api`; cover SchemaForm,
  Inspector, and a FlowEditor palette→spawn→wire path. (The repo already has a
  Playwright-based `run-web-e2e` skill to build on.)
- **[M]** Hot-path re-renders: memoise `displayNodes` granularly and wrap
  `NodeCard` in `React.memo` — a one-field edit currently redraws every node.
- **[M]** API client (`api.ts`): callers discriminate errors by both `.code`
  and substring-matching `.message`. Add `isErrorCode(err, code)` and converge
  on `code` (pairs with the backend envelope cleanup in §1).
- **[M]** Copy/paste listeners capture `nodes`/`edges`/`paramsByID` in a stale
  closure registered once at mount — re-bind in an effect keyed on those deps.
- **[M]** Mobile/touch & a11y: add `role`/`aria-label` to nodes/edges, focus
  management on palette open/close, and verify pinch-zoom / bottom-sheet
  swipe-to-close on touch. (See also the standing "Delete flow on mobile" work.)
- **[L]** `JsonEditor.tsx` uses `dangerouslySetInnerHTML` (input is escaped, so
  low risk) — prefer React span tokens. `resourceNames` Map is never pruned
  (slow leak over long editing sessions). Replace ad-hoc `as {…}` casts with
  nominal param types.

---

## 4. Security, secrets & compliance

- **[M]** No audit event on **platform-admin escalation** via
  `HAZYFLOW_PLATFORM_ADMINS` — emit `platform_admin.granted` on first apply.
- **[M]** **Rate-limit the email-verification / resend routes** with the same
  limiter the other auth routes use (defense-in-depth against token brute-force).
- **[M]** **Egress allowlist is operator-global, not per-tenant** — blocks
  proper multi-tenant isolation. Introduce a `core.EgressPolicy` resolved
  per-tenant, falling back to the global list.
- **[M]** **Secret reads are not audited** (`encrypted_secrets.go` says "add
  when compliance asks"). Gate behind `HAZYFLOW_AUDIT_SECRET_READS` (default off).
- **[L]** `RewrapDEKs` assumes `rand.Read` never fails — treat a non-nil error
  as fatal and halt rotation (AES-GCM nonce reuse is catastrophic). Add a test
  for the error path.
- **[L]** `engine/redact.go` `minRedactableSecretLen = 6` can miss very short
  vendor tokens — document the gap in PRIVACY.md (the save-time linter already
  flags `secret_to_persistence`).
- **[L]** `COMPLIANCE.md` retention claim ("retain indefinitely") contradicts
  the actual 30/90/30-day defaults in `cmd/hzd/main.go` — correct it.

---

## 5. Build, CI & repo hygiene

- **[M]** `govulncheck` runs but there's no documented remediation SLA — add
  `SECURITY-SLA.md` (e.g. high/critical 7d, med 30d, low 90d) operators can cite.
- **[L]** No SBOM artifact — add a `syft . -o spdx` step to the build and
  attach it to releases (CycloneDX/SPDX) for supply-chain compliance.
- **[L]** Compiled binaries (`hzd`, `hzctl`, `transformer`) sit in the working
  tree; they're `.gitignore`d but confirm none are tracked (`git rm --cached`
  if so). CI rebuilds them anyway.

---

## 6. Documentation

- **[L]** No user-facing manual — end users get the UI but no reference for what
  each node does, building templates, or troubleshooting ("why did my flow
  pause?"). (A dedicated task — "Write a manual.md proposal" — covers this.)
- **[L]** PRIVACY.md / SECURITY.md are strong but could anchor feature claims to
  `file:line` for maintainability.

---

## Suggested order of attack

1. **Web component split + first real web tests** (`FlowEditor`, `SchemaForm`) —
   biggest risk reducer; everything else in `web/` gets safer to change after.
2. **Backend error-handling + concurrency sweep** (channel-close, lazy-map
   races, ignored errors, unified error envelope) — small, high-confidence diffs.
3. **Security hardening trio**: audit platform-admin grants, rate-limit email
   verification, per-tenant egress policy.
4. **Drop param-name standardisation + a lint** to keep it from regressing.
5. **Docs + CI**: user manual, SECURITY-SLA, SBOM.
