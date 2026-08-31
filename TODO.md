<!--
SPDX-FileCopyrightText: 2026 Angels' Ware
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Dazyflow TODO

The single backlog: what's left, roughly in priority order. Every `- [ ]` in
**Open** is work somebody could pick up today.

Convention: an item says what a user experiences, then what to do about it. If
it is blocked, the reason is on the line — a backlog that doesn't say why
something is stalled turns into a graveyard.

Three things deliberately live elsewhere, because keeping them here is what
made the open list unfindable:

- **Completed work** is in `git log`, and where the *reasoning* behind a fix is
  worth keeping, in **[docs/decisions/](docs/decisions/)**. Each Open section
  below links to its record.
- **Things decided against** are in [Decided against](#decided-against). They
  are not checkboxes: a `- [ ]` that will never be ticked is noise in a count.
- **Findings that were raised and disproved** are in
  [Corrections to the record](#corrections-to-the-record). Without them the
  same suggestions arrive again every review.

---

## Open

### Runners

Context: **[2026-08-25 — runners review](docs/decisions/2026-08-25-runners-review.md)**.
Fifteen gaps closed across three seams; these three were left alone on purpose,
and the reasons still hold.

- [ ] **The lease is still written with the claiming daemon's wall clock**
      and compared against another's. Fixing it properly means computing
      `lease_until` from the database's `now()`, which the injected-clock
      contract suite is built against — so it is a store-API change, not a
      one-liner. Low harm meanwhile: `TaskLease` is two minutes and NTP skew is
      seconds. Do it with the clock injection, not around it.

- [ ] **The registration token is still passed in argv**, so it is readable via
      `/proc/*/cmdline` during install. Moving it to an environment variable or
      a file would help a little (`/proc/*/environ` is owner-only) but the
      token is single-use and 30-minute-lived, and `runner.sh` spends it in the
      foreground within seconds. Worth doing when the installer next changes,
      not on its own.

- [ ] **Two assertions the review called vacuous were not located.** The report
      named `engine/coverage_test.go` and `daemon/runners_test.go` without
      lines, and nothing in either is obviously unable to fail. The nearest
      candidate — `remote_multidrop_test.go` asserting no namespaced alias is
      filed — is a deliberate regression guard and stays.

### Signup and onboarding

Context: **[2026-08-31 — cold signup walkthrough](docs/decisions/2026-08-31-cold-signup-walkthrough.md)**.

- [ ] **The hero screenshot still contradicts the hero copy.** `shots/branch.png`
      leads with a Schedule step reading `0 7 * * 1-5`, a Gmail step reading
      `is:unread newer_than:1d` and a router listing *Routing slot 1…8* —
      cron and Gmail query syntax, directly above *"No code, no consultants"*
      and a stat band claiming *"0 lines of code to write"*. **Not fixed:** this
      is a capture job, not a code change. It needs a demo workspace, connected
      accounts or the documented CSS hiding trick, and a re-shoot at a 1120x700
      viewport with devicePixelRatio 2.4 (see `dazyflow-web/README.md`), and a
      worse screenshot is a real risk. The product can already express both in
      plain language — the schedule editor hides cron behind *Custom* — so
      build the demo flow that way and re-shoot.

### Templates — thin, and the front door

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

### Web

Context: **[2026-08-20 — web fixes](docs/decisions/2026-08-20-web-fixes.md)**.

- [ ] **Redundant fallbacks on tokens that DO exist** — noticed while fixing
      the above, not part of it. `--warning` is defined, yet carries 13
      different hardcoded fallbacks scattered through `app.css` (`#c98a2b`,
      `#d08700`, `#d29922`, `#d97706`, `#d9822b`, `#d99e2b`, …). Each is dead
      code that also documents a *different* intended amber, so they disagree
      with each other and with the token. Harmless today — the token always
      wins — but it is the same drift one step earlier, and the token guard
      deliberately does NOT flag it (the token exists, so nothing is broken).
      Sweep them out; check the others while you're there.

### Copy and vocabulary

Context: **[2026-08-20 — step vocabulary](docs/decisions/2026-08-20-step-vocabulary.md)**
and **[2026-08-22 — plain language](docs/decisions/2026-08-22-plain-language.md)**.
The first is required reading before writing anything user-facing: `step` in
copy, `drop` in code, and the list of names that must not move.

No open work here — both entries are standing guidance, not tasks, which is why
neither is a checkbox.

- **Hold the line in new copy.** The next person writing user-facing text needs
  the split above. CHANGELOG entries from 0.5.0 on should say step; earlier
  entries stay as written (a changelog is a record, not documentation).

- **Two judgement calls, deliberately not decided here.** "Inspector" is
      Figma/devtools vocabulary and *Step settings* would read plainer, but
  renaming a panel mid-flight has its own cost. "Jump to it on the canvas"
  (`editor.configModalLead_*`) is probably fine in a visual editor. Both are
  listed so the next review does not re-raise them as findings.

---

## Decided against

Considered, declined, and recorded so the next review doesn't re-raise them as
findings. Not checkboxes — nothing here is waiting to be picked up.

- **Do NOT put `Manifest.Summary` in the step tooltip** — raised in the
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

- **Breadcrumbs in the header** — deferred, but not for the reason this
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

- **Collections column headers must stay the stored key.** The 2026-08-31
  walkthrough flagged `name` / `email` / `message` / `saved_at` as snake_case
  leaking into a page written for non-technical readers, and they were briefly
  humanized to "Saved at". That was wrong, and `Results.test.tsx` already said
  so: *"a header is a name someone has to match against their data, so
  'Ordered' is not 'ordered' and `orderTotal` keeps its hump."* The columns are
  the user's own field names, shared with their flow and with the CSV this same
  page downloads; prettifying them breaks the match in all three places. Eight
  tests caught it. What WAS a real defect on that page is the values, not the
  headings: a cell holding `2026-08-31T07:21:54Z` is not a name anyone matches
  against anything, and `formatCellDisplay` now renders instants in local time
  while `toCSV` keeps the raw form. Do not re-raise the headings.

- **A signed-in visitor asking for `/signin` wants the app, not a sign-out.**
  The same walkthrough found that `/signin` and `/signup` exist only in the
  signed-OUT route tree, so with a session in hand they hit the authenticated
  catch-all and answered "page not found" — including from the sign-up form's
  own "Already have an account? Sign in" link. The first fix routed them to a
  component that called `signOut()`, on the reasoning that asking for the
  sign-in page means you want to sign in. It logged people out **the instant
  they created an account**: a successful sign-up sets the token while the URL
  is still `/signup`, so the authenticated tree renders there and the effect
  fires. Both routes are a plain `<Navigate to="/" replace />`, and
  `authedAuthRoutes.test.tsx` exists to keep them one. Genuine sign-out belongs
  to the account menu, which already has it.

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
  sat in code CI never exercises; CI stands up both and exports
  `DAZYFLOW_TEST_DB`. What CI genuinely never ran was the frontend vitest
  suite — since fixed. (Both were true of `.builds/archlinux.yml`, the
  builds.sr.ht manifest CI ran from at the time; the gates now live in
  `.github/workflows/ci.yml`.)

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
