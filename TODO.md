# Dazyflow TODO

The single backlog: what's left, grouped by what unblocks it. Read it top to
bottom — it's roughly in priority order.

Convention: a `- [ ]` says what a user experiences, then what to do about it.
If an item is deferred or blocked, the reason is on the line — a backlog that
doesn't say why something is stalled turns into a graveyard.

Completed work is not archived here. `git log` is the record, and keeping a
second copy of it in this file made the open list hard to find. What does stay
is **Corrections to the record** at the bottom: findings that were raised and
then disproved, or deliberately declined. Those earn their place because
without them the same suggestions arrive again every review.

---

## Open

### Vocabulary — renamed, with one convention to hold

The product says **step** (sv: *steg*) everywhere a person can read it, as of
2026-08-20. The docs already said it; the UI, the MCP tool descriptions, the
operator docs, `.env.example` and the user-visible Go error strings now agree.
Swedish moved with it, including gender agreement — *steg* is neuter where
*dropp* was common, so 49 determiners and adjectives changed (`den här
droppen` → `det här steget`).

Deliberately still `drop`, and these are the CONVENTION to hold when adding
code: the Go catalog and package paths, API routes and JSON field names, MCP
tool NAMES (`list_drops`, `describe_drop`), error codes (`drop_not_found`),
audit action names, CSS classes, frontend identifiers (`dropText.ts`,
`DropAdjacency`), and ~900 code comments. Those are the contract every
non-human consumer is grounded on; renaming them buys nothing a user can see.
`describe_drop`'s description carries a note telling an assistant to say
"step" to the user, so the split doesn't leak into a conversation.

Also still `drop`: the VERB, in ~35 places — "drop rows", "drop a pin", "drop
to upload", "would drop Caddy", and the `drop` param on Choose & rename
columns. Renaming those was the most likely way to make this look careless.

- [ ] **Hold the line in new copy.** No open work, but the next person writing
      user-facing text needs the split above. CHANGELOG entries from 0.5.0 on
      should say step; earlier entries stay as written (a changelog is a
      record, not documentation).

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

One token-drift item open, plus the plain-language review below. The three
items that used to lead this section are done (2026-08-20); what they turned
out to be is recorded below, because in each case the entry's diagnosis was off
and the next person shouldn't re-derive it.

- **A switched-off trigger step now refuses deliveries.** The entry said
  "node-level `disabled` doesn't stop it firing", which conflated two
  switches. There were three: `graph.Disabled` (honoured everywhere),
  `Params["disabled"]` (a per-trigger pause the schedules API writes,
  honoured only by the scheduler), and `Node.Disabled` (the editor's step
  toggle, honoured only at execution time). So a disabled webhook trigger
  accepted the POST, started a run, and the worker skipped the very node
  meant to receive it — a 202 for a run that did nothing. Rather than invent
  a fourth mechanism, `triggerNodeDisabled` now treats either switch as
  paused and the inbound endpoints call it. A flow whose webhook steps are
  ALL paused gets a 403 `trigger_disabled`; partially-paused still fires
  (the active steps have work); a flow with no webhook step at all still
  fires, since posting to kick such a flow is a legitimate use. The entry's
  warning about `MigrateWebhookPublish` was a false lead — its deliberate
  permissiveness is about published-vs-HEAD, not about disabled.

- **The CSS token drift included two live bugs**, not just tidying. The entry
  said "they render correctly". `--text` was referenced 36 times with NO
  fallback and defined nowhere, so every one of those `color:` declarations
  was invalid at computed-value time and silently inherited — most visibly
  `.plan-feat-up`, the "what differs" upgrade cue, which inherited `--muted`
  from `.plan-feat` and so rendered identically to the rows it was supposed
  to stand out from. `--r-md` rendered one card square. The entry's advice to
  "point them at real tokens" was also wrong for four of them
  (`--node-accent`, `--op-color`, `--enter-delay`, `--draw-delay`) which
  components set inline at run time; defining those in CSS would override
  per-node values. `web/scripts/check-css-tokens.mjs` now runs from
  `npm test` and fails on either shape, with an allowlist for the runtime-set
  four that it also keeps honest.

- **The `configPath` i18n keys are literal now**, so no note is needed to
  protect them. `t(`${prefix}.${os}`)` meant no static reference existed and
  every unused-key audit flagged all six as dead — which nearly deleted them.

- [ ] **Redundant fallbacks on tokens that DO exist** — noticed while fixing
      the above, not part of it. `--warning` is defined, yet carries 13
      different hardcoded fallbacks scattered through `app.css` (`#c98a2b`,
      `#d08700`, `#d29922`, `#d97706`, `#d9822b`, `#d99e2b`, …). Each is dead
      code that also documents a *different* intended amber, so they disagree
      with each other and with the token. Harmless today — the token always
      wins — but it is the same drift one step earlier, and the token guard
      deliberately does NOT flag it (the token exists, so nothing is broken).
      Sweep them out; check the others while you're there.

### Plain language — what a non-technical reader actually meets

From a read-through of every page a Viewer/Editor can reach (2026-08-22). The
groundwork is done and should not be redone: *drop→step*, *graph→flow*,
*node→step*, the category chips off their enum values, `explain.*` errors that
name a cause and link the fix, and progressive disclosure that already hides
cron behind "Custom", webhook setup behind "For developers", embed code behind
"Put this form on my own website", and raw params JSON behind an explicit
toggle. Admin is permission-gated, so a plain Editor never meets a daemon log.
What follows is what leaks through that. Three of the four items found are
done (2026-08-22): the (i) affordance is a real popover, the canvas no longer
says *wire* / *pin* / *upstream*, and *daemon* / *metering* / *property bag*
are gone from copy a business user reads. Two of that third item's six strings
turned out not to be defects at all — see **Corrections to the record**.

The same day, the vocabulary sweep was carried through every remaining
user-facing surface: the 151 Go drop descriptions and their params help, the
runtime error strings, and the Apps page, which had never had the
drop→step / node→step / graph→flow rename at all. `wire` / `upstream` /
`downstream` and the old product nouns now return zero across the catalogue,
the manifests and both locales. Three uses of *drop* survive on purpose and
must stay — "Drop a meeting onto a calendar", "drop generated files back into
a folder", "someone drops a file into the workspace" are the verb.

- [ ] **Two judgement calls, deliberately not decided here.** "Inspector" is
      Figma/devtools vocabulary and *Step settings* would read plainer, but
      renaming a panel mid-flight has its own cost. "Jump to it on the canvas"
      (`editor.configModalLead_*`) is probably fine in a visual editor. Both
      are listed so the next review does not re-raise them as findings.

### Connectors — Sweden first, then the Nordics

The catalog is ~130 drops across 38 integrations, which is on par with n8n's
core node set, so **breadth is demand-led, not a structural gap**. Every
standard primitive is shipped (date, expression, regex, csv/xml, base64/hash,
phone, rss). The moat is the local services Zapier / Make / n8n
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

- [ ] **Do NOT put `Manifest.Summary` in the step tooltip** — raised in the
      2026-08-22 plain-language review as the cheap fix for 131-word hover
      text, and declined on inspection. Summary is the right length (median
      78 chars, never empty, required at registration) and the frontend does
      not read it, so the idea is tempting. What it costs: Summary has no
      Swedish, so surfacing it would drop 151 English one-liners into a
      localized UI, add a THIRD fingerprint-guarded translation surface
      beside descriptions and integration prose, and import fresh jargon —
      15 Summaries say *wired* / *downstream* / *graph-author-supplied*, the
      last being the pre-rename vocabulary this backlog spends a section
      holding the line on. The actual problems were the container, not the
      length: a native `title=` never fires on touch and cannot scroll.
      HelpPopover fixes both for every locale at once, with no new prose.
      Summary stays what its doc comment says it is — for the API and the
      flow generator.

- [ ] **Breadcrumbs in the header** — deferred, but not for the reason this
      entry used to give. "The IA is flat" is only true of the customer-facing
      app, where every sidebar destination is one level deep and a trail would
      render a redundant `Home > Flows` above a title that already says Flows.
      The operator surfaces are three and four levels deep
      (`/admin/platform/orgs/:tenant`, `/support/flows/:tenant/:workspace/:id`)
      and are absent from the sidebar entirely — that is where a trail would
      earn its place.
      The real reason not to build it: those pages already have per-page
      `← <parent>` links (AdminPlatformOrgDetail, AdminPlatformUserDetail,
      RunDetail, SupportTickets, FlowEditor), so a breadcrumb system would
      REPLACE five working affordances rather than fill a gap — a refactor,
      not a new capability.
      What is worth doing instead is much smaller: those five links have
      drifted. Two admin pages override the `.back-link` class with a
      copy-pasted inline style that re-declares what the class already gives,
      and hardcode `gap: 4` / `--space-2` against the class's `--space-1`; and
      the labels follow three conventions at once (name the parent, "Back to
      runs", bare "Back"). One shared `<BackLink to label>` component fixes
      the drift and settles the labelling convention at a fraction of the
      cost. Do that; revisit breadcrumbs only if the operator IA gets deeper.

---

## Corrections to the record

- **`settings.notifications.webhookDesc` naming `graph_id` is correct, not a
  vocabulary leak.** The 2026-08-22 review flagged it as the last survivor of
  the step/flow rename. It is not: `graph_id`, `failed_node`, `error_code` and
  the rest are the literal JSON keys `daemon/failure_notify.go` POSTs, pinned
  by `failure_notify_test.go`. The copy documents a wire format, so renaming it
  in the UI would make the documentation wrong and renaming it in the payload
  would break every existing consumer. It is also already behind "Advanced:
  send to Slack, Teams, or another service", i.e. shown only to someone who
  asked for the payload shape. Leave it.

- **`schemaForm.fallbackHint` is unreachable, so it was never the UX problem it
  looked like.** The review called it "the worst of the set — it replaces the
  form, so it is the only thing on screen when the user is already stuck."
  Tracing it: Inspector gates on `supportsSchemaForm`, which asserts precisely
  the condition the branch tests; both recursive call sites check
  `schema.properties` first; and the ten drops whose schema is an object with
  no properties (`branch`, `merge`, `and`/`or`/`not`, five event triggers) hit
  Inspector's `noSettings` and render an empty panel instead. Only a unit test
  constructing `{type:"string"}` reaches it. The wording was still replaced —
  a defensive branch should degrade into a sentence, not jargon — but nobody
  should spend time on it as a user-facing bug.

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
  so no overflow menu was needed — only the pinning fix, since shipped.
- **CI does run the Postgres/MariaDB-gated tests.** A review claimed findings
  sat in code CI never exercises; `.builds/archlinux.yml` stands up both and
  exports `DAZYFLOW_TEST_DB`. What CI genuinely never ran was the frontend
  vitest suite — since fixed in `.builds/archlinux.yml`.

Also from the 2026-08-20 sweep — raised, considered, and deliberately NOT
changed. The reasoning is recorded so the next review doesn't re-raise them:

- **`GetJob` does not enforce workspace isolation, and shouldn't.**
  `core.RequireWorkspace` ignores its workspace argument on purpose: the
  platform settled on one workspace per org, so workspace stopped being an
  authorization dimension. The defect was the doc comment claiming otherwise
  — fixed. Adding real enforcement would re-litigate a settled design.
- **`excel_write`'s `autosize`/`freezeRow` are disclosed no-ops.** The schema
  says "Accepted for compatibility; not applied." right where an LLM or a user
  reads it. That is documentation, not silent drift.
- **Invitation tokens stay unhashed.** `auth/invitation.go` carries the
  rationale: the token grants view+accept of an invite, and storing the
  plaintext keeps operator inspection of pending invites possible. Overturn it
  with an argument about what accepting actually grants, not on symmetry with
  session tokens alone.
- **The git pre-flight SSRF DNS-rebinding window stays open.** Accepted
  residual risk, already annotated in `drops/git/git_checkout.go`: go-git
  exposes no dial hook, so resolve-then-check is the best available. Close it
  when go-git does.
- **Slow MCP tools blocking the stdio connection is the documented trade.**
  `waitForRun` and friends dispatch synchronously, which is correct for a
  single-LLM session. Revisit only if multi-client MCP ever matters.
