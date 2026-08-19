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

### Web — found while translating (2026-08-19)
- [ ] **`technical_notes` and `blurb` render nowhere** — `IntegrationMeta`
      documents a collapsible "Technical details" disclosure and
      `OAuthProviderMeta.blurb` a one-line pitch in the Connections panel, but
      no component reads either field: ~11k characters of maintained developer
      prose (auth schemes, env vars, API versions) and five provider blurbs are
      dead data. Either wire the disclosure on `/apps/:slug` and the blurb into
      the Connections panel, or delete the fields and their prose. The Swedish
      translations exist and are fingerprinted either way, so wiring it later is
      a render-only change. (`web/src/integrationMeta.ts`, `web/src/pages/Apps.tsx`)
- [ ] **Connection-field `help` is dropped on the floor** — the daemon sends
      `help` on each connection field (e.g. "usually your email address",
      "Create one in HA → Profile → Long-Lived Access Tokens") but the web
      `ConnectionField` type doesn't declare it and the connect form doesn't
      render it, so the guidance a user needs mid-setup only exists over the
      MCP. Add `help?: string` to the type and render it under the input.
      (`web/src/types.ts:195`, `web/src/pages/Apps.tsx`)

### Support — open review findings (2026-08-19)
An independent review of `5e754ae` ("Complete support") turned up seven items.
None are shipped-and-broken today; findings 1, 3 and 4 sit in Postgres-backed
code CI does not exercise (`DAZYFLOW_TEST_DB` gates those tests).
- [ ] **Bundle retention can orphan a live ticket's diagnostic link** (medium) —
      `PgBundleStore.Prune` keys on the bundle's `created_at` while
      `PgTicketStore.Prune` keys on the ticket's `updated_at`, so a long-running
      ticket resolved last week loses a 13-month-old bundle while the ticket
      survives. `bundle_id` stays set → "View diagnostic" 404s. Gate the bundle
      on the referencing ticket's `updated_at`, or only prune bundles whose
      ticket row is gone. (`daemon/support_bundle.go:250`)
- [ ] **Per-user erasure leaves support PII** (medium) — only `deleteOrgData`
      got the ticket/bundle/grant steps; `eraseUserIdentity` doesn't, so an
      erased member of a surviving org keeps their email in
      `support_tickets.created_by`, `support_ticket_messages.author` and
      `access_grants.agent` — with no `rep.warnf` either, so the erase report
      reads as complete. (`daemon/gdpr.go:206`)
- [ ] **Summary cache can lose a write-invalidation** (low) — a scan already in
      flight writes its pre-write result back after `invalidateSummary` clears
      the flag, so an agent's own claim can stay invisible for the full 5s TTL
      — the exact property the comment promises. Capture a generation counter
      before the scan and compare before storing. (`daemon/ticket_store.go:344`)
- [ ] **`summaryInFlgt` is not reset via `defer`** (low) — a panic in the query
      path (recovered by the HTTP middleware) leaves it `true` forever and every
      later caller gets frozen counts for the life of the process.
      (`daemon/ticket_store.go:361`)
- [ ] **Repeated "resolved" mail on a no-op status change** (low) —
      `setSupportTicketStatus` lacks the same-status guard its customer-side
      twin `setMyTicketStatus` has, so a double-click re-notifies, re-appends
      the system note, and re-emails. (`daemon/ticket_routes.go:419`)
- [ ] **Duplicated error icon after the ErrorNotice migration** (low) —
      `ErrorNotice` renders its own `AlertCircle` but the hand-pasted one was
      left in place, so the banner shows two; `SupportAgentHome.tsx:73` shows
      `AlertCircle` + `Lock`. The nested icon also escapes the `.card.error >
      svg` rule. (`web/src/pages/AdminOAuthProviders.tsx:69`)
- [ ] **Literal NUL byte makes a source file binary to git** (low,
      pre-existing) — `const NEW_DRAFT = "\x00new"` puts a raw 0x00 at byte
      1092, so git treats `EmailTemplates.tsx` as binary: its diffs are
      invisible without `--text` and any concurrent edit is an unresolvable
      binary conflict. Use `"\u0000new"` or a non-NUL sentinel.
      (`web/src/pages/EmailTemplates.tsx:23`)

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
- [x] **Translated drops (sv)** — the catalog stays English on the Go side (it
      is what the API, the MCP tools and the AI generator are grounded on) and
      the human UI translates at render time through
      `web/src/lib/dropText.ts`: 48 generic labels (brands untouched), 89
      action subtitles, 196 port labels, and the category chips. Keys are the
      English catalog strings themselves, so a label changing on the Go side
      misses and falls back to the new English rather than showing a stale
      translation. Wired into the step palette (rows, ranking, sort), node
      cards (title, subtitle, pin names, tooltips), the Inspector header,
      Apps drop cards, the run timeline, the AI-draft flow summary, the support
      read-only view and the platform-admin catalog. Search matches the
      localized text as well as the English (`scoreDrop`'s `localized` arg), so
      "vänta" finds Wait for approval by its Swedish name and "email" still
      finds it by its English one. `dropLabelIsDefault` lets a language switch
      rename untouched node cards while leaving a hand-typed name alone.
      Port-type words ("Items (a table)") are our copy, not the catalog's, so
      they went into i18n as `portType.*`. Tests: `dropText.test.ts` (21) +
      localized-search cases in `dropSearch.test.ts`. Verified live in a
      Swedish UI: palette, canvas card ("E-post · Skicka e-post", pins
      "Till/Ämne/Innehåll/Bilagor"), pin tooltips, Apps card.
      **Descriptions too** (`web/src/lib/dropDescriptions.sv.ts`): all 145
      paragraphs, ~53k characters, keyed by drop ID rather than by their English
      text — using a paragraph as a key would be unreadable and
      whitespace-fragile. Each entry records `descriptionFingerprint` (32-bit
      FNV-1a over code points) of the English it was translated from, so a
      paragraph reworded on the Go side stops matching and the reader gets the
      new English instead of a translation of behaviour that no longer exists.
      A test pins the fingerprint pair, so if the generator's hash and the
      runtime's ever diverged (which would silently drop every description back
      to English) it fails loudly. Prose is translated; identifiers, param keys
      and API values (`row`, 'mode', whsec_…, ORDER_OPEN) stay English because
      that is what the user types and the service returns.
      **The params-schema surface too** (`web/src/lib/dropFields.sv.ts`): 227
      field labels, 384 help strings, 207 dropdown options (the ISO currency
      list included), 35 connection-field labels/help and the keeps-state chip
      — natural-keyed, since these are all sentences or shorter, so the key IS
      the fingerprint. Wired into SchemaForm (labels, help tooltips, every
      enumNames read, oneOf branch titles, aria-labels), the Apps connection
      forms and the node card's state chip.
      **And the Apps prose** (`web/src/lib/integrationProse.sv.ts`): 33
      integration descriptions, 29 technical notes and 5 OAuth provider blurbs,
      keyed by slug with the same fingerprint guard as the drop descriptions —
      which matters more here, because this English lives in the frontend and
      changes without a catalog rebuild. A test walks `integrationMeta.ts` and
      fails when its English is edited without retranslating, so the drift is
      caught in CI rather than shipped as a stale paragraph.
      Deliberately left English (verified by test): strings identical in Swedish
      ("Status", "SQL", "Metadata"), product and model names ("GPT-4o"), and
      example values / placeholders (`smtp.example.com`, `sk-ant-…`,
      `eu-playground`) — those are things to type, not read.
      Cost: the vocabulary is bundled, not fetched, so the main JS chunk grew
      1,824 → 2,013 kB (gzip 524 → 590 kB) and an English-only reader downloads
      the Swedish too. Acceptable at one locale; splitting the vocabulary into a
      dynamic import keyed by language is the fix when a second one lands.
      Descriptions are not searched in Swedish — description hits are the
      weakest ranking tier and the alias table already covers that vocabulary.
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
- [x] **`delete_flow` works over the API/MCP** — `deleteFlowMe` demanded the
      caller's account password, which an API-key principal has none of, so
      every key-authenticated DELETE answered 401 and the MCP `delete_flow`
      tool could never succeed. The gate now splits by credential KIND
      (`auth.IsAPIKeyCredential`): a session still re-supplies its password (it
      is ambient and hijackable), while a key must carry `graph:admin` —
      which the deliberately narrow `claude-mcp` default role (graph:run +
      graph:edit) does NOT, so an agent's key still cannot destroy a flow's
      history. Missing scope answers 403 `admin_scope_required` naming the way
      out, and the MCP tool description says so up front instead of letting the
      model discover it by failing. The audit line records `via=session` /
      `via=api_key`. Tests: `daemon/deleteflow_test.go` (both credential paths,
      incl. a key that supplies a password anyway). Verified live through the
      MCP: delete succeeds with the dev key, 403 with a freshly minted
      claude-mcp key.
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
