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

### Runners — the 2026-08-25 review, worked through

`ccb6f6f..98ac656` landed tenant-owned runners; a review the same day found 15
gaps plus a tail of smaller ones. **All of them are fixed** — the entry stays
because several were resolved in a way a future reader should not have to
re-derive, and three were deliberately left alone with a reason.

The gaps clustered in three seams, and the fixes follow them.

**Seam 1 — resolved secrets and the tenant boundary.**

- **A queued task's script, stdin and env are now sealed at rest** under the
  tenant's DEK (`EncryptedSecrets.SealPayload`, AAD-bound to the row AND the
  field so a ciphertext cannot be relocated). They arrive at `Enqueue` with
  every `${secret.…}` already expanded, which contradicted
  `engine/secrets.go`'s stated contract. A deployment with no
  `DAZYFLOW_MASTER_KEY` still stores cleartext and logs that it is doing so —
  the same posture as every other stored secret there. Rows written before
  sealing read back unchanged, told apart by a `sealed:v1:` marker.
  **Deliberately NOT added to `core/lint.go`'s `persistenceModules`:** that
  rule's message says a value "could land in plaintext on disk or in a
  database", which sealing makes untrue, so the warning would be noise.
- **`Complete` and `Extend` take the whole `Runner`, not its name.**
  `tenant_runners` is keyed `(tenant, name)`, so the name alone was never an
  identity. Both stores now match on tenant as well, via one `heldBy` predicate,
  and the shared contract suite has a same-named-runner-in-another-org case —
  the old test only tried a differently-named impostor.
- **The four `/api/v1/runner/*` endpoints are throttled per IP.** `/register`
  takes the tighter webhook allowance; the polled endpoints take a new runner
  allowance sized so a whole office of agents behind one NAT fits (they poll
  every 5s, and that poll is also the heartbeat).

**Seam 2 — nothing outside the live `Dispatch` goroutine owned a task.**

- **`RunnerTaskSweeper` now does**, on its own minute-long ticker with a
  startup pass, closing what nobody is waiting for: a running task whose lease
  lapsed, and a queued task past its own timeout plus the dispatch grace (or
  `DefaultRunnerQueuedCeiling` when it carries no timeout). It closes each row
  through the existing `FailAbandoned`/`CancelQueued`, so the wording and the
  atomic re-check stay in one place, and it is safe on every daemon at once.
  This is what stops a redeploy leaving a queued row *claimable* — a machine
  switched on an hour later running a script for a run that died.
- **An oversized result no longer strands the task.** The agent trims to 1 MiB
  per stream and FAILS the step naming the limit, rather than handing on half a
  document; the server answers 413 with a message that says what to do; and
  `Complete` failures are no longer all collapsed into a terminal 409 — a
  transient error is a 503 and the agent retries with backoff.
- **A dropped packet no longer kills the agent.** `post()` catches `OSError`
  (which covers the read-phase `TimeoutError` that is not a `URLError`) and
  `ValueError` (a proxy's HTML error page), `execute()` is total, and
  `subprocess.run` uses `errors="replace"` so binary output cannot escape.
  `--once` also exits on an empty queue now, which is what it always claimed.

**Seam 3 — the runner host's own guarantees.**

Each was promised in a comment or in `docs/guide/runners.md` and not delivered.

- **`--allow` turns the shell off.** Checking `parts[0]` and then running the
  string with `shell=True` enforced nothing. With an allow-list the command is
  `shlex.split` and executed directly, so metacharacters are argument text.
  This is a **behaviour change** for anyone whose allowed command used a pipe:
  the docs, the changelog and the refusal message all say to put it in a script
  and allow that.
- **The credential is created 0600** via `os.open`, not written and chmod'ed,
  and its directory is 0700.
- **`install` restarts**, so re-running it — the documented way to tighten the
  allow-list — actually applies the new list instead of printing "Started."
  over the old one.
- **The unit sets `KillMode=mixed` and `TimeoutStopSec=660s`**, which is what
  makes the "stops between tasks" promise in `runner.sh` and `dzrunner.py`
  true; systemd's default SIGTERMs the whole cgroup.
- **Setup moves files into place instead of writing over them**, so re-running
  the installed copy no longer truncates the script `sh` is reading — which
  used to happen *after* the single-use token was spent.
- **The installer carries the agent's SHA-256** and refuses a mismatch before
  anything is made executable, and `runnerBaseURL` now goes through
  `effectiveBaseURL` so `X-Forwarded-Proto`/`-Host` are gated on
  `TrustProxyHeaders` like everywhere else. The checksum is not a signature —
  it comes down the same channel — but it closes the split channel, a tampered
  mirror and a truncated download.

**Standalone.**

- **`InlineOnly` is enforced by the ENGINE, from the manifest**
  (`refuseInlineOnlyFileRefs`), for every drop rather than inside
  `RemoteTransport.Execute`. That fixed two things at once: `run_on_runner` — a
  native drop — silently ran with empty stdin on a file input and reported
  SUCCESS, and every remote transport refused file refs even for a co-located
  gRPC module that never declared the flag.
- **The step normalizes its `runner`/`label` params** the way registration
  normalizes them, so `Linux` matches the `linux` the admin page shows.
- **`manifestsSnapshot` is tenant-scoped**, so a support bundle stops accusing a
  working flow of referencing an unknown module (and skipping every dependent
  check on that node). The platform killswitch page uses a new
  `NodeResolver.AllManifests`, which is the one legitimate instance-wide view —
  a tenant runner's drops were invisible on the only surface that can switch
  them off.
- **A remote may not take a built-in's id.** `lookup` prefers Native but the
  manifest map added Remote after it, so the palette described the remote while
  every run executed the built-in. Refused at registration via
  `RemoteCatalog.Reserved`, with `addKeeping` as the belt-and-braces half.
- **`ListManifests` falls back to `GetManifest`** (invoked by name; deliberately
  not re-declared in the `.proto`) when a server answers `Unimplemented`, so
  runners built against the older contract keep registering — which is what the
  proto's own comment about a runner's binary "not being the daemon's to
  update" asks for.
- **Progress is forwarded.** The handler decoded `Message` and dropped it; there
  is now a `progress` column, and `Dispatch` emits each new line as it polls.
- **Delete asks first** in `AdminRunners`, like every sibling admin page, and
  the test that pinned its absence now pins the confirmation.
- Cleanup done: `Env` is populated from `job.Env` rather than being decoration;
  `RemoteTransport.Close` is a documented no-op and the per-drop `conn` field is
  gone (the connection is the catalog's, shared per runner);
  `RunnerCategories` includes `"external"`; `runner_tokens (expires_at)` and
  `runner_tasks (finished_at)` are indexed for the sweeps that scan them; the
  501 is no longer written twice; the dispatch poll loosens to 2s after 30s and
  the "is anything online?" listing is throttled to 5s.

**Left alone, on purpose.**

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

### Cold signup walkthrough — 2026-08-31, worked through

A non-technical persona ("Nora", runs a small café-supply business) was pointed
at the marketing site with no prior knowledge and told to sign up and see how
far she got. She reached a published contact form with a real submission in
Collections, so the product works; ~30 things got in the way and **all but one
are fixed.** The entry stays for the same reason the runners one does: three of
the fixes are not the obvious ones, and two findings turned out to be wrong.

The run was driven against `dzd` serving `web/dist` **and** `../dazyflow-web`
from one origin (`DAZYFLOW_WEB_DIST` + `DAZYFLOW_LANDING_DIR`), the shape
`docs/DEPLOY.md` describes, rather than the usual Vite-on-5173 dev split. Two
of the worst bugs exist only in that shape and cannot appear in dev. **Run a
cold path against the single-host shape before a release**; the dev split is
not a substitute.

**The funnel had no entrance.** All 28 links on `landing.html` were enumerated:
seven CTAs, every one a `mailto:`, and nothing anywhere pointing at `/signup`
or `/signin` — while self-serve signup was live and working. Nora only got in
by guessing the URL. The `mailto:` gate is a deliberate pre-launch decision and
is untouched; what was not a decision is that an already-onboarded customer had
no way back into the product from the marketing site. Every page now carries a
**Log in** link in the top bar and footer. That link is not part of the gate and
should survive whatever happens to the CTAs.

**Two deployment-shaped bugs, both hit before she did anything.**

- **`DAZYFLOW_PUBLIC_BASE_URL` was never trusted as a CSRF origin.** Serving the
  SPA ourselves makes our own public origin a browser origin doing
  cookie-authenticated writes, and `AllowedOrigins` came from
  `DAZYFLOW_WEB_ORIGIN` alone. The failure hid itself: sign-up and sign-in carry
  no session cookie yet, so `verifyCookieOrigin` waves them through and the
  daemon looks healthy right up to the first authenticated POST. Nora's first
  click produced the raw server string, in red, in the UI. `buildGateway` now
  appends `publicBaseURL` when `webDist` is set, and logs that it did. Both CSRF
  refusals also carry the machine code `csrf_origin`, mapped to
  `apiError.csrfOrigin` — a sentence that says it is a server setting and not
  something the reader did.
- **The auto-start deep link always lost a race.** `?start=try-it-now` is the
  Welcome screen's headline CTA. The effect gated on `templates && token`, but
  `useTemplate` also needs `activeTenant && activeWorkspace`; the template index
  is a static fetch and resolves first, so a cold load reported **"Not signed
  in."** to a signed-in user with their email in the sidebar. It also set
  `started.current` and stripped the param *before* failing, so reloading could
  not retry. Guard widened, and there is now a distinct
  `templates.workspaceLoading` string for "signed in, workspace still opening".

**Connecting an app is where "no technical setup" broke.** Clicking *Connect* on
`/apps/gmail` did a top-level navigation to the authorize endpoint and left her
on a white page reading `{"error":{"code":"not_found","message":"unknown OAuth
provider \"google\""}}` — the application simply gone. `OAuthCard` treated
"provider absent from the list" as "not connected yet", so the button was live.
The template gallery had already solved this; `OAuthCard` now takes the same
signal and says so instead. Related: the gallery's own copy told hosted users
that *"whoever runs the server has to enable it first"*, which is correct
self-host language and a dead end on the hosted product — reworded, and given
the plural forms it never had (*"Needs Gmail, Slack, which isn't set up"*).

**Jargon the 2026-08-22 sweep did not reach**, all fixed: the "Built-in" card
(the biggest on the Apps page at 57 steps) opened with `split_rows`,
`await_approval` and "file I/O"; app detail pages printed raw step ids under
every step name; the Apps list was alphabetical, so a Swedish SMS gateway led
the page ahead of Gmail and Google; the hosted-form templates shipped a step
labelled *Webhook* with *Body* and *Headers* ports inside a template called
*Web form → Collection*; Collections rendered `2026-08-31T07:21:54Z` in a cell.
Auth screens gained Privacy and Terms links, stopped sending the logo to a
hardcoded `dazyflow.app` (which left any self-hosted install), and say
"© 2026 Dazyflow" rather than naming an operating company the visitor has never
seen. The verify-email banner now says what the reader loses by ignoring it.

Marketing-side fixes live in `../dazyflow-web`: the Log in links, a
`scroll-padding-top` so in-page anchors stop landing under the sticky topbar
(measured: the `#how` eyebrow sat at y=60 with the bar occupying through y=72,
and nothing compensated anywhere), a uniform nav order across all eight pages,
run-allowance and secret-store copy that a non-technical buyer can read, and a
stats band that counts apps instead of logo files.

**One more found while fixing those, and it is the one to remember.** The
topbar budget was already blown on the Swedish pages before "Log in" existed,
and it fails invisibly: the brand is a flex item, so when the nav wants more
room than the bar has, the LOGO is squeezed to zero width and disappears. No
wrapping, no scrollbar, no console warning — just a bar with no logo on it,
which is easy to read as a design choice. Swedish binds because "Dokumentation"
and "Begär en inbjudan" are much wider than "Docs" and "Request an invite":
it needed 591px before the new link and 662px after, while the wordmark only
gave way at 560. Fixed by bringing the compact nav type in at 700px instead of
560px, which carries every link down to 631px, then dropping Docs / the
language switch / Pricing at 630 / 420 / 360. **Measure the Swedish page, not
the English one** — the widths are recorded beside the rules in `style.css`.

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
