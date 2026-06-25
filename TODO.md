# Dazyflow TODO

Two tracks: **Web UI/UX polish** and **Platform / iPaaS hardening**. Open work
is listed first; completed work is archived at the bottom (one line each — the
detail lives in the code and commit history).

---

## Open

### Web UX
- [x] **Friendly error sweep** — built `web/src/lib/explainApiError.ts` (the
      general-purpose companion to explainRunError): maps an APIError's
      status/code/message to one plain localized sentence and SWALLOWS leaked
      Go/OS strings into a generic message, while passing through genuine
      server validation hints. +12 tests. Routed EVERY API-error render site
      through it — auth (sign-in/sign-up/TOTP/org-switch), run detail + actions,
      Apps/Connections, Secrets + CredentialsManager, Files, SchemaForm,
      Inspector, all Admin pages, and the misc modals/pages. Signup decode-body
      friendlied at source. Deliberately left client-side parse/validation
      catches (JSON.parse, image-upload checks) on their own actionable
      messages. (Remaining is a manual walk-the-app QA pass — not a code task.)
- [ ] **Subdomain UI** — let a tenant set a subdomain (e.g. `klahr.dazyflow.app`).

### Web polish — blocked / deferred
- [ ] Per-card flow actions (duplicate / rename / delete) — BACKEND-BLOCKED: no
      flow delete/rename/duplicate API in client or daemon.
- [ ] Runs date-range filter — BACKEND-BLOCKED: runs API has no date params;
      a client-only filter over loaded rows would mislead.
- [ ] Breadcrumbs — DEFERRED: the IA is flat (sidebar + page title); low value.
- [ ] Editor undo/redo — DEFERRED: needs a real graph history stack
      (snapshot/coalesce + autosave interplay). Version snapshots already cover
      coarse rollback.

### Platform
- [ ] **Connector breadth** (ONGOING) — grow connector count + output-shape
      quality. The typed-port architecture is sound; this is perpetual content
      work, not a structural risk. (Output-shape contract tests are in place.)
- [ ] Multi-node write dedupe (follow-up to #9) — the engine dedupe store is
      in-memory / single-node; a cross-process reclaim can still double-fire.
      A shared (Postgres-backed) impl behind `core.WriteDedupeStore` closes it.

---

## Done

### Web UI/UX polish
- [x] **Flows overview cards** — equal-height layout, per-card operational
      signal (last-run dot + recent-runs sparkline), search / sort / status
      filter.
- [x] **Canvas minimap + auto-layout** — themed React Flow MiniMap; "Tidy"
      dependency-depth auto-arrange.
- [x] **Runs filtering** — text search, server-side per-flow filter, result
      count + load-more.
- [x] **Global ⌘K command bar** — jump to any page or flow (step palette keeps
      ⌘K inside the editor).
- [x] **Overview dashboard** (`/overview`) — stat cards, needs-attention list,
      recent activity, flow inventory; landing page for returning users.
- [x] **Header polish** — help button + "?" keyboard-shortcuts modal.

### Platform / iPaaS hardening
- [x] **Rate limiting & 429 handling** — per-(tenant, host) token-bucket +
      concurrency cap on every outbound call; honors 429/503 + Retry-After /
      RateLimit-* and feeds it into the engine retry backoff; rate-bounds
      fan-out. (`drops/net/ratelimit.go`, `core/retryhint.go`)
- [x] **Polling at scale** — first-fire jitter (anti-thundering-herd),
      conditional-request caching (ETag/If-Modified-Since, `cache_key`), and
      adaptive empty-streak backoff. (`daemon/scheduler.go`, `drops/net/httpcache.go`,
      `pollstate/`)
- [x] **Idempotency without an upstream key** — engine-side write dedupe
      (`Manifest.DedupeWrites` + `core.WriteDedupeStore`) for twilio/gmail/
      discord/sheets/homeassistant; at-least-once contract documented.
- [x] **Output-shape contract tests** — all built-ins checked for well-formed
      ports + no undeclared emitted ports. (`drops/output_contract_test.go`)
- [x] **Plain-English failure UX** — transport errors → human cause + action in
      the run viewer; transient 5xx headline; explicit auto-retry ("next
      attempt in Ns") vs terminal "needs you" distinction.
