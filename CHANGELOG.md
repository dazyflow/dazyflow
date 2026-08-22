# Changelog

All notable changes to Dazyflow are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The repository, Go module, and daemon binary are named `dazyflow` / `dzd`.
Versions here correspond to git tags `X.Y.Z` on
[git.sr.ht/~klahr/dazyflow](https://git.sr.ht/~klahr/dazyflow). The running
version is stamped into the binary at build time and surfaced on
`GET /api/v1` (the `build` block) and in the web UI's account menu.

Releasing: write what shipped under `[Unreleased]` as you go, then run
`make patch` (or `minor` / `major`). That target promotes `[Unreleased]`
under a new `[X.Y.Z] - YYYY-MM-DD` heading, leaves a fresh empty
`[Unreleased]`, and commits the changelog together with `./VERSION`
before tagging — so the tag lands on the commit that announces the
version. An empty `[Unreleased]` aborts the release. (`VERSION` matters
because the Docker build reads that file when it isn't handed a `VERSION`
build arg, so it is what a production `docker compose up --build` stamps
into the image.)

## [Unreleased]

## [0.9.0] - 2026-08-22

### Added

- **A flow waiting on a person now emails the people who can decide.** The
  `await_approval` step has always handed you an approval link on
  `pending_url` and left delivering it to you: you wired an ntfy or email step
  after it, or the run sat there until someone happened to look at the
  Approvals inbox. It now mails the approvers itself, when the run parks and
  again once a decision lands — so the same people who were asked are the
  people told the outcome, and a second approver doesn't go hunting for an
  item somebody already resolved.

  Who gets it is the step's new **Email these people** field: comma-separated
  addresses (semicolons and newlines are accepted too — a list that silently
  notified nobody because of the wrong separator is the worst failure this
  could have). It is also the only way to reach someone who isn't a member —
  an external reviewer, a shared ops alias.

  **Leave it blank and nothing is sent.** Existing approval steps are
  therefore untouched by the upgrade: they park and resume exactly as before,
  and you deliver the `pending_url` link yourself or let people work the
  Approvals inbox. Defaulting to "everyone in the organization who could act
  on it" was the other option and was rejected — approving is not
  permission-gated, so that set is wide, and it would have turned every
  already-deployed approval step into a mailshot the moment the daemon was
  upgraded. Opt-in costs one field and surprises nobody.

  Each message goes to one address at a time. A shared `To`/`Cc` would leak
  the organization's member list to every approver, including any external
  reviewer named on the step.

- **Approve or reject straight from the canvas.** An `await_approval` card
  that the run is parked on now carries the two buttons itself, in the same
  flush footer bar as the Connect and "needs configuration" banners. The
  Inspector panel stays — it is still where you add a comment with the
  decision — but the common case, "the flow is waiting on me, right there on
  screen", no longer needs you to select the step and read a side panel first.

- **Step and field help is reachable on a tablet.** Both the (i) on a step
  header and the one on every form field were native `title=` tooltips. A
  native tooltip does not fire on touch **at all**, so on a tablet every word
  of that guidance — 151 step descriptions and 605 field help texts — was
  simply absent; and it cannot be scrolled, which a 131-word step description
  needs. Both are now click-to-open popovers that stay put, scroll when they
  have to, close on Escape or an outside click, and hand focus back.

### Changed

- **The Swedish translation reads like Swedish software.** Around 270 strings
  across the UI catalogue were rewritten from a literal translation into the
  vocabulary Swedish applications actually use: *Redigerare* not *Redaktör*
  for the Editor role, *Läsare* not *Granskare* for Viewer, *Användning* not
  *Förbrukning*, *Blockera* not *Bannlysa*, *Logghistorik* not *Loggretention*,
  and *publicera* in place of the Swenglish *hosta*. Terms that had drifted
  into two, three and four different Swedish words — secret store, Reconnect,
  Retry, Copied, Delete, output, notification — were each settled on one. The
  step-catalogue category chips were translated from the raw engine enum
  (*In/ut*, *Nätverk*, *Omvandling*) rather than the product wording English
  shows, and now mirror it: *Filer och data*, *Appar och tjänster*,
  *Ändra data*.

- **Plain language across every surface a non-technical person reads.** A
  read-through of every page a viewer or editor can reach turned up two
  clusters and both are gone. The canvas talked like a circuit diagram —
  *wire*, *pin*, *upstream*, *downstream* — in the 151 step descriptions, the
  605 params-schema help texts, the runtime error strings and the UI
  catalogue; it now says *connect*, *input*/*output*, and *earlier*/*later
  steps*. And the Apps pages had never had the drop→step / node→step /
  graph→flow rename at all: "the Download drop exports them", "no key on the
  node", "embedding it in graph JSON". Both languages moved together. Genuine
  other senses were left alone on purpose — "Drop a meeting onto a calendar",
  "drop rows", a map pin, and `node`/`nodes` where they name an actual param
  key or JSON shape.

- **Implementation words are out of copy a business user reads.** Flow
  Settings → General no longer mentions the *daemon* ("the daemon's default
  still applies", "Stamped by the daemon"); Plan & usage no longer says
  "Usage metering is not enabled on this deployment"; and the form's
  last-resort message no longer reads "Top-level schema isn't a property bag".

### Removed

- **The "notify me on ntfy with the approval link" button.** It was a
  one-click shortcut that built an ntfy step next to an `await_approval` step
  and wired it up. Now that the step emails its approvers directly, the
  shortcut points at the harder path — and the manual route it automated is
  unchanged: `pending_url` still carries the link into any notify step you
  want.

### Fixed

- **Approvals taken from an email link were never recorded in the audit
  trail.** The authenticated inbox path audited through the caller's
  principal, but the HMAC `/approve/{run}/{node}` path — the one used from a
  notification, by whoever holds the URL, and therefore the one with no proven
  identity — wrote nothing but a line to the daemon log. It now appends to the
  same trail, with the tenant read off the graph record and the name recorded
  as `"someone@example.com (self-declared, via approval link)"` rather than
  passed off as a verified subject.

- **The Swedish UI documented a broken secret reference.** Flow Settings →
  Secrets told Swedish readers to write `${secret.NAMN}` — the code literal
  had been translated along with the prose, so the documented syntax simply
  did not work. It is `${secret.NAME}` again.

- **Engine vocabulary was showing on an admin page.** The organization's flow
  limits listed "Max antal grafnoder" (max graph nodes) where English said
  "Max steps per flow".

- **Two full-width buttons were centred when they should have been
  left-aligned.** The global `button {}` rule sets `justify-content: center`,
  and both `.help-link` and `.sidebar-collapse-toggle` set `display: flex`
  without overriding it — so Help → Contact support (a `<button>`) sat
  centred beside Documentation (an `<a>`, which defaults to `flex-start`),
  and the expanded sidebar's collapse row sat centred under nav items that
  were not. `text-align: left` was already on both, which does nothing to
  flex children.

- **Swedish gender agreement, and an email header where an account should
  be.** Around fifteen neuter/common errors (*ett stegtyp*, *steget … den*,
  *Steg tillagd*, *avstängd* for a step) are corrected, and the account menu's
  fallback no longer reads "(inget subjekt)" — the email-header sense of
  *subject* — where it means an unknown account.

- **Two English typos.** The file drop zones read "**Step** files to upload"
  and "**Step** a file here or click to browse", collateral from a global
  drop→step replace; both are drag-and-drop targets and say **Drop** again.

### Developer

- **The Swedish drop catalogue can no longer rot silently.** Every lookup in
  `dropText.ts` falls back to the English it was handed, so a missing or stale
  translation renders perfectly good English and nothing says so — which is
  how six untranslated step descriptions and four that had gone stale against
  reworded English sat unnoticed. `make catalogs` regenerates a snapshot of
  the live registry, `make catalogs-check` fails if it is out of date (CI runs
  it), and a new test names every step or app whose Swedish is missing, stale
  or orphaned. Coverage is 151/151 steps and 35/35 apps. `make ci` also runs
  the web test suite now, which it had skipped while calling itself a full
  mirror of CI.

## [0.8.0] - 2026-08-21

### Added

- **Mirror your flows to your own git.** A workspace's flows have always been
  stored as a real git repository — `graphs/<id>.json`, one commit per save
  with the author in the message, the live revision as a tag — but that
  repository only existed inside the daemon. It can now be pushed to a remote
  you own: GitHub, GitLab, Gitea, sr.ht, or a bare repo on your own box.
  Configure it under **Git credentials → Mirror to your own git**.

  The mirror is a real clone, not an export: full history, every flow, and the
  published-revision tags, so you can read it, diff it, review changes in a
  pull request, and restore from it. Choose whether it pushes when a flow is
  published (the default — the mirror tracks what you have actually shipped)
  or on every saved change.

  Authentication is an **SSH key only**, from one of the Git credentials you
  already manage; the credential picker lists only the ones that have a key.
  A PAT would work at the protocol level and the credential store holds one,
  but a deploy key is scoped to a single repository, which is what an
  unattended continuous push should be — it needs read *and* write access,
  since the push lists the remote's refs before transferring to work out
  which are stale. Host keys are verified against the same pinned set the
  `git_checkout` step uses — github.com, gitlab.com and git.sr.ht are built
  in; anything else needs a `known_hosts` line on the credential.

  The remote is treated as a **replica**: the push forces every ref and
  deletes the ones that no longer exist locally, so a commit made directly on
  the remote is overwritten by the next push. Forcing isn't incidental —
  editor autosaves amend the previous commit, so a workspace's history is
  legitimately rewritten during ordinary editing and a non-forced mirror
  would start rejecting pushes almost immediately. Point it at a repository
  nothing else writes to.

  Because that push is destructive, it will not touch a remote it shares no
  history with. The case that matters: a deployment whose data volume was
  lost comes back with an empty workspace and would otherwise mirror it over
  the very repository it should have been restored *from*, deleting every
  flow. A wrong URL and a repository someone else's project owns fail the
  same way. Automatic mirroring can never override this; **Push now** offers
  an explicit "overwrite the remote" confirmation for the case where
  repointing a mirror really is what you meant. One recognised ref is enough
  to count as shared history, so an ordinary push and an amended autosave
  both pass without a prompt.

  A failed push never fails the save or publish that triggered it. The
  outcome is recorded per workspace and shown on the settings panel, with the
  git error verbatim ("permission denied (publickey)", "host key mismatch")
  and the last time a push actually succeeded — so a mirror that quietly
  stopped working three weeks ago reads differently from one that failed a
  minute ago. **Push now** runs a push synchronously and reports the result,
  ignoring the automatic-mirroring switch, which is how you test a new remote
  or a rotated key before turning it on.

  One caveat worth knowing before pointing this at a **public** repository:
  webhook trigger keys live in a flow's JSON by design (the `/trigger`
  endpoint authenticates callers against them), so they travel to the mirror
  with everything else. Real credentials don't — those are `${secret.…}`
  references resolved at run time — and the existing lint still flags a
  provider token pasted into a step. Mirror to a private repository.

- **⌘K reaches settings and administration.** The command bar indexed
  workspace pages and flows only, so everything *configured* rather than
  browsed — credentials, secrets, API keys, the audit log, the new git mirror —
  could not be reached from it at all. Those destinations are listed now
  (behind the same permission bar the Admin index itself uses), each with
  search terms that don't appear in its name: "backup", "mirror" or "sync"
  find git mirroring, "logs" finds run history, "members" finds People. The
  aliases carry English and Swedish together, since matching is a substring
  test and the active locale is irrelevant — the same reasoning as the step
  palette's Swedish aliases.

### Fixed

- **The git page was unfindable for what most people want it for.** Its Admin
  card read "SSH keys or access tokens for cloning private repos", which says
  nothing about mirroring, so someone looking to back their flows up to git
  had no reason to click it — and no other route in except the `git_checkout`
  step's account picker. The card and the page are now "Git credentials &
  mirroring", and the editor's Settings → General points at it from the
  tenant/workspace field, where "where does this flow live?" is already the
  question on screen.

- **nShift and Roaring had blank app pages.** Both connectors shipped without
  the curated prose their `/apps/:slug` page renders, so the page showed a
  title and nothing else — no explanation of what the app does, how it
  authenticates, or what it costs you to get wrong (nShift books billable
  consignments). Both are written now, in English and Swedish. A new test
  fails the build when a connector is added without them, checked against a
  generated list of what the catalog actually ships (`make integration-catalog`),
  so the next one can't slip through the same way.

## [0.7.4] - 2026-08-21

### Fixed

- **The Apps index showed a green dot for a broken connection.** The grid
  only knew "set up" or "not set up", so an app whose account had stopped
  working sat under *Connected* with the same green dot as a healthy one —
  and the index is the first place someone looks when a flow starts failing.
  A connected app with an account that needs reconnecting now shows amber,
  with a tooltip saying so, matching what its own page reports.

## [0.7.3] - 2026-08-21

### Fixed

- **The Apps page waited for a run to notice a dead connection.** 0.7.1 could
  only learn that a grant was dead when something used the token, so the page
  went on saying "connected" until the next scheduled run happened to fail —
  which is precisely when someone is standing on that page wondering why their
  flow broke. Listing the connections now refreshes any account whose access
  token has already expired, which is the same work the next run would do,
  just sooner: a rejected refresh marks the account, a successful one clears it
  and stores the fresh token. An account whose token is still valid costs
  nothing — no network call, nothing to report.

## [0.7.2] - 2026-08-21

### Fixed

- **The fix-it link in the run timeline still went to the Apps index.** 0.7.1
  deep-linked the failure banner at the top of a run, but each step expanded in
  the timeline renders its error through its own component, which was still
  sending people to the list of every app. It now deep-links too — and more
  precisely than the banner, because it knows which step you opened rather than
  which step failed first.
- **Two undefined CSS variables broke the build.** The connection-account rows
  added in 0.7.1 referenced `--radius-md` and `--text-muted`, neither of which
  exists — they render as nothing, which is what `check-css-tokens` fails the
  build over. Now `--r-2` and `--warning`, the tokens the rest of the
  stylesheet uses.

## [0.7.1] - 2026-08-21

### Added

- **Reconnect, per account.** An app's connection card now lists each
  connected account on its own row, with its own state and its own
  **Reconnect** button, which re-authorises *that* account in place instead of
  adding a second one. Previously the card could only say "connected" and the
  only action on offer was "Connect another" — which, confusingly, was the
  reconnect people needed all along, just labelled as though it weren't.

### Fixed

- **A dead OAuth grant showed as connected.** When a Google (or any OAuth)
  grant is revoked, expires, or is invalidated by a password change, every run
  fails with a 401 from the provider — while the Apps page went on reporting
  the account as connected, because all it could see was whether a token
  existed. Nothing anywhere said the one thing that mattered: sign in again.
  The daemon now records the rejection at the moment it happens — a refused
  token refresh is the point where the grant is known to be dead rather than
  the call merely unlucky — and clears it as soon as a refresh works or a new
  token is stored. `GET /oauth/providers` reports those accounts as
  `needs_reconnect`, and the app's card marks them and offers the fix. This is
  distinct from the existing `stale_accounts`, which is about scopes added
  since; that check is deliberately skipped for providers authorised
  incrementally (Google), which is exactly where this gap bit.

  Known limit: the signal comes from a rejected *refresh*. A token stored
  without an expiry or a refresh token is never refreshed, so a grant that
  dies in that state is still only visible as a failing run. Marking on a 401
  from the API call itself is the remaining half.
- **A failed run's fix-it link went to the Apps index.** "Reconnect" on a
  failed run dropped the user on the list of every app, leaving them to work
  out which one broke — and then, on the right app, to work out which account.
  The error text can't say: "Gmail returned 401" names neither. The failing
  step does, though — its module gives the app and its settings give the
  account — so the button now lands on that app with that account called out
  (`/apps/gmail?reconnect=default`).

## [0.7.0] - 2026-08-21

### Added

- **The scenario corpus is now RUN, not just validated** (`tests/journey/usecases_run_test.go`).
  Eleven shapes — a loop handing a step structured data, a read-act-write-back
  round trip (on three different services), collecting loop results,
  transition-only firing, tolerating a dead channel, surviving an upstream
  outage and recovering from it, pausing for a person and resuming, an AI
  judgement routing the original submission, and dedupe across runs — are
  saved through the real API,
  published, fired and waited on, with every outside service mocked by a
  stateful fake (`fakesaas_test.go`, including a small SMTP server). The
  assertions are on what the world received. Every applicable test runs its
  flow a second time, because eleven scenarios promise nothing happens twice,
  and two inject faults. That layer found the two fixes below.
- **`sheets_update_cells` (Google Sheets — Update cells), plus row numbers on
  Read range.** A spreadsheet could be read and appended to, never changed —
  so no flow could mark a row done, and every "handle the new rows" job had to
  keep a private ledger of what it had already processed and filter against it.
  Read range can now include each row's real position in the sheet (`_row`,
  opt-in), and Update cells writes values back into exactly those rows, adding
  a column with its header when the sheet doesn't have it yet. Read what's
  outstanding → act → mark it done → skip it next time.
- **`gmail_get_thread` (Gmail — Read conversation).** "Has anyone answered?" is
  a question about a conversation, and every mail step worked on single
  messages; Gmail has no "unanswered" search operator either. This returns a
  thread's messages and answers the question directly — Replied is No while the
  newest message in the thread is still one of yours — plus a one-row Summary
  per conversation for collecting a table of what's outstanding.
- **`site_check` (Is it up?).** Watches a site and fires only on the
  transitions: once when it breaks, once when it recovers, nothing in between,
  so an outage pages you once rather than twelve times an hour. A site already
  down on the first check does fire. Optionally requires a phrase on the page,
  which catches a server answering 200 with an error page. Distinct from
  `web_watch`, which compares content and treats a bad response as a failure.
- **`stripe_get_customer` (Stripe — Get customer).** Every payment and
  subscription event names the customer by `cus_…` id, and Stripe's search
  cannot look up by id — so "email whoever just cancelled" had no path at all.
- **String helpers in formulas.** `substring`, `split`, `join`, `replace`,
  `trim`, `lowerAscii`, `upperAscii`, `indexOf`, `charAt` are now in scope for
  the row formulas (calculated columns, filters, routing) and the Expression
  step alike. Without them "the first ten characters of the date", "tidy these
  addresses" and "shorten this into a title" needed an extra step, or couldn't
  be said.
- **Steps can be marked non-critical** (`continue_on_error` on a node). The
  tolerate-this-failure policies live on connections, so a step at the end of a
  branch — a notification, a final write — had nowhere to hang one and always
  failed the whole run. In a fan-out that's wrong: Discord being down is no
  reason for the Slack post and the email not to count.
- **More fields take a wire.** Create event's start, end, description, location
  and attendees (start/end also accept relative values like `tomorrow+9h`, while
  an absolute plain date still means an all-day event); Slack's
  reply-in-thread, so a bot can answer under the message that summoned it;
  Drive's file name, so a weekly backup can be dated instead of overwriting
  itself.

- **A generator eval built on the scenario corpus** (`make flowgen-eval`). The
  thirty-five plain-language asks in `/SCENARIOS.md` are each already paired
  with a known-good graph, which makes them an eval set for the AI flow
  generator: feed each scenario's own words to the same generator the editor
  calls, and score the draft on whether it passes the save gate, picks the same
  kind of trigger, and reaches the services the job needs. The three are scored
  separately because a flow that does nothing passes the gate. Live runs are
  opt-in (they cost money and aren't deterministic) and report rather than
  fail; two parts run in ordinary CI with no model — one checks the corpus and
  the scorer stay honest, the other drives the whole eval against a scripted
  model so the live path can't rot.

### Fixed

- **The approval link never reached anyone.** The await-approval step tells you
  to put it before the step that notifies a person and wire its link into that
  notification — but the dispatcher treated a parked step as having produced
  nothing, so the notification only went out *after* the approval. Nobody was
  ever told there was something to approve. A parked step's emitted ports are
  now live at once, while the ports that arrive with the decision keep their
  branches waiting rather than skipping them; re-dispatch on resume is a no-op,
  so nobody is notified twice. Affects every approval flow and the
  `approve-before-refund` template.
- **A loop where every item failed now fails.** Carrying on past a bad row is
  right for one row among many; reporting success when nothing worked is an
  outage dressed up as a result — and a following step that records the work as
  done then records work that never happened. Partial failures continue and
  surface on the `errors` port exactly as before.
- **`make test` could not finish.** The daemon package takes about 13 minutes
  under the race detector, past Go's default 10-minute per-package ceiling, so
  the suite failed on a timeout and blamed whichever test was running when the
  alarm fired. `make test`, `make ci` and CI now allow 30 minutes (per package,
  so it is headroom for the slowest one, not a slow-suite allowance).
- **The flow generator's instructions couldn't build a loop.** Its hand-written
  guidance said to "wire for_each.body into the per-item step's input" — the
  documented footgun, since the body pin is a control pin and pointing it at a
  typed input injects the whole row where a string was expected — and never
  mentioned `${item.…}`, so nothing told the model how a step inside a loop
  reads the current item. A model could only recover by choosing to call
  describe_drop on for_each. The guidance now states the mechanism, including
  that a param whose whole value is one reference keeps the value's real type.
- **Compare reads numeric text as a number.** Steps report counts, status codes
  and spreadsheet cells as text, so "is this count greater than 0" failed with
  `non-numeric operand in <,> comparison: string vs float64` — a message the
  author can act on only by giving up, since no step converts text to a number.
  Numeric text is now read as the number it plainly is, on either side of the
  comparison; text that isn't a number still fails rather than counting as zero.
- **A loop can hand a step structured data.** A For each body's steps see the
  current item only through `${item.…}` in their own settings, and those were
  resolved as text — so a step needing an object or a list (a shipment address,
  an email template's merge data, a set of invoice lines) received JSON as a
  *string* and couldn't read it. "One X per row" only worked when X needed
  nothing but scalars. A setting whose whole value is one `${item.…}` reference
  now keeps the value's real shape, exactly as `${resource.…}` already did;
  inline references and scalars are unchanged.


## [0.6.0] - 2026-08-20

### Added

- **Attachments can be read off incoming email.** `gmail_get_attachments`
  (Gmail — Download attachments) saves the files attached to a message and
  hands them on: `first` is a file ref that wires straight into Upload to
  Drive / Write file / an email's Attachments, and `files` lists them all with
  name, type, size and path. "Only these types" takes just the PDFs and ignores
  signature images, which are skipped anyway (they carry no filename). Files
  land in the run's scratch area by default, or a workspace folder if one is
  named; sender-controlled filenames are sanitised, so `../../etc/passwd`
  cannot escape the sandbox. Dazyflow could already *send* attachments — this
  closes the other half, and with it the "file the invoices people email me"
  job, which previously had no path at all.
- **`web_watch` (Watch a page — tell me when it changes).** Pair it with an
  Interval trigger and it fetches a page, compares it with what it said last
  time, and emits `on_change` only when it actually changed — so, like an
  unused Branch port, everything downstream stays dormant on a quiet check. The
  first check baselines silently. It compares the visible words rather than the
  HTML by default, so a rotating CSRF token or an asset hash doesn't cry wolf,
  and a "Watch just this" pattern narrows it to one price or status line.
  Watching an arbitrary page previously meant four steps and a hand-rolled
  collection to diff against.
- **Five templates**, covering the requested jobs that were buildable but
  laborious to assemble: Stripe payment → thank-you/team ping/sales log, AI
  inbox triage, approval before a refund, invoices emailed to you → filed in
  Drive, and watch a page → ping my phone (which needs no connected account at
  all).
- **`join_rows` gained `kind: "anti"`** — the left rows with no match on the
  right, carrying only their own columns. This is the "which of these haven't I
  processed yet?" question every sync asks; see the matching fix below for why
  the left-join answer was a trap.
- **Relative time windows on Google Calendar.** `time_min`/`time_max` now
  accept `now`, `today`, `tomorrow`, `yesterday`, `+3d`, `-2h30m`,
  `tomorrow+9h` as well as absolute timestamps, take a `tz` for the day
  boundaries, and can be wired from an upstream step. A nightly reminder flow
  can finally say "tomorrow" and mean it on every run; before, the field took
  RFC3339 only, so the window had to be left wide open and filtered afterwards.
  The grammar lives in `drops/internal/reltime`, shared with the Date step's
  offset parser.
- **The Regex step's text can be typed on the step.** Inside a For each there
  is no upstream node to wire from, so `text: "${item.description}"` is now how
  a loop body reads a field. A wired input still wins.

### Fixed

- **Formulas can produce rows and objects again.** A CEL expression returning a
  map — `{'payment_id': input.id}`, or a `map()` over rows — came back from
  cel-go as `map[any]any`, which `encoding/json` refuses and every row consumer
  rejects with "expected object, got map[interface {}]interface {}". So the
  obvious way to shape data for a "log this" step passed every validation and
  then failed at run time. `unwrapCEL` now normalises composites recursively,
  which fixes both `expression` and a `compute_rows` column whose formula
  returns an object.
- **`group_aggregate` accepts the short form.** `{"revenue": "sum"}` — the op
  alone, with the output name doubling as the source column — now works
  alongside `{"revenue": {"op": "sum", "column": "revenue"}}`. `sort_rows`
  takes a friendly string, so the nested-object requirement next door was a
  stumble that only the editor's form hid.
- **ntfy's "Link to open" takes a wire.** Its own help text says to wire an
  approval step's link into it, but it was a typed setting with no input port,
  so the link had to be spliced into the message body by an extra step.


- **Emailed links now open in the right org.** The "View run details" link in a
  flow-failure email pointed at `/runs/<id>` with no org in it, and the customer
  side of the support-ticket emails pointed at `/support/<id>` the same way.
  Because the app's routes carry no org segment — the active org is browser state
  plus the session's scope — those links opened against whichever org that
  browser last used, so anyone who belongs to more than one org usually landed in
  the wrong one and was told the run or ticket did not exist (the tenant-scoped
  loaders answer `ticket_not_found` / no such run).

  Such links now carry `?org=<tenant>` (the same query key the sign-in page
  already uses), and the app honours it on boot: it re-scopes the session server
  side when needed and then lands on the deep-linked page rather than dumping the
  user at `/`. An `?org=` naming an org the user cannot act in is ignored, so a
  forwarded or hand-edited link can't disturb a working session.

  The Stripe checkout and billing-portal return URLs are pinned the same way:
  `/usage` is org-scoped and Stripe hands the user back to a browser whose active
  org may have moved on (switching org in another tab mid-checkout is enough), so
  an unpinned return could show the wrong org's usage right after an upgrade.
  Those two URLs also now trim a trailing slash off the configured base, so a
  `DAZYFLOW_PUBLIC_BASE_URL` ending in `/` no longer produces `//usage`.

  Only genuinely tenant-scoped links are pinned. The support **agent** queue
  resolves tickets cross-tenant by design, so its links are deliberately left
  unpinned — agents generally aren't members of the filing org, and moving them
  there would be wrong as well as useless. Token-bearing links (invite, email
  verification, password reset, signup) identify their org through the token and
  are unaffected.

### Changed

- **Shared the location connectors' coordinate helpers.** `weather`
  (OpenWeather), `openmeteo`, `smhi` and `geo` each carried their own copy of
  the same "lat,lon" parser, range check, numeric-param reader, unit symbols,
  number formatters and SSRF/transport error prologue — four copies whose
  bodies and user-facing error strings had never diverged. They now live once in
  `drops/internal/geoloc`, along with the one-shot `Probe` the OpenWeather and
  Open-Meteo connection verifiers both used to hand-roll. Connector behaviour
  and every error string are unchanged; the tests that pinned them moved to the
  shared package as the union of what the four asserted separately.
- **Split the HTTP gateway by concern.** `daemon/httpgateway.go` had grown to
  2,822 lines and 71 declarations covering the route table, static asset
  serving, session cookies, CORS/CSP, sign-in and role elevation, org profile
  routes, run listing, API keys, run control, SSE streams and the response
  writers. It is now twelve files named for those concerns, with the route table
  alone in `daemon/httproutes.go`. No behaviour change. `route_sweep_test.go`
  now globs the package's source files instead of naming two of them, so the
  sweep can't go stale when the gateway is split again (it also no longer scans
  `httpsecrets.go`, which had stopped registering routes).

## [0.5.0] - 2026-08-20

### Added

- **Undo/redo in the flow editor** — `Cmd/Ctrl+Z`, `Cmd/Ctrl+Shift+Z` and
  `Ctrl+Y`, plus toolbar buttons that stay visible (disabled) so the feature
  and its shortcut are discoverable before you need them. Built on
  whole-document snapshots rather than a command stack: the editor already
  maintains a complete serializer and deserializer that saving and loading
  depend on, so history cannot fall behind the document the way a stack of
  per-action inverses would. Snapshots are applied by reconciling the node and
  edge arrays, preserving object identity for everything unchanged, so undoing
  one step's move re-renders one card instead of the whole canvas. Continuous
  gestures coalesce — a drag or a run of keystrokes in one field is a single
  undo — and the stack is fenced whenever the document is replaced from
  outside the editor, including an edit arriving over the MCP flow-watch, so
  undo can never silently discard an assistant's change.

### Changed

- **"Drop" is now "step" (Swedish: *steg*) everywhere a person can read it.**
  The documentation already said step — 38 uses, a glossary entry, a "Step
  catalog" — while the UI said drop in 106 strings, so this closed a
  docs/UI mismatch rather than choosing new vocabulary. 104 English and 96
  Swedish strings changed, along with the MCP tool descriptions, the operator
  docs, `.env.example`, and the user-visible Go error strings. Swedish gender
  agreement moved with it: *steg* is neuter where *dropp* was common, so 49
  determiners and adjectives shifted (`den här droppen` → `det här steget`).
  Deliberately unchanged, and now the convention: the Go catalog and package
  paths, API routes and JSON field names, MCP tool *names* (`list_drops`,
  `describe_drop`), error codes (`drop_not_found`), audit action names, CSS
  classes, frontend identifiers — the contract every non-human consumer is
  grounded on — and the verb "to drop" ("drop rows", "drop a pin", "drop to
  upload"). `describe_drop`'s description now tells an assistant to say
  "step" to the user, so the split doesn't leak into a conversation.

- **`make patch` / `minor` / `major` promote the changelog themselves.** The
  release recipe moves `[Unreleased]` under the new version heading, leaves a
  fresh empty one, and commits it with `./VERSION` before tagging. This was
  previously a manual step the Makefile only *documented*, and it drifted:
  0.3.0, 0.3.1, 0.3.2 and 0.4.0 were all tagged with no changelog entry
  because nothing checked. An empty `[Unreleased]` now aborts the release,
  and a hand-written version heading is detected and left alone.

- Detail pages share one `BackLink` component instead of five hand-rolled
  copies. Two admin pages had been overriding the shared `.back-link` class
  with a duplicated inline style, putting them at double the bottom margin of
  every other detail page; labels now consistently name the parent
  ("Organizations") rather than mixing that with "Back to runs" and a bare
  "Back", which also reads better to a screen reader.

### Security

- **A store read failure could no longer be mistaken for "this flow doesn't
  exist".** `saveGraph` treated *any* error from the workspace store as the
  new-flow case, and the new-flow path skips the per-flow ownership gate and
  the active-run lock and enforces only the weaker `graph:edit`. A corrupt
  object or a transient I/O fault was therefore enough to let a non-owner
  overwrite an existing private flow. The store now returns a typed
  `ErrGraphNotFound` and every other error fails closed. The same fail-open
  shape in the delete path — which reported success without checking edit
  permission — was fixed with it.

- **Google sign-in is bound to the browser that started it.** The callback
  consumed its state token with no cookie check, so an attacker could complete
  a sign-in in a victim's browser for the attacker's account, and anything the
  victim went on to create or connect landed in the attacker's organization.
  The integrations OAuth flow already had the correct binding pattern; it had
  simply never been applied to the sign-in leg. The binding is mandatory here
  rather than skipped when absent, because sign-in has exactly one start path.

- **Org Google OAuth client secrets are encrypted at rest.**
  `org_auth.google_client_secret` was a plain `TEXT` column while every
  comparable secret in the system was encrypted, so a database dump exposed
  every organization's live client secret. Secrets now live in the per-tenant
  encrypted store under a per-tenant DEK, the column is written empty, and
  rows written before this migrate on first read with no operator step.

- **Secret redaction no longer leaks when one secret contains another.**
  Replacement ran in map-iteration order, so replacing a shorter secret first
  cut it out of the middle of a longer one and the longer secret's tail
  survived into the persisted run record in cleartext — intermittently, which
  is worse than always. Secrets are now replaced longest-first.

- The `DAZYFLOW_DEV_KEY` dev admin token is refused when the deployment
  doesn't look local (a public base URL or a remote database). It mints a
  publicly-known admin bearer token at every boot and was previously guarded
  only by a line in the documentation.

- bcrypt cost raised from 10 to 12, with an opportunistic re-hash on
  successful login — the only moment a correct plaintext is available, so the
  only place an old hash can be strengthened without involving the user.

- The audit trail no longer accepts forged entries. The failed-sign-in path
  records the email as typed — it must, that address is the credential-stuffing
  signal — and nothing had validated it, so a newline forged a second line in
  a compliance-relevant log.

- A Content-Security-Policy is set on the authenticated app surface, and the
  `shell` and `git` steps resolve their working directory through an
  `os.Root` handle instead of cleaning the path as a string, so a symlink
  planted inside the workspace can no longer be followed out of it.

### Fixed

- Postgres and the in-memory job store now agree on `core.ErrConflict` for a
  duplicate enqueue; they had diverged because the conformance test only
  asserted that *some* error came back.

- Error-to-HTTP-status mapping uses typed sentinels instead of matching
  substrings of user-facing messages, so rewording a message can no longer
  change a status code. Authorization failures that wrapped
  `core.ErrUnauthorized` without the exact phrase the matcher looked for had
  been returning 500 instead of 403.

- 27 steps that perform non-idempotent external writes now opt into
  engine-side write dedupe, up from 10 — so an expired-lease reclaim replays
  the recorded result instead of posting, charging or sending twice. Slack,
  Notion, Fortnox, GitHub, Stripe, Drive, calendar, MQTT, email, ntfy and
  webhook sends were all uncovered.

- MCP idempotency keys are hashed over canonical JSON. The key was hashed over
  raw argument bytes on the assumption that a retry sends byte-identical JSON,
  which the protocol doesn't guarantee — so a host that re-serialized its
  arguments produced a new key and a duplicated side effect, in exactly the
  situation the key exists to make safe.

- `on_error` is validated when a graph is saved, so an unrecognized value can
  no longer be accepted and then silently ignored, quietly downgrading a
  fallback edge to abort.

- Panics in drop-spawned goroutines are recovered instead of taking down the
  daemon — notably `for_each`, which runs arbitrary per-item node execution.
  The engine's recover only covers the calling goroutine.

- 31 steps declared no `meta` output port while emitting one, so a third of
  the catalog produced data the editor could not wire and an assistant reading
  the manifest could not see. A new contract sweep mutates one parameter of a
  valid worked example at a time, which reaches the connector code paths the
  previous adversarial sweep could never get past parameter validation.

- Unparseable `DAZYFLOW_*` values log the fallback instead of discarding the
  operator's setting silently; a missing database DSN is reported before a
  malformed SMTP URL can kill the process; and the HTTP listener's drain is
  awaited before exit so in-flight SSE streams and uploads aren't cut.

## [0.4.0] - 2026-08-19

Everything below shipped between 0.2.0 and 0.4.0. The 0.3.0, 0.3.1 and 0.3.2
tags were same-day interim cuts rather than distinct milestones, and none of
the four carried a changelog entry at the time — the release recipe only
documented the step instead of performing it (fixed in `[Unreleased]` above).
They are folded into this one heading rather than split retroactively, because
the boundaries can no longer be reconstructed accurately from the entries.

### Added

- **Support dashboard** (Phase 3 — the Support feature is now complete) — the
  cross-org queue grew the tools a support team actually works from: **assignment**
  (claim a ticket, hand it to a colleague, release it back to the pool — only
  provisioned support agents can be named), **ownership + status filters** on the
  queue (`?assignee=me`, `?unassigned=true`, `?status=`), and **stat tiles**
  counted server-side over the whole queue (`GET /support/tickets/summary`), each
  tile doubling as the filter for what it counts. **Role separation** was tightened
  in both directions: the customer's view of a ticket no longer carries the support
  organisation's internals (who owns it, which individual replied), the requester
  can close or reopen their own ticket but only support can declare it *resolved*,
  and a platform admin still isn't support staff (`platform:admin` does not imply
  `support:agent`). Every assignment is audited into the **org's own** log. The
  feature is now documented for operators: `DAZYFLOW_SUPPORT_ENABLED` in
  `.env.example` and a *Support tickets & consented flow access* section in
  `docs/DEPLOY.md`.
- **Support tickets + chat** (native, Phase 2 of the Support feature) — an org
  member can file a ticket about a flow and chat with support in-app; support
  agents work a cross-tenant queue and reply/resolve. Filing auto-attaches a
  **redacted** diagnostic bundle for the referenced flow/run (structure + error,
  no secrets or run data), so support can help the common case without a live
  read-only grant. Chat bodies are secret-scrubbed on ingest. New "Report a
  problem" action on the run-failure page and a Support section in the nav.
  Gated by `DAZYFLOW_SUPPORT_ENABLED`; `whoami` exposes `support_tickets_enabled`.
- **Actionable "contact support"** — the operator-configured support contact is
  now a real link beyond the Connections page: on generic errors (flow editor)
  and, prefilled with run diagnostics, on the run-failure page.

- **Fortnox connector** (`drops/fortnox/`) — Sweden's dominant SMB accounting:
  create customer, create invoice, list invoices (paid-invoice poll source),
  and a customer picker. OAuth 2.0 via `client_secret_basic` (new daemon
  support, below).
- **46elks connector** (`drops/elks/`) — send SMS via the Swedish/Nordic 46elks
  API. Static-credential (HTTP Basic) service connection; no daemon changes.
- **Klarna connector** (`drops/klarna/`) — Order Management: get order, capture
  (full/partial), refund (full/partial). Static-credential (HTTP Basic) service
  connection, region-hosted (EU/NA/OC × prod/playground); no daemon changes.
  Money-moving POSTs are retry-off + write-deduped (no upstream idempotency key).
- **nShift connector** (`drops/nshift/`) — Nordic multi-carrier shipping over the
  Unifaun ExtAPI: create a shipment (book), get a shipment (status/tracking), and
  delete an unprinted draft (cancel). Static-credential (Bearer API key) service
  connection, environment-hosted (integration/production, defaulting to the
  sandbox); no daemon changes. Booking/delete are retry-off + write-deduped.
- **Roaring connector** (`drops/roaring/`) — Nordic company-data enrichment:
  company overview (org number → registered name / status / full record) and
  company search (name → candidate matches). Uses Roaring's OAuth2
  client-credentials grant, exchanged for a bearer token by the connector itself
  and cached in-process — so it's a static-credential (Consumer Key + Secret)
  service connection with no daemon OAuth changes.
- **Phone value drop** (`drops/value/`) — validate and normalize a phone number
  to E.164 with a default-region setting (libphonenumber), emitting country,
  national number, and type; the flow editor shows a live country flag beside
  the field for international input. The SMS-input sibling of the `url` drop.
- **OAuth `client_secret_basic` support** in the daemon's OAuth registry
  (`daemon/oauth.go`) — token requests can present client credentials in an
  HTTP Basic header instead of the form body, selected per provider via
  `TokenAuthStyle: "basic"`. Fortnox requires it; all existing providers keep
  the default form-body behavior.
- **Runs date-range filter.** `GET /api/v1/me/runs` and
  `GET /api/v1/me/flows/{flow_id}/runs` accept `since` and `until` query params
  (RFC3339 timestamp or bare `YYYY-MM-DD`) bounding a run's enqueue time —
  `since` inclusive, `until` exclusive. The Runs page gains a From/To date
  picker that resolves a selected day to local-midnight instants, so filtering
  is server-side and paginates correctly instead of narrowing only the rows
  already loaded.

### Changed

- **Twilio, Discord, MQTT, and Stripe now use a first-class service connection**
  (`ConnectionFields`) instead of loose secret references, so each gets a proper
  entry form on the Apps page (the same shape as ntfy / Home Assistant / SMTP)
  and a "connected / needs setup" state — previously you had to create the
  secret by hand in the secrets manager. Credentials are entered once and are no
  longer node params, so they never appear in the graph.

  **BREAKING — re-enter credentials once after upgrading.** The credentials move
  to per-tenant connection storage; the old secret names are no longer read:
  - Twilio: `TWILIO_ACCOUNT_SID` / `TWILIO_AUTH_TOKEN` → `conn.twilio.*`
  - Discord: `DISCORD_WEBHOOK_URL` → `conn.discord.webhook_url`
  - MQTT: `MQTT_USERNAME` / `MQTT_PASSWORD` (and the per-node `broker` param) →
    `conn.mqtt.*` (broker is now part of the connection)
  - Stripe: `STRIPE_API_KEY` → `conn.stripe.api_key` (the webhook triggers'
    `STRIPE_WEBHOOK_SECRET` is unchanged — it's verified server-side)

  Open each integration on the Apps page and enter its credentials once. Flows
  themselves need no edits.
- Realigned the OpenAPI definition of `GET /api/v1/me/runs` to the parameters
  the endpoint actually accepts (`status`, `since`, `until`, `limit`, `offset`,
  `workspace`, `tenant`); it had drifted to aspirational `from`/`to` plus
  `PageToken`/`PageSize`/`Sort` that were never implemented.

### Security

- Bumped `github.com/go-git/go-git/v5` to v5.19.2, clearing GO-2026-6214 (path
  traversal via crafted reference names) and GO-2026-6213 (worktree operations
  following symlinks). Both were reachable from our own call graph — the
  reference paths through `workspace.Store.SetRevisionLabel` /
  `PromoteToEnvironment`, and the worktree paths through the `git_checkout` /
  `git_diff` drops — so CI's symbol-granularity `govulncheck` failed on them
  rather than reading them as uncalled.

- **Secret references are no longer resolvable out of flow data.** The
  whole-string `secret://NAME` form was matched AFTER `${upstream.…}` /
  `${item.…}` substitution, so data a flow ingested from the outside world — a
  webhook body, an HTTP response, a form field, a spreadsheet cell — was
  re-interpreted as a credential reference. Anyone able to influence that data
  could read any secret in the organization by supplying the literal text
  `secret://NAME`, connection credentials (`conn.<slug>.<field>`) included, and
  through the `vault://` / `aws://` / `gcp://` schemes anything in the tenant's
  cloud secret manager. Redaction did not contain it: the drop received the
  plaintext in its params regardless of what the persisted run detail showed.
  The reference is now matched against the raw parameter only, before any
  substitution runs — mirroring `SubstituteString`, which likewise never
  re-scans its own replacements. Author-written references are unaffected.
- Stored secrets and wrapped DEKs are now bound to their row with AES-GCM
  additional authenticated data (`(tenant, name)` for a secret, `tenant` for a
  DEK). Without the binding, GCM proved only "sealed under this tenant's DEK",
  so an attacker with database write access could relocate a ciphertext — copy
  `conn.stripe.api_key`'s blob into a secret their flow may read — and recover
  the plaintext through an ordinary reference. Existing ciphertext keeps
  decrypting (an unbound open is attempted as a fallback) and upgrades to the
  bound form as values are rewritten; `--rotate-master-key` upgrades every DEK.
- `Vary: Origin` is now sent on every response, not only when the request's
  Origin matched. A shared cache could otherwise store one origin's
  `Access-Control-Allow-Origin` and replay it to a different origin. A
  disallowed origin in credentialed mode now gets no ACAO header at all rather
  than the comma-joined allowlist, which was never a valid header value.
- The auth/webhook rate limiter now reclaims per-IP buckets that were left
  DEPLETED. Token counts are only updated inside `Allow`, so an abandoned
  bucket kept a stale near-zero count and never satisfied the sweep's
  "fully refilled" test — selecting against exactly the buckets worth expiring,
  since a scanner or credential-stuffer leaves its bucket drained and never
  returns. Those entries survived until the map hit its cap and every insert
  started paying an O(n) eviction scan.
- Run quota is now enforced atomically. The monthly run-cap gate was a
  read-then-increment, so concurrent submissions at the limit could all pass
  and exceed the cap; it now reserves a slot in a single atomic step
  (`AddRunIfUnder`) before the run is enqueued.
- Stripe webhook events are recorded as processed only AFTER the plan change is
  applied (was: before), so a transient apply failure can no longer ack a
  retried delivery without ever applying the subscription state change.
- The sign-in page now validates the `return_to` parameter before navigating,
  closing an open-redirect reachable via a crafted `/signin?return_to=` link.
- The public `/form/` endpoints are rate-limited and cap their request body,
  matching the `/trigger/` surface.
- Workspace file download now refuses the internal `.scratch` tree, like the
  other file operations.
- A panic while processing a node (resolve, template rendering over untrusted
  graph data, sandbox setup) or while fanning out a webhook/event trigger no
  longer crashes the whole multi-tenant daemon: the worker recovers and
  force-fails the node, and the detached trigger fan-out recovers and logs.
- Added `X-Frame-Options: DENY` and `Referrer-Policy` to the app surface
  (clickjacking hardening); the embeddable `/form/` surface is exempt.
- Unauthenticated, DB-touching public endpoints (invitation view, SSO/auth
  config, subdomain resolve, TLS-allow, Google sign-in, handoff) are now
  per-IP rate-limited, matching the rest of the public surface.

### Fixed

- The web UI no longer reports its version as `vdev` on a production deploy. The
  image version was stamped only when `VERSION` was exported into the
  environment of the `docker compose` call — which the Makefile targets do but
  the documented production command (`docker compose -f docker-compose.yml -f
  docker-compose.prod.yml up -d --build`) does not — and compose's `${VERSION:-dev}`
  default then baked a literal `dev` into the build arg. The build now falls back
  to a committed `./VERSION` file (kept in step with the tag by `make
  patch`/`minor`/`major`), so every build path stamps a real release. This also
  restores the admin System panel's update check for all operators: it reads the
  canonical instance's reported version, which an unstamped canonical build made
  unusable.
- `make upgrade` no longer tears down a production stack. It invoked
  `docker compose` with no `-f` flags, so on a host running the Caddy + docs
  overlay it recreated the stack from `docker-compose.yml` alone — dropping TLS
  termination and the docs site — and it picked the "latest" tag with
  `git tag --sort=-v:refname | head -1`, which sorts any non-version tag (a
  `nightly`) above every release. It now takes a `PROD=1` switch that merges the
  production overlay for every stack target, selects the newest bare `X.Y.Z` tag
  (skipping pre-releases), and stays on the deployed tag instead of returning to
  master. It also refuses to recreate the stack when `caddy` is running but the
  file set it would apply omits it — a check on what is running versus what is
  about to be applied, so a host that configures the overlay through compose's
  own `COMPOSE_FILE` (the recommended setup for a permanent production host) is
  not nagged about a flag it doesn't need. New `make latest` prints the selected
  tag for use in a deploy script.
- The sign-in form's submit button is no longer permanently disabled after a
  session expires. Clearing the token re-ran the identity bootstrap effect, whose
  cleanup could flip its `cancelled` guard before the in-flight `whoami`
  settled — so the `.finally()` that clears `loading` never ran and the button
  stayed gated on a stale `loading: true`, with a full page reload the only way
  back in.

- A graph run can no longer strand permanently when a worker shuts down. Once a
  node was claimed, some of its bookkeeping still ran on the claim context, so a
  SIGTERM could land between a terminal write and the dispatch of that node's
  dependents — leaving the node terminal, the dependents never enqueued, and the
  run "running" forever, which `ReapStuckGraphRuns` cannot recover because it
  bails on a MISSING node record. The same exposure let a shutdown fail the
  graph/predecessor READS and mark a node failed for no reason. All of a claimed
  job's store I/O now runs on a context detached from the claim loop; lease loss
  remains the only thing that aborts a claimed job.
- The scheduler no longer holds its mutex across the poll-outcome marker read,
  which is a store round-trip in production — a slow or hung secret store stalled
  rescan's map swap, leader re-anchoring, and `TrackedCount` behind it.
- `Engine.Run` keeps the results of a failed node's SIBLINGS. Merging and
  error-checking shared one pass, so nodes ordered after the failing one were
  dropped from `GraphResult.Nodes` — and since a layer is sorted by node ID,
  which results survived was decided alphabetically. Loop bodies read that map,
  so a body with one failing node was silently losing its other nodes' output.
- Graph/node `timeout_seconds` is clamped against int64 overflow — a hostile
  huge value previously wrapped negative and silently disabled the run/node
  timeout instead of capping it.
- Per-item write dedupe (engine) no longer aliases or re-fires across an
  auto-fanned node; the in-memory dedupe store returns isolated copies; the
  TOTP challenge attempt-counter increments atomically.
- Run-list pagination orders by `(enqueued_at, id)` so rows that share a
  timestamp can't duplicate or vanish across pages; added a `jobs(tenant,
  status)` index for the tenant-scoped hot paths.
- A `ListJobsForGraph` scope check no longer admits records with an empty
  tenant.
- The Postgres event bus no longer permanently drops an event whose row
  committed out of `BIGSERIAL` order (a lower id committing after the listener
  advanced past it): the listener re-scans a bounded trailing window and
  dedupes, so live run/node/progress UI events aren't skipped under multi-node
  load. (Run correctness was never affected — the job store is authoritative.)
- The Postgres pool now reserves headroom for the two permanently-held
  connections (event-bus listener + leader lock) so a low `max_conns` can't
  starve the workqueue.

## [0.2.0] - 2026-06-27

### Added

- **Flow duplicate.** `POST /api/v1/me/flows/{flow_id}/duplicate` copies a
  flow under a fresh ID (new trigger URLs, empty run history) and starts it as
  a disabled draft owned by the caller, so a copied cron/webhook can't fire
  before it's reviewed. Exposed as a per-card "Duplicate" action in the flow
  list that opens the copy in the editor.
- Licensed the project under the GNU Affero General Public License v3.0 or
  later (AGPL-3.0-or-later); added `LICENSE` and a README license section.
- SPDX license headers across all Go and TypeScript source files.

### Changed

- **Write dedupe is now Postgres-backed in multi-node deployments.** The
  engine's write-dedupe store (which suppresses a re-fire of a non-idempotent
  external write — Twilio SMS, Gmail/Discord/Sheets/Home Assistant — when a
  lease reclaim or crash recovery re-runs the same job) is now the shared
  `write_dedupe` table instead of a process-local map, so a reclaim by a
  *different* `dzd` replica sees the recorded result instead of sending the
  message twice. `dzd` fails to boot if the table can't be created, matching
  every other Postgres-backed store; the in-memory store remains the
  single-node/test implementation. The contract is unchanged (at-least-once).
- Consolidated 167 scattered coverage test files (`*_cov_test.go`,
  `*_cov2-4_test.go`, `*_coverage_test.go`, `*_extra_test.go`) into their
  per-subject `_test.go` files. No test functions were removed (3306 before
  and after); only the file layout changed.
- Decluttered the repository root: moved reference docs (`DEPLOY.md`,
  `COMPLIANCE.md`, `PRIVACY.md`, `SECURITY-SLA.md`, `TODO.md`) into `docs/`
  and the `Caddyfile` into `deploy/`, updating all cross-references.
  `README.md`, `LICENSE`, `CHANGELOG.md`, and `SECURITY.md` stay at the root
  by convention.

### Removed

- Stale planning docs `GDPR_FIXES.md` and `manual.md` (history retained in
  git); fixed the dangling links in `PRIVACY.md` and `COMPLIANCE.md`.
- Orphaned root `package-lock.json` stub (no root `package.json` exists).
- The dev-only `cmd/email-preview` template-preview generator and its
  generated `email-preview.html` artifact — unreferenced by the build, CI,
  and docs. Email templates are still previewable in the web UI.
- The `scripts/ha_loadtest` multi-node HA load-test harness — never wired
  into CI or the Makefile; leader-election and failover are covered by
  `daemon/leader_test.go`.

## [0.1.0] - 2026-06-08

Initial release.

### Added

- **Flow engine.** Graph-based flows with conditional branching, fan-out
  (`for_each`), reusable subgraphs, and per-node retry policies. Runs are
  persisted and observable end to end.
- **Connectors.** Built-in integrations for HTTP, Postgres, Slack, Gmail,
  GitHub, Git, Notion, Google Sheets, and Excel, plus shell and
  transform/value utility nodes.
- **AI steps.** Claude-backed LLM nodes for generation and transformation
  inside a flow.
- **Triggers.** Start flows from inbound webhooks or timezone-aware cron
  schedules.
- **Web UI.** Visual flow builder and run viewer, with light and dark
  themes.
- **MCP server.** Exposes the connector catalog so an LLM agent can
  discover, compose, and run flows.
- **Control plane.** gRPC API with the `dzctl` CLI, plus a REST surface
  documented by an OpenAPI spec under `/api/v1`.
- **Auth & multi-tenancy.** Organizations, role-based access control, TOTP
  two-factor auth, invitations, and a platform super-admin role.
- **Secrets.** Master-key-encrypted storage for connector credentials.
- **Deployment.** Docker Compose stack (daemon + Postgres) with a boot
  guard that refuses to start on insecure defaults.
- **Versioning.** Version metadata stamped into the binary at build time,
  surfaced on `GET /api/v1` and in the web UI; `make bin`/`major`/`minor`/
  `patch`/`upgrade` release targets.
