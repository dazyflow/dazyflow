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
- [x] **Subdomain UI** — orgs claim a custom web address (`klahr.dazyflow.app`).
      Completed the half-built feature end to end: a unique, validated
      `subdomain` slug on the org profile (Postgres column + partial unique
      index; `auth.ValidateSubdomain` mirrors the web's reserved/DNS-label
      rules); owner-only PUT + live availability check (409 on conflict);
      public label→tenant resolver wired into sign-in; Admin → Workspace UI
      with availability + URL preview + friendly errors; Caddy on-demand TLS
      (`*.apex` with an `ask` gate hitting `/auth/tls-allow` so only CLAIMED
      orgs get certs — no wildcard cert / DNS plugin); DEPLOY.md + dazyflow-infra
      README updated (one-time `*` A record is the only ops step). Tests across
      auth/daemon/web.

### Platform — found while verifying
- [ ] **`delete_flow` is unusable over the API/MCP** — `DELETE /me/flows/{id}`
      re-verifies the caller's account password (`daemon/me_routes.go`
      `deleteFlowMe`), so an API-key principal — which has no password — always
      gets 401 `bad_credentials`. The MCP `delete_flow` tool therefore cannot
      ever succeed, and its description promises the opposite ("Idempotent:
      deleting a missing flow is a no-op"). Decide: exempt key principals
      holding a `graph:delete` scope, or say so in the tool description and
      have it fail with a clear message. Found 2026-08-19 while verifying the
      round-2 MCP fixes.

### Web polish — blocked / deferred
- [ ] Breadcrumbs — DEFERRED: the IA is flat (sidebar + page title); low value.
- [ ] Editor undo/redo — DEFERRED: needs a real graph history stack
      (snapshot/coalesce + autosave interplay). Version snapshots already cover
      coarse rollback.

### Platform
- [ ] **Connector breadth** (ONGOING) — grow connector count + output-shape
      quality. The typed-port architecture is sound; this is perpetual content
      work, not a structural risk. (Output-shape contract tests are in place.)

---

## Done

### Web UI/UX polish
- [x] **Swedish search in the step palette** — the third finding of the Marina
      walkthrough: "schema", "e-post", "mejl" returned 0 hits because the drop
      catalog is authored in English on the Go side. Fixed by translating the
      QUERY instead of the catalog: `web/src/lib/dropSearch.ts` holds a
      Swedish→catalog alias table (~190 terms, vowel-folded, with a light
      inflection stripper and prefix expansion so "fakt"/"fakturor" both reach
      Fortnox's invoice drops), and `scoreDrop` — moved here out of
      QuickDropPalette so it is unit-testable — scores an alias hit at 0.7x the
      literal hit it mimics, so English ranking is untouched and aliases can
      only add results. Brand names are kept out of alias values unless every
      drop of that brand answers the Swedish word (nShift yes, Fortnox no),
      which is what stops one word flooding the list with a whole connector.
      Checked against the live 145-drop catalog: 73 Swedish queries, zero
      dead ones. Tests: `web/src/lib/dropSearch.test.ts` (37). Verified in the
      browser: "schema" → Schedule, "mejl" → Email/Gmail.
      Still English-only, deliberately: drop labels and descriptions
      themselves (the palette shows "Send email" under a Swedish query).
- [x] **Per-card flow actions (duplicate / rename / delete)** — delete (password-
      gated, run-locked, audited) and rename (Name is editable metadata; ID is
      the immutable handle) already shipped. Added flow **duplicate**:
      `POST /me/flows/{flow_id}/duplicate` → `Service.DuplicateGraph` copies the
      source under a fresh unique ID (so fresh trigger URLs + empty run history),
      starts it as a DISABLED draft owned by the duplicator (unpublished flows
      still fire at HEAD, so a copy must not auto-fire pre-review), and reuses
      SaveGraph's create-path guards (graph:edit, MaxFlows, validation, owner
      stamp). Wired a per-card Duplicate button that opens the copy in the editor.
- [x] **Flows overview cards** — equal-height layout, per-card operational
      signal (last-run dot + recent-runs sparkline), search / sort / status
      filter.
- [x] **Canvas minimap + auto-layout** — themed React Flow MiniMap; "Tidy"
      dependency-depth auto-arrange.
- [x] **Runs filtering** — text search, server-side per-flow filter, result
      count + load-more.
- [x] **Runs date-range filter** — was backend-blocked; added `since`/`until`
      params on `ListGraphRunsOpts` (inclusive lower / exclusive upper bound on
      enqueue time) through both job stores, parsed from `?since=&until=`
      (RFC3339 or YYYY-MM-DD) on `/me/runs` + `/me/flows/{id}/runs`, and a
      From/To date picker on the Runs page that resolves a picked day to
      local-midnight instants so it's server-side and paginates. OpenAPI for the
      `/me/runs` endpoint also realigned to the actual params (it had drifted to
      aspirational `from`/`to`/PageToken/PageSize/Sort). Tests: store conformance
      date-range cases + handler parse unit tests.
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
- [x] **Multi-node write dedupe** — shared, Postgres-backed `core.WriteDedupeStore`
      (`daemon.PgWriteDedupeStore`: keyed by job ID, first-writer-wins
      `ON CONFLICT DO NOTHING`, TTL + background sweep) so a lease reclaim by
      ANOTHER dzd replica sees the recorded write instead of re-firing it. Wired
      as the only dedupe store in `cmd/dzd` (fatal-on-error like every other
      Postgres store); `engine.NewMemoryWriteDedupe` stays the single-node/test
      impl. The residual non-atomic Get→write→Put window (two replicas that both
      miss before either records) is inherent to at-least-once without an upstream
      idempotency key and is deliberately accepted — a pre-write claim would risk
      at-most-once (dropping a message that never sent), the worse failure here.
      Tests in `daemon/pgstores_test.go` (miss, round-trip, first-writer-wins,
      stale TTL).
- [x] **Output-shape contract tests** — all built-ins checked for well-formed
      ports + no undeclared emitted ports. (`drops/output_contract_test.go`)
- [x] **Plain-English failure UX** — transport errors → human cause + action in
      the run viewer; transient 5xx headline; explicit auto-retry ("next
      attempt in Ns") vs terminal "needs you" distinction.
