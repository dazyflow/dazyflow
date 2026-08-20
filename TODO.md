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
