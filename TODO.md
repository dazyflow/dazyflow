# Dazyflow TODO

The single backlog. **Open** is what's left, grouped by what unblocks it —
read it top to bottom, it's roughly in priority order. **Done** is the archive,
grouped by area: entries stay because the *reasoning* is often the thing worth
keeping, not the fact that a box got ticked.

Convention: a `- [ ]` says what a user experiences, then what to do about it.
If an item is deferred or blocked, the reason is on the line — a backlog that
doesn't say why something is stalled turns into a graveyard.

---

## Open

### Product decisions — blocked on a call, not on work

These aren't defects. Each needs a direction before any code moves.

- [ ] **HELD — "Drop" is invented jargon, and it's the most-used noun in the
      product.** "Add drop", "Search drops…", "Select a drop to edit", "No drop
      records for this run". Nothing in the UI defines it, so every user has to
      reverse-engineer that a drop is a step. Worse in Swedish, where it was
      left untranslated: *droppar* means drips of liquid, so "Sök droppar"
      reads as "Search drips". The mechanical change is small — rename in the
      UI vocabulary only (`web/src/lib/dropText.ts` is already the hop that
      makes this UI-only) and keep `drop` in the Go catalog, the API and the
      MCP tools where it's a stable contract that non-human consumers are
      grounded on. What needs deciding is whether "drop" is load-bearing brand
      vocabulary worth teaching, or an accident worth dropping. Suggested
      replacement: **step** (sv: *steg*).

### Templates — thin, and now the front door

The create page defaults to the template gallery (confirmed 2026-08-19), so the
gallery is the first thing a new user sees. It currently holds **5 templates
touching 3 of 34 integrations** (Gmail, Sheets, Slack). Three of the five are
under "Spreadsheets"; one needs no setup; the other four all require a Google
or Slack account, so a self-hoster with neither sees a wall of things they
can't use.

The gap that matters most: **the Nordic connectors are the stated moat and have
zero templates.** Fortnox, Klarna, nShift, Roaring and 46elks are the reason to
pick Dazyflow over Zapier/Make/n8n, and nothing in the gallery shows them off.

- [ ] **Templates for the Nordic connectors** — at minimum a Fortnox one
      ("New paid invoice → Slack", which the Fortnox notes already describe as
      Schedule → List invoices (filter=fullypaid) → For each → dedupe on
      DocumentNumber) and a 46elks one ("Text me when X"). These are the demos
      that justify the go-to-market.
- [ ] **A second no-setup template** — `try-it-now` is the only one that runs
      with nothing connected, and it is now the Welcome CTA's target. One more
      (a weather or HTTP-fetch → render, both need no account) would give the
      "Try it now" category something to be a category of.
- [ ] **Notification breadth beyond Slack** — Discord, ntfy, Twilio and plain
      email all ship as drops and none appear in a template, even though
      "get notified when X" is the archetypal first automation.

### Web

- [ ] **Node-level `disabled` on a trigger doesn't stop it firing** — the
      /trigger endpoint, the hosted form and the provider-event fan-outs only
      check the whole-flow switch, so disabling just the trigger NODE still
      accepts inbound requests and starts a run; the worker then records that
      node as skipped, producing a run that does nothing. Either honour it at
      the endpoints (and revisit `MigrateWebhookPublish`, which currently
      preserves the permissive behaviour on purpose) or drop the per-node
      toggle from trigger nodes so the UI stops implying it works.

- [ ] **Verify the `configPath` keys stay reachable** — the six
      `connectMcp.clients.*.configPath.{macos,windows,linux}` keys resolve
      through a VARIABLE key (`` t(`${active.configPathKey}.${os}`) ``), so no
      literal reference exists anywhere in the source and every static audit
      flags them as dead. They were nearly deleted in the i18n sweep. Either
      leave this note in place, or make the lookup literal so the keys are
      greppable. (`web/src/components/ConnectMcpClientModal.tsx`)
- [ ] **Stale CSS token aliases** — ~16 custom properties (`--fg`,
      `--text-muted`, `--r-md`, `--shadow-2`, `--space-2h`, …) are referenced
      only through their `var(…, fallback)` default; nothing defines them. They
      render correctly, so this is tidying rather than a bug — but the same
      pattern without a fallback is what made `var(--radius)` silently render
      square corners in 12 places. Point them at real tokens. (`web/src/app.css`)

### Connectors — Sweden first, then the Nordics

The catalog is ~130 drops across 38 integrations, which is on par with n8n's
core node set, so **breadth is demand-led, not a structural gap**. Every
standard primitive is shipped (date, expression, regex, csv/xml, base64/hash,
phone, rss — see Done). The moat is the local services Zapier / Make / n8n
don't build, so rank by Nordic demand rather than generic popularity.

Cost rule that has held so far: prefer **static-key** connectors — no daemon
change, 46elks was about half a day — and pay the OAuth/token tax only where
the market needs it (Fortnox did; Signicat will). The static-key momentum picks
are now spent, so the next Nordic step costs more than the last few did.

- [ ] **Signicat / BankID** — eID auth & signing. OAuth + session polling, so
      this is the one that pays the token tax. Highest Nordic value.
- [ ] **Visma** — all-Nordics accounting; broadens beyond Fortnox/SE.
- [ ] **Telegram** — cheap, popular, fits the European / self-hosted lean.
      Ranks above M365 and the CRMs despite being smaller.
- [ ] **Microsoft 365** (Teams / Outlook / OneDrive) — the biggest single gap
      for business users, and the biggest build.
- [ ] **PM trackers** (Jira / Linear / Asana / Trello / Airtable).
- [ ] Also unranked and unclaimed: cloud storage (S3 / Dropbox / Box), CRM
      (HubSpot / Salesforce), support (Zendesk); and two small primitives —
      `uuid`/`random`, and an explicit `filter` (`route_rows` mostly covers it).

### Deferred — decided, not forgotten

- [ ] **Breadcrumbs** — the IA is flat (sidebar + page title); low value.
- [ ] **Editor undo/redo** — needs a real graph history stack (snapshot +
      coalesce, and an interplay with autosave). Version snapshots already
      cover coarse rollback, so this buys less than it costs today.

---

## Corrections to the record

Findings raised by a review and then disproved. Kept so they don't get
re-raised.

- **The Inspector does not offer "Params (JSON)" as a default surface.** There
  is no UI to switch into JSON mode — `setMode` only picks it when
  `supportsSchemaForm(schema)` is false, i.e. as a fallback for schemas the
  form renderer can't handle. Correct behaviour already.
- **Text templating already has the plain-language builder.**
  `RenderTextPreview` offers Table / Bulleted list / Comma-separated presets
  built from the step's real columns with a live preview, and
  `RenderTemplatePreview` / `RenderTableColumns` do the same for their modules.
  The starter template's hand-written CEL even round-trips through
  `detectPreset` and shows "Bulleted list" as selected; the raw formula field
  sits below the builder as the escape hatch.
- **The editor toolbar was never the 20 permanent controls a review claimed.**
  Align/distribute render only at 2+ selected and the pause point at exactly 1,
  so no overflow menu was needed — only the pinning fix (see Done).
- **CI does run the Postgres/MariaDB-gated tests.** A review claimed findings
  sat in code CI never exercises; `.builds/archlinux.yml` stands up both and
  exports `DAZYFLOW_TEST_DB`. What CI genuinely never ran was the frontend
  vitest suite — fixed, see Done → CI.

---

## Done

Archive, newest area first. The detail stays because the reasoning is the
reusable part.

### Publish semantics unified (2026-08-19)
- [x] **"Published" now means one thing on every path** — the scheduler
      required a published commit; the webhook, hosted-form and provider-event
      paths called `LoadPublishedOrHead` and fell back to HEAD, so the SAME
      flow was live-when-unpublished with a webhook and dark with a schedule.
      Replaced with `Store.LoadPublished`, which returns `ErrNotPublished` and
      fires nothing; all four call sites converted. `UnpublishFlow` now really
      does take a flow offline (it previously left webhook flows running).
- [x] **Upgrade migration so live webhooks don't go dark**
      (`daemon/MigrateWebhookPublish`, wired before the scheduler starts) —
      publishes HEAD once for every flow with a reachable webhook or a
      provider-event trigger and no published commit: exactly the revision it
      was already running, so the upgrade is a no-op from the flow's side.
      Deliberately does NOT touch unpublished cron/poll drafts (never live —
      publishing would START them) or paused flows (not firing, and publishing
      would make an old revision live the moment someone un-pauses).
      Idempotent, and pinned by a test that it never advances an existing
      published pointer to a newer HEAD.
- [x] **BUG: provider-event triggers didn't count as triggers at all** —
      `HasConfiguredAutoTrigger` only knew about scheduler and webhook nodes,
      so a flow whose only trigger was "On mention" / "On push" / "On payment"
      reported as **Manual only** while firing on every event. Added
      `core.EventTriggerModules` + `HasEventTrigger`, mirrored in
      `web/src/flowStatus.ts`, with `TestEventTriggerModulesMatchCatalog`
      failing if a new `category: "trigger"` drop is added and not classified.
- [x] **BUG: the "you haven't published" nudge almost never appeared** — it
      was gated on `triggers.length`, the DEPRECATED graph-level trigger array
      the runtime mostly ignores, so a flow triggered by a `cron_trigger` or
      `webhook_input` NODE — essentially every modern flow — never saw it.
      Now gated on `runStatus === "needs_publish"`. This was the single biggest
      reason people believed a saved flow was a running flow.
- [x] **Five verbs collapsed to two** — one **Publish** (was Publish / Go live
      / Make changes live / Make live) and one **On/Off** switch (was
      Enabled / Paused). Plus a continuously visible draft-vs-live line under
      the toolbar: "Published — the live version matches your draft" or
      "Published, but your draft has changes that aren't live yet" with an
      inline Publish changes action. Both locales.
- [x] **Tests updated to reflect that a live flow is a published flow** — 18
      fixtures across webhook/form/Slack/GitHub/Stripe events and the e2e
      suite saved a graph and expected it to fire; they now use a
      `savePublished` helper. The user-journey test gained an explicit
      `publishFlow` step between "turn it on" and "fire the webhook", which is
      the sequence a real user has to follow.

      **Known gap, deliberately preserved:** node-level `disabled` on a trigger
      does NOT stop the flow firing — the /trigger endpoint and the event
      fan-outs only consult the whole-flow switch, and the worker then records
      the disabled node as skipped. `classifyTriggers` mirrors that rather than
      pretending otherwise, and the migration preserves it. Whether those paths
      SHOULD honour a disabled trigger node is a separate question; the
      migration test pins current behaviour so it fails if that changes.

### Templates (2026-08-19)
- [x] **BUG: two of the five templates spammed, forever** — `email-to-sheet`
      and `email-to-slack` both poll Gmail every 300s with
      `max_results: 20` and did NOT set `only_new`, which defaults to false.
      The `gmail_search_messages` schema documents exactly this trap: "Turn
      this on when a published, polling flow acts on each match … so it doesn't
      re-process the same emails on every poll." Both templates are published
      polling flows that act on each match, so as shipped each one re-emitted
      the same 20 messages 288 times a day — **5,760 duplicate rows appended to
      the user's sheet, or 5,760 duplicate Slack posts, per day, forever**. Set
      `only_new: true` on both. Worth noting these are 40% of the gallery and
      the two a "get notified" user reaches for first.
      (First run now correctly emits nothing — the cursor baselines to the
      newest message — which is the documented behaviour, not a regression.)
- [x] Checked the other three and they are sound: `sheet-summary-to-slack`
      carries its 09:00 cron in the top-level `triggers` array rather than a
      trigger node (I misread this as a missing schedule at first — it is
      correct); `form-to-sheet` is webhook-driven so has no dedupe exposure;
      `try-it-now` is manual-only. The `REPLACE_WITH_YOUR_SHEET_ID` placeholders
      in three templates are deliberate and the editor's `lintPlaceholder` rule
      already catches them before a run.

### Apps & connections (2026-08-19)
- [x] **Connection-field setup guidance now has a home** — the TODO said the
      daemon sends a `help` field the web drops on the floor. It doesn't: there
      was **no `help` field anywhere**, and the guidance was being stuffed into
      `Placeholder` — which lives INSIDE the input and vanishes on the first
      keystroke, exactly when someone is off in another product's settings
      hunting for a token. One was 97 characters, so the input truncated it
      before that. Added `core.ConnectionField.Help` (Go) with a doc comment
      spelling out placeholder-vs-help, since 11 fields had already made the
      same mistake; moved those 11 instructions across 14 drop files (Home
      Assistant, nShift, Roaring, Klarna, 46elks, MQTT, SMTP, Open-Meteo,
      OpenWeather, geo), splitting the mixed ones so Klarna keeps `PK…` as its
      placeholder and gains "From the Klarna Merchant Portal." as help; added
      `help?: string` to the web type and rendered it under the input through
      the same localization hop as the label. The 14 genuine example values
      (`sk-ant-…`, `smtp.example.com`) stayed put. Rekeyed all 11 Swedish
      translations — they were keyed by the OLD English strings and would have
      silently fallen back once the wording changed.
- [x] **`technical_notes` wired, `blurb` deleted** — 29 of 33 integrations
      carry technical notes (~11k chars) and all 29 already had fingerprinted
      Swedish. It is real operator documentation — daemon env var names, event
      endpoint paths, API version pins, token-rotation windows, idempotency
      caveats — and the only way to read it was over MCP or in the source. Now
      renders on `/apps/:slug` behind a shut-by-default disclosure, the same
      pattern as the per-drop "Wiring details", so it is out of the way of
      someone who just wants to connect an account.
      `blurb` went the other way: 7 of 33 providers, one of them EMPTY, 235
      characters total, and every provider already has a `description` doing
      the same job. Half-populated optional prose is the worst state to leave a
      field in — removed the field, its 6 values, its 5 Swedish translations,
      the fingerprint-guard loop that covered it, and the stale doc comments
      naming it.

### Localization (2026-08-19)
- [x] **Terminology swept for consistency** — 20 English and 24 Swedish strings
      where the same meaning used a different word than the rest of the app.
      Counted every competing term per concept rather than going on impression,
      then fixed the outliers and left the legitimate ones alone.
      EN: "Drop executions" → "Drop runs" (the sibling metric on the same page
      already said "Flow runs"; "run" is used 162× against 1 "execution");
      node/step/module → drop in the six places they leaked (`bundle.stepsHead`,
      `admin.platformTiers.nodes` — which sat in a row with Runs/Members/Flows —
      `issueKey.perm.moduleRegister`, `runDetail.helpStep`, and two prose
      strings); "graph" → "flow" in the two remaining user-facing spots;
      integration/connector → "app" where it meant a connected service (the nav
      label and page title are both "Apps"); "tenant id" → "Organization ID";
      "no login required" → "no sign-in required" ("sign in" 27× vs "login" 1×).
      Spelling normalised to the house style, which is US English by 73 to 8:
      organisation→organization, colour→color, recognise→recognize, and
      canceled→Cancelled to match the in-app 5:1 majority for that word.
- [x] **BUG: Swedish told users to press a button that doesn't exist** —
      `flowStatus.manual.tip` and `flowStatus.needs_publish.tip` both said
      "trycker på Run", but the Swedish Run button is labelled **Kör**. Found by
      scanning Swedish copy for English control names and checking each against
      its actual label. (`admin.sso.walkthroughStep2Body` also names a "Save"
      button and is CORRECT — that one is in Google Cloud Console, not here.)
- [x] **Swedish settled on *trigger*, not *utlösare*** — the consistency sweep
      first normalised the other way (*utlösare* was the majority at 13:4), and
      Joachim corrected it: **trigger** is the right Swedish word here. Swept
      all 21 strings plus the drop-category chip. The paradigm now in use is
      *en trigger* / *triggern* / *triggers*, with verb forms *trigga*,
      *triggas*, *triggade* — the plural follows the existing product voice
      (`usage.proPitch` and `settings.general.privateVisibleDesc` already said
      "triggers" before this session). **Open question:** standard Swedish
      declension would be *triggrar* / *triggrarna* rather than the English
      plural — say the word and it's a one-line sweep. *utlösare* is kept as a
      SEARCH synonym in `lib/dropSearch.ts`: that map exists to accept whatever
      word the user reaches for, so removing a working way in would be a
      regression, not consistency.
- [x] **Swedish terminology aligned** — "secrets" → *hemligheter* in the three
      `issueKey.template*Desc` strings (the word is *hemligheter* 48× elsewhere);
      *funktion* → *drop* in `admin.platformDrops.subtitle`; *Noder* → *Droppar*;
      *Grafgränser* → *Flödesgränser*; and the secret-manager remove dialogs now
      say *hemlighetsförråd*, matching the intro that introduces the term.
      Left alone: "AWS Secrets Manager", "Client secret", "Secret ID" and
      friends are proper nouns.
      Deliberately NOT changed: `graph:admin` is a literal permission scope,
      `{{node}}` is a variable name that never renders, and "Connector apps" is
      the admin OAuth-credentials page — a different object from user-facing
      Apps, and consistently named across all three of its uses.

- [x] **Locale bundles swept and guarded** — audited both catalogues (2,039
      keys each) against every `.ts`/`.tsx` source. Parity was already perfect
      and stayed so; removed **32 dead keys** whose surfaces are gone: the
      admin "Advanced" group and its secret-manager card, the AI-assist block
      of `RenderTemplatePreview`, the standalone `/templates` page (folded into
      CreateFlow, which owns the heading), the old New-flow modal and the three
      competing create buttons, the `/plans` page headings and plan names
      (folded into `/usage`), the superseded secret chips, and two run-detail
      error strings that `explainApiError` replaced. Empty parent objects
      pruned. DELIBERATELY KEPT the six
      `connectMcp.clients.*.configPath.{macos,windows,linux}` keys — see the
      open note above; they resolve through a variable key and every static
      audit calls them dead.
- [x] **`connections.secretManager.intro` wired instead of deleted** — it was
      unused, but it describes a page that still exists and it is the only copy
      there that tells a non-IT reader "if that means nothing to you, you don't
      need this". The page opened straight onto a raw
      `${vault.PATH#FIELD}` heading while the AWS and GCP sections below it
      each paired a head with an intro; Vault was the odd one out. Now renders
      as the page lead-in. (`pages/AdminSecretManager.tsx`)
- [x] **Structural guards on the catalogues** (`src/i18n/catalog.test.ts`, +5
      tests) — key parity both ways, no empty strings, every `{{interpolation}}`
      and every `<0>…</0>` Trans placeholder preserved across languages, and
      plural forms paired (`_one` without `_other` silently renders the key
      name). These catch what no reviewer can eyeball across 2,000 keys: a
      Swedish string that loses its `{{count}}` doesn't crash, it renders a
      sentence with a hole in it, in the language the reviewer doesn't read.
      Mutation-tested — each guard verified to FAIL on a real violation.
      Deliberately no "unused key" rule: keys are legitimately built at
      runtime, so it would produce false positives and get silenced.
- [x] **Audited the Swedish vocabulary maps — no changes needed.** Dumped the
      live catalog from the Go registry (145 manifests) and diffed
      `lib/dropText.ts`, `dropDescriptions.sv.ts`: **0** stale labels, **0**
      stale subtitles, **0** fingerprint drift, and every one of the 145 drops
      has a Swedish description. Two earlier scrape-based passes reported
      "stale" and "102 drifted" — both were extraction bugs on my side (a
      lowercase struct literal and a `unicode_escape` that mangled the em-dash
      in every UTF-8 description), not rot. Trust the fingerprints, not a
      regex over the Go source.

### Web — UX review, non-technical user's lens (2026-08-19)
- [x] **First-run leads with a working flow, not an empty canvas** — Welcome's
      primary action was "Create your first flow" hard-coded to `?tab=blank`,
      the hardest of the three ways in. It now copies the no-setup starter and
      opens it in the editor (`?tab=template&start=try-it-now`), with "Browse
      templates" and "Start from blank" demoted to a quiet second line. Added
      `?start=<id>` to TemplateGallery: copies that template once per mount
      (a ref, not state, so a re-render mid-copy can't mint a second flow) and
      strips the param as it goes, so Back doesn't re-fork. Revived the
      `welcome.featured*` copy the old removal stranded and deleted the ten
      keys whose UI is gone for good (`goal*`, `signedInAs`, `inTenant`,
      `featuredTitle`). Also dropped four sv-only keys with no English
      counterpart. (`pages/Welcome.tsx`, `components/TemplateGallery.tsx`)
- [x] **Templates is now the default create tab, and leads the tab order** —
      the AI tab was the default on the theory that it was the fastest path for
      a non-technical user, but it needs a connected Claude/OpenAI account, so
      on a fresh or self-hosted workspace the default tab asked the user to go
      connect a paid service before they had seen the product do anything.
      Order is Template · AI · Blank. REVERSIBLE product call — one ternary in
      `pages/CreateFlow.tsx` — flag it if you disagree.
- [x] **Run/Publish can no longer scroll off the toolbar** — the whole bar was
      the scroll container with the scrollbar hidden for looks, so on a narrow
      window (or any window with the inspector open) the primary actions slid
      off the right edge with nothing on screen to suggest they were still
      there. Split into a scrolling `.toolbar-scroll` region (authoring tools,
      save/history/publish) and a pinned sibling holding the status chip, the
      config counter and Run/Test/Stop. Added a right-edge mask so the scroll
      region reads as scrollable, kept the primary label at every width (the
      640px rule used to hide it, leaving an unlabelled purple glyph as the
      Run button), and gave all 15 icon-only toolbar buttons an `aria-label` —
      `title` alone is not an accessible name and does not exist on touch.
      NOTE: the review overstated the clutter — align/distribute already only
      render at 2+ selected and the pause point at exactly 1 selected, so the
      bar is never the 20 controls claimed. No overflow menu was needed.
      (`pages/FlowEditor.tsx`, `web/src/app.css`)
- [x] **"?" is a real help menu now** — it opened keyboard shortcuts, which
      quietly failed the person who needed it most, while the docs site had no
      link from anywhere in the app. `ShortcutsModal` → `HelpModal`: leads with
      Documentation (docs.dazyflow.app) and Contact support (the in-app ticket
      thread when `support_tickets_enabled`, else the operator-configured
      contact, else nothing rather than a dead link), with the shortcut tables
      below. Also un-hid the button on mobile — it was hidden there on the
      grounds that a phone has no keyboard, which stops being true the moment
      the button carries docs and support.
- [x] **English category chips read as product, not engine** — Swedish had a
      category map so it read *Nätverk · In/ut · Omvandling*; English had none
      and fell through to the raw catalog value, so an English reader saw
      lowercase **network · io · transformation**. Added `EN_CATEGORIES` as a
      BASE layer (language map → English map → raw), so an unmapped locale gets
      product words too, and pinned the Swedish readings for `ai`/`trigger`
      that previously relied on the fallback. +4 tests.
- [x] **Permission scopes replaced with guidance** — "Read-only — missing
      graph:edit" → "You can view this flow but not change it — ask an admin
      for edit access"; "Missing graph:run" → "You don't have permission to run
      flows — ask an admin." Both locales.
- [x] **The empty Flows page renders the two paths its copy promises** — it
      offered "a ready-made template, or a blank flow" and then showed one
      generic button that went to the AI tab, which is neither. The two
      matching keys existed and were unused.
- [x] **One word per concept** — "Pause point" everywhere (the toolbar said
      pause point while the badge, context menu and clear action all said
      breakpoint); `/collections` is now the canonical URL with `/results`
      redirecting, matching the nav label and page title; "graph"/"fork" out of
      the English replay tooltip and template-gallery intro — the Swedish for
      both was already jargon-free, so only English needed it.
- [x] **Show-password toggle on all three auth screens** — one `PasswordField`
      component rather than a per-page toggle, so sign-in, sign-up and reset
      behave identically. `type="button"` and `tabIndex={-1}` so it never
      submits and never sits between the password field and the submit button
      in the tab order.
- [x] **`--faint` now clears AA on the surfaces that carry text** — dark went
      #706b8e → #918bb0 (was 2.96:1 on --surface-2 and 2.35:1 on --surface-3);
      LIGHT was worse and the review missed it entirely, measuring 2.87:1 on
      --bg — #8b93a7 → #656e85. Both stay a visible step below --muted.
      --surface-3 is still under in dark: it's the deepest well, so keep it for
      chrome rather than text a user must read.
- [x] **Buttons have a designed focus ring** — one `:focus-visible` rule for
      buttons, links, summary and `[tabindex]`, using `outline` (never clipped
      by an ancestor's overflow) rather than the inputs' box-shadow.
- [x] **Editor touch targets under `@media (pointer: coarse)`** — 34px tall /
      36px square goes to 44px, toolbar 52px → 60px. Hit area only; icons and
      type are unchanged, so the bar is identical on a desktop.
- [x] **Run search asks for what people know** — "Search by run ID or flow…" →
      "Search by flow name…". ID matching still works, silently.
- [x] **BUG found on the way in: `var(--radius)` was never defined** — used in
      12 places across `.welcome-mcp`, `.mcp-client-card`, `.subdomain-suffix`
      and the render-text/table preview panels, all of which had been rendering
      SQUARE with no one noticing. Pointed at `--r-2` (8px), the app's card
      radius, matching the neighbouring rules in each block. Audited the rest:
      every other undefined custom property carries a fallback, so nothing else
      is silently broken — though ~16 stale aliases (`--fg`, `--text-muted`,
      `--r-md`, `--shadow-2`, …) still resolve only via their fallback and
      could be swept to real tokens someday.
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

### Web — UI/UX polish
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

### Drops & connectors
- [x] **date** drop — parse/now + timezone + offset + format (`drops/datetime/`)
- [x] **parse_csv** / **build_csv** drops (`drops/transform/`)
- [x] **expression** drop — CEL formula over a value (`drops/transform/`)
- [x] **base64** + **hash** (HMAC) drops (`drops/encoding/`)
- [x] **parse_xml** drop (`drops/transform/`)
- [x] **regex** drop — extract/replace/split/match (`drops/transform/`)
- [x] **rss** drop — RSS/Atom reader, Interval-paired, cursor dedupe on by default (`drops/rss/`)
- [x] **phone** drop — E.164 validate/normalize + region default, card flag hint (`drops/value/`)
- [x] daemon **`client_secret_basic`** OAuth support (`daemon/oauth.go`) — Fortnox needs it
- [x] **Fortnox** connector — customer/invoice + poll, OAuth (`drops/fortnox/`)
- [x] **46elks** connector — SMS, static-key (`drops/elks/`)
- [x] **Klarna** — payments, static-key (`drops/klarna/`). Order Management first
  slice: get order, capture (full/partial), refund (full/partial). HTTP Basic +
  region-hosted (EU/NA/OC × prod/playground) as a connection field; no daemon
  change. Money-moving POSTs are RetryNever + DedupeWrites (no reliable idem key).
- [x] **nShift** — Nordic multi-carrier shipping, static-key (`drops/nshift/`).
  Unifaun ExtAPI v1 first slice: create_shipment (book), get_shipment
  (status/tracking), delete_shipment (cancel an unprinted draft). Bearer API key
  + environment-hosted (integration/production) as connection fields, defaulting
  to the integration sandbox so a half-configured connection can't book a real
  consignment; no daemon change. Booking/delete are RetryNever + DedupeWrites
  (no idem key). Shipment payload is a pass-through JSON object built upstream.
- [x] **Roaring** — org-number → company enrichment, static-key from the
  platform's view (`drops/roaring/`). company_overview (org number → name /
  status / full record) + company_search (name → candidate matches). Roaring's
  OAuth2 **client-credentials** grant needs NO daemon OAuth provider: the
  connector exchanges the Consumer Key/Secret for a bearer at POST /token itself
  and memoises it in-process until near expiry (keyed per connection). Reads —
  RetryExponentialBackoff. Country segment defaults to `se`, generalizes to the
  other Nordic registers.

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

### Support — findings from the review of 5e754ae (2026-08-19)
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
