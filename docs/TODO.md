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

### Web — UX review, non-technical user's lens (2026-08-19)
A read of the whole web UI (`web/src` — pages, components, `app.css`, both
locale bundles) against the question "what does someone who doesn't know what a
webhook is actually experience?". 17 findings; the two cheapest were fixed in
the same pass and are archived under Done. The rest are below, worst first.

The recurring theme: the *copy* is already written for a non-technical reader
(the `explain.*` failure table, the row-condition builder, cron hidden behind
"Custom", the complete Swedish catalog). What isn't is the **vocabulary**, the
**first-run path**, and the **editor chrome** — all structural, none fixable
string by string.

**Stops a new user**
- [ ] **"Drop" is invented jargon, and it's the most-used noun in the product**
      — "Add drop", "Search drops…", "Select a drop to edit", "{n} drops still
      need required values", "No drop records for this run". Nothing in the UI
      defines it, so every user has to reverse-engineer that a drop is a step.
      Worse in Swedish, where it was left untranslated: *droppar* means drips of
      liquid, so "Sök droppar" reads as "Search drips". Fix: call it a **step**
      in the UI (sv: *steg*) and keep `drop` in the Go catalog, the API and the
      MCP tools, where it's a stable contract that non-human consumers are
      grounded on. `web/src/lib/dropText.ts` is already the vocabulary hop that
      makes this a UI-only change. Highest-leverage item on this list.
- [ ] **Four different words for "is this switched on?"** — a user has to hold
      *Save* (draft persisted), *Publish / Go live / Publish changes* (draft
      promoted), *Enabled / Paused* (a separate toggle), a four-state status
      chip (Live / Manual only / Not live yet / Paused), and *Make live* in
      version history — five verbs on one axis. The rules also differ by
      trigger: scheduled and polling triggers only fire when published, but
      webhooks fire either way, while `editor.confirmPauseBody` tells the user
      pausing stops "cron, poll, webhook, and forms". So the model is both large
      and inconsistently described. Fix: collapse to **"Save changes"** plus one
      **On/Off** switch, with publish becoming what Save does while the flow is
      On; keep history/rollback for people who go looking. If the two-stage
      model has to stay, put one sentence on the flow header that states the
      whole truth ("Saved. Your live version is from 3 days ago.").
      (`web/src/flowStatus.ts`, `editor.publish*`, `flowStatus.*`)
- [ ] **First-run points a beginner at an empty canvas** — Welcome's primary
      button says "Create your first flow" and hard-codes `?tab=blank`, the
      hardest of the three options. Templates have no nav entry anywhere and sit
      in the *third* tab of the create page, behind an AI tab that is the
      default and needs a connected Claude/OpenAI account to do anything. And
      the ideal first experience already exists but is unreachable: the
      `try-it-now` template ("no accounts, no setup, nothing to connect"), whose
      Welcome copy is still in `en.json` with no component rendering it — along
      with the three "what do you want to do?" goal cards. Fix: Welcome's
      primary action opens the no-setup starter and runs it; blank canvas
      demotes to a text link; tab order becomes Template · AI · Blank, and the
      AI tab stops being the default when no provider is connected. Dead keys to
      revive or delete: `welcome.featuredTitle`, `welcome.goalNotifyTitle`,
      `welcome.goalReportTitle`, `welcome.goalDataTitle`, `welcome.goalDevLink`,
      `welcome.signedInAs`, `welcome.inTenant`, `nav.templates`.
      (`web/src/pages/Welcome.tsx`, `pages/CreateFlow.tsx`)
- [ ] **The editor toolbar scrolls sideways with the scrollbar switched off** —
      ~20 controls in one bar (Add drop, Note, Tidy, 6 align, 2 distribute,
      Group, Breakpoint, Save, History, Publish, Changes, config counter, Test
      event, Run, Pause, Inspect) under `overflow-x: auto` with
      `scrollbar-width: none` and the webkit scrollbar hidden. On a laptop with
      the inspector open, **Run and Publish move off the right edge with no
      visual indication anything is there**. Below 960px of canvas width every
      label disappears; below 640px even the primary button's does — leaving a
      row of unlabelled 36px glyphs whose only explanation is a `title` tooltip,
      which doesn't exist on touch at all. Fix: pin Save/Run/Publish outside the
      scrolling region; move align/distribute/group/tidy/breakpoint into an
      overflow "⋯" menu (power-user tools don't deserve permanent width); keep
      labels on primary actions at every width; pair every `title` with an
      `aria-label`. (`web/src/app.css:8980-9002` and `:9380-9408`,
      `pages/FlowEditor.tsx:3822+`)

**Slows everyone down**
- [ ] **The "?" button opens keyboard shortcuts, and nothing links to the docs**
      — a stuck user reaches for the universally understood help affordance and
      gets a shortcut table. There is a full docs site (`docs.dazyflow.app`,
      built by `make docs-site`) that the application links to **zero** times;
      the only route left for a confused person is filing a support ticket. Fix:
      "?" becomes a menu — Documentation · Keyboard shortcuts · Contact support
      — plus contextual doc links from the empty canvas, the connect-an-app
      gate, and the run-failure panel. (`web/src/components/AppShell.tsx:458`)
- [ ] **English users see raw category enums; Swedish users don't** — every step
      card carries a category chip. Swedish has a translation map so it reads
      *Nätverk · In/ut · Omvandling · Logik*; English has none and falls through
      to the raw catalog value, so an English user sees lowercase **network ·
      io · transformation · system**. "io" is not a word. Fix: add an English
      map (Apps & services · Files & data · Change data · Logic · Triggers ·
      System) and reuse those names as the palette's section headings so the two
      vocabularies agree. (`web/src/lib/dropText.ts:419` and `:575`)
- [ ] **Code shows up on the paths advertised as "no setup"** — the starter
      template billed as "no accounts, no setup, nothing to connect" has two
      steps, and the only editable field in the second is
      `'• ' + row.region + ': ' + string(row.orders) + ' orders'`. A curious
      beginner who clicks the step to see how it works meets a typed expression
      language. Elsewhere: the formula placeholder is `input.filter(x, x.paid)`,
      the help text links out to "CEL (Common Expression Language)", the
      inspector offers "Params (JSON)", and the webhook test panel says "Edit
      the JSON payload below, then fire it through this flow's webhook trigger."
      Fix: you solved this once already — the row-condition builder is a
      plain-language front end over the same engine with "Advanced" as the
      escape hatch. Give text templating the same treatment (a field-picker
      sentence builder) and move the JSON params editor behind "Advanced".
      (`web/public/templates/try-it-now.json`, `celInput.placeholder`,
      `schemaForm.celHint`, `inspector.paramsJson`, `editor.testRunHelp`)
- [ ] **Permission scopes are shown to the person who can't act on them** — a
      viewer-role user sees the badge "Read-only — missing graph:edit" and a
      disabled Run reading "Missing graph:run". These are internal scope
      identifiers; they tell the user nothing usable and imply breakage. Fix:
      "You can view this flow but not change it — ask an admin for edit access."
      The rest of the app's error copy already writes at this level; these two
      strings just never got the sweep. (`editor.readOnly`,
      `editor.missingRunPerm`)
- [ ] **The empty Flows page promises two paths and renders one button** — the
      copy reads "Start from a ready-made template, or create a blank flow and
      build it yourself", then shows a single generic *Create a flow* button
      that lands on the AI tab, which is neither. The two matching buttons
      (`flowList.emptyTemplateCta`, `flowList.emptyBlankCta`) are still in the
      copy file, unused. Fix: render the two buttons the copy promises, or
      rewrite the copy — and sweep the other dead onboarding keys while in
      there. (`web/src/pages/FlowList.tsx:311-325`)
- [ ] **The same concept has two names on the same screen** — the toolbar button
      says "Pause point" while the card badge, the context menu and the clear
      action all say "breakpoint" (someone softened one string and stopped); the
      nav says **Collections** while the URL is `/results`; and "graph" leaks
      into user-facing copy ("Replay — Re-run this graph from scratch", and the
      gallery's "Pre-built workflows you can **fork** in one click. Each one
      lands in your workspace as a normal **graph**."). Fix: one word per
      concept, then grep for the others — *pause point* (never breakpoint),
      *flow* (never graph), *copy* (never fork).

**Polish**
- [ ] **No show-password toggle on sign-up or sign-in** — sign-up asks for 8+
      characters and then a confirmation, both fully masked; the most common
      cause of a failed first sign-up is a typo the user can't see. Autocomplete
      attributes are otherwise correct throughout, so this is the one gap. Fix:
      an eye toggle, and consider dropping the confirm field once the password
      is visible. (`web/src/pages/SignUp.tsx`, `pages/SignIn.tsx`)
- [ ] **`--faint` falls below readable contrast on raised surfaces** — the token
      (`#706b8e`, used 75×) measures 4.0:1 on the page background, but **2.96:1
      on `--surface-2`** and **2.35:1 on `--surface-3`**, well under the 4.5:1
      body-text threshold. Cards, popovers and inspector panels are exactly
      where hint text lives. Fix: lighten to ~`#8c86a8`, or restrict `--faint`
      to decorative use and move hint text to `--muted` (5.5:1 on surface-2).
      (`web/src/theme.css`)
- [ ] **Buttons have no designed focus ring** — inputs get an accent border plus
      a 3px accent halo; buttons get nothing, leaving keyboard users with the
      browser default ring over a violet ground. Given how carefully the rest of
      the button system is specified this reads as an oversight. Fix: one
      `button:focus-visible` rule reusing the input halo. (`web/src/theme.css`)
- [ ] **Editor touch targets are 34-36px** — toolbar buttons are 34px tall and
      collapse to 36px squares, under the ~44px commonly recommended for touch;
      on a phone the editor is a row of small unlabelled icons over a pan/zoom
      canvas. Fix: raise editor controls to 40-44px under
      `@media (pointer: coarse)`, and consider making the editor read-only on
      phones with a clear "open on a computer to edit" message rather than
      shipping a cramped version of it. Viewing runs and approving on a phone is
      plausible; wiring a flow there probably isn't. (`web/src/app.css:8980+`)
- [ ] **The run search asks for something nobody knows** — the Runs placeholder
      is "Search by run ID or flow…". No non-technical user has a run ID; the
      flow name and the date filters are what they'll use, and leading with the
      ID makes the field look like it isn't for them. Fix: "Search by flow
      name…" — ID matching keeps working silently. (`runList.searchPlaceholder`)

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

### Support — review findings, all fixed (2026-08-19)
Kept here as the record of what the independent review of `5e754ae` found and
what each fix was; the detail lives in the code and in the tests named below.
(The review also claimed findings 3 and 4 sit in code CI never exercises — that
was WRONG: `.builds/archlinux.yml` stands up Postgres + MariaDB and exports
`DAZYFLOW_TEST_DB`, so the gated tests do run. What CI genuinely never ran was
the whole frontend vitest suite — fixed in the same pass, see the CI note below.)
- [x] **Bundle retention orphaned a live ticket's diagnostic link** (medium) —
      the two prunes key on different timestamps (a bundle on `created_at`, a
      ticket on `updated_at`), and the bundle prune only spared bundles whose
      ticket was still OPEN. A ticket filed 13 months ago, conversed on for a
      year and resolved last week therefore kept its row while its bundle was
      swept, and "View diagnostic" 404'd for customer and agent alike. Now a
      bundle is kept while ANY ticket references it, whatever its status: the
      ticket's own retention decides when the pair goes, and since the sweep
      prunes tickets first the freed bundle is collected in the same pass.
      Regression test in `daemon/support_prod_test.go` — verified it fails
      against the old predicate.
- [x] **Per-user erasure left support PII** (medium) — `eraseUserIdentity`
      never touched the support stores, so an erased member of a surviving org
      kept their address in `support_tickets.created_by` / `assigned_to`,
      `support_ticket_messages.author` and the four identity columns of
      `access_grants` — with their own message bodies — while the erase report
      read as complete. Fixed by pseudonymising, not deleting, exactly as the
      audit trail already does (`actor → '[erased]'`, detail cleared): a support
      thread is the ORG's record of a problem with the org's flows, so one
      member leaving must not erase it for everyone else, but it must not carry
      their identity or their words either. New `AnonymizeSubject` on the ticket
      and grant stores, Postgres AND in-memory (a self-hosted single-node
      deployment must erase just as thoroughly), scrubbing both the email and
      the subject since the two paths write different ones. Tests:
      `daemon/gdpr_test.go` (whole path, in-memory) +
      `daemon/support_prod_test.go` (the SQL), both asserting the agent's side
      of the thread survives untouched.
- [x] **Summary cache lost a write-invalidation** (low) — a scan already in
      flight stored its pre-write result after `invalidateSummary` had cleared
      the flag, so an agent's own claim could stay invisible for the full 5s TTL
      — the exact opposite of what the cache's comment promises. Fixed with a
      generation counter captured before the scan and compared before the store.
- [x] **`summaryInFlgt` was not reset via `defer`** (low) — a panic in the
      query path (recovered by the HTTP middleware, so the process survives)
      left it set, after which every caller took the "someone else is scanning"
      branch and got frozen counts for the life of the process. Now deferred.
      Both cache fixes are covered by `daemon/ticket_summary_cache_test.go`,
      which drives the interleavings through an injectable `summaryCompute` —
      no Postgres needed, so they run in the ordinary `go test`. Verified both
      tests fail against the old code.
- [x] **Repeated "resolved" mail on a no-op status change** (low) —
      `setSupportTicketStatus` lacked the same-status guard its customer-side
      twin has, so a double-clicked button re-narrated the change, re-bumped
      `updated_at` (re-sorting the queue) and re-emailed the customer. Guard
      added; asserted in `daemon/ticket_routes_test.go`.
- [x] **Duplicated error icon after the ErrorNotice migration** (low) —
      `ErrorNotice` renders its own `AlertCircle`, but a hand-pasted one
      remained in `AdminOAuthProviders` and a `Lock` in `SupportAgentHome`, so
      those banners showed two icons and the nested one escaped the
      `.card.error > svg` rule. Both removed (a swept repo-wide check found no
      others), unused imports dropped.
- [x] **Literal NUL byte made a source file binary to git** (low,
      pre-existing) — `EmailTemplates.tsx`'s `NEW_DRAFT = "\x00new"` held a raw
      0x00, so git treated the file as binary: diffs invisible without `--text`,
      and any concurrent edit an unresolvable binary conflict. Now `"\u0000new"`
      — identical to the compiler, and the file is text again (this one commit
      still shows as `Bin`, because HEAD's side of the diff is the binary one).

### CI
- [x] **The frontend test suite now runs in CI** — the `web` task was
      `npm ci && npm run build`. `build` is `tsc -b && vite build`, so type
      errors failed the build, but `npm test` was never invoked: all 340 vitest
      tests were dead weight in CI, including the drift guards that make the
      Swedish catalog vocabulary safe to maintain (edit an English string in
      `integrationMeta.ts` without retranslating and a test is supposed to fail
      — it can only do that if CI runs it). One line in
      `.builds/archlinux.yml`.

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

### Web UX — review fixes (2026-08-19)
- [x] **Dead CSS swept out of `app.css`** — 11,863 → 10,805 lines; the built
      bundle drops 242.18 → 222.38 kB (gzip 43.24 → 40.65). Audited all 1,223
      class selectors against every `.ts`/`.tsx`/`.html` source, then removed
      **173 rules covering 129 classes** whose components no longer exist:
      the AI chat panel (`.chat-*`), the old provider grid (`.provider-*`), the
      pre-`/apps` catalog (`.catalog-*`), the port inspector (`.port-*`), the
      Welcome featured/goal cards (`.welcome-featured-*`, `.welcome-goal-*` —
      the same removal that stranded the copy keys noted above), plus
      `.pipeline-log-*`, `.mk-*`, `.trigger-row/-chip/-head/-list`,
      `.connections-first-tile*`, `.sf-credential-chip*`, `.signin-tabs`,
      `.admin-advanced-*` and friends. Four selector LISTS were trimmed rather
      than dropped, keeping their live halves (`.credentials-empty`,
      `.dz-node .icon.branded`, `.config-checklist-item .icon.brand-logo`).
      Deliberately KEPT the 46 classes with no literal match that are built at
      runtime or owned by a vendor: `flow-status-${status}`, `run-dot-${status}`,
      `" status-" + node.status`, `tv-*`/`dash-stat-*`/`callout-${variant}`,
      the `cel-${token}` names emitted by `lib/celHighlight.ts`, and the
      React Flow / Leaflet / xterm DOM hooks. Also merged 6 selectors that were
      declared twice in the same scope (`.app-shell`, `.topbar .user`,
      `.sidebar[data-collapsed] a`, `.editor .canvas > .react-flow`,
      `.create-draft-issue`) and renamed the two keyframes left orphaned by the
      chat panel (`chat-spin`/`chat-pulse` → `dz-spin`/`dz-pulse`), which the
      toolbar save spinner and `.dot.pulse` still drive.

      NOT done, on purpose: 55 rules share an identical declaration body with
      at least one other rule (`display:flex; flex-direction:column; gap:…`
      appears 11×). Merging them into shared selector lists would break the
      file's component-locality and its per-rule commenting for no real payoff —
      gzip already collapses repeated byte runs, and the 5 remaining same-scope
      duplicate selectors are the deliberate "all entrance animations next to
      their @keyframes" block. The stylesheet is otherwise clean: zero rules
      declare a property twice, and only 12 `!important` in 10.8k lines.
- [x] **Type scale raised one step and moved to `rem`** — the app read at IDE
      density: counting token uses in `app.css`, 12px appeared 154×, 13px 102×,
      11px 101× and 10px 20×, against only 30 uses of 14px. And because the
      scale was declared in `px`, a reader who raised their browser's default
      font size got no change at all. Shifted the whole ramp up one step
      (10/11/12/13/14 → 11/12/13/14/15, and 18→20, 28→30) and redeclared it in
      `rem` with px equivalents in comments; layout tokens stay px on purpose.
      Also routed the last three raw `px` font-sizes in `app.css` through the
      scale (`.token-chip-x`, `.cel-textarea`, `.ai-step-dot`), leaving
      `.geo-pin` as the one documented exception — it sizes a map-marker glyph
      to its anchor box, not text. (`web/src/theme.css`, `web/src/app.css`)
- [x] **Theme defaults to the system setting** — `getTheme()` returned `"dark"`
      for anyone who hadn't explicitly chosen and `prefers-color-scheme` was
      never consulted, so a user whose whole OS is in light mode was handed a
      near-black violet app on first sign-in. Split the resolver into two
      layers: `ThemeMode` (`"system" | "dark" | "light"`, the user's CHOICE,
      default system) and `ResolvedTheme` (`"dark" | "light"`, what lands on
      `<html data-theme>`), so the stylesheet still keys off a concrete value
      and needs no `prefers-color-scheme` rules of its own. `initTheme` now also
      watches the media query, so a system user repaints when the OS flips at
      dusk; an explicit choice ignores it. Added a **System** option (first, and
      the default) to Settings with a split dark/light swatch, en + sv copy, and
      widened the server validator so `"system"` round-trips like any other
      value — `""` stays valid as what pre-picker accounts hold. +9 web tests
      covering the fallback matrix (stale `""`, absent `matchMedia`, mid-session
      OS flip), +1 daemon round-trip assertion. (`web/src/theme.ts`,
      `web/src/theme.test.ts`, `pages/Settings.tsx`, `auth.tsx`, `api.ts`,
      `i18n/*.json`, `daemon/preferences.go`)


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
